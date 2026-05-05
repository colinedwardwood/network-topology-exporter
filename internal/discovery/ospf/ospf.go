// Package ospf infers L3 topology from OSPF neighbour adjacencies.
//
// # Specification sources
//
//   - RFC 4750 — OSPF Version 2 Management Information Base. OID base
//     1.3.6.1.2.1.14. ospfNbrTable (1.3.6.1.2.1.14.10) contains one row per
//     OSPF neighbour; the relevant fields are ospfNbrIpAddr (.1),
//     ospfNbrRtrId (.3), ospfNbrState (.6), and ospfNbrHelloSuppressed (.14).
//     A neighbour in state full(8) or 2way(5) is an active adjacency.
//   - RFC 2328 — OSPF Version 2. Defines the OSPF protocol itself; needed to
//     understand area adjacency semantics and what ospfNbrState values mean.
//
// # Design references
//
//   - Breitbart et al. — "Topology Discovery in Heterogeneous IP Networks:
//     The NetInventory System", IEEE/ACM ToN 2004. Describes combining L2
//     (LLDP/FDB) and L3 (routing protocol) evidence to build a complete
//     topology picture; OSPF adjacency is the L3 component for IP-only paths.
//     https://dl.acm.org/doi/abs/10.1109/TNET.2004.828963
//
// # Notes
//
//   - OSPF adjacency means the two routers are in the same area and exchange
//     LSAs. As with BGP, physical adjacency is not implied — OSPF virtual
//     links and multi-access segments mean adjacencies can traverse multiple
//     L2 hops. Edges are emitted with Confidence=medium (stronger than BGP
//     because OSPF is a link-state protocol and adjacencies are typically
//     between directly-connected routers, but not guaranteed).
//   - RFC 4750 OSPF-MIB is not widely implemented on modern network OS.
//     Cisco IOS-XR, Juniper Junos, and Arista EOS have varying levels of
//     OSPF MIB support. Treat empty walk results as normal, not as an error.
package ospf

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker reads OSPF-MIB neighbour state and emits one Edge per adjacency.
type Walker struct{}

// Walk returns the OSPF-derived edges for one device. Stub.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
