# Plan: Long-Running Test Lab — Stability + Mutator Observability

**Status:** Approved (brainstorm 2026-05-22), pending implementation
**Author:** Adversarial remediation, 2026-05-22
**Created:** 2026-05-22
**Estimate:** ~1 engineering day
**Risk:** Low — touches a non-production harness, no exporter code changes

## Problem statement

The long-running test lab at `deploy/long-running-test/` has been silently
broken for at least three hours. Live signal as of 2026-05-22 22:30 UTC:

- **0 devices discovered** in the current cycle.
- **600 `network_topology_discovery_hard_fail_total{reason="system_group_walk_error"}`** events in the last hour.
- **Mutator deploys failed at 20:00, 21:00, and 22:00 UTC** with `'nte-dynamic-1' lab has already been deployed`. `--reconfigure` left partial state behind.
- Container IP collision: the exporter (`172.20.20.3`) is querying the Alloy container (`172.20.20.2`) and itself because both attached to the shared `clab` bridge before the lab IPs were assigned.
- **Zero log streams** with `tester_id=long-running-lab` in Loki — Alloy's docker scrape isn't catching the mutator at all.

The mutator is a black box. From the outside there is no signal that distinguishes "mutator is working" from "mutator hasn't run in 12 hours." We learned 3 hours of partial-state failures had been accumulating only because we walked over and looked.

## Goals

- **Unblock discovery** so the exporter sees real devices and edges again.
- **Make the mutator observable** so future failures self-announce within one mutation cycle (1 hour).
- **Eliminate the unrealistic destroy-and-redeploy churn** in favour of a model that actually exercises the exporter's edge-diff code path — which is the whole point of the harness.

## Non-goals

- Steady-state 24h validation. That's a separate exercise once this lands.
- Replacing the bash mutator with a Go binary (deferred — keep within "unblock" scope).
- Supply-chain digest pinning of `alpine:latest` / `grafana/alloy:latest` / `ghcr.io/srl-labs/clab:latest`. Flagged in adversarial review; will be addressed in a follow-up.
- Touching `deploy.sh` beyond what unblocks the lab. The AI-slop block and rsync key-leak still need cleanup but are filed for a follow-up PR.

## Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| AC1 | The lab boots from cold with `docker compose --env-file .env up -d` and the exporter discovers ≥5 devices within 3 minutes | Cycle log shows `device_info` series ≥5; `network_topology_discovery_devices_total{status="success"}` ≥5 in Grafana |
| AC2 | Per-topology mutation does not destroy or restart any container; SNMP/LLDP daemons keep running across mutations | `docker inspect --format '{{.State.StartedAt}}' clab-nte-dynamic-1-spine1` unchanged after a mutation |
| AC3 | The exporter emits `network_topology_change_total{change_kind="added"}` and `change_kind="removed"` at each hourly mutation | LogQL on Loki for `topology change` events; PromQL `increase(network_topology_change_total[1h])` ≥1 per hour |
| AC4 | The mutator emits a structured JSON log line for `mutation_start`, `mutation_success`, and `mutation_failed`, each with `topo`, `links_added`, `links_removed`, `duration_s`, and `error` fields where applicable | LogQL query `{tester_id="long-running-lab",job="long-running-mutator"} \| json` returns one event per mutation |
| AC5 | A `Mutator Events` panel on the `Test Harness Health` dashboard shows the most recent mutation events from Loki, colour-coded by outcome | Visual inspection of dashboard after a mutation fires |
| AC6 | Stable IPs: every clab node has the same management IP across reboots and across topology mutations | `mgmt-ipv4` declared per node; verify via `clab inspect` |
| AC7 | The lab survives the presence of an unrelated containerlab lab (`arista-ceos-bgp`) sharing the host without IP collision | Bring up `arista-ceos-bgp`, then deploy `long-running-test`, verify no overlap |
| AC8 | The mutator is resilient to `clab destroy` being needed: if the base lab is in a partial state at startup, it cleans up and redeploys | Manually break state (delete a container), restart mutator, observe self-heal log line |

## Technical approach

### Architecture

The current flow:

```
mutate.sh ─hourly─▶ containerlab deploy -t topo-N.yml --reconfigure
                                    ├─ destroy all containers
                                    └─ recreate all containers (often partial-failing)
```

The new flow:

```
deploy.sh ─once─▶ containerlab deploy -t base.clab.yml
                              └─ 6 nodes (spine1, spine2, leaf1..4) with mgmt-ipv4 pins, no data-plane links

mutate.sh ─hourly─▶ link reconciler
                              ├─ read topologies/topo-N.yml (existing file, unchanged)
                              ├─ compute desired link set
                              ├─ diff against currently-attached veths
                              ├─ apply (containerlab tools veth create, ip link del)
                              └─ emit JSON event
```

The exporter's discovery loop runs continuously across all of this. Containers stay up. SNMP/LLDP daemons keep their state. The exporter sees edges *appear* and *disappear* on the hour boundary — exercising the same code path it exists to validate.

### Components

#### 1. `base.clab.yml` (new file)

