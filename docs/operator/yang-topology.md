# RFC 8345 YANG topology mapping

> **Status: design reference (v2.0.0).** This document defines how the
> exporter's in-memory topology graph maps onto the RFC 8345 / RFC 8346 YANG
> data models. The YANG output path itself is planned for v2.0.0 behind
> `output.yang.enabled: false` (tracked in #75); this document is the mapping
> contract the implementation and its `pyang`/`yanglint` conformance tests will
> follow. It is published now so the mapping is reviewable independently of the
> code.

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
augmentation module** (working name `ntx-topology`, namespace TBD) that adds:

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

## Planned emission and validation (v2.0.0)

- **Config:** `output.yang.enabled` (default `false`) plus a destination
  (file path written each cycle, parallel to the snapshot, and/or a
  read endpoint). Exact surface decided when #75 is implemented.
- **Conformance CI:** a job validates the emitted document against the canonical
  RFC 8345 / RFC 8346 modules (plus the `ntx-topology` augmentation) using
  `pyang` and `yanglint`. The mapping above is the contract those tests assert.
- **Known gaps to encode as test expectations:** empty `l3-node-attributes`
  (no router-id/prefix collection today); termination-points are derived from
  observed edges, so a device with no discovered links contributes a node with
  no termination-points.

## References

- [RFC 8345 — A YANG Data Model for Network Topologies](https://www.rfc-editor.org/rfc/rfc8345)
- [RFC 8346 — A YANG Data Model for Layer 3 Topologies](https://www.rfc-editor.org/rfc/rfc8346)
- `internal/discovery/discovery.go` — `Device` / `Edge` source structs
- `internal/graph/graph.go` — reconciliation and port-name normalisation
- ROADMAP.md § v2.0.0; issue #75
