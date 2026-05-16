# Metrics Reference

All metrics use the `network_` prefix. No metric uses a raw IP address or free-form text as a label value.

## Topology inventory

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_device_info` | gauge (always 1) | `device_id`, `vendor`, `model`, `os_version`, `site` | One series per discovered device. Absent from `/metrics` when device is no longer in graph. |
| `network_topology_device_uptime_seconds` | gauge | `device_id` | From SNMP sysUpTime. |
| `network_topology_edge_info` | gauge (always 1) | `src_device`, `src_port`, `dst_device`, `dst_port`, `discovery_proto`, `link_kind`, `direction` | One series per edge. Absent from `/metrics` when edge is removed. |
| `network_topology_change_total` | counter | `change_kind` (added\|removed\|updated), `discovery_proto` | Resets on restart. Use `increase()` not `rate()` for alerting on sparse events. |
| `network_topology_out_of_scope_neighbours_total` | gauge | (none) | Count of distinct neighbours seen this cycle whose IPs fall outside the CIDR allow-list. Detail (device, port, hint) is in log lines. |

## Conflict detection

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_conflict_total` | counter | `conflict_type` (`neighbour_disagreement`) | Source disagreements detected during reconciliation. Resets on restart. Only `neighbour_disagreement` is currently emitted (two protocols name different neighbours for the same local port). A sustained rate may indicate stale LLDP/CDP cache or protocol misconfiguration on the local device. |

## Graph freshness

| Metric | Type | Notes |
|--------|------|-------|
| `network_topology_graph_stale` | gauge (0/1) | 1 while serving snapshot on startup; 0 after first live cycle. Alert: `network_topology_graph_stale == 1 for 15m` |
| `network_topology_snapshot_last_written_timestamp_seconds` | gauge | Alert: absent or stopped advancing after two cycle intervals. |
| `network_topology_snapshot_loaded_devices_total` | gauge | Sanity check at startup; compare to expected fleet size. |

## Discovery cycle health

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_discovery_devices_total` | gauge | `status` (success\|failed\|timeout) | Resets each cycle to reflect the most recent outcome. |
| `network_topology_discovery_cycle_duration_seconds` | histogram | (none) | Buckets: 0.5s–256s (exponential, factor 2). Alert when p99 approaches `discovery.interval`. |
| `network_topology_discovery_module_duration_seconds` | histogram | `module` | Per-module time within a cycle. Valid values: snmp, lldp, cdp, bgp, ospf, fdb, isis, mpls_te. |
| `network_topology_snmp_walks_total` | counter | `status` (ok\|timeout\|error) | Aggregate across all devices. |
| `network_topology_discovery_decode_issues_total` | counter | `module`, `oid`, `reason` | SNMP decode anomalies (for example, invalid integer type in a walked column). Non-zero sustained rate indicates schema/vendor drift or corrupt agent responses. |
| `network_topology_discovery_quarantined_rows_total` | counter | `module`, `oid`, `reason` | Required/optional table rows dropped from processing due to decode anomalies. A rising rate with stable hard-fail counts indicates resilient degradation. |
| `network_topology_discovery_degraded_total` | counter | `module`, `reason` | Module runs that completed in degraded mode (for example, missing optional metadata table). |
| `network_topology_discovery_hard_fail_total` | counter | `module`, `reason` | Hard failures where required discovery policy was breached (for example, `required_table_no_valid_rows`, `required_table_invalid_ratio_exceeded`). |
| `network_topology_credential_trials_total` | counter | `status` (ok\|failed) | Auth trial attempts against polled devices. A sustained `failed` rate on a fresh deployment may indicate auth lockout. |
| `network_topology_bgp_walker_outcome_total` | counter | `walker` (v2_draft\|vendor_cisco\|vendor_juniper\|vendor_nokia\|rfc4273), `outcome` (edges\|empty\|error\|malformed_index) | Per-walker outcome accounting for the BGP module's fallback chain. `outcome=edges` means the walker produced rows; `empty` means the table returned no rows and the next walker was tried; `error` means the SNMP walk failed and the next walker was tried; `malformed_index` means a row was dropped because its InetAddress index could not be decoded. A non-zero `malformed_index` rate on a known vendor is a signal that the column numbers in `internal/discovery/bgp/bgp_vendor.go` may be wrong for that vendor's OS version — the documented failure mode this counter exists to surface. |

## Cardinality budget

Dominant series: `network_topology_edge_info`. Each unique `(src_device, src_port, dst_device, dst_port, discovery_proto, link_kind, direction)` tuple is one series.

For a 500-device network with an average of 4 links per device and 2 protocols reporting each link:
- Edges: ~1000 physical links × 2 protocols = ~2000 edge series
- Devices: ~500 device_info series + ~500 uptime series = ~1000
- Total topology series: ~3000

This is well within Prometheus defaults. If LLDP and CDP both report the same link, the reconciler collapses them to the highest-precedence source before metric emission — duplicate edges do not multiply series.

## OTLP output

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_otlp_push_total` | counter | `status` (ok\|error\|dropped) | Incremented after each OTLP push attempt (both `/v1/metrics` and `/v1/logs` share this counter). Only present when `output.otlp.enabled: true`. A sustained `error` rate indicates the receiver endpoint is unreachable or rejecting payloads; check `output.otlp.endpoint` and that `otlp.timeout` is less than half the scrape interval. `dropped` means the per-process push concurrency limit (`maxOTLPPushConcurrency = 4`) was saturated and the push was discarded without contacting the receiver — see operator troubleshooting "OTLP push saturation". |
| `network_topology_metrics_render_duration_seconds` | histogram | (none) | Wall time to render one `/metrics` scrape response. Alert at p99 against the scraper's `scrape_timeout`. See `docs/operator/scale.md` for guidance. |
| `network_topology_metrics_payload_bytes` | histogram | (none) | Response body size of one `/metrics` scrape, in bytes. Tracks growth as the topology scales; useful for capacity planning. See `docs/operator/scale.md`. |