The union of all four topology files' nodes (`spine1`, `spine2`, `leaf1`, `leaf2`, `leaf3`, `leaf4`), with pinned management IPs, no data-plane links. Deployed once at lab bring-up; never destroyed except by an explicit operator.

```yaml
name: nte-dynamic-1
mgmt:
  network: nte-dynamic-clab
  ipv4-subnet: 172.30.0.0/24

topology:
  nodes:
    spine1: { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.11 }
    spine2: { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.12 }
    leaf1:  { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.21 }
    leaf2:  { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.22 }
    leaf3:  { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.23 }
    leaf4:  { kind: linux, image: nte-testnode:latest, mgmt-ipv4: 172.30.0.24 }
```

**Why a dedicated subnet (172.30.0.0/24) instead of 172.20.20.0/24?** Avoids collision with the `arista-ceos-bgp` lab and any other clab lab the operator runs. Each lab gets its own bridge.

#### 2. `topologies/topo-{1..4}.yml` (existing files, unchanged)

The four topology files stay exactly as they are. Their `links:` blocks are parsed by the mutator to derive the desired link set. We do not destroy or redeploy the lab when switching between them.

#### 2a. `tests/e2e/testnode/start.sh` (one-line change)

Today the testnode waits *forever* for `eth1` to come up before starting lldpd. That's correct for the existing e2e tests (where clab attaches `eth1` at deploy time) but **breaks the new design** — the base lab has no data-plane links, so `eth1` never appears and lldpd never starts.

Change: cap the wait at 30 seconds; start lldpd anyway after the timeout. lldpd's netlink listener picks up later interface additions via `RTM_NEWLINK` events, so the original race concern (lldpd scanning before clab attaches) is mitigated as long as lldpd is *running* by the time the mutator adds the first link.

```sh
# Wait up to 30s for eth1 to come up; if it never does (long-running-lab base
# deploy attaches links lazily), start lldpd anyway and rely on its netlink
# listener to pick up subsequent additions.
for i in $(seq 1 60); do
    ip link show eth1 2>/dev/null | grep -q "LOWER_UP" && break
    sleep 0.5
done
```

The existing e2e tests are unaffected: `eth1` is already up when they reach this loop, so it exits on the first iteration. Risk: a real lldpd race where eth1 comes up at the boundary of the 30s timeout could miss the initial scan — verified by running `make test-e2e` after the change.

#### 3. `mutate.sh` (rewrite)

New bash script (~80 lines) that:

1. **State discovery**: for each of the 6 base nodes, runs `docker exec <node> ip -br link | awk '/^eth[1-9]/ {print $1}'` to list current data-plane interfaces. Resolves each interface's peer by reading the ifindex (`@if<N>`) from `ip link show` and looking up the matching interface across the other base containers. Builds an undirected set of `(node:iface, peer:peer-iface)` tuples.
2. **Desired-state computation**: parses the target topology's `links:` block via `yq` (add `yq` to the apk install in the compose entrypoint).
3. **Diff**: computes `(to_delete, to_keep, to_add)` as sets of `(node:iface, peer:peer-iface)` tuples. Adding `yq` to the mutator's `apk add` is the only dependency change.
4. **Apply**:
   - Delete: `docker exec <node> ip link del <iface>` (the veth peer auto-deletes).
   - Add: `containerlab tools veth create --name nte-dynamic-1 --a-endpoint <node>:<iface> --z-endpoint <peer>:<peer-iface>`.
5. **Logging**: emits one JSON line at each phase (`mutation_start`, per-link `link_added` / `link_removed`, `mutation_success` or `mutation_failed`). All logs to stdout — Alloy's docker scrape ships them to Loki.
6. **Self-heal**: on startup, checks whether `base.clab.yml`'s lab is in expected state (all 6 nodes running). If not, destroys and redeploys the base. Logs `self_heal_triggered` event.

Structured event schema (one per line, JSON):

```json
{"time":"2026-05-22T23:00:01Z","event":"mutation_start","topo":"topo-3","prev_topo":"topo-2","hour":23}
{"time":"2026-05-22T23:00:01Z","event":"link_removed","a":"leaf1:eth2","z":"leaf2:eth2"}
{"time":"2026-05-22T23:00:02Z","event":"link_added","a":"leaf1:eth2","z":"leaf2:eth1"}
{"time":"2026-05-22T23:00:03Z","event":"mutation_success","topo":"topo-3","duration_s":2.1,"links_added":3,"links_removed":2,"links_kept":2}
```

If the operation fails:

```json
{"time":"2026-05-22T23:00:05Z","event":"mutation_failed","topo":"topo-3","duration_s":4.3,"phase":"apply","error":"clab tools veth: exit 1: ..."}
```

The script does **not** retry on failure. The next hour boundary triggers a fresh attempt. Errors persist as the most recent event in Loki — the dashboard surfaces them.

#### 4. `docker-compose.yml` (changes)

- **Exporter and Alloy leave the `clab` network**. They run on the default compose bridge (`long-running-test_default`) and reach the lab's management subnet (`172.30.0.0/24`) via Docker's bridge-to-bridge routing (allowed by default in `iptables` for RFC1918 traffic, which Docker enforces on standard installs).
- **Mutator container** stays `--privileged --network host --pid host` because it needs to drive `containerlab` and exec into target node netns.
- **Replaces `apk add --no-cache bash docker-cli`** at start time with a baked image? **Deferred to follow-up.** Today's version still does the apk install.

