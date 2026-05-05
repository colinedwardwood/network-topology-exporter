# Metrics Reference

All metrics use the `network_` prefix. No metric uses a raw IP address or free-form text as a label value.

## Topology inventory

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_device_info` | gauge (always 1) | `device_id`, `vendor`, `model`, `os_version`, `site` | One series per discovered device. Absent from `/metrics` when device is no longer in graph. |
| `network_device_uptime_seconds` | gauge | `device_id` | From SNMP sysUpTime. |
| `network_topology_edge_info` | gauge (always 1) | `src_device`, `src_port`, `dst_device`, `dst_port`, `discovery_proto`, `link_type`, `direction` | One series per edge. Absent from `/metrics` when edge is removed. |
| `network_topology_change_total` | counter | `change_kind` (added\|removed\|updated), `discovery_proto` | Resets on restart. Use `increase()` not `rate()` for alerting on sparse events. |
| `network_topology_out_of_scope_neighbours_total` | gauge | (none) | Count of distinct neighbours seen this cycle whose IPs fall outside the CIDR allow-list. Detail (device, port, hint) is in log lines. |

## Graph freshness (LD-13)

| Metric | Type | Notes |
|--------|------|-------|
| `network_topology_graph_stale` | gauge (0/1) | 1 while serving snapshot on startup; 0 after first live cycle. Alert: `network_topology_graph_stale == 1 for 15m` |
| `network_topology_snapshot_last_written_unix` | gauge | Alert: absent or stopped advancing after two cycle intervals. |
| `network_topology_snapshot_loaded_devices_total` | gauge | Sanity check at startup; compare to expected fleet size. |

## Discovery cycle health

| Metric | Type | Labels | Notes |
|--------|------|--------|-------|
| `network_topology_discovery_devices_total` | gauge | `status` (success\|failed\|timeout) | Resets each cycle to reflect the most recent outcome. |
| `network_topology_discovery_cycle_duration_seconds` | histogram | (none) | Buckets: 0.5s–256s (exponential, factor 2). Alert when p99 approaches `discovery.interval`. |
| `network_topology_discovery_module_duration_seconds` | histogram | `module` | Per-module time within a cycle. Valid values: snmp, lldp, cdp, bgp, ospf, fdb. |
| `network_topology_snmp_walks_total` | counter | `status` (ok\|timeout\|error) | Aggregate across all devices. |
| `network_topology_credential_trials_total` | counter | `status` (ok\|failed) | Under the LD-12 rate limiter. A sustained `failed` rate on a fresh deployment may indicate auth lockout. |

## Cardinality budget

Dominant series: `network_topology_edge_info`. Each unique `(src_device, src_port, dst_device, dst_port, discovery_proto, link_type, direction)` tuple is one series.

For a 500-device network with an average of 4 links per device and 2 protocols reporting each link:
- Edges: ~1000 physical links × 2 protocols = ~2000 edge series
- Devices: ~500 device_info series + ~500 uptime series = ~1000
- Total topology series: ~3000

This is well within Prometheus defaults. If LLDP and CDP both report the same link, the reconciler collapses them to the highest-precedence source before metric emission — duplicate edges do not multiply series.

## Recommended alerts

```promql
# Graph stopped refreshing
network_topology_graph_stale == 1

# Discovery cycle slower than configured interval (using 60s as example)
histogram_quantile(0.99, rate(network_topology_discovery_cycle_duration_seconds_bucket[10m])) > 60

# Snapshot stopped being written (dead man's switch)
time() - network_topology_snapshot_last_written_unix > 300

# Sustained credential failures (possible lockout)
increase(network_topology_credential_trials_total{status="failed"}[10m]) > 20
```
