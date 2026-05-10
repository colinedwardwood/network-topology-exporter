# Engineering Improvement Plan: Network Topology Exporter

## 1. Executive Summary
The exporter is not public-release ready: it has credible architecture and protocol coverage, but it still publishes misleading topology under common enterprise conditions and carries metric/schema debt that serious Prometheus users will notice immediately. The remaining work is not cosmetic; it is the difference between a useful topology collector and a public GitHub repository that teaches operators to distrust its own graph.

## 2. Immediate Remediation (High Priority)
- [x] **Stop Treating Host MACs as Topology Devices**: `resolveEdgeDstDevices` now drops edges whose DstDevice is an unresolved MAC (no LLDP correlation) rather than hashing them into `mac-<hash>` pseudo-device IDs. `macAddrHash` removed.
- [x] **Fix FDB/LLDP Deduplication for Missing Remote Ports**: DstPort backfill after `resolveEdgeDstDevices` copies the remote port from a matching LLDP observation `(SrcDevice, SrcPort, DstDevice)` before `graph.Reconcile` runs, so FDB and LLDP edges now land in the same reconciliation bucket.
- [x] **Stop Publishing Ambiguous Federated Device Merges**: `FederationHubConfig.StrictDeviceNameMatching` flag; when true, `canonicalizeDeviceName` only lowercases (skips FQDN suffix stripping), preventing `core-sw.dc1` and `core-sw.dc2` from collapsing into the same graph node.
- [x] **Fix UTF-8-Unsafe Label Truncation**: `sanitizeLabel` now walks backward from byte 128 until `utf8.ValidString` is satisfied, producing a valid rune boundary.
- [x] **Cap SNMP `sysName` Before It Becomes a Graph Key**: `NormaliseName` enforces a 255-byte cap (RFC 1213 `sysName` limit) before the string is used as a map key or graph ID.
- [x] **Remove Silent Credential Profile Skips**: `credentialCandidates` now logs a `Warn` with profile name, IP, and error before skipping a profile that `profileToParams` cannot resolve.
- [x] **Repair Snapshot Writer Leak Semantics**: Persistent `writeDone chan error` across loop iterations; a timed-out write goroutine is detected at the top of the next iteration and the new snapshot is dropped rather than spawning another blocked goroutine.
- [x] **Remove the Silent CIDR Parser Footgun**: `ParseCIDRsStrict` added to `internal/discovery/snmp/pdu.go`; returns an error on the first malformed CIDR instead of silently skipping it. `ParseCIDRs` retained for backward compat.
- [x] **Validate Federation Payload Semantics, Not Just Counts**: `validateSpokePayload` in `internal/federation/hub.go` checks device-ID length/UTF-8/duplicates, edge required-field presence, self-edges, and port-name length/UTF-8. Called after size checks in `handlePush`.
- [x] **Correct Operator Credential Metric Documentation**: Three occurrences of `topology_credential_trials_total` in `docs/operator/cold-start-credentials.md` corrected to `network_topology_credential_trials_total`.

## 3. Architectural & Scaling Overhaul
- [ ] **Replace FDB Edge Emission With Identity-Gated Link Synthesis**: FDB should produce raw observations, not public topology edges, unless the MAC can be correlated to known infrastructure through LLDP chassis MAC, ARP/IP-MIB helper data, ENTITY-MIB, or explicit operator inventory.
  **Reference**: Bejerano/Breitbart physical topology discovery principle: bridge tables are evidence for inference, not direct node identity.

