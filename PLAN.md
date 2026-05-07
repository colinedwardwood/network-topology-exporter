# v1.2 Remediation Plan

## Objective

Close all production-readiness gaps identified in the research-backed review (2026-05-07) and ship `v1.2.0`.

## Rules for This Plan

- Track only unresolved work (no historical "done" sections).
- Every deficiency must map to code changes, tests, and a verification command.
- No item is considered complete until its acceptance criteria are met.

---

## Deficiency Register — HIGH

### D5 - OTLP push goroutines not tracked in shutdown WaitGroup

Risk:
- `otlpPush` spawns goroutines outside `workerDone`. At shutdown, in-flight pushes
  (up to 4 × 10 s) can race against a nil logger and unregistered metrics.

Required remediation:
- Add a dedicated `otlpWg sync.WaitGroup` to `loopConfig`.
- `otlpPush` calls `wg.Add(1)` before spawning; goroutine calls `defer wg.Done()`.
- `run()` calls `otlpWg.Wait()` after `workerDone.Wait()`.

Acceptance criteria:
- `go test -race` passes with no data-race reports involving `OTLPPushTotal`.
- Log line after "discovery loop exited" confirms in-flight pushes drained.

---

### D6 - Readiness probe uses /healthz instead of /readyz

Risk:
- `values.yaml` readinessProbe points to `/healthz` (always 200). Pods route
  traffic before the first snapshot cycle completes, producing empty topology data.

Required remediation:
- Change `readinessProbe.httpGet.path` to `/readyz` in `values.yaml`.

Acceptance criteria:
- `helm template` renders readinessProbe with path `/readyz`.
- `helm lint` passes.

---

### D7 - OTLP push has no retry/backoff on 429/503

Risk:
- Transient receiver overload causes permanent topology-event data loss. OTel
  spec (OTLP/HTTP §4.6) requires retry with exponential backoff on 429 and 503.
  The federation spoke's `Push` already implements this correctly; `otlp.post` does not.

Required remediation:
- Add 3-attempt exponential backoff (100 ms base, ×2, jitter) to `otlp/otlp.go post()`.
- Respect `Retry-After` header on 429.
- Only increment `OTLPPushTotal{error}` on final failure.

Acceptance criteria:
- Unit test: mock server returns 503 × 2 then 200; single `ok` counter increment.
- Unit test: mock server returns 429 with `Retry-After: 1`; second attempt delayed.

---

### D8 - Unbounded VLAN community FDB walk on large IOS switches

Risk:
- `walkVlanCommunityFdbs` opens one SNMP session per VLAN sequentially. On a
  campus switch with 1000 VLANs, this exhausts `TimeoutPerDevice` and starves
  other modules.

Required remediation:
- Add `fdb.max_vlans` config option (default 100, max 4096) to `FDBConfig`.
- Log a warning when the ceiling is hit.
- Document the trade-off in operator guide.

Acceptance criteria:
- Config with `max_vlans: 5` limits walk to first 5 VLANs in test.
- Config validation rejects `max_vlans: 0` and `max_vlans: 5000`.

---

### D9 - Public HTTP server missing ReadTimeout / WriteTimeout

Risk:
- A slow Prometheus scraper or attacker holds a goroutine open indefinitely.
  `/metrics` can be multi-megabyte; without `WriteTimeout` a slow-read client
  pins a connection for the process lifetime.

Required remediation:
- Set `ReadTimeout: 30 * time.Second` and `WriteTimeout: 60 * time.Second`
  on the public `http.Server` in `main.go` (mirror the hub server pattern).

Acceptance criteria:
- `go vet` / lint pass.
- Unit test: connect and stall; connection closes within `ReadTimeout`.

---

## Deficiency Register — MEDIUM

### D10 - BulkWalk context cancellation ineffective mid-walk

Risk:
- `gosnmp.BulkWalkAll` has no context parameter. A slow device holds the
  goroutine for the full GoSNMP `Timeout` after parent ctx is cancelled.
  The `SetDeadline(time.Now())` interrupt trick from the system GET is not applied.

Required remediation:
- After a pre-walk ctx check, launch `BulkWalkAll` in a goroutine; select on
  `ctx.Done()` and call `conn.SetDeadline(time.Now())` to interrupt it.

Acceptance criteria:
- Test: cancelled ctx causes BulkWalk to return within 2 × network RTT.

---

### D11 - BGP / OSPF / IS-IS edges use raw IP as DstDevice

Risk:
- BGP/OSPF/IS-IS set `DstDevice` to peer IP. LLDP sets it to sysName. Same
  physical link appears as two separate edges — deduplication in `Reconcile` misses it.

Required remediation:
- After each device walk, resolve discovered peer IPs to sysName using the
  `deviceInventory` map already built in `main.go`. Pass the resolver into
  `bgp.Walk`, `ospf.Walk`, `isis.Walk` or apply as a post-processing step.

Acceptance criteria:
- Test: device A (LLDP-visible) and device B (BGP peer only) → single canonical edge.

---

### D12 - OTLP resource missing service.version and service.instance.id

Risk:
- OTLP backends cannot distinguish topology data from different binary versions
  or differentiate hub vs. spoke instances without these required OTel attributes.

Required remediation:
- Add `service.version: version.Version` and `service.instance.id: hostname`
  to `serviceRes` in `otlp/otlp.go`.

