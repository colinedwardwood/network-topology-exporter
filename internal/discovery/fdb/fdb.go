// Package fdb infers L2 topology from the bridge forwarding database.
//
// # Specification sources
//
//   - RFC 4188 — Definitions of Managed Objects for Bridges (Bridge MIB v2,
//     SMIv2). Obsoletes RFC 1493. Defines dot1dTpFdbTable (OID
//     1.3.6.1.2.1.17.4.3): dot1dTpFdbAddress (.1) is the learned MAC,
//     dot1dTpFdbPort (.2) is the bridge port number, dot1dTpFdbStatus (.3)
//     is the entry state (learned=3, self=4, mgmt=5).
//   - RFC 2863 — The Interfaces Group MIB (IF-MIB). dot1dTpFdbPort gives a
//     bridge port number, not an ifIndex. The cross-reference chain is:
//     dot1dTpFdbPort → dot1dBasePortTable.dot1dBasePortIfIndex
//     (1.3.6.1.2.1.17.1.4.1.2) → ifXTable.ifName. Both walks are required.
//
// # Design references
//
//   - Lowekamp, O'Hallaron, Gross — "Topology Discovery for Large Ethernet
//     Networks", ACM SIGCOMM 2001. Proved that bridged Ethernet topology can
//     be inferred from standard SNMP Bridge MIBs alone, even when only a
//     subset of devices is accessible. Foundation for all FDB-based topology
//     algorithms. https://dl.acm.org/doi/10.1145/383059.383078
//   - Bejerano, Breitbart, Garofalakis, Rastogi — "Physical Topology
//     Discovery for Large Multisubnet Networks", IEEE INFOCOM 2003. First
//     complete multi-subnet algorithm. The direct/indirect adjacency
//     classification used here (one MAC on a port = direct; many MACs =
//     indirect downstream switch) is taken from this paper's core insight.
//     https://ieeexplore.ieee.org/document/1208686
//   - Breitbart et al. — "Topology Discovery in Heterogeneous IP Networks:
//     The NetInventory System", IEEE/ACM ToN 2004. System paper for the above;
//     describes how FDB-derived edges are combined with SNMP routing data and
//     ranked against stronger sources. https://dl.acm.org/doi/abs/10.1109/TNET.2004.828963
//   - "Improved algorithm for network topology discovery based on STP",
//     IEEE 2011 (ieeexplore.ieee.org/document/5689816) — addresses the
//     practical problem of incomplete FDB tables: bridges do not retain entries
//     for traffic they have not recently seen. This module must tolerate partial
//     FDB data and should not infer absence-of-edge from absence-of-entry.
//
// # Critical implementation notes
//
//  1. Only entries with dot1dTpFdbStatus=learned(3) are topology-relevant.
//     Status=self(4) is the device's own MAC; status=mgmt(5) is statically
//     configured. Both must be filtered before edge construction.
//
//  2. ARP data is NOT used to infer edges. The literature (Bejerano INFOCOM
//     2003; Pandey, ICOIN 2009) uses ARP tables as an IP→MAC→port resolution
//     helper when the IP address of a bridge-table MAC entry is needed — not
//     as an independent source of adjacency. This module uses only FDB + IF-MIB.
//
//  3. FDB entries age out (default 300 seconds on most platforms). An edge
//     that disappeared from the FDB may still be physically present. The graph
//     layer's LD-14 unconfirmed-link TTL should be set longer than the FDB
//     aging timer to avoid spurious remove events.
package fdb

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker reads dot1dTpFdbTable and emits one Edge per learned MAC.
type Walker struct{}

// Walk returns the FDB-derived edges for one device. Stub.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
