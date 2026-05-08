# Engineering Improvement Plan: Network Topology Exporter

## 1. Executive Summary
The exporter is not public-release ready for enterprise use because it can still collapse under predictable scale conditions (especially FDB cardinality and long discovery cycles), and several implementation choices contradict its own hardening claims. The project has strong intent and good module boundaries, but today it is one adversarial topology or one large campus core away from becoming operationally expensive and publicly embarrassing.

## 2. Immediate Remediation (High Priority)
- [x] **Remove Edge-Per-MAC Topology Explosion**: Indirect ports (multi-MAC) suppressed entirely; only direct ports (single MAC) emit edges.
  **File**: `internal/discovery/fdb/fdb.go`.

- [x] **Kill Insecure `public` Community Fallback**: Remove the automatic fallback to SNMP community `public` when profile/env is missing; fail closed at startup.
  **Rationale**: Default-accepting weak credentials is indefensible under public scrutiny and invites accidental insecure deployments.
  **File**: `cmd/topology-exporter/main.go` (legacy fallback path in `credentialCandidates`, lines 763-778).

- [x] **Fix Metric Contract Drift**: Reconcile metric name mismatch between docs and implementation (`network_topology_discovery_devices_total` vs `network_topology_discovery_devices`), and lock with tests.
  **Rationale**: Public users will call this a broken observability contract; dashboards and alerts will silently fail.
  **File**: `README.md` (metrics table, lines 53-56), `internal/metrics/metrics.go` (metric declaration, lines 96-99).

- [x] **Bound Snapshot Writer Concurrency**: Single bounded writer goroutine + capacity-1 channel; excess writes dropped with Warn log.
  **File**: `cmd/topology-exporter/main.go`, `internal/federation/hub.go`.

- [x] **Stop Silent Error Suppression in Discovery**: Q-BRIDGE and VLAN community walk failures now logged at Debug.
  **File**: `internal/discovery/fdb/fdb.go`.

- [x] **Harden Label Input Surface**: `sanitizeLabel()` strips non-printable runes and caps at 128 chars on all SNMP-derived label values in `Collect()`.
  **File**: `internal/metrics/topology_collector.go`.

### Additional fixes shipped (from adversarial review, not in original plan)
- [x] **Hub publishMetrics lost-update race**: Added generation counter + CAS-based `tryPublishMetrics` so a slow goroutine starting from an older spoke snapshot cannot overwrite a newer combined graph.
  **File**: `internal/federation/hub.go`
- [x] **OTLP metadata double-prefix**: `MetadataKeyDegraded`/`MetadataKeyDegradedReason` bare keys now (`"degraded"`, `"degraded_reason"`); OTLP exporter adds `"network.topology."` prefix once.
  **File**: `internal/discovery/discovery.go`
- [x] **Hub device dedup absent**: `buildCombinedGraph` now deduplicates devices by ID; prevents duplicate `network_device_info` Prometheus series when spokes share border routers.
  **File**: `internal/federation/hub.go`

## 3. Architectural & Scaling Overhaul
- [ ] **Introduce Discovery Budget Controller**: Add cycle budget enforcement (`max_cycle_fraction`), per-module budget, and adaptive target throttling when projected runtime exceeds interval.
  **Reference**: Queueing stability principle (`service_time < inter-arrival_time`) and Prometheus scrape SLO design.

- [ ] **Two-Phase Graph Assembly**: Separate raw observation ingest from canonical link synthesis (identity resolution, dedupe, confidence arbitration, and suppression of endpoint noise).
  **Reference**: NetInventory reconciliation model and RFC 8345 logical separation of nodes/links.

- [ ] **Replace Global Fan-Out with Work-Stealing + Deadline Partitioning**: Keep fixed workers, but assign per-target module deadlines to prevent one slow module from consuming full `timeout_per_device`.
  **Reference**: Tail-latency control (deadline partitioning) and bounded-concurrency scheduler design.

- [ ] **Add Cardinality Budget Enforcement in CI**: Create hard fail tests that cap worst-case series count for synthetic 500/1000-device datasets.
  **Reference**: Prometheus instrumentation best practice: cardinality budgets as testable contracts.

- [ ] **Add Memory Residency Guardrails**: Track and alarm on graph size, edge churn, snapshot queue depth, and stale goroutine counts; refuse updates when over budget.
  **Reference**: Backpressure-first design for long-running collectors.

## 4. Standards & Compliance Checklist
- [ ] **IEEE 802.1AB Compliance**: Add explicit LLDP row validity checks covering mandatory semantics (Chassis ID, Port ID, TTL interpretation/liveness policy), and reject malformed subtype/address encodings with audited counters.
- [ ] **RFC 2922/1213 Compliance**: Add a standards matrix documenting implemented MIB objects vs missing Physical Topology MIB coverage; either implement RFC 2922 object support or clearly declare non-support and fallback strategy. Verify MIB-II/IF-MIB object handling and type enforcement in integration tests.

## 5. Prometheus & Observability Polish
- [ ] **Enforce Stable Metric Schema**: Align implementation/docs, add promtool schema tests, and add a changelog gate requiring explicit migration notes for any metric rename/removal.
- [ ] **Reduce High-Risk Labels**: Remove or bucket labels with uncontrolled value domains; where identity is unavoidable, provide hashed surrogate labels and emit raw values only in logs.
- [ ] **Publish Scrape-Time SLOs**: Add exporter self-metrics for render duration and sample count per scrape and alert when approaching scrape timeout.
- [ ] **Add Degraded-State Truthfulness**: For every suppressed or partial module result, emit one explicit status metric (`module_status{state="degraded|failed|ok"}`) so operators can trust edge absence.
