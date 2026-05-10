# Engineering Improvement Plan: Network Topology Exporter

## 1. Executive Summary
The exporter is not public-release ready: it has credible architecture and protocol coverage, but it still publishes misleading topology under common enterprise conditions and carries metric/schema debt that serious Prometheus users will notice immediately. The remaining work is not cosmetic; it is the difference between a useful topology collector and a public GitHub repository that teaches operators to distrust its own graph.

## 2. Immediate Remediation (High Priority)
- [ ] **Stop Treating Host MACs as Topology Devices**: FDB emits an edge when a bridge port has exactly one learned MAC, then unresolved peers become `mac-<hash>` device IDs.
  **Rationale**: A single learned MAC on an access port is usually a host, not infrastructure. Publishing it as a topology node makes access layers look like switch-to-switch fabric and turns inference evidence into false fact.
  **File**: `internal/discovery/fdb/fdb.go:401-452`, `cmd/topology-exporter/main.go:797-827`.

- [ ] **Fix FDB/LLDP Deduplication for Missing Remote Ports**: FDB edges lack `DstPort`, while LLDP edges include it; `graph.normalizedGroupKey` includes all four endpoint fields, so observations for the same physical link can land in different reconciliation buckets.
  **Rationale**: The exporter can emit duplicate `network_topology_edge_info` series for one physical link, with FDB and LLDP disagreeing only because one source cannot know the remote port. That is silent wrong output, not a harmless low-confidence signal.
  **File**: `cmd/topology-exporter/main.go:734-746`, `internal/graph/graph.go:352-367`, `internal/metrics/topology_collector.go:140-146`.

- [ ] **Stop Publishing Ambiguous Federated Device Merges**: Hub federation strips FQDN suffixes during OOS matching, warns on collisions, and still publishes the merged graph.
  **Rationale**: `core-sw.dc1.example.com` and `core-sw.dc2.example.com` collapsing into `core-sw` is normal enterprise naming, not an exotic corner case. Publishing a known-ambiguous graph is worse than failing closed.
  **File**: `internal/federation/hub.go:298-335`, `internal/federation/hub.go:414-418`, `internal/federation/hub.go:583-592`.

- [ ] **Fix UTF-8-Unsafe Label Truncation**: `sanitizeLabel` slices at `maxLabelLen` bytes and can split a multi-byte rune.
  **Rationale**: Invalid UTF-8 labels are a downstream compatibility failure waiting to happen in Grafana, Alertmanager, remote write, and OTel bridges. Sanitizing at the metric layer must produce valid strings, not arbitrary byte fragments.
  **File**: `internal/metrics/topology_collector.go:14-27`.

- [ ] **Cap SNMP `sysName` Before It Becomes a Graph Key**: `NormaliseName` trims and lowercases untrusted SNMP strings but does not enforce the RFC 1213 `sysName` size limit before IDs are used in maps, graph keys, federation state, and snapshots.
  **Rationale**: Label capping happens too late. A broken or malicious agent can force large allocations before Prometheus ever sees the value.
  **File**: `internal/discovery/snmp/pdu.go:295-300`, `internal/discovery/snmp/snmp.go:181-186`.

- [ ] **Remove Silent Credential Profile Skips**: `credentialCandidates` silently drops profiles when `profileToParams` fails, so env-var typos look like generic device failures.
  **Rationale**: Credential resolution is a security-sensitive operational path. Hiding why a profile was unusable guarantees bad on-call diagnostics and unnecessary credential churn.
  **File**: `cmd/topology-exporter/main.go:834-884`.

- [ ] **Repair Snapshot Writer Leak Semantics**: The bounded snapshot queue prevents infinite queued snapshots, but timed-out writes can still leave a blocked inner goroutine behind when the filesystem call never returns.
  **Rationale**: A permanent NFS stall should degrade snapshot persistence, not leak goroutines over time until the process needs a restart.
  **File**: `cmd/topology-exporter/main.go:393-415`, `internal/federation/hub.go:544-611`.

- [ ] **Remove the Silent CIDR Parser Footgun**: Config validation rejects malformed `discovery.scope.cidr_allow_list` entries, but `ParseCIDRs` itself still silently skips malformed CIDRs and returns the valid remainder.
  **Rationale**: Today the validated config path saves this from being an active bug; tomorrow a helper reuse can silently distort the polling security boundary. Security-scope parsers should not have a best-effort mode unless it is explicitly named as such.
  **File**: `internal/discovery/snmp/pdu.go:302-313`, `internal/config/config.go:145-149`, `cmd/topology-exporter/main.go:390-391`.

