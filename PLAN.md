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
- [x] **Introduce Discovery Budget Controller**: `CycleBudgetFraction` (default 0.8) derives a `cycleCtx` deadline at `now + interval × fraction`; all module goroutines use `cycleCtx` so cycles are cancelled before the next interval begins.
  **Reference**: Queueing stability principle (`service_time < inter-arrival_time`) and Prometheus scrape SLO design.

- [ ] **Two-Phase Graph Assembly**: Separate raw observation ingest from canonical link synthesis (identity resolution, dedupe, confidence arbitration, and suppression of endpoint noise).
  **Reference**: NetInventory reconciliation model and RFC 8345 logical separation of nodes/links.

- [x] **Replace Global Fan-Out with Work-Stealing + Deadline Partitioning**: `TimeoutPerModule` (opt-in) wraps each module walk in `context.WithTimeout(devCtx, TimeoutPerModule)`; prevents one slow SNMP walk from consuming the full per-device budget.
  **Reference**: Tail-latency control (deadline partitioning) and bounded-concurrency scheduler design.

- [x] **Add Cardinality Budget Enforcement in CI**: `cardinality_test.go` hard-fails if 500-device graph exceeds 1,400 series. `schema_test.go` hard-fails on any metric rename/removal.

- [x] **Add Memory Residency Guardrails (partial)**: `network_topology_graph_edges_total` and `network_topology_graph_devices_total` gauges enable alerting on graph size; snapshot queue is bounded capacity-1. Circuit-breaker (refuse updates when over budget) remains unimplemented.
  **Reference**: Backpressure-first design for long-running collectors.

## 4. Standards & Compliance Checklist
- [x] **IEEE 802.1AB Compliance**: `buildEdges` validates chassis/port subtypes (1–7), MAC lengths (6 bytes), and network-address family prefix. Invalid entries logged at Debug and dropped.
- [x] **RFC 2922/1213 Compliance**: `docs/standards.md` documents all implemented MIB objects and explicitly declares RFC 2922 (PTOPO-MIB) as intentionally not implemented — vendors universally deploy LLDP instead, making PTOPO-MIB polling a no-op on all known production hardware. MIB-II `system` group and IF-MIB usage are documented. IF-MIB/MIB-II type enforcement in integration tests remains an open item.

## 5. Prometheus & Observability Polish
- [x] **Enforce Stable Metric Schema**: `schema_test.go` enumerates all 26 expected metric names via `Registry().Describe`; hard-fails on any rename/removal.
- [x] **Reduce High-Risk Labels**: FDB `dst_device` now emits `mac-<8hex>` SHA-256 surrogate instead of raw MAC string; raw MAC logged at Debug. Other label domains (sysName, ifName) are already controlled.
- [x] **Publish Scrape-Time SLOs**: `network_topology_last_scrape_duration_seconds` and `network_topology_last_scrape_samples_total` emitted by TopologyCollector.
- [x] **Add Degraded-State Truthfulness**: `network_topology_module_last_status{module}` gauge: 0=ok, 1=degraded, 2=failed; worst-case across all devices per cycle.
