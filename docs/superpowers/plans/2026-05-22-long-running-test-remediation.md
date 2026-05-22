# Long-Running Test Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the destroy-and-redeploy hourly mutation model with a static-nodes-dynamic-links model so the exporter's edge-diff code path actually fires; surface every mutation event as a structured JSON log line that lands in Loki.

**Architecture:** A `base.clab.yml` deploys six nodes (`spine1, spine2, leaf1..leaf4`) once with pinned management IPs in a dedicated `172.30.0.0/24` subnet. Containers stay up across mutations; lldpd/snmpd keep state. A rewritten `mutate.sh` parses the active hour's topology YAML, diffs link sets against current OS state via `docker exec ip link`, and applies adds via `containerlab tools veth create` and removes via `ip link del`. Each phase emits a JSON event on stdout for Alloy's docker scrape to ship to Loki.

**Tech Stack:** bash 5.x + busybox `ip` + `yq` (Go) + containerlab 0.75 + docker compose v2 + Grafana Alloy + Loki (Grafana Cloud).

---

## File map

| Path | Action | Purpose |
| ---- | ------ | ------- |
| `deploy/long-running-test/base.clab.yml` | Create | One-time base lab definition: 6 nodes, pinned IPs, dedicated subnet, no data-plane links. |
| `deploy/long-running-test/mutate.sh` | Rewrite | Hourly link reconciler with JSON event logging and self-heal. |
| `deploy/long-running-test/topologies/topo-{1..4}.yml` | Unchanged | Source of truth for desired link set per hour. Mutator reads only the `links:` block. |
| `deploy/long-running-test/config.yaml` | Modify | Targets become pinned `172.30.0.x` IPs; `cidr_allow_list` tightened to the lab subnet. |
| `deploy/long-running-test/docker-compose.yml` | Modify | Exporter and Alloy leave the `clab` network. Mutator gains `yq` in its `apk add`. |
| `deploy/long-running-test/alloy-config.alloy` | Modify | Docker log-scrape filter extends to `long-running-mutator` and `long-running-alloy` containers. |
| `deploy/long-running-test/deploy.sh` | Modify (minimal) | Run `containerlab deploy -t base.clab.yml` before `docker compose up -d`. |
| `tests/e2e/testnode/start.sh` | Modify | Cap the `eth1` LOWER_UP wait at 30s; start lldpd unconditionally after the timeout. |
| `deploy/long-running-test/README.md` | Modify | Document the new model (static nodes, link mutation, mutator events). |

The four `topo-*.yml` files in `deploy/long-running-test/topologies/` are read-only inputs for the mutator and require no changes.

Verification across the plan uses live signals from macbookpro-2015 over SSH (`ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 ...`) and Grafana Cloud reads via the token at `/Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token`.

---

## Task 1: Patch testnode/start.sh to bound the eth1 wait

The current `until` loop spins forever if `eth1` never appears. The new base lab deploys with no data-plane links, so lldpd would never start under that loop. Cap the wait at 30s; start lldpd anyway after the timeout. Existing e2e tests are unaffected because `eth1` is already up by the time they reach this loop.

**Files:**
- Modify: `tests/e2e/testnode/start.sh:36-38`

- [ ] **Step 1: Replace the wait loop with a bounded version**

Replace lines 32-38 (the comment block + `until` loop) with:

```sh
# containerlab attaches data-plane interfaces (eth1, eth2, ...) after the
# container starts. Wait up to 30s for eth1 to come up so lldpd's initial
# scan picks it up. If eth1 never appears (long-running-lab base deploy
# attaches links lazily via the mutator), start lldpd anyway and rely on
# its netlink listener to handle RTM_NEWLINK events for later additions.
for _ in $(seq 1 60); do
    ip link show eth1 2>/dev/null | grep -q "LOWER_UP" && break
    sleep 0.5
done
```

The loop counter variable becomes `_` because we never read it.

- [ ] **Step 2: Verify the existing e2e suite still passes**

From the repo root:

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
make e2e-image                 # rebuild nte-testnode:latest with the patched start.sh
make test-e2e | tail -40       # run the full e2e suite
```

Expected: all e2e tests pass. Look for `--- PASS:` lines, no `--- FAIL:`.

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add tests/e2e/testnode/start.sh
git commit -m "fix(testnode): cap eth1 wait at 30s so lldpd can start without data-plane links

The long-running-test base lab deploys nodes with no data-plane interfaces;
the mutator attaches them later. Unbounded wait would deadlock the
testnode. lldpd's netlink listener handles RTM_NEWLINK events for the
later additions."
```

---

## Task 2: Add base.clab.yml — the one-time node deploy

This file defines the union of every topology's nodes with pinned management IPs in a dedicated subnet. Once deployed, `containerlab destroy` is the only way to remove it; the mutator never touches the nodes themselves.

**Files:**
- Create: `deploy/long-running-test/base.clab.yml`

- [ ] **Step 1: Write base.clab.yml**

```yaml
# Base lab for long-running-test. Deployed once via deploy.sh; never destroyed
# by the hourly mutator. Six nodes — the union of every topo-*.yml file's
# node set — with pinned management IPs in a dedicated subnet so the exporter
# can target stable addresses regardless of which topology is active.
#
# Data-plane links (eth1, eth2, ...) are NOT defined here. The mutator
# attaches and detaches them per topology at runtime via
# `containerlab tools veth create` and `ip link del`.

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

- [ ] **Step 2: Validate the YAML parses**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
python3 -c "import yaml; print(yaml.safe_load(open('deploy/long-running-test/base.clab.yml')))" | head -10
```

