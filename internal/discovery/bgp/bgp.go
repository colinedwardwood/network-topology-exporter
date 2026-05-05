// Package bgp infers L3 topology from BGP peer adjacencies.
//
// # Specification sources
//
//   - RFC 1657 — Definitions of Managed Objects for the Fourth Version of the
//     Border Gateway Protocol (BGP4) Using SMIv2. OID base 1.3.6.1.2.1.15.
//     bgpPeerTable (1.3.6.1.2.1.15.3) contains one row per BGP peer; the
//     relevant fields are bgpPeerState (.2), bgpPeerRemoteAddr (.7), and
//     bgpPeerRemoteAs (.9). A peer in state established(6) is an active
//     adjacency.
//
// # Design references
//
//   - Donnet, Friedman — "Internet Topology Discovery: A Survey", IEEE
//     Communications Surveys and Tutorials, vol. 9 no. 4, 2007. Places BGP
//     table analysis in the broader context of AS-level topology discovery;
//     confirms that BGP adjacency is a logical relationship, not evidence of
//     physical proximity. https://hal.science/hal-01151820
//   - Motamedi, Rejaie, Willinger — "A Survey of Techniques for Internet
//     Topology Discovery", IEEE COMST 2015. Surveys BGP-based topology methods
//     and their limitations; the key lesson is that BGP peers are often not
//     directly connected at L2.
//     https://ieeexplore.ieee.org/document/6970764/
//
// # Notes
//
//   - BGP adjacency means the two routers exchange BGP UPDATE messages; it
//     does not mean they are physically adjacent. iBGP sessions routinely
//     span multiple hops through intermediate switches. This module emits
//     edges with Confidence=low and Adjacency=unknown to reflect this.
//   - RFC 1657 BGP4-MIB SNMP support is deprecated or incomplete in many
//     modern router OS versions (Cisco IOS-XR, Arista EOS, Juniper Junos
//     post-18.x). SNMP BGP walks may return empty results on modern gear.
//     Streaming telemetry (gNMI) is the preferred path for BGP adjacency
//     on those platforms, but is out of scope for v1.
//   - iBGP vs eBGP is determined by comparing bgpPeerRemoteAs to the local
//     bgpLocalAs (1.3.6.1.2.1.15.2.0). Same AS = iBGP edge; different = eBGP.
package bgp

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker reads BGP4-MIB peer state and emits one Edge per peer relationship.
type Walker struct{}

// Walk returns the BGP-derived edges for one device. Stub.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
