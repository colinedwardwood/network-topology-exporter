# OTLP Payload Schema

Version: 1.0 (stable from v1.2.0)
Schema URL: https://opentelemetry.io/schemas/1.21.0

## Resource attributes

| Attribute | Type | Description |
|---|---|---|
| `service.name` | string | Always `"network-topology-exporter"` |
| `service.version` | string | Binary version (semver) |
| `service.instance.id` | string | Hostname of the exporter instance |

## Edge metrics — `network_topology_edge`

One gauge data point per edge, value `1.0`. Attributes:

| Attribute | Type | Description |
|---|---|---|
| `src_device` | string | Source device sysName |
| `src_port` | string | Source interface name (empty if unknown) |
| `dst_device` | string | Destination device sysName or IP |
| `dst_port` | string | Destination interface name (empty if unknown) |
| `proto` | string | Discovery protocol: `lldp`, `cdp`, `bgp`, `ospf`, `isis`, `mpls_te`, `fdb` |
| `link_kind` | string | Transport type: `ethernet`, `ip`, `mpls-te`, etc. |
| `network.topology.mpls_te.admin_status` | string | MPLS-TE tunnel admin status: `up`, `down`, `testing`, `unknown` (present only on `mpls_te` edges) |

## Device metrics — `network_topology_device`

One gauge data point per device, value `1.0`. Attributes:

| Attribute | Type | Description |
|---|---|---|
| `device` | string | Device sysName or management IP |

## Versioning policy

Attribute names are stable within a major version.
New optional attributes may be added in minor releases.
Attribute removals or renames require a major version bump and CHANGELOG notice.