Expected: prints the parsed dict starting with `{'name': 'nte-dynamic-1'...`.

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/base.clab.yml
git commit -m "feat(long-running-test): add base.clab.yml for static-node deploy"
```

---

## Task 3: Update config.yaml — pinned targets + tight CIDR

**Files:**
- Modify: `deploy/long-running-test/config.yaml`

- [ ] **Step 1: Rewrite the file**

Full new contents (overwrites all 38 lines):

```yaml
discovery:
  interval: 30s
  timeout_per_device: 10s
  parallelism: 20
  scope:
    cidr_allow_list:
      - "172.30.0.0/24"

modules:
  snmp:
    enabled: true
    version: v2c
  lldp:
    enabled: true

credentials:
  profiles:
    - name: lab-v2c
      type: snmp_v2c
      community_env: SNMP_COMMUNITY
  fallback_order: [lab-v2c]

snapshot:
  path: /var/lib/network-topology-exporter/snapshot.json

targets:
  - host: 172.30.0.11 # spine1
    labels: { site: long-running-lab }
  - host: 172.30.0.12 # spine2
    labels: { site: long-running-lab }
  - host: 172.30.0.21 # leaf1
    labels: { site: long-running-lab }
  - host: 172.30.0.22 # leaf2
    labels: { site: long-running-lab }
  - host: 172.30.0.23 # leaf3
    labels: { site: long-running-lab }
  - host: 172.30.0.24 # leaf4
    labels: { site: long-running-lab }
```

Changes vs the existing file:
- `cidr_allow_list` is one entry, no `0.0.0.0/0`
- 6 targets (added leaf4)
- IPs moved from `172.20.20.x` to `172.30.0.x` matching `base.clab.yml`

- [ ] **Step 2: Validate the YAML parses**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
python3 -c "import yaml; d=yaml.safe_load(open('deploy/long-running-test/config.yaml')); print('targets:', len(d['targets']), 'cidr:', d['discovery']['scope']['cidr_allow_list'])"
```

Expected: `targets: 6 cidr: ['172.30.0.0/24']`

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/config.yaml
git commit -m "fix(long-running-test): pin targets to base.clab.yml mgmt IPs

Move exporter targets to 172.30.0.0/24 to match the new base lab subnet.
Drop the 0.0.0.0/0 catch-all that disarmed the cidr_allow_list safety net."
```

---

## Task 4: Update docker-compose.yml — exporter+alloy off clab bridge, mutator gets yq

The exporter and Alloy were on the shared `clab` bridge and competed with other labs' containers for IPs. They no longer need to be on that bridge to *reach* the lab — Docker's default bridge-to-bridge iptables rules allow RFC1918 traffic. The mutator also needs `yq` to parse topology files.

**Files:**
- Modify: `deploy/long-running-test/docker-compose.yml`

- [ ] **Step 1: Rewrite the file**

Full new contents:

```yaml
services:
  topology-exporter:
    image: network-topology-exporter:latest
    container_name: long-running-exporter
    volumes:
      - ./config.yaml:/etc/topology-exporter/config.yaml:ro
      - exporter-data:/var/lib/network-topology-exporter
    environment:
      - SNMP_COMMUNITY=${SNMP_COMMUNITY:-public}
    ports:
      - "9100:9100"
    restart: unless-stopped

  alloy:
    image: grafana/alloy:latest
    container_name: long-running-alloy
    volumes:
      - ./alloy-config.alloy:/etc/alloy/config.alloy:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports:
      - "12346:12345" # Alloy UI
      - "4319:4317"   # OTLP gRPC
      - "4320:4318"   # OTLP HTTP
    command: run --storage.path=/var/lib/alloy/data /etc/alloy/config.alloy
    environment:
      - ALLOY_DEPLOY_MODE=docker
      - GRAFANA_CLOUD_TOKEN
      - GRAFANA_PROM_USER
      - GRAFANA_LOKI_USER
      - GRAFANA_TEMPO_USER
    restart: unless-stopped

  mutator:
    image: alpine:latest
    container_name: long-running-mutator
    volumes:
      - .:/work
      - /var/run/docker.sock:/var/run/docker.sock
    working_dir: /work
    network_mode: host
    pid: host
    privileged: true
    environment:
      - TARGET_PWD=${PWD}
    entrypoint: ["/bin/sh", "-c", "apk add --no-cache bash docker-cli yq && ./mutate.sh"]
    restart: unless-stopped

volumes:
  exporter-data:
```

Changes vs the existing file:
- Removed the top-level `networks:` block (no longer attaching to `clab`)
- Removed `networks: [clab]` from `topology-exporter` and `alloy` services
- Removed the obsolete `version: '3.8'` line (compose v2 warns about it)
- Added `yq` to the mutator's `apk add` line
- Mutator gets explicit `network_mode: host`, `pid: host`, `privileged: true` (these were implicit in the previous setup but worth making explicit)

- [ ] **Step 2: Validate the compose file**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter/deploy/long-running-test
docker compose config > /dev/null && echo "compose OK"
```

Expected: prints `compose OK`. Any warnings about missing env vars are fine — those come from the `.env` file at runtime.

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/docker-compose.yml
git commit -m "fix(long-running-test): move exporter+alloy off clab bridge; add yq to mutator

