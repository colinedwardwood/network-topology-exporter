# Glossary

Abbreviations and domain terms used throughout the README, operator docs, and
metric/log output. Protocol entries note the MIB the exporter walks and where
the module is documented.

## Discovery protocols and transports

| Term | Expansion | Meaning in this project |
|---|---|---|
| SNMP | Simple Network Management Protocol | The only discovery transport today. The exporter polls devices over SNMP (v2c or v3) and walks the per-protocol MIB tables below. gNMI is roadmap work. |
| LLDP | Link Layer Discovery Protocol (IEEE 802.1AB) | Vendor-neutral L2 neighbour discovery; the highest-confidence edge source. Walked via LLDP-MIB. |
| CDP | Cisco Discovery Protocol | Cisco-proprietary equivalent of LLDP. Walked via CISCO-CDP-MIB. |
| BGP | Border Gateway Protocol | L3 routing protocol; established peerings become (low-confidence) edges. Walked via BGP4-MIB (RFC 4273) plus vendor BGP4V2 tables. |
| BGP4V2 | BGP version 4, MIB version 2 | The second-generation BGP MIB design (IPv6/VRF-capable). Never standardised by the IETF, so each vendor ships it under its own enterprise OID — hence the per-vendor walkers. |
| OSPF | Open Shortest Path First | L3 link-state routing protocol; full/two-way neighbours become edges. Walked via OSPF-MIB (RFC 4750). |
| IS-IS | Intermediate System to Intermediate System | L3 link-state routing protocol; up adjacencies become edges. Walked via ISIS-MIB (RFC 4444). |
| FDB | Forwarding DataBase | A switch's MAC-address learning table. MACs seen on exactly one port become L2 edges. Walked via BRIDGE-MIB / Q-BRIDGE-MIB (RFC 4188). |
| MPLS-TE | Multiprotocol Label Switching — Traffic Engineering | TE tunnels become edges to the egress router. Walked via MPLS-TE-STD-MIB (RFC 3812). |
| ARP | Address Resolution Protocol | IP-to-MAC mappings, used to resolve FDB MAC edges to device identities (not an edge source itself). |
| gNMI | gRPC Network Management Interface | Streaming-telemetry management protocol. Not yet a discovery transport — tracked on the [roadmap](../ROADMAP.md). |

## SNMP terms

| Term | Expansion | Meaning in this project |
|---|---|---|
| MIB | Management Information Base | The schema of objects an SNMP agent exposes. Each discovery module walks one or more MIB tables. |
| OID | Object IDentifier | The dotted-number address of a MIB object (e.g. `1.3.6.1.2.1.14.10` = `ospfNbrTable`). |
| PDU | Protocol Data Unit | One SNMP value on the wire. Decode-strictness counters (`decode_issues`, `quarantined_rows`) count rejected PDUs/rows. |
| USM | User-based Security Model | SNMPv3's authentication/privacy model — usernames plus auth (e.g. SHA) and privacy (e.g. AES) keys, configured via the credential profiles. |
| v2c | SNMP version 2c ("community") | Community-string authentication; no encryption. |
| sysName / sysUpTime / sysObjectID | — | Standard SNMP system-group objects used for device identity, uptime, and vendor detection respectively. |

## Topology and graph terms

| Term | Expansion | Meaning in this project |
|---|---|---|
| OOS | Out Of Scope | A neighbour whose IP falls outside `discovery.scope.cidr_allow_list`. OOS neighbours are never polled; they surface as `network_topology_out_of_scope_neighbours_total`, boundary-observation series (uncoordinated mode), and hub OOS hints (hub/spoke mode). |
| CIDR | Classless Inter-Domain Routing | The `a.b.c.d/nn` prefix notation used by the allow-list that bounds polling scope. |
| MAC | Media Access Control (address) | L2 hardware address; the raw `DstDevice` of an FDB edge before ARP correlation. |
| VLAN | Virtual Local Area Network | L2 segment. FDB walks may iterate per-VLAN on classic Cisco IOS (`modules.fdb.max_vlans`). |
| VRF | Virtual Routing and Forwarding | Per-tenant routing table; BGP4V2 vendor tables expose VRF-scoped peers that the RFC 4273 table cannot. |
| LSR | Label Switching Router | An MPLS-capable router; the egress LSR IP is the `DstDevice` of an MPLS-TE edge. |
| Clos | (after Charles Clos) | The multi-stage spine/leaf fabric topology used by one of the long-running-lab test rotations. |

## Output and interchange formats

| Term | Expansion | Meaning in this project |
|---|---|---|
| OTLP | OpenTelemetry protocol | The push protocol for the optional `output.otlp` signal path (metrics, logs, and opt-in traces). |
| OTel | OpenTelemetry | The observability framework OTLP belongs to; also the SDK used for the exporter's own tracing. |
| YANG | Yet Another Next Generation (data modeling language) | The IETF schema language. With `output.yang.enabled`, `GET /topology/yang` emits the graph as RFC 8345/8346 YANG-JSON. See [`operator/yang-topology.md`](operator/yang-topology.md). |
| NETCONF / RESTCONF | Network Configuration Protocol / its RESTful sibling | Management protocols whose tooling consumes YANG documents — the audience for the YANG output. |
| RFC | Request For Comments | An IETF standards document (e.g. RFC 8345, the network-topology YANG model). |
| IETF | Internet Engineering Task Force | The standards body behind the RFCs above. |

## Federation and operations

| Term | Expansion | Meaning in this project |
|---|---|---|
| mTLS | mutual TLS | Both ends present certificates. Required for all hub/spoke communication; the spoke's client cert is its identity. |
| HA | High Availability | The opt-in, Kubernetes-only hub mode: replicas elect a leader via a `Lease`; spokes fail over to the new leader. See [`operator/federation.md`](operator/federation.md#hub-high-availability-native-opt-in). |
| TLS / CA / PEM / PKI / CN | Transport Layer Security / Certificate Authority / Privacy-Enhanced Mail (cert file format) / Public Key Infrastructure / Common Name | Standard certificate machinery used by the mTLS setup in the federation runbook. |
| SLO / SLI | Service Level Objective / Indicator | Alerting targets and the measurements behind them — see [`operator/slos.md`](operator/slos.md). |
| TOCTOU | Time Of Check to Time Of Use | A race where a checked condition (e.g. "am I leader?") changes before the action depending on it completes. Discussed in the HA leader-flip acceptance window. |
| GA | General Availability | A stable, production-supported release — the bar described in [`operator/stability.md`](operator/stability.md). |

## Packaging and supply chain

| Term | Expansion | Meaning in this project |
|---|---|---|
| AGPL | GNU Affero General Public License (v3.0) | The project's license. |
| GHCR | GitHub Container Registry | Where released images are published (`ghcr.io/...`). |
| SLSA | Supply-chain Levels for Software Artifacts | The provenance-attestation framework attached to release builds. |
| SBOM | Software Bill Of Materials | Dependency inventory shipped with releases. |
