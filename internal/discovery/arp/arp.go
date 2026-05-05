// Package arp is intentionally not a topology Prober.
//
// # Why ARP is not a topology source
//
// ARP tables (RFC 4293 IP-MIB ipNetToPhysicalTable, OID 1.3.6.1.2.1.4.35)
// record IP→MAC mappings on directly attached subnets. They do not encode
// link adjacency. Two devices sharing a /24 both appear in each other's ARP
// tables — that is L3 reachability, not a physical or logical link.
//
// This was the basis for removing ARP-as-topology from the design:
//
//   - Bejerano, Breitbart, Garofalakis, Rastogi — "Physical Topology
//     Discovery for Large Multisubnet Networks", IEEE INFOCOM 2003. The most
//     cited complete L2 topology algorithm uses ARP data only as an
//     IP→MAC→port resolution helper (resolving which IP address corresponds
//     to a MAC seen in the bridge FDB) — never as an independent edge source.
//     https://ieeexplore.ieee.org/document/1208686
//   - Pandey, Choi, Lee, Hong — "IP Network Topology Discovery Using SNMP",
//     ICOIN 2009. Also uses ipNetToPhysicalTable as a cross-reference tool
//     alongside the Bridge MIB, not as an adjacency claim.
//     https://ieeexplore.ieee.org/document/4897254
//
// # If IP→MAC resolution is needed
//
// The FDB topology algorithm (internal/discovery/fdb) requires resolving MAC
// addresses to IP addresses to identify the far-end device. If that becomes
// necessary, ARP table queries belong as an internal helper inside the fdb
// package — not as a standalone Prober emitting Edge values.
package arp