The clab bridge was shared with other labs, causing IP collisions
(exporter ended up at 172.20.20.3, querying itself). Default Docker
bridge-to-bridge routing covers the management subnet. The mutator
needs yq to parse topology files in the rewritten reconciler."
```

---

## Task 5: Update alloy-config.alloy — scrape mutator + alloy logs

The current docker scrape regex `regex = "/long-running-exporter"` only matches one container. Extend it so mutator and alloy logs also reach Loki.

**Files:**
- Modify: `deploy/long-running-test/alloy-config.alloy:75-92`

- [ ] **Step 1: Read the current relabel block**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
sed -n '75,100p' deploy/long-running-test/alloy-config.alloy
```

Expected: shows the `loki.relabel "exporter"` block and its filter rule.

- [ ] **Step 2: Replace the relabel block**

Find this block (the exporter-only filter) and replace it:

```alloy
loki.relabel "exporter" {
  forward_to = [loki.write.grafana_cloud_loki.receiver]
  rule {
    source_labels = ["__meta_docker_container_name"]
    regex         = "/long-running-exporter"
    action        = "keep"
  }
  rule {
    source_labels = ["__meta_docker_container_name"]
    target_label  = "job"
    replacement   = "topology-exporter"
  }
  rule {
    source_labels = ["__meta_docker_container_name"]
    target_label  = "instance"
    replacement   = "macbookpro-2015"
  }
}
```

with the broader filter:

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
  rule {
    target_label  = "instance"
    replacement   = "macbookpro-2015"
  }
}
```

Then update `loki.source.docker "exporter"` to forward to the renamed receiver:

```alloy
loki.source.docker "exporter" {
  host    = "unix:///var/run/docker.sock"
  targets = discovery.docker.exporter.targets
  forward_to = [loki.relabel.long_running_containers.receiver]
}
```

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/alloy-config.alloy
git commit -m "fix(long-running-test): ship mutator + alloy logs to Loki

The previous regex matched only /long-running-exporter, dropping
mutator and alloy logs on the floor. Job label now derives from the
container name suffix so dashboards can filter by job=long-running-mutator."
```

---

## Task 6: Rewrite mutate.sh — link reconciler with JSON events

This is the biggest task. The new script:
1. On every tick, reads current UTC hour and selects `topo-N.yml`.
2. Reads the topology's `links:` block via `yq`.
3. Reads the **previously applied topology** from `/work/.last-topo`. Computes the "current" link set as the desired set of that previous topology — we trust the state file rather than introspecting OS state (busybox `ip` in the testnodes is too limited to enumerate veth peers reliably).
4. Computes diff sets `(to_delete, to_add, to_keep)`.
5. Applies them via `ip link del` and `containerlab tools veth create`.
6. Writes the new topology name to `/work/.last-topo` on success.
7. Emits one JSON line per phase to stdout.
8. On startup, runs a self-heal pass: ensures all 6 base nodes are present and running; if not, redeploys the base lab and clears `.last-topo` so the next mutation does a full rebuild from scratch.

**State file trade-off:** the file is the source of truth for what links *should* exist. If something goes out-of-band (a node crashes mid-mutation, a manual `ip link del`), the next diff will be wrong about reality but the resulting mutation still moves the lab *toward* the desired topology — it just won't clean up unexpected leftovers. The next self-heal triggered by a missing-node check will hard-reset the state. Documented as a known limitation in the README.

**Files:**
- Rewrite: `deploy/long-running-test/mutate.sh`
- Modify: `.gitignore` (add `deploy/long-running-test/.last-topo`)

- [ ] **Step 1: Write the new mutate.sh**

Full new contents:

