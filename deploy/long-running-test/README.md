# Long-running test lab

A continuously-running validation environment for the topology exporter. A
single mutator container swaps the underlying containerlab topology every UTC
hour, exercising add/remove/swap operations against a live exporter shipping
to Grafana Cloud.

## Stack

| Container             | Image                          | Purpose                                                  |
| --------------------- | ------------------------------ | -------------------------------------------------------- |
| `clab-nte-dynamic-1-{spine1,spine2,leaf1..leaf4}` | `nte-testnode:latest` | The lab itself: 6 always-on nodes with pinned mgmt IPs in `172.30.0.0/24`. |
| `long-running-exporter` | `network-topology-exporter:latest` | Discovers the lab, exposes `/metrics`                  |
| `long-running-alloy`    | `grafana/alloy:latest`         | Scrapes the exporter and all `long-running-*` container logs; ships metrics/logs/traces to Grafana Cloud |
| `long-running-mutator`  | `alpine:latest` + `clab`       | Reconciles veth links between the 6 base nodes every UTC hour; emits structured JSON events to stdout |

## Mutation schedule

The mutator selects a topology file by `UTC hour mod 4` and applies it
by **reconciling veth links between always-on nodes**. Nodes never
restart; SNMP/LLDP keep state across mutations.

| `UTC hour % 4` | File          | Topology                                                |
| -------------- | ------------- | ------------------------------------------------------- |
| `0`            | `topo-1.yml`  | spine1 + 4 leaves, star (chain)                         |
| `1`            | `topo-2.yml`  | star + leaf cross-link, leaf4 daisy-chained             |
| `2`            | `topo-3.yml`  | 5-node ring                                             |
| `3`            | `topo-4.yml`  | CLOS — spine1+spine2 × leaf1..3                         |

Nodes that don't participate in the current topology (`spine2` in
`topo-1/2/3`; `leaf4` in `topo-4`) stay running with no data-plane
links — the exporter sees the device but reports zero edges.

The mutator emits JSON events on stdout that Alloy ships to Loki:
`mutator_starting`, `mutation_start`, `link_added`, `link_removed`,
`link_add_failed`, `link_remove_failed`, `mutation_success`,
`mutation_failed`, `self_heal_triggered`, `self_heal_success`,
`self_heal_failed`. Query with:

    {job="long-running-mutator", tester_id="long-running-lab"} | json

## Prerequisites

- Docker + Docker Compose v2
- Containerlab installed on the host (the mutator pulls `ghcr.io/srl-labs/clab`
  at runtime to avoid a host-side dependency)
- A Grafana Cloud Access Policy token with write scopes for metrics, logs, traces
- A built `network-topology-exporter:latest` image on the host
- A built `nte-testnode:latest` image (used by every containerlab node)

## Bring-up

```bash
cp .env.example .env
$EDITOR .env

bash deploy.sh    # rsyncs, brings up base lab, starts compose
```

`deploy.sh` is idempotent: rerun it any time the base lab or compose
stack needs a refresh. Use `REMOTE_HOST=...`, `REMOTE_USER=...`, and
`REMOTE_DIR=...` env vars to target a host other than the default.

## Operator notes

- **All Alloy credentials come from `.env`**. Never edit `alloy-config.alloy`
  to inline a token. The `.env` file is gitignored; `.env.example` is the
  on-repo template.
- **The mutator runs `--privileged --network host --pid host`** because that
  is what containerlab needs to manage Linux netns. Treat the host as
  root-controlled.
- **`exporter-data` is a named volume**. The snapshot persists across
  topology swaps; the exporter will surface stale devices for the first
  cycle after a mutation until eviction catches up. To start clean,
  `docker compose down -v`.
- **Telemetry labels**: all series carry `tester_id=long-running-lab` and
  `env=e2e` so the test data can be filtered out of dashboards that share
  the same Grafana stack.
- **No alerting is wired up.** If `containerlab deploy` fails or the cycle
  budget is exhausted, you will only see it on the test-harness dashboards.

## Known limitations

- `deploy.sh` rsyncs the working tree but does *not* push the
  `nte-testnode:latest` image — build it on the remote host once.
- The mutator does not retry on failure; the next hour boundary
  triggers a fresh attempt. The most recent `mutation_failed` event
  in Loki is the current failure signal.
- All four topology files share the lab name `nte-dynamic-1`. The
  mutator addresses links by node name (`spine1:eth1`), so this is
  fine — but `clab inspect` always points at `base.clab.yml`.
- The mutator tracks state in `.last-topo` (gitignored). On manual
  intervention (e.g. someone runs `ip link del` outside the
  mutator), the next mutation will compute a wrong diff. Restarting
  the mutator triggers a self-heal pass that destroys + redeploys
  the base lab and resets the state file.
- The `cidr_allow_list` in `config.yaml` includes `0.0.0.0/0`. This is
  intentional: nte-testnode advertises a MAC chassis ID over LLDP, and
  the exporter's scope filter (LD-11) drops MAC-chassis neighbours
  when the allow-list is non-catch-all. See CHANGELOG entry D54.