- [x] **Parallelize IOS VLAN Community FDB Walks With a Per-Device Budget**: `walkVlanCommunityFdbs` now uses a bounded goroutine pool (`maxVlanConcurrency = 8`); each goroutine gets its own SNMP session and local entry map, merged after all complete.

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
- [x] **IEEE 802.1AB Compliance**: `decodeChassisID` and `decodePortID` default cases now check `utf8.ValidString` and fall back to `hex.EncodeToString` for binary subtypes. `TestIEEE802_1ABCompliance` (7 subtests) and `TestMandatoryTLVValidation` (4 subtests) added to `internal/discovery/lldp/lldp_test.go`.
- [x] **IEEE 802.1AB Compliance**: LLDP TTL citation corrected from RFC 2922 to IEEE 802.1AB-2016 §9.6.3 in `internal/discovery/lldp/lldp.go`. Remaining: formal test for TTL liveness assumption (agent table aging) — deferred; behavior is already correct and documented.
- [ ] **RFC 2922/1213 Compliance**: Publish a standards matrix that clearly states RFC 2922 PTOPO-MIB is not implemented, why LLDP is the practical default, and what behavior operators lose by not polling PTOPO-MIB.
- [x] **RFC 1213/MIB-II bounds for `sysName`**: `NormaliseName` enforces 255-byte cap. Remaining: document `sysUpTime` 497-day rollover, add tests for abnormal PDU types and lengths.
- [ ] **RFC 1213 Compliance**: Add a strategy for `sysUpTime` rollover; either document it aggressively or emit a wrap counter backed by persisted previous ticks.
- [ ] **RFC 2863/IF-MIB Compliance**: Require deterministic fallback behavior when `ifName` is absent and validate that `ifDescr` or numeric fallback cannot create duplicate port identities across modules.
- [x] **MIB Citation Hygiene**: Removed contradictory enterprise-prefix ordering comment and replaced `normalizeSysDescr` comment with cardinality rationale.

## 5. Prometheus & Observability Polish
- [x] **Fix Namespace Drift in Device Metrics**: Renamed `network_device_info` → `network_topology_device_info` and `network_device_uptime_seconds` → `network_topology_device_uptime_seconds`.
  **Rationale**: Current naming violates the project namespace pattern and forces dashboard authors to memorize special cases.

- [x] **Use Standard Timestamp Suffixes**: Replaced `_unix` gauges with `_timestamp_seconds` variants (`network_topology_snapshot_last_written_timestamp_seconds`, `network_topology_federation_spoke_last_push_timestamp_seconds`).
  **Rationale**: Prometheus/Grafana tooling recognizes `_timestamp_seconds`; `_unix` is homemade schema drift.

- [x] **Rename `link_type` to `link_kind`**: The `link_type` label on `network_topology_edge_info` is now `link_kind`, matching the internal `LinkKind` field.
  **Rationale**: Public metric schemas should not expose inconsistent vocabulary.

- [ ] **Bound Decode-Issue Label Values Structurally**: `DiscoveryDecodeIssues{module,oid,reason}` is safe only while callers pass table-root OIDs. Encode table identity as an enum or allow-list instead of trusting future contributors not to pass full instance OIDs.
  **Rationale**: One future caller passing per-PDU OIDs turns malformed SNMP rows into unbounded Prometheus series.

- [x] **Expose FDB MAC Suppression Counter**: `network_topology_fdb_suppressed_macs_total` counter added; incremented in `resolveEdgeDstDevices` when an unresolved MAC peer is dropped. Remaining counters (module cancellations, budget skips, graph rejections, snapshot drops) deferred to the next observability pass.
- [x] **Add Stable Metric Schema Tests and Release Gates**: `TestMetricSchemaStable` in `internal/metrics/schema_test.go` locks exported metric names; any rename now requires updating the expected list. CHANGELOG entry required for breaking renames.

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
- [x] **Add Federation Payload Validation Tests**: `TestValidateSpokePayload` in `internal/federation/hub_test.go` covers 11 cases: overlong device IDs, invalid UTF-8, duplicate devices, empty endpoint fields, self-edges, overlong port names.

- [x] **Add Scrape-Path Performance Benchmarks**: `BenchmarkCollect1000Edges` and `BenchmarkCollect10000Edges` in `internal/metrics/topology_collector_test.go` (~2ms/1k edges, ~20ms/10k edges). Hard budget limits deferred pending baseline collection across dev machines.
- [x] **Add Vulnerability Gate**: `govulncheck ./...` job added to `.github/workflows/ci.yml`. License scanning deferred — no GPL/copyleft dependencies found in `go.mod`.