```bash
#!/bin/bash
# Long-running lab mutator: applies the hourly target topology by
# reconciling veth links between always-on base nodes. Emits structured
# JSON events on stdout for Alloy to ship to Loki.
set -uo pipefail

TOPODIR="/work/topologies"
BASE_TOPO="/work/base.clab.yml"
STATE_FILE="/work/.last-topo"
NODES=(spine1 spine2 leaf1 leaf2 leaf3 leaf4)
LAB_NAME="nte-dynamic-1"
TOPOS=("topo-1.yml" "topo-2.yml" "topo-3.yml" "topo-4.yml")

# Emit a JSON line. First arg is the event name, remaining args are key=value
# pairs that become string fields. Numeric and boolean values must be passed
# as raw JSON via the prefix '!' (e.g., 'duration_s=!2.13').
emit() {
  local event="$1"; shift
  local ts; ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  local out="{\"time\":\"$ts\",\"event\":\"$event\""
  while [ "$#" -gt 0 ]; do
    local kv="$1"; shift
    local key="${kv%%=*}"
    local val="${kv#*=}"
    if [[ "$val" == "!"* ]]; then
      out+=",\"$key\":${val#!}"
    else
      val=$(printf '%s' "$val" | sed 's/\\/\\\\/g; s/"/\\"/g')
      out+=",\"$key\":\"$val\""
    fi
  done
  out+="}"
  printf '%s\n' "$out"
}

# Self-heal: if any base node is missing or not running, redeploy the base lab
# AND clear the state file so the next mutation does a full rebuild.
self_heal_if_needed() {
  local missing=()
  for n in "${NODES[@]}"; do
    local cname="clab-$LAB_NAME-$n"
    if ! docker inspect -f '{{.State.Running}}' "$cname" 2>/dev/null | grep -q true; then
      missing+=("$n")
    fi
  done
  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi
  emit self_heal_triggered "missing_count=!${#missing[@]}" "missing=${missing[*]}"
  local t0; t0=$(date +%s)
  containerlab destroy -t "$BASE_TOPO" --cleanup >/dev/null 2>&1 || true
  if containerlab deploy -t "$BASE_TOPO" 2>&1 >/dev/null; then
    : > "$STATE_FILE"    # truncate; next apply_topology will treat current as empty
    emit self_heal_success "duration_s=!$(( $(date +%s) - t0 ))"
    return 0
  else
    emit self_heal_failed "duration_s=!$(( $(date +%s) - t0 ))"
    return 1
  fi
}

# Parse a topology YAML's `links:` block into canonical "a:eth|b:eth" lines,
# one per line, sorted. Endpoints in each pair are lexicographically ordered
# so set operations work regardless of source ordering.
parse_links() {
  local file="$1"
  [ -r "$file" ] || return 0
  yq -r '.topology.links[].endpoints | (.[0] + "|" + .[1])' "$file" 2>/dev/null | \
    awk -F'|' '{ a=$1; b=$2; if (a < b) print a"|"b; else print b"|"a }' | sort -u
}

# Apply one mutation. $1 is the target topology filename (basename).
apply_topology() {
  local topo="$1"
  local file="$TOPODIR/$topo"
  local t0; t0=$(date +%s)

  emit mutation_start "topo=$topo"

  if [ ! -r "$file" ]; then
    emit mutation_failed "topo=$topo" "phase=read" "error=topology file not readable: $file"
    return 1
  fi

  # "Current" = whatever the last applied topology said. Empty if cold start.
  local prev=""
  [ -r "$STATE_FILE" ] && prev=$(tr -d '\n\r ' < "$STATE_FILE")
  local cur_file=""
  [ -n "$prev" ] && cur_file="$TOPODIR/$prev"

  local cur des add del keep
  cur=$(parse_links "$cur_file")
  des=$(parse_links "$file")
  add=$(comm -13 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))
  del=$(comm -23 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))
  keep=$(comm -12 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))

  local n_add n_del n_keep
  n_add=$(printf '%s' "$add" | grep -c .)
  n_del=$(printf '%s' "$del" | grep -c .)
  n_keep=$(printf '%s' "$keep" | grep -c .)

  # Delete first so freed eth slots are available for reuse on add.
  while IFS= read -r link; do
    [ -z "$link" ] && continue
    local left right node iface
    left="${link%%|*}"; right="${link#*|}"
    node="${left%%:*}"; iface="${left#*:}"
    if docker exec "clab-$LAB_NAME-$node" ip link del "$iface" >/dev/null 2>&1; then
      emit link_removed "a=$left" "z=$right"
    else
      emit link_remove_failed "a=$left" "z=$right"
    fi
  done <<< "$del"

  local add_failures=0
  while IFS= read -r link; do
    [ -z "$link" ] && continue
    local a z
    a="${link%%|*}"; z="${link#*|}"
    if containerlab tools veth create --name "$LAB_NAME" --a-endpoint "$a" --z-endpoint "$z" >/dev/null 2>&1; then
      emit link_added "a=$a" "z=$z"
    else
      emit link_add_failed "a=$a" "z=$z"
      add_failures=$((add_failures + 1))
    fi
  done <<< "$add"

  local dt=$(( $(date +%s) - t0 ))
  if [ "$add_failures" -eq 0 ]; then
    # Record success in the state file so the next mutation diffs from here.
    printf '%s\n' "$topo" > "$STATE_FILE"
    emit mutation_success "topo=$topo" "prev=$prev" "duration_s=!$dt" "links_added=!$n_add" "links_removed=!$n_del" "links_kept=!$n_keep"
  else
    emit mutation_failed "topo=$topo" "phase=apply" "duration_s=!$dt" "add_failures=!$add_failures"
    return 1
  fi
}

# --- main loop ---
emit mutator_starting "nodes=${NODES[*]}" "topos=${TOPOS[*]}"

self_heal_if_needed || emit mutator_self_heal_giving_up

LAST_HOUR=-1
while true; do
  HOUR=$(date -u +%H)
  if [ "$HOUR" != "$LAST_HOUR" ]; then
    INDEX=$(( 10#$HOUR % ${#TOPOS[@]} ))
    TOPO=${TOPOS[$INDEX]}
    apply_topology "$TOPO" || true
    LAST_HOUR="$HOUR"
  fi
  sleep 60
done
```

Notes for the implementer:
- `set -e` is intentionally **not** set; we want individual link failures to emit a `*_failed` event rather than crash the whole loop.
- The bash version on Alpine 3.x (installed via `apk add bash`) is 5.x and supports `[[ ]]`, `${var%%pat}`, and associative arrays. Verify with `bash --version` inside the running mutator container if you hit syntax errors.
- `comm` is provided by busybox on Alpine — no extra package needed. Verify with `comm --version 2>&1 | head -1`.
- The `.last-topo` file lives in the mutator's `/work` (mounted from `deploy/long-running-test/.`). It must be gitignored.

- [ ] **Step 2: Add .last-topo to .gitignore**

Add a line to `.gitignore`:

```
deploy/long-running-test/.last-topo
```

A clean place to add it is right after the existing `deploy/long-running-test/.env` line.

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
grep -q "long-running-test/.last-topo" .gitignore || \
  printf '\ndeploy/long-running-test/.last-topo\n' >> .gitignore
grep "last-topo" .gitignore
```

Expected: prints `deploy/long-running-test/.last-topo`.

- [ ] **Step 3: Make it executable**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
chmod +x deploy/long-running-test/mutate.sh
```