Acceptance criteria:
- Unit test: `PushGraph` payload contains `service.version` and `service.instance.id`.

---

### D13 - No seccompProfile in default pod security context

Risk:
- Fails Kubernetes `Restricted` Pod Security Standard. Required for Grafana Cloud
  hosted environments.

Required remediation:
- Add `seccompProfile: {type: RuntimeDefault}` to `podSecurityContext` in `values.yaml`.

Acceptance criteria:
- `helm template | kubectl neat` shows seccomp annotation.
- Pod passes `Restricted` PSS admission.

---

### D14 - No PodDisruptionBudget template

Risk:
- During node drain, the pod is evicted without warning. First post-drain cycle
  takes a full discovery interval before `/readyz` recovers.

Required remediation:
- Add `templates/pdb.yaml` with `maxUnavailable: 0` default, `enabled: false`.
- Document in values comments.

Acceptance criteria:
- `helm template --set pdb.enabled=true` renders a valid PDB manifest.

---

### D15 - SNMP Retries hardcoded; SNMPv3 ContextName not configurable

Risk:
- Operators on lossy management networks cannot tune retries without a code change.
  Cisco VRF-aware management requires non-empty ContextName.

Required remediation:
- Add `retries` (default 1) to `CredentialProfile` / SNMP params.
- Add `context_name` to `CredentialProfile`.

Acceptance criteria:
- Config with `retries: 3` propagates to GoSNMP `Retries` field.
- Config with `context_name: "mgmt"` propagates to GoSNMP `ContextName`.

---

### D16 - os_version label uses raw sysDescr — TSDB churn on upgrades

Risk:
- Every OS patch creates a new label value, producing phantom metric churn in
  Prometheus TSDB and potentially hitting cardinality limits.

Required remediation:
- Normalise `sysDescr` to major version before using as label value, OR
  move to a separate `network_device_info` gauge-1 info metric pattern.

Acceptance criteria:
- Two devices differing only in patch version produce identical label values.

---

### D17 - Hub OOS FQDN normalisation can produce false merges

Risk:
- `normalizeDeviceName` strips after first dot. `core-sw-01.dc1` and
  `core-sw-01.dc2` (different devices from different spokes) normalise to
  the same name and are merged in the topology graph.

Required remediation:
- Normalise both directions (bare and FQDN) and log a warning if multiple
  raw names map to the same normalised name.

Acceptance criteria:
- Test: two spokes report the same base hostname with different domain suffixes;
  no silent merge; warning logged.

---

## Deficiency Register — LOW

### D18 - ISIS and OSPF share precedenceRank = 5

Assign IS-IS rank 5, OSPF rank 6 (or vice-versa with documented rationale).

### D19 - SnapshotLastWrittenUnix alert fires on cold start

Initialise gauge to `time.Now()` at startup or add `unless network_topology_graph_stale == 1` guard.

### D20 - `_total` suffix on GaugeVec `discovery_devices_total`

Rename to `network_topology_discovery_devices` (Prometheus naming convention).

### D21 - `enterprisePrefixes` map iteration non-deterministic

Replace with ordered `[]struct{prefix, vendor string}` slice.

### D22 - Snapshot temp file permissions rely on process umask

Explicitly `chmod 0600` after `os.CreateTemp`.

---

## Work Plan

### Workstream A — Correctness & Stability (D5, D7, D9, D10)

- [ ] D5: `otlpWg` shutdown drain
- [ ] D7: OTLP retry/backoff
- [ ] D9: `ReadTimeout` / `WriteTimeout` on public server
- [ ] D10: BulkWalk context interrupt via `SetDeadline`

### Workstream B — Configuration & Limits (D8, D15, D18)

- [ ] D8: `fdb.max_vlans` config + validation
- [ ] D15: `retries` + `context_name` in credential profile
- [ ] D18: distinct IS-IS / OSPF precedence ranks

### Workstream C — Helm / Deployment (D6, D13, D14)

- [ ] D6: readinessProbe → `/readyz`
- [ ] D13: `seccompProfile: RuntimeDefault`
- [ ] D14: optional PDB template

### Workstream D — Topology Quality (D11, D17)

- [ ] D11: IP→sysName resolution for BGP/OSPF/IS-IS edges
- [ ] D17: Hub FQDN normalisation warning

### Workstream E — OTLP / Metrics Hygiene (D12, D16, D19, D20, D21, D22)

- [ ] D12: `service.version` + `service.instance.id` in OTLP resource
- [ ] D16: sysDescr normalisation
- [ ] D19: cold-start alert guard
- [ ] D20: rename `discovery_devices_total` gauge
- [ ] D21: ordered enterprisePrefixes
- [ ] D22: explicit snapshot file chmod

### Workstream F — Release Gate (D4)

- [ ] `go test ./tests/integration/... -tags integration`
- [ ] `make e2e-image && CLAB_DOCKER=1 make test-e2e`

---

## Exit Criteria (Ship Blockers for v1.2.0)

- [ ] D5-D9 (HIGH) all closed with merged tests.
- [ ] D10-D17 (MEDIUM) addressed or explicitly deferred with rationale.
- [ ] Release gate (D4 / Workstream F) passes in full environment.
- [ ] Changelog updated.
