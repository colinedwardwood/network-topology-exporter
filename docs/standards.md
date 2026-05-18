# Standards Compliance Matrix

## Implemented Standards

| Standard | Scope in this exporter | Notes |
|---|---|---|
| **IEEE 802.1AB-2016 (LLDP)** | `lldpRemTable` walk; chassis/port ID decode for subtypes 1–7; TTL aging delegated to SNMP agent; CIDR allow-list scope enforcement on network-address chassis IDs | Primary topology discovery protocol. Mandatory-field validation (chassis ID, port ID, subtype range, MAC length, network-address family prefix) enforced in `buildEdges`. |
| **ANSI/TIA-1057 (LLDP-MED)** | Not implemented | LLDP-MED extensions (media endpoint discovery) are irrelevant for network-infrastructure topology. |
| **Cisco CDP (proprietary)** | `cdpCacheTable` walk; device ID, port ID, address decode | Implemented as a higher-precedence fallback for Cisco infrastructure that pre-dates LLDP deployment. |
| **IEEE 802.1D-2004 (STP)** | `dot1dStpPortState` walk; forwarding(5) filter in FDB module | STP state used to suppress stale FDB entries on non-forwarding ports. |
| **IEEE 802.1Q-2014 (VLANs)** | Q-BRIDGE-MIB `dot1qTpFdbTable`; VLAN-community string walk | FDB per-VLAN disambiguation for switches that partition FDB tables per VLAN. |
| **IETF RFC 4188 (Bridge MIB)** | `dot1dTpFdbTable`, `dot1dBasePortTable` | Base FDB table walk for switches implementing B-MIB only. RFC 4188 obsoletes RFC 1493. |
| **IETF RFC 4363 (Q-Bridge MIB)** | `dot1qTpFdbTable` extensions | VLAN-aware forwarding-table extensions to the Bridge MIB. |
| **IETF RFC 4273 (BGP4-MIB)** | `bgpPeerTable` walk; peer state, remote address, and remote AS decode | BGP peer adjacency edges at precedence rank 7. Remote AS is attached as edge metadata (`bgp.remote_as`). |
| **IETF RFC 3414 (SNMP v3 USM)** | SNMPv3 USM with SHA-family auth (SHA / SHA-256 / SHA-384 / SHA-512) and AES-family priv (AES / AES-192 / AES-256). Per-profile credentials configured via `credentials.profiles[].type: snmp_v3`. | MD5 auth and DES priv are rejected at startup as cryptographically broken. |
| **IETF RFC 4750 (OSPF-MIB)** | `ospfNbrTable` walk; neighbour state decode | OSPF adjacency edges at precedence rank 6. Supersedes the obsoleted RFC 1850. |
| **IETF RFC 4444 (IS-IS MIB)** | `isisISAdjTable`, `isisISAdjIPAddrTable`, `isisCircTable` walk | IS-IS adjacency edges at precedence rank 5. Hard-fail on adjState decode errors; optional circuit/ifDescr enrichment. |
| **IETF RFC 3812 (MPLS-TE MIB)** | `mplsTunnelTable`; operStatus and adminStatus decode | MPLS-TE tunnel edges at precedence rank 8. Hard-fail on operStatus; adminStatus is optional metadata. |
| **IETF RFC 2863 (IF-MIB)** | `ifDescr` (OID `1.3.6.1.2.1.2.2.1.2`), `ifName` (OID `1.3.6.1.2.1.31.1.1.1.1`) | Interface name resolution used by IS-IS, FDB, and CDP modules to map port indices to human-readable names. |
| **IETF RFC 1213 (MIB-II)** | `sysName`, `sysDescr`, `sysUpTime`, `sysObjectID` from `system` group; `ipNetToMediaTable` (`ipNetToMediaPhysAddress`) for ARP-based MAC→IP enrichment | Device identity and uptime. `sysName` is the canonical device identifier (normalized lowercase). ARP enrichment is gated by `modules.arp.enabled` (default false, consistent with every other module toggle) and is *not* an edge source — it backfills MAC→IP mappings for FDB stitching when LLDP/CDP do not name the neighbour. Enable alongside `modules.fdb.enabled: true`; the exporter logs a startup warning if FDB is on without ARP. |

## Explicitly Not Implemented

| Standard | Reason |
|---|---|
| **IETF RFC 2922 (Physical Topology MIB, PTOPO-MIB)** | RFC 2922 defines a MIB for physical topology discovery (port-to-port adjacency) but is almost never deployed on real equipment. Vendors universally implement LLDP (IEEE 802.1AB) instead. Implementing PTOPO-MIB would require polling an object that returns empty results on all known production hardware. The LLDP module covers the same use case with universally-supported hardware. |
| **IETF RFC 2674 (802.1p/Q MIB)** | VLAN membership topology is out of scope; we use Q-BRIDGE solely for FDB per-VLAN disambiguation. |
| **IETF RFC 3635 (Ethernet-like interfaces MIB)** | Ethernet physical statistics are not part of topology discovery. |
| **IETF RFC 6353 (HTTPS-based DTLS for SNMP)** | Not implemented. SNMP over TLS/DTLS is not deployed widely enough to justify the operational complexity. |

## MIB Object Reference

Key OID prefixes used by each module:

| Module | OID prefix | Description |
|---|---|---|
| LLDP | `1.0.8802.1.1.2.1.4` | `lldpRemTable` |
| LLDP (local port) | `1.0.8802.1.1.2.1.3.7` | `lldpLocPortTable` |
| FDB (B-MIB) | `1.3.6.1.2.1.17.4.3` | `dot1dTpFdbTable` |
| FDB (Q-BRIDGE) | `1.3.6.1.2.1.17.7.1.2.2` | `dot1qTpFdbTable` |
| FDB (base port) | `1.3.6.1.2.1.17.1.4` | `dot1dBasePortTable` |
| FDB (STP state) | `1.3.6.1.2.1.17.2.15` | `dot1dStpPortState` |
| BGP | `1.3.6.1.2.1.15.3` | `bgpPeerTable` |
| OSPF | `1.3.6.1.2.1.14.10` | `ospfNbrTable` |
| IS-IS adj | `1.3.6.1.2.1.138.1.6.1` | `isisISAdjTable` |
| IS-IS adj IP | `1.3.6.1.2.1.138.1.6.2` | `isisISAdjIPAddrTable` |
| IS-IS circuit | `1.3.6.1.2.1.138.1.4.1` | `isisCircTable` |
| MPLS-TE | `1.3.6.1.2.1.10.166.3.2.2` | `mplsTunnelTable` |
| IF-MIB ifDescr | `1.3.6.1.2.1.2.2.1.2` | Interface description |
| IF-MIB ifName | `1.3.6.1.2.1.31.1.1.1.1` | Interface name |
| MIB-II system | `1.3.6.1.2.1.1` | Device identity group |