- [ ] **Step 4: Lint with shellcheck if available**

```bash
which shellcheck && shellcheck deploy/long-running-test/mutate.sh || echo "shellcheck not installed, skipping"
```

Expected: either shellcheck passes with at most informational notes, or shellcheck is absent (skipped). Address any errors before continuing.

- [ ] **Step 5: Verify the JSON emitter shape with a dry run**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter/deploy/long-running-test
bash -c '
emit() {
  local event="$1"; shift
  local ts="2026-05-22T23:00:00Z"
  local out="{\"time\":\"$ts\",\"event\":\"$event\""
  while [ "$#" -gt 0 ]; do
    local kv="$1"; shift
    local key="${kv%%=*}"
    local val="${kv#*=}"
    if [[ "$val" == "!"* ]]; then
      out+=",\"$key\":${val#!}"
    else
      val=$(printf "%s" "$val" | sed "s/\\\\/\\\\\\\\/g; s/\"/\\\\\"/g")
      out+=",\"$key\":\"$val\""
    fi
  done
  out+="}"
  printf "%s\n" "$out"
}
emit mutation_start topo=topo-3
emit mutation_success topo=topo-3 duration_s=!2.1 links_added=!3 links_removed=!2 links_kept=!2
' | python3 -m json.tool --json-lines
```

Expected: two valid JSON objects printed in indented form.

- [ ] **Step 6: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/mutate.sh .gitignore
git commit -m "feat(long-running-test): rewrite mutator as link reconciler with JSON events

Replaces hourly destroy-and-redeploy with veth link reconciliation
between always-on base nodes. Uses a state file (.last-topo, gitignored)
to track the most recently applied topology so successive mutations
diff yaml-to-yaml rather than introspect OS state (busybox ip in the
testnodes is too limited for reliable peer enumeration). Emits
structured JSON events per phase for Alloy to ship to Loki. Self-heals
if base nodes go missing."
```

---

## Task 7: Update deploy.sh — pre-deploy the base lab

The `docker compose up` no longer triggers `containerlab` to create the bridge — the mutator manages links, not the lab. Deploy.sh runs the one-time base lab bring-up before compose.

**Files:**
- Modify: `deploy/long-running-test/deploy.sh`

- [ ] **Step 1: Rewrite deploy.sh**

Full new contents:

```bash
#!/bin/bash
set -euo pipefail

# Long-running-lab deploy. Pushes this directory to the remote host,
# brings up the base containerlab lab (once), then starts the compose
# stack. Re-runnable: clab destroy is idempotent.
#
# CAVEAT: this script is pinned to the colinwood homelab. Parameterize
# REMOTE_HOST / REMOTE_USER / REMOTE_DIR before sharing.

REMOTE_HOST="${REMOTE_HOST:-macbookpro-2015}"
REMOTE_USER="${REMOTE_USER:-ansible}"
REMOTE_DIR="${REMOTE_DIR:-/home/ansible/long-running-test}"

# 1) Sync (exclude SSH keys and local env)
ssh "$REMOTE_USER@$REMOTE_HOST" "mkdir -p $REMOTE_DIR"
rsync -avz \
  --exclude 'deploy.sh' \
  --exclude 'id_ed25519' \
  --exclude 'id_ed25519.pub' \
  --exclude '.env' \
  . "$REMOTE_USER@$REMOTE_HOST:$REMOTE_DIR/"

# 2) Bring up the base lab (idempotent)
ssh "$REMOTE_USER@$REMOTE_HOST" "cd $REMOTE_DIR && sudo containerlab deploy -t base.clab.yml --reconfigure"

# 3) Start the compose stack
ssh "$REMOTE_USER@$REMOTE_HOST" "cd $REMOTE_DIR && docker compose --env-file .env up -d"
```

