# Metric Cardinality

Every Prometheus label with values controlled by network data or configuration
has a documented bound. Violating these bounds silently bloats TSDB.

## network_topology_device_info / network_topology_device_uptime_seconds
Labels: `device_id`, `vendor`, `model`, `os_version`, `site`

Cardinality = number of discovered devices. Expected: 1–5 000 for a single
exporter instance. Hub mode aggregates spoke graphs; expect up to hub's
`max_graph_devices` limit.

`vendor`, `model`, `os_version` are truncated to 128 bytes and sanitized;
they should have low cardinality within any single fleet.

## network_topology_edge_info
Labels: `src_device`, `src_port`, `dst_device`, `dst_port`, `discovery_proto`, `link_kind`, `direction`

Cardinality = number of reconciled edges. Expected: 1–10 000 per instance.
`src_port` and `dst_port` are truncated to 128 bytes. `discovery_proto` is
one of: `lldp`, `cdp`, `fdb`, `bgp`, `ospf`, `isis`, `mpls_te`, `configured`.
`link_kind` mirrors `discovery_proto`. `direction` is `bidirectional` or `unidirectional`.

## network_topology_discovery_decode_issues_total
Labels: `module`, `oid`, `reason`

`oid` is always a table-root OID (e.g., `1.3.6.1.2.1.17.4.3.1`), never a
per-row instance OID. The type system enforces this via `snmputil.TableOID`.
Current table OIDs walked: ~12. `module` is one of the ~9 discovery modules.
`reason` is one of: `invalid_type`, `invalid_oid`. Maximum cardinality: ~200.

## network_topology_snapshot_drops_total
Labels: `reason`

Cardinality = 2 (closed enum: `queue_full`, `write_in_flight`). Counter
increments when the snapshot persistence pipeline cannot absorb a write
(typically during a storage stall — slow NFS, full disk, EBS hang). The
two reasons surface the same underlying condition at different layers:

- `queue_full` — caller couldn't enqueue (bounded snapshot channel full).
- `write_in_flight` — writer goroutine dequeued a new snapshot but found
  its previous write still pending from an earlier timed-out goroutine.

Alert: `rate(network_topology_snapshot_drops_total[10m]) > 0` for 5m.
Persistent drops mean the on-disk state is increasingly stale and a
restart will cold-start from a worse position the longer the stall lasts.

## network_topology_federation_spoke_last_push_timestamp_seconds
Labels: `spoke_id`

Cardinality = number of active federation spokes. `spoke_id` is bound to the
mTLS certificate CN; operators control this via PKI.

## High-cardinality risk labels

`device_id`, `src_device`, `dst_device` reflect untrusted `sysName` values
from SNMP agents. Per-trust-boundary they are capped at 256 bytes by the
federation push validator (`internal/limits.MaxDeviceIDBytes`); per-row
on the SNMP side, `NormaliseName` separately caps sysName at 255 bytes
to match the SNMPv2-MIB sysName SIZE(0..255) constraint (RFC 3418, which
obsoletes RFC 1213). All values are sanitized to valid UTF-8 before use
as labels. A compromised agent could generate arbitrary
sysName values within these bounds; the exporter does not deduplicate across
`sysName` values that differ only by case or whitespace beyond the `NormaliseName`
normalisation.