- [ ] **Validate Federation Payload Semantics, Not Just Counts**: Hub push handling caps payload bytes and object counts, but accepts device IDs, ports, labels, and edge fields without field-length, UTF-8, duplicate-ID, or topology-semantic validation before storing the spoke payload and rebuilding the combined graph.
  **Rationale**: mTLS proves which spoke sent the payload; it does not prove the payload is sane. A compromised or buggy spoke can still poison hub memory, produce collision-heavy labels, or publish nonsensical graph state inside the current count limits.
  **File**: `internal/federation/hub.go:149-159`, `internal/federation/hub.go:214-220`, `internal/federation/hub.go:258-418`.

- [ ] **Correct Operator Credential Metric Documentation**: `docs/operator/cold-start-credentials.md` references `topology_credential_trials_total`, but the implementation exposes `network_topology_credential_trials_total`.
  **Rationale**: Public operators will grep for the wrong metric during auth rollout. Bad docs on credential troubleshooting are not a paper cut; they prolong outages and increase lockout risk.
  **File**: `docs/operator/cold-start-credentials.md:15`, `docs/operator/cold-start-credentials.md:29`, `docs/operator/cold-start-credentials.md:36`, `internal/metrics/metrics.go:142-145`.

## 3. Architectural & Scaling Overhaul
- [ ] **Replace FDB Edge Emission With Identity-Gated Link Synthesis**: FDB should produce raw observations, not public topology edges, unless the MAC can be correlated to known infrastructure through LLDP chassis MAC, ARP/IP-MIB helper data, ENTITY-MIB, or explicit operator inventory.
  **Reference**: Bejerano/Breitbart physical topology discovery principle: bridge tables are evidence for inference, not direct node identity.

- [ ] **Parallelize IOS VLAN Community FDB Walks With a Per-Device Budget**: `walkVlanCommunityFdbs` opens and walks one SNMP session per VLAN sequentially.
  **Reference**: Tail-latency control for polling systems; I/O-bound SNMP walks need bounded fan-out and module deadlines, not serial multiplication by VLAN count.

- [ ] **Make Discovery Budgeting Explicit and Observable**: `cycle_budget_fraction` and `timeout_per_module` exist, but the exporter does not expose enough counters for skipped targets, module cancellations, or budget exhaustion by module/device class.
  **Reference**: Queueing stability: discovery service time must remain below polling interval or the system becomes permanently stale.

- [ ] **Add Graph Size Admission Control Outside Hub Mode**: Hub mode can reject graphs over configured device/edge limits, but standalone and spoke cycles can still publish arbitrarily large local graphs.
  **Reference**: Backpressure before publication; memory and cardinality limits must be enforced at every producer, not only the aggregator.

- [ ] **Prove Reconciliation Complexity With Synthetic Scale Tests**: `graph.Reconcile` is approximately `O(E log E)`, but no CI benchmark proves behavior at large enterprise sizes, and hub OOS matching still deserves adversarial duplicate-observation tests.
  **Reference**: Algorithmic rigor for graph systems; performance contracts must be executable, not asserted in comments.

- [ ] **Add Cardinality Budget Enforcement in CI**: Generate synthetic 500, 1,000, and 5,000 device graphs and fail tests when sample count, scrape render time, or heap growth exceeds the declared budget.
  **Reference**: Prometheus instrumentation best practice: cardinality budgets are testable contracts.

- [ ] **Add Memory Residency Guardrails**: Track graph device count, edge count, edge churn, snapshot queue depth, rejected graph updates, and goroutine count under stalled IO.
  **Reference**: Backpressure-first collector design for long-running polling systems.

- [ ] **Introduce a Two-Phase Graph Assembly Boundary**: Keep raw protocol observations separate from canonical link synthesis, identity resolution, dedupe, confidence arbitration, and endpoint-noise suppression.
  **Reference**: RFC 8345-style separation of topology nodes/links from protocol observations; NetInventory-style reconciliation rather than direct metric emission from raw walks.