Changes vs the previous version:
- Added `set -euo pipefail`.
- Removed the AI-slop commentary block.
- Added `--exclude id_ed25519` and `--exclude .env`.
- Replaced the "build the image on the remote" step (which assumed paths that don't exist) with a `containerlab deploy` of the base lab.
- Parameterized host / user / dir via env vars with sensible defaults.

- [ ] **Step 2: Lint**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
which shellcheck && shellcheck deploy/long-running-test/deploy.sh || echo "shellcheck not installed, skipping"
```

Expected: clean output or "skipping".

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/deploy.sh
git commit -m "fix(long-running-test): pre-deploy base lab; exclude key+env from rsync

Adds set -euo pipefail, drops the AI-slop commentary block, and ensures
the base lab is brought up before docker compose starts the mutator."
```

---

## Task 8: Deploy and verify AC1 — exporter discovers ≥5 devices within 3 minutes

The mutator is rewritten and the network model is new. Now we actually deploy and watch the lab come up.

- [ ] **Step 1: Tear down the existing broken state on macbookpro-2015**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
cd /home/ansible/long-running-test
docker compose down -v 2>/dev/null || true
sudo containerlab destroy --all --cleanup 2>&1 | tail -10
docker ps --filter "name=clab-nte-dynamic-1" --format "{{.Names}}"
'
```

Expected: the final `docker ps` lists no `clab-nte-dynamic-1-*` containers.

- [ ] **Step 2: Build nte-testnode:latest on macbookpro-2015 with the patched start.sh**

The patched start.sh is in this repo. Either rsync it to the remote and build there, or build locally and ship the image. Use the simpler "build remote" path:

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 'ls /home/ansible/long-running-test/' 2>&1
```

If this is a fresh deploy and the testnode image is missing, you'll need to scp the testnode dir. From the local repo root:

```bash
scp -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E -r tests/e2e/testnode \
  ansible@macbookpro-2015:/home/ansible/
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
cd /home/ansible/testnode
docker build -t nte-testnode:latest .
docker image inspect nte-testnode:latest --format "{{.Created}}"
'
```

Expected: the inspect prints a recent timestamp.

- [ ] **Step 3: Run deploy.sh from this repo**

From your local workstation in the repo:

```bash
cd /Users/colin/Code/grafana/network-topology-exporter/deploy/long-running-test
# The .env file MUST exist locally with the GRAFANA_* tokens. If missing:
# cp .env.example .env && $EDITOR .env
bash deploy.sh
```

Expected: rsync completes, `containerlab deploy` succeeds, compose `up -d` returns. No errors.

- [ ] **Step 4: Verify all 6 base nodes are running with pinned IPs**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
sudo containerlab inspect -t /home/ansible/long-running-test/base.clab.yml
'
```

Expected: a table with 6 rows: `clab-nte-dynamic-1-spine1` at `172.30.0.11`, `spine2` at `172.30.0.12`, `leaf1` at `172.30.0.21`, ..., `leaf4` at `172.30.0.24`. All `State = running`.

- [ ] **Step 5: Wait 90 seconds, then verify the exporter discovers ≥5 devices**

```bash
sleep 90
GRAFANA_TOKEN=$(head -1 /Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token | tr -d '\n\r ')
curl -s -G "https://networko11ydev.grafana.net/api/datasources/proxy/uid/grafanacloud-prom/api/v1/query" \
  --data-urlencode 'query=count(network_topology_device_info{tester_id="long-running-lab"})' \
  -H "Authorization: Bearer $GRAFANA_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
res=d.get('data',{}).get('result',[])
if res:
  print('discovered devices:', res[0]['value'][1])
else:
  print('NO DATA YET')
"
```

Expected: `discovered devices: 5` (or 6 depending on the current hour's topology — `topo-1/2/3` leave spine2 idle so it returns no `device_info` series unless the SNMP system-group walk succeeds anyway; CLOS leaves leaf4 idle).

Accept ≥5. If 0 after 3 minutes, fall through to Task 11 (debug).

---

## Task 9: Verify AC4 — mutator events reach Loki

- [ ] **Step 1: Look at the live mutator stdout to confirm JSON shape**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker logs long-running-mutator --tail 20 2>&1
'
```

Expected: lines starting with `{"time":"...","event":"mutator_starting"...}`, then `{"time":"...","event":"mutation_start","topo":"topo-N"}`, then per-link events, then `{"time":"...","event":"mutation_success",...}`.

- [ ] **Step 2: Query Loki for the events**

```bash
GRAFANA_TOKEN=$(head -1 /Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token | tr -d '\n\r ')
LOKI="https://networko11ydev.grafana.net/api/datasources/proxy/uid/grafanacloud-logs/loki/api/v1"
curl -s -G "$LOKI/query_range" \
  --data-urlencode 'query={job="long-running-mutator",tester_id="long-running-lab"}' \
  --data-urlencode 'limit=5' \
  --data-urlencode "start=$((($(date +%s) - 600) * 1000000000))" \
  --data-urlencode "end=$(($(date +%s) * 1000000000))" \
  -H "Authorization: Bearer $GRAFANA_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
res=d.get('data',{}).get('result',[])
print('streams:', len(res))
for s in res:
  print(' labels:', s.get('stream',{}))
  for ts,line in s.get('values',[])[:5]:
    print('  ', line[:160])
"
```

Expected: at least one stream with labels including `job=long-running-mutator` and `tester_id=long-running-lab`; values are the JSON event lines.

If `streams: 0`, that means the Alloy relabel filter isn't matching. Revisit Task 5's regex and re-deploy.

---

## Task 10: Verify AC2 + AC3 — mutation does not restart containers; change_total increments

- [ ] **Step 1: Record current container start times**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
for n in spine1 spine2 leaf1 leaf2 leaf3 leaf4; do
  docker inspect "clab-nte-dynamic-1-$n" --format "{{.Name}} {{.State.StartedAt}}"
done > /tmp/start-times-before.txt
cat /tmp/start-times-before.txt
'
```

- [ ] **Step 2: Force a mutation by setting LAST_HOUR=-1 in the running container**

The simplest way is to restart the mutator (single-container restart):

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker restart long-running-mutator
sleep 30
docker logs long-running-mutator --tail 30
'
```

Expected: a fresh `mutator_starting` event, a self-heal pass (probably no missing nodes → success), a `mutation_start`, then `link_added`/`link_removed` events, then `mutation_success`.

- [ ] **Step 3: Verify container start times are unchanged**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
for n in spine1 spine2 leaf1 leaf2 leaf3 leaf4; do
  docker inspect "clab-nte-dynamic-1-$n" --format "{{.Name}} {{.State.StartedAt}}"
done > /tmp/start-times-after.txt
diff /tmp/start-times-before.txt /tmp/start-times-after.txt && echo "UNCHANGED" || echo "CHANGED"
'
```

Expected: prints `UNCHANGED`.

- [ ] **Step 4: Query change_total**

```bash
GRAFANA_TOKEN=$(head -1 /Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token | tr -d '\n\r ')
PROM="https://networko11ydev.grafana.net/api/datasources/proxy/uid/grafanacloud-prom/api/v1"
sleep 60   # let the exporter cycle pick up the new state
curl -s -G "$PROM/query" \
  --data-urlencode 'query=sum by (change_kind)(increase(network_topology_change_total{tester_id="long-running-lab"}[10m]))' \
  -H "Authorization: Bearer $GRAFANA_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for r in d.get('data',{}).get('result',[]):
  print(r['metric'].get('change_kind','?'), '=', r['value'][1])
"
```

Expected: at least one of `added` or `removed` has a non-zero value.

If both are zero, the exporter cycle hasn't picked up the new state yet — wait another minute and retry. If still zero after 3 retries, debug the cycle.

---

## Task 11 (conditional): Debug if AC1 fails

Only run this task if Task 8 step 5 reported fewer than 5 devices after 3 minutes.

- [ ] **Step 1: Check exporter logs**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker logs long-running-exporter --tail 30 2>&1
'
```

Look for: `snmp walk failed`. If `error: context deadline exceeded`, network reachability. If `connection refused`, SNMP daemon not bound. If `authentication failure`, community string mismatch.

- [ ] **Step 2: Verify reachability from exporter container to a node**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker exec long-running-exporter sh -c "
  for ip in 172.30.0.11 172.30.0.12 172.30.0.21 172.30.0.22 172.30.0.23 172.30.0.24; do
    echo -n \"$ip: \"; nc -uvz -w 2 \$ip 161 2>&1 | tail -1
  done
"
'
```

Expected: each line ends with `succeeded` or `open`. If `Connection refused`, the testnode container booted but snmpd died — check `docker exec clab-nte-dynamic-1-spine1 ps aux`. If `No route to host`, the exporter container can't reach the lab subnet — verify Docker iptables rules allow forwarding (`sudo iptables -L DOCKER-USER -n`).

- [ ] **Step 3: Verify SNMP walk from inside the exporter container**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker exec long-running-exporter snmpwalk -v2c -c public 172.30.0.11 system 2>&1 | head -5
'
```

If `snmpwalk` isn't in the exporter image, run from a temporary debug container:

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker run --rm --network long-running-test_default alpine:latest sh -c "
  apk add --no-cache net-snmp-tools >/dev/null 2>&1 && snmpwalk -v2c -c public 172.30.0.11 system
" 2>&1 | head -10
'
```

Expected: SNMP system group OIDs print. If `Timeout`, the lab subnet is unreachable from `long-running-test_default`. The fix: attach the exporter to a network that *does* reach `172.30.0.0/24`, either by joining the `nte-dynamic-clab` network or by adding a static route.

- [ ] **Step 4: If reachability fails, attach exporter to nte-dynamic-clab as well**

This is the fallback if Docker's bridge-to-bridge routing turns out not to be enabled on this host. Edit `docker-compose.yml` to add:

```yaml
services:
  topology-exporter:
    ...
    networks:
      - default
      - nte-dynamic-clab
  alloy:
    ...
    networks:
      - default
      - nte-dynamic-clab

networks:
  nte-dynamic-clab:
    external: true
```

The bridge name is what `clab` created for the base lab — verify with `docker network ls | grep nte-dynamic`. Commit, redeploy compose, retry Task 8 step 5.

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/docker-compose.yml
git commit -m "fix(long-running-test): attach exporter+alloy to nte-dynamic-clab network

Default Docker bridge-to-bridge routing was not enabled on the host;
explicit network attachment required for the exporter to reach lab IPs."
```

---

## Task 12: Update the README

The mental model changed — the README needs to match.

**Files:**
- Modify: `deploy/long-running-test/README.md`

- [ ] **Step 1: Rewrite the "Stack" and "Mutation schedule" and "Bring-up" sections**

Replace the existing Stack table with:

```markdown
## Stack

| Container             | Image                          | Purpose                                                  |
| --------------------- | ------------------------------ | -------------------------------------------------------- |
| `clab-nte-dynamic-1-{spine1,spine2,leaf1..leaf4}` | `nte-testnode:latest` | The lab itself: 6 always-on nodes with pinned mgmt IPs in `172.30.0.0/24`. |
| `long-running-exporter` | `network-topology-exporter:latest` | Discovers the lab, exposes `/metrics`                  |
| `long-running-alloy`    | `grafana/alloy:latest`         | Scrapes the exporter and all `long-running-*` container logs; ships metrics/logs/traces to Grafana Cloud |
| `long-running-mutator`  | `alpine:latest` + `clab`       | Reconciles veth links between the 6 base nodes every UTC hour; emits structured JSON events to stdout |
```

Replace the "Mutation schedule" section with:

```markdown
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
```

Replace the "Bring-up" section:

```markdown
## Bring-up

```bash
cp .env.example .env
$EDITOR .env

bash deploy.sh    # rsyncs, brings up base lab, starts compose
```

`deploy.sh` is idempotent: rerun it any time the base lab or compose
stack needs a refresh. Use `REMOTE_HOST=...`, `REMOTE_USER=...`, and
`REMOTE_DIR=...` env vars to target a host other than the default.
```

Delete the "Known limitations" entries that referenced the old destroy-and-redeploy model. Replace with:

```markdown
## Known limitations

- `deploy.sh` rsyncs the working tree but does *not* push the
  `nte-testnode:latest` image — build it on the remote host once.
- The mutator does not retry on failure; the next hour boundary
  triggers a fresh attempt. The most recent `mutation_failed` event
  in Loki is the current failure signal.
- All four topology files share the lab name `nte-dynamic-1`. The
  mutator addresses links by node name (`spine1:eth1`), so this is
  fine — but `clab inspect` always points at `base.clab.yml`.
```

- [ ] **Step 2: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add deploy/long-running-test/README.md
git commit -m "docs(long-running-test): document static-nodes-dynamic-links model"
```

---

## Task 13: Update CHANGELOG

- [ ] **Step 1: Replace the long-running-lab CHANGELOG entry**

Open `CHANGELOG.md`. Under `## Unreleased`, replace the existing **Long-running validation lab** bullet with:

```markdown
- **Long-running validation lab** — Added `deploy/long-running-test/`, a
  continuously-running harness. Six containerlab nodes (`spine1, spine2,
  leaf1..leaf4`) deploy once with pinned management IPs in a dedicated
  `172.30.0.0/24` subnet. An hourly mutator reconciles veth links
  between them per topology (`topo-1` chain → `topo-2` cross-link →
  `topo-3` ring → `topo-4` CLOS) without restarting containers. The
  exporter sees real edge add/remove events on the hour boundary,
  exercising the same reconciliation code path it exists to validate.
  Mutator emits structured JSON events to stdout; Alloy ships them to
  Loki labelled `tester_id=long-running-lab, job=long-running-mutator`.
```

- [ ] **Step 2: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add CHANGELOG.md
git commit -m "docs(changelog): update long-running-lab entry to reflect new model"
```

---

## Task 14: Add Mutator Events panel to harness-health dashboard

This step is partially manual (Grafana UI). The repo-side change is just pulling the updated dashboard back.

- [ ] **Step 1: Add the panel via Grafana UI**

In a browser, open https://networko11ydev.grafana.net/d/harness-health/01-test-harness-health and:

1. Click "Add" → "Visualization".
2. Set the datasource to `grafanacloud-networko11ydev-logs` (Loki).
3. Query:
   ```logql
   {job="long-running-mutator", tester_id="long-running-lab"} | json
   ```
4. Visualization type: **Logs**.
5. Panel title: `Mutator Events`.
6. Drag to the top row, full width.
7. Save dashboard.

- [ ] **Step 2: Pull the updated dashboard JSON back into the repo**

```bash
GRAFANA_TOKEN=$(head -1 /Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token | tr -d '\n\r ')
STACK_URL="https://networko11ydev.grafana.net"
cd /Users/colin/Code/grafana/network-topology-exporter
curl -s -H "Authorization: Bearer $GRAFANA_TOKEN" \
  "$STACK_URL/api/dashboards/uid/harness-health" | \
  python3 -c "
import json,sys
raw=json.load(sys.stdin)
d=raw.get('dashboard',raw)
d['id']=None
json.dump(d, open('dashboards/test-harness/harness-health.json','w'), indent=2)
open('dashboards/test-harness/harness-health.json','a').write('\n')
print('wrote, version=', d.get('version'), 'panels=', len(d.get('panels',[])))
"
```

Expected: prints `wrote, version=N panels=M` with M ≥ previous panel count + 1.

- [ ] **Step 3: Commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add dashboards/test-harness/harness-health.json
git commit -m "feat(dashboards): add Mutator Events panel to harness health"
```

---

## Task 15: Final verification pass against acceptance criteria

- [ ] **Step 1: AC1 — exporter discovers ≥5 devices**

(Already verified in Task 8.) Re-check now that everything is stable:

```bash
GRAFANA_TOKEN=$(head -1 /Users/colin/Code/grafana/network-o11y-demo/grafana-cloud-api.token | tr -d '\n\r ')
curl -s -G "https://networko11ydev.grafana.net/api/datasources/proxy/uid/grafanacloud-prom/api/v1/query" \
  --data-urlencode 'query=count(network_topology_device_info{tester_id="long-running-lab"})' \
  -H "Authorization: Bearer $GRAFANA_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('devices:', d.get('data',{}).get('result',[{}])[0].get('value',[None,'0'])[1])
"
```

Expected: devices: 5 or 6.

- [ ] **Step 2: AC6 — IPs are stable across container restart**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker restart clab-nte-dynamic-1-leaf1
sleep 5
docker inspect clab-nte-dynamic-1-leaf1 --format "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}"
'
```

Expected: `172.30.0.21` (unchanged).

- [ ] **Step 3: AC7 — coexists with arista-ceos-bgp**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
sudo containerlab inspect --all 2>&1 | head -40
'
```

Expected: both labs present, no IP conflicts. `nte-dynamic-1` on `172.30.0.0/24`, `arista-ceos-bgp` on `172.20.20.0/24`.

- [ ] **Step 4: AC8 — self-heal fires on missing node**

```bash
ssh -i /Users/colin/.ssh/homelab-ansible-0CB3BF5E ansible@macbookpro-2015 '
docker kill clab-nte-dynamic-1-leaf2
sleep 5
docker restart long-running-mutator  # restart so self-heal runs immediately
sleep 60
docker logs long-running-mutator --tail 20 2>&1 | grep -E "self_heal"
'
```

Expected: at least one line containing `self_heal_triggered` and one containing `self_heal_success`. After the self-heal, `docker ps --filter name=clab-nte-dynamic-1-leaf2` shows the container running again.

If self-heal succeeded, optionally restore the standard 1-hour mutation cadence by leaving the mutator running.

- [ ] **Step 5: Final tag check**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git log --oneline -10
```

Expected: 8–10 commits since the start of this plan, with messages matching the conventional-commit style used elsewhere in the repo.
