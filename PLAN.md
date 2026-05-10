# Engineering Improvement Plan: Network Topology Exporter

## Open Items

### Architectural & Scaling

- [ ] **Add Graph Size Admission Control Outside Hub Mode**: `FederationHubConfig` has `MaxGraphDevices`/`MaxGraphEdges` but standalone and spoke cycles update the graph unconditionally. Add `max_graph_devices`/`max_graph_edges` to `DiscoveryConfig` and enforce before `lc.m.Topology.Update(newGraph)` in the cycle loop in `cmd/topology-exporter/main.go`.

- [ ] **Make Discovery Budgeting Observable**: `cycle_budget_fraction` and `timeout_per_module` exist and work, but there is no counter for targets cancelled by the budget deadline. Add `network_topology_cycle_budget_skips_total` so operators can see how often devices are being dropped from cycles.

- [ ] **Prove Reconciliation Complexity With Synthetic Scale Tests**: `graph.Reconcile` is approximately `O(E log E)`. Add `BenchmarkReconcile` benchmarks in `internal/graph/graph_test.go` with 100, 1 000, and 10 000 edges to make the contract executable rather than asserted in comments.

- [ ] **Add Cardinality Budget Enforcement in CI**: Generate a synthetic 500-device, 1 000-device, and 5 000-device graph and fail the test when the total `prometheus.Gather` sample count exceeds the declared budget per device.

- [ ] **Add Memory Residency Guardrails**: Add runtime gauges for goroutine count and snapshot queue depth. `network_topology_graph_devices_total` and `network_topology_graph_edges_total` already exist; what's missing is snapshot queue depth and goroutine count under stalled IO.

- [ ] **Replace FDB Edge Emission With Identity-Gated Link Synthesis**: FDB should produce raw observations, not public topology edges, unless the MAC can be correlated to known infrastructure through LLDP chassis MAC, ARP/IP-MIB, ENTITY-MIB, or explicit operator inventory. This is a deep architectural rework (two-phase assembly boundary); scope and design decision required before implementation.

### Standards & Compliance

- [ ] **RFC 2863/IF-MIB: Deterministic ifName Fallback**: `WalkIfNames` fails hard if the agent does not implement ifXTable. CDP and FDB then return an error instead of degrading gracefully. Fall back to `WalkIfDescr` (ifTable.ifDescr), then to the numeric string `"if{ifIndex}"`. `ifDescr` can produce duplicates across module boundaries on chassis devices — document the limitation.
  **Files**: `internal/discovery/cdp/cdp.go:64`, `internal/discovery/fdb/fdb.go:158`, `internal/discovery/snmp/pdu.go:153`.

- [ ] **RFC 2922/1213 Standards Matrix**: Publish `docs/operator/standards-compliance.md` documenting: RFC 2922 PTOPO-MIB is not implemented (LLDP is the practical replacement), RFC 1213 sysUpTime 497-day rollover behavior (currently undocumented — emit warning log at wrap, or document explicitly), and what operators lose by not having PTOPO-MIB.

- [ ] **sysUpTime Rollover Strategy**: `sysUpTime` wraps at 2^32 centiseconds (~497 days). The current code does not detect or warn on wrap. Either: (a) log a Warn when computed uptime decreases for a device that was previously seen, or (b) document the rollover behavior prominently in docs.

### Prometheus & Observability

- [ ] **Bound Decode-Issue OID Labels Structurally**: `network_topology_discovery_decode_issues_total{oid=...}` is safe today because all callers pass table-root OIDs. Make this a structural guarantee: introduce a `TableOID` named string type in `internal/discovery/snmp/pdu.go` and change `DecodeIssue.OID` to use it. This prevents future contributors from accidentally passing per-PDU OIDs (which would unboundedly increase label cardinality).

- [ ] **Document Metric Cardinality Assumptions**: Write `docs/operator/cardinality.md` documenting the expected bound for every high-cardinality label: `device_id` (one per polled device), `src_device`/`dst_device` (same bound), `src_port`/`dst_port` (bounded by port count per device), `spoke_id` (one per spoke), `oid` (bounded by number of table OIDs walked — currently ~12). Add cardinality assertions in `schema_test.go` where bounds can be enforced programmatically.