#### 5. `config.yaml` (changes)

- Targets become the pinned management IPs:
  ```yaml
  targets:
    - host: 172.30.0.11 # spine1
    - host: 172.30.0.12 # spine2
    - host: 172.30.0.21 # leaf1
    - host: 172.30.0.22 # leaf2
    - host: 172.30.0.23 # leaf3
    - host: 172.30.0.24 # leaf4
  ```
- `cidr_allow_list` becomes `["172.30.0.0/24"]` — drops the `0.0.0.0/0` catch-all that disarmed the safety net.

#### 6. `alloy-config.alloy` (one fix)

The current `loki.relabel.exporter` filter only matches `/long-running-exporter` containers. Add the mutator and alloy itself so all three ship logs:

```alloy
loki.relabel "long_running_containers" {
  forward_to = [loki.write.grafana_cloud_loki.receiver]
  rule {
    source_labels = ["__meta_docker_container_name"]
    regex         = "/long-running-(exporter|mutator|alloy)"
    action        = "keep"
  }
  rule {
    source_labels = ["__meta_docker_container_name"]
    regex         = "/long-running-(.+)"
    target_label  = "job"
    replacement   = "long-running-$1"
  }
  ...
}
```

The mutator's logs will land in Loki with labels `{tester_id="long-running-lab", job="long-running-mutator"}`.

#### 7. `dashboards/test-harness/harness-health.json` (add panel)

A new **Mutator Events** panel: a Loki logs panel querying

```logql
{tester_id="long-running-lab", job="long-running-mutator"} | json | event=~".+"
```

Colour-coded by `event` value (`mutation_success` green, `mutation_failed` red, `link_added`/`link_removed` neutral). Placed in the top row of `Test Harness Health` so the operator sees mutator state first.

Pulled back into Grafana Cloud via the existing `make dashboards-apply` workflow — but only after this lands.

### Failure modes

| Failure | Behaviour |
| --- | --- |
| `containerlab tools veth create` fails | `mutation_failed` event emitted; partial state may exist. Next hour, self-heal at script start detects drift (current ≠ desired ≠ either prev or new topology) and triggers a destroy+redeploy of the base lab as a hard reset. |
| Mutator container restarts mid-mutation | Next iteration of the loop checks UTC hour, finds `LAST_HOUR=-1`, recomputes the desired topology for the current hour, reconciles. No state in the script itself. |
| Base lab containers OOM-killed by the host | Self-heal at script tick redeploys the base lab. Logs `self_heal_triggered`. |
| `yq` not installed in mutator image | `apk add` extended to include `yq`. Failure logged at startup. |
| Two mutators racing (e.g. accidental `docker compose up` twice) | Compose blocks the second `mutator` container by name collision (`long-running-mutator`). |

### Testing

This is a manual-verification effort — no unit tests because nothing here lives in Go.

1. **Cold-boot AC1**: bring up the lab from scratch on macbookpro-2015, watch exporter logs for first successful cycle, confirm device count.
2. **Mutation AC2/AC3**: trigger a manual mutation by overriding `HOUR` in mutate.sh, verify container start times unchanged and `change_total` increments.
3. **Mutator log AC4**: query Loki for the JSON event line.
4. **Dashboard panel AC5**: open `Test Harness Health` dashboard, see the events panel populate.
5. **IP stability AC6**: redeploy the lab from scratch, confirm pinned IPs are identical.
6. **Lab co-existence AC7**: bring up `arista-ceos-bgp` first, then this lab, confirm both run.
7. **Self-heal AC8**: `docker kill clab-nte-dynamic-1-leaf2`, watch for `self_heal_triggered` event within the next mutator tick.

### Implementation order

1. Add `base.clab.yml` + `mgmt-ipv4` pins; update `config.yaml` targets and `cidr_allow_list`.
2. Move exporter+alloy off the `clab` network in `docker-compose.yml`.
3. Rewrite `mutate.sh` (link reconciler + JSON events + self-heal).
4. Fix `loki.relabel.exporter` regex in `alloy-config.alloy`.
5. Deploy from macbookpro-2015, verify AC1–AC8 in order.
6. Pull the updated `Test Harness Health` dashboard from Grafana Cloud after I add the `Mutator Events` panel via the UI (matches the existing dashboard workflow — UI-authored, repo-synced).

### Out of scope (filed as follow-ups)

- Bake a `nte-mutator:latest` image so we stop `apk add`-ing on every container restart.
- Pin image digests on `alpine`, `grafana/alloy`, `ghcr.io/srl-labs/clab`.
- Rewrite `deploy.sh` to use `set -euo pipefail`, exclude `id_ed25519` from rsync, parameterize the host.
- Add a Grafana alert on `count_over_time({tester_id="long-running-lab", event="mutation_failed"}[1h]) > 0`.
- Rotate the leaked `glc_` token and `id_ed25519` SSH key (operator action, not code).