## Federation

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_boundary_observation_info` | gauge (always 1) | `peer_a`, `peer_b`, `reporting_device`, `src_port`, `proto` | Uncoordinated mode only. One series per out-of-scope neighbour, reset each cycle. `peer_a` is always the alphabetically-smaller endpoint. Recording rule: `count by(peer_a, peer_b, proto)(...) == 2` fires when both sides have reported, producing a confirmed cross-boundary edge. |
| `network_topology_federation_spoke_up` | gauge (0/1) | `spoke_id` | Hub mode only. 1 while a spoke has pushed within `federation.spoke_timeout`; drops to 0 after eviction. Alert: `network_topology_federation_spoke_up == 0` |
| `network_topology_federation_spoke_last_push_timestamp_seconds` | gauge | `spoke_id` | Hub mode only. Unix timestamp in seconds of the most recent push from each spoke. Alert: `time() - network_topology_federation_spoke_last_push_timestamp_seconds > federation.spoke_timeout` |
| `network_topology_federation_spoke_push_failures_total` | counter | (none) | Spoke mode only. Incremented each time a push attempt exhausts all retries. A non-zero rate means the hub is not receiving topology data from this spoke. |
| `network_topology_hub_oos_unmatched_total` | gauge | (none) | Hub mode only. Count of out-of-scope hints received this cycle that could not be matched to any known device name via `normalizeDeviceName`. A value > 0 indicates vendor chassis-id encoding differences; add static `known_inter_domain_links` entries as the reliable workaround. |
| `network_topology_graph_updates_rejected_total` | counter | `reason` (size_budget_exceeded\|invalid_label_key\|invalid_label_value) | Combined-graph updates rejected at publish time, partitioned by reason. `size_budget_exceeded` means the graph exceeded `MaxGraphEdges` or `MaxGraphDevices` (either federation hub or local standalone). `invalid_label_key` / `invalid_label_value` are hub-mode only and mean a spoke-supplied label failed validation (see `docs/operator/federation.md` for the on-wire reject contract). Alert: `rate(network_topology_graph_updates_rejected_total[5m]) > 0`, with per-reason breakdown for triage. |

## Recommended alerts

```promql
# Graph stopped refreshing
network_topology_graph_stale == 1

# Discovery cycle slower than configured interval (using 60s as example)
histogram_quantile(0.99, rate(network_topology_discovery_cycle_duration_seconds_bucket[10m])) > 60

# Snapshot stopped being written (dead man's switch)
time() - network_topology_snapshot_last_written_timestamp_seconds > 300

# Sustained credential failures (possible lockout)
increase(network_topology_credential_trials_total{status="failed"}[10m]) > 20

# SNMP decode anomalies (schema drift or bad agent data)
increase(network_topology_discovery_decode_issues_total[10m]) > 0

# Discovery degraded mode entered
increase(network_topology_discovery_degraded_total[10m]) > 0

# Hard failures in required discovery stages
increase(network_topology_discovery_hard_fail_total[10m]) > 0

# Federation spoke not pushing (hub mode)
network_topology_federation_spoke_up == 0
```