## 4. Standards & Compliance Checklist
- [ ] **IEEE 802.1AB Compliance**: Add explicit LLDP tests for mandatory Chassis ID, Port ID, TTL/liveness assumptions, subtype length validation, binary subtype handling, and malformed row quarantine metrics. The code validates some subtypes, but TTL behavior is assumed from agent table aging rather than proven.
- [ ] **IEEE 802.1AB Compliance**: Treat LLDP subtype 7 (`local`) and chassis-component/port-component values as binary-capable. Invalid UTF-8 should become hex, not mojibake.
- [ ] **IEEE 802.1AB Compliance**: Correct the LLDP TTL citation; RFC 2922 is Physical Topology MIB and does not define `lldpRemTable` aging.
- [ ] **RFC 2922/1213 Compliance**: Publish a standards matrix that clearly states RFC 2922 PTOPO-MIB is not implemented, why LLDP is the practical default, and what behavior operators lose by not polling PTOPO-MIB.
- [ ] **RFC 2922/1213 Compliance**: Enforce RFC 1213/MIB-II bounds for `sysName`, document `sysUpTime` 497-day rollover, and add tests for abnormal PDU types and lengths.
- [ ] **RFC 1213 Compliance**: Add a strategy for `sysUpTime` rollover; either document it aggressively or emit a wrap counter backed by persisted previous ticks.
- [ ] **RFC 2863/IF-MIB Compliance**: Require deterministic fallback behavior when `ifName` is absent and validate that `ifDescr` or numeric fallback cannot create duplicate port identities across modules.
- [ ] **MIB Citation Hygiene**: Remove contradictory or misleading comments, including the enterprise-prefix ordering comment and the `normalizeSysDescr` comment that says what the regex does without explaining the label-cardinality reason.

## 5. Prometheus & Observability Polish
- [x] **Fix Namespace Drift in Device Metrics**: Renamed `network_device_info` → `network_topology_device_info` and `network_device_uptime_seconds` → `network_topology_device_uptime_seconds`.
  **Rationale**: Current naming violates the project namespace pattern and forces dashboard authors to memorize special cases.

- [x] **Use Standard Timestamp Suffixes**: Replaced `_unix` gauges with `_timestamp_seconds` variants (`network_topology_snapshot_last_written_timestamp_seconds`, `network_topology_federation_spoke_last_push_timestamp_seconds`).
  **Rationale**: Prometheus/Grafana tooling recognizes `_timestamp_seconds`; `_unix` is homemade schema drift.

- [x] **Rename `link_type` to `link_kind`**: The `link_type` label on `network_topology_edge_info` is now `link_kind`, matching the internal `LinkKind` field.
  **Rationale**: Public metric schemas should not expose inconsistent vocabulary.

- [ ] **Bound Decode-Issue Label Values Structurally**: `DiscoveryDecodeIssues{module,oid,reason}` is safe only while callers pass table-root OIDs. Encode table identity as an enum or allow-list instead of trusting future contributors not to pass full instance OIDs.
  **Rationale**: One future caller passing per-PDU OIDs turns malformed SNMP rows into unbounded Prometheus series.

- [ ] **Expose Suppression and Budget Counters**: Add counters for FDB observations suppressed, VLAN walks skipped/truncated, module timeout cancellations, cycle-budget target skips, graph updates rejected, and snapshot writes dropped/timed out.
  **Rationale**: Silent absence of topology is not observability; it is wishful thinking.

- [ ] **Add Stable Metric Schema Tests and Release Gates**: Lock exported metric names, labels, and units with schema tests and require changelog migration notes for any rename or removal.
  **Rationale**: Public users build alerts and dashboards against metric contracts. Breaking them casually is how exporters lose trust.

- [ ] **Document Metric Cardinality Assumptions**: For every label with user-controlled or network-controlled values (`device_id`, ports, `spoke_id`, `oid`), document its expected bound and enforce it in tests where possible.
  **Rationale**: Cardinality safety must be an explicit design constraint, not tribal knowledge hidden in code comments.

## 6. Adversarial Review Addendum
### 6.1 The Fatal Flaw
The fatal flaw is that the system still lets protocol observations become published topology before they have passed a strict identity, standards, and cardinality boundary. The worst expression is FDB: bridge-table evidence becomes graph edges, unresolved MACs become pseudo-devices, and federation can then redistribute those claims as if they were canonical network truth.

