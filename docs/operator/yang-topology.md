# RFC 8345 YANG topology mapping

> **Status: implemented behind a flag (#75).** This document defines how the
> exporter's in-memory topology graph maps onto the RFC 8345 / RFC 8346 YANG
> data models. The YANG output path is implemented and ships disabled by default
> behind `output.yang.enabled: false`; when enabled, `GET /topology/yang`
> renders the current reconciled topology as RFC 8345/8346 YANG-JSON (see
> [Accessing the output](#accessing-the-output)). This document remains the
> mapping contract, and the emitted document is validated against the canonical
> modules with `yanglint` in CI (the `yang-validate` workflow).

## Why YANG

The exporter already emits the topology as Prometheus metrics
(`network_topology_device_info`, `network_topology_edge_info`) and as OTLP
metrics/logs. Those are *signal* encodings — good for querying and alerting,
not for interchange with NETCONF/RESTCONF tooling or other controllers.
[RFC 8345](https://www.rfc-editor.org/rfc/rfc8345) ("A YANG Data Model for
Network Topologies") and [RFC 8346](https://www.rfc-editor.org/rfc/rfc8346)
("A YANG Data Model for Layer 3 Topologies") are the IETF-standard schema for
exchanging a discovered topology as a structured document. Emitting them lets
the discovered graph flow into any RESTCONF/YANG-aware consumer.

## Source data model

The mapping is from the reconciled graph the exporter holds each cycle:

```go
// internal/discovery/discovery.go
type Device struct {
    ID        string        // sysName (normalised lowercase); fallback: management IP
    Vendor    string
    Model     string
    OSVersion string
    Site      string        // per-target enrichment config, not SNMP
    Uptime    time.Duration
    Labels    map[string]string
}

type Edge struct {
    SrcDevice      string
    SrcPort        string
    DstDevice      string
    DstPort        string
    DiscoveryProto DiscoveryProtocol // lldp | cdp | bgp | ospf | fdb | isis | mpls_te | configured
    Direction      Direction         // bidirectional | unidirectional
    Confidence     Confidence         // high | medium | low
    Adjacency      Adjacency          // direct | indirect | unknown
    LinkKind       LinkKind           // ethernet | ip | mpls-te | logical
    // PrecedenceRank, ObservedAt, Metadata omitted here
}
```

## Target schema

A single discovered topology becomes **one `network` instance** in the
`ietf-network:networks` container. Because the exporter discovers both L2
adjacency (LLDP/CDP/FDB) and L3 routing adjacency (BGP/OSPF/IS-IS), the network
declares the RFC 8346 L3 topology type:

```jsonc
{
  "ietf-network:networks": {
    "network": [
      {
        "network-id": "<tester_id or deployment name>",
        "network-types": {
          "ietf-l3-unicast-topology:l3-unicast-topology": {}
        },
        "node": [ /* one per Device — see below */ ],
        "ietf-network-topology:link": [ /* per Edge — see below */ ]
      }
    ]
  }
}
```

## Mapping

### Device → `node`

Each `Device` becomes one `node`. `node-id` is the device identity; each port
that appears on any incident edge becomes a `termination-point` under the node.

| Source | RFC 8345 element | Notes |
|---|---|---|
| `Device.ID` | `node/node-id` | The canonical key. Referenced by links' `source-node` / `dest-node`. |
| every `SrcPort`/`DstPort` incident to the device | `node/ietf-network-topology:termination-point/tp-id` | One termination-point per distinct port name on the device. Ports are normalised (`Gi0/1`, not `GigabitEthernet0/1`) — same normalisation reconciliation uses. |
| `Device.Vendor`, `Model`, `OSVersion`, `Site`, `Labels` | *(no standard field)* | RFC 8345 has no inventory attributes on `node`. Carried as annotations under a vendor augmentation (`ntx-topology:` — see [Exporter-specific attributes](#exporter-specific-attributes)), not in the base model. |
| `Device.Uptime` | *(dropped)* | Operational, not topological; available via `network_topology_device_uptime_seconds`. |

RFC 8346 adds `l3-node-attributes` (router-id, prefixes). **The exporter does
not collect router-id or advertised prefixes today**, so `l3-node-attributes`
is emitted empty (or omitted). This is a known gap recorded here so the
conformance tests don't assert on fields we can't populate.

### Edge → `link`

RFC 8345 links are **unidirectional**. The exporter's reconciled edge is one
record per physical/logical link with a `Direction` flag, so:

- a **`unidirectional`** edge → **one** `link` (source → destination as recorded);
- a **`bidirectional`** edge → **two** `link` instances (forward and reverse),
  so a NETCONF consumer sees reachability in both directions.

| Source | RFC 8345 element | Notes |
|---|---|---|
| derived: `<src-node>:<src-tp>-<dst-node>:<dst-tp>` | `link/link-id` | Stable, deterministic; the reverse link appends a direction discriminator. |
| `Edge.SrcDevice` | `link/source/source-node` | |
| `Edge.SrcPort` | `link/source/source-tp` | Must match a termination-point under the source node. |
| `Edge.DstDevice` | `link/destination/dest-node` | |
| `Edge.DstPort` | `link/destination/dest-tp` | |

### Exporter-specific attributes

`DiscoveryProto`, `LinkKind`, `Direction`, `Confidence`, and `Adjacency` have no
home in base RFC 8345/8346. They are the exporter's reconciliation provenance
and are too useful to drop. They are carried under a small **vendor
augmentation module** (`ntx-topology`, namespace
`https://github.com/colinedwardwood/network-topology-exporter/yang/ntx-topology`)
that adds:

- `link/ntx-topology:discovery-protocol` (enum mirroring `DiscoveryProtocol`)
- `link/ntx-topology:link-kind`
- `link/ntx-topology:confidence`
- `link/ntx-topology:adjacency`
- `node/ntx-topology:vendor`, `model`, `os-version`, `site`

A consumer that only understands base RFC 8345 ignores the augmentation; one
that imports `ntx-topology` gets the provenance. The augmentation YANG module
ships alongside the emitter and is part of the `pyang`/`yanglint` validation set.

## Worked example

Two switches with a confirmed bidirectional LLDP link `leaf1:Gi0/1 ↔ spine1:Gi0/2`:

```jsonc
{
  "ietf-network:networks": {
    "network": [{
      "network-id": "long-running-lab",
      "network-types": { "ietf-l3-unicast-topology:l3-unicast-topology": {} },
      "node": [
        { "node-id": "leaf1",
          "ietf-network-topology:termination-point": [{ "tp-id": "Gi0/1" }] },
        { "node-id": "spine1",
          "ietf-network-topology:termination-point": [{ "tp-id": "Gi0/2" }] }
      ],
      "ietf-network-topology:link": [
        { "link-id": "leaf1:Gi0/1-spine1:Gi0/2",
          "source": { "source-node": "leaf1", "source-tp": "Gi0/1" },
          "destination": { "dest-node": "spine1", "dest-tp": "Gi0/2" },
          "ntx-topology:discovery-protocol": "lldp",
          "ntx-topology:link-kind": "ethernet" },
        { "link-id": "spine1:Gi0/2-leaf1:Gi0/1",
          "source": { "source-node": "spine1", "source-tp": "Gi0/2" },
          "destination": { "dest-node": "leaf1", "dest-tp": "Gi0/1" },
          "ntx-topology:discovery-protocol": "lldp",
          "ntx-topology:link-kind": "ethernet" }
      ]
    }]
  }
}
```

## Accessing the output

Enable the output in config:

```yaml
output:
  yang:
    enabled: true            # default false
    network_id: my-fabric    # optional; default "network-topology-exporter"
```

When enabled, `GET /topology/yang` on the main listen port returns the current
reconciled topology as RFC 8345/8346 YANG-JSON with
`Content-Type: application/yang-data+json`. The `network_id` (config or the
default) is emitted as the single `network`'s `network-id`.

This read carries the **same trust level as `/metrics`**: it is served on the
same listener with the same auth posture (no auth in the default ground state;
basic_auth/mTLS when `listen.web_config_file` is configured). It is a
RESTCONF-*flavored* convenience read — a single GET that returns a YANG-JSON
document — **not** a conformant RESTCONF datastore: there is no
`/restconf/data` tree, no query parameters, no edit/PATCH semantics, and no
NETCONF datastore behind it.

## Emission and validation

- **Config:** `output.yang.enabled` (default `false`) plus optional
  `output.yang.network_id`. The output is a read endpoint
  (`GET /topology/yang`), not a file written each cycle.
- **Conformance CI:** the `yang-validate` workflow validates an adversarial
  emitted document against the canonical RFC 8345 / RFC 8346 modules (plus the
  `ntx-topology` augmentation) with `yanglint`. The mapping above is the
  contract those checks assert.
- **Known gaps encoded as test expectations:** empty `l3-node-attributes`
  (no router-id/prefix collection today); termination-points are derived from
  observed edges, so a device with no discovered links contributes a node with
  no termination-points.

## References

- [RFC 8345 — A YANG Data Model for Network Topologies](https://www.rfc-editor.org/rfc/rfc8345)
- [RFC 8346 — A YANG Data Model for Layer 3 Topologies](https://www.rfc-editor.org/rfc/rfc8346)
- `internal/discovery/discovery.go` — `Device` / `Edge` source structs
- `internal/graph/graph.go` — reconciliation and port-name normalisation
- ROADMAP.md § v2.0.0; issue #75