### 6.2 The Council's Grievances
- **Distributed Systems Architect**: Hub federation authenticates spokes with mTLS and binds `spoke_id` to certificate CN, but the trust boundary ends too early: after count checks, the hub stores the spoke payload and rebuilds global topology from raw remote data. A distributed topology system needs schema and semantic validation at each trust boundary, not just transport authentication.
  **Evidence**: `internal/federation/hub.go:149-159`, `internal/federation/hub.go:175-190`, `internal/federation/hub.go:214-220`.

- **Low-Level Specialist**: `TopologyCollector.Collect` walks every device, edge, and boundary observation on every scrape and sanitizes label strings repeatedly. That is acceptable at toy scale, but at 100k edges it becomes scrape-path CPU churn with no cache or precomputed sanitized representation.
  **Evidence**: `internal/metrics/topology_collector.go:122-180`.

- **Slop Forensic Analyst**: The code still contains comments that are either wrong or explain the obvious while hiding the real invariant. The RFC 2922 LLDP TTL citation is false, and `normalizeSysDescr` repeats what the regex does instead of documenting why cardinality is being collapsed.
  **Evidence**: `internal/discovery/lldp/lldp.go:210-214`, `internal/discovery/snmp/snmp.go:205-210`.

- **Security Breach-Lead**: The credential path fails closed for missing legacy community, rejects MD5/DES in config validation, and uses mTLS for federation, but untrusted SNMP and federation payload strings are still accepted before hard size and UTF-8 normalization at graph boundaries.
  **Evidence**: `cmd/topology-exporter/main.go:851-865`, `internal/config/config.go:533-557`, `internal/metrics/topology_collector.go:16-27`, `internal/federation/hub.go:149-159`.

- **Chaos Engineer**: Snapshot writes are bounded at the queue level, but the timed-out write goroutine can still remain blocked forever. A permanent filesystem stall becomes a slow goroutine leak with warning logs, not a contained failure mode.
  **Evidence**: `cmd/topology-exporter/main.go:393-415`, `internal/federation/hub.go:544-580`.

- **FinOps Controller**: Sequential VLAN community walks multiply SNMP sessions by VLAN count. A campus core with 100 VLANs can burn the module budget on one device before the rest of the topology protocols get useful time.
  **Evidence**: `internal/discovery/fdb/fdb.go:310-337`.

- **Compliance/Legal Hawk**: Dependency surface is small, but there is no visible release gate for vulnerability or license scanning. Public release should not rely on manual inspection of `go.mod`.
  **Evidence**: `go.mod:5-23`.

- **Brutal Maintainer**: The current plan is the real source of truth for fix ordering, but code comments still carry LD labels, design references, and stale assertions that require archaeology at 3 AM. Incorrect comments are worse than no comments because they create false confidence.
  **Evidence**: `internal/discovery/lldp/lldp.go:210-214`, `internal/discovery/snmp/snmp.go:331-336`, `PLAN.md`.

### 6.3 Survival Score
- **Scaling**: 58%. The architecture has bounded global parallelism and some cycle-budget controls, but FDB semantics, serial VLAN walks, scrape-time rendering, and missing standalone graph admission controls are still enough to hurt a large enterprise deployment.
- **Security**: 67%. Credential handling and federation transport are not amateur, but payload validation, string bounds, and diagnostic transparency still fall short of a zero-trust release.
- **Maintainability**: 61%. Module boundaries are workable, but stale comments, inconsistent metric vocabulary, and protocol observations leaking into canonical graph state make the code harder to reason about than it should be.

### 6.4 Additional Non-Negotiable Backlog
- [ ] **Add Federation Payload Validation Tests**: Build table-driven tests for overlong device IDs, invalid UTF-8, duplicate devices, empty endpoint fields, impossible self-edges, and label values that would be truncated in metrics.
  **Rationale**: Transport auth without semantic validation lets a compromised spoke poison the hub with authenticated garbage.

- [ ] **Add Scrape-Path Performance Benchmarks**: Benchmark `TopologyCollector.Collect` with synthetic 10k, 50k, and 100k edge graphs and fail CI when render time or allocations exceed the declared scrape budget.
  **Rationale**: Exporters fail in production when the scrape path becomes the bottleneck, not when the happy-path unit tests pass.

- [ ] **Add Dependency License and Vulnerability Gates**: Add `govulncheck` and license scanning to CI, and document the accepted license policy for direct and transitive dependencies.
  **Rationale**: A public infrastructure exporter with no dependency gate is asking reviewers to find supply-chain hygiene problems for you.
