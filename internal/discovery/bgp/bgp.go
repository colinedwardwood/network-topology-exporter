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
//   - prometheus/snmp_exporter (Apache 2.0) — BulkWalk table subtree,
//     group columns by index key.
//     https://github.com/prometheus/snmp_exporter
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
//   - iBGP vs eBGP classification is not emitted in v1. bgpPeerRemoteAs is
//     read from the walk results but the distinction is not reflected in
//     LinkKind ("ip" is used for all peers). A follow-up can set LinkKind to
//     "ibgp" or "ebgp" once the graph layer consumes that field.
package bgp

import (
	"context"
	"fmt"
	"net"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidBgpPeerTable = "1.3.6.1.2.1.15.3"
	precedenceRank  = 6
)

// bgpPeerTable column numbers (RFC 1657 §3.4).
const (
	colBgpPeerState      = 2
	colBgpPeerRemoteAddr = 7
	colBgpPeerRemoteAs   = 9
)

const bgpStateEstablished = 6

type bgpPeer struct {
	state    int
	remoteIP net.IP
	remoteAS int
}

// Walk returns BGP-peer edges for the device at p.IP. Only peers in
// state established(6) produce edges. Peers outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("bgp %s: %w", p.IP, err)
	}
	defer client.Conn.Close()

	peers, err := walkBgpPeerTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("bgp peer table %s: %w", p.IP, err)
	}

	edges, oos := buildEdges(localDevice, peers, allowedNets)
	return edges, oos, nil
}

func walkBgpPeerTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*bgpPeer, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidBgpPeerTable)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.15.3.1."
	peers := make(map[string]*bgpPeer)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, ipKey, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || ipKey == "" {
			continue
		}
		peer := peers[ipKey]
		if peer == nil {
			peer = &bgpPeer{}
			peers[ipKey] = peer
		}
		switch col {
		case colBgpPeerState:
			peer.state = snmputil.PDUInt(pdu)
		case colBgpPeerRemoteAddr:
			peer.remoteIP = pduIP(pdu)
		case colBgpPeerRemoteAs:
			peer.remoteAS = snmputil.PDUInt(pdu)
		}
	}
	return peers, nil
}

// pduIP extracts an IPv4 address from an SNMP PDU. gosnmp decodes IpAddress
// type OIDs as a dotted-decimal string; some test harnesses encode them as raw
// 4-byte slices. Both representations are handled.
func pduIP(pdu gsnmp.SnmpPDU) net.IP {
	switch v := pdu.Value.(type) {
	case string:
		if ip := net.ParseIP(v); ip != nil {
			return ip.To4()
		}
	case []byte:
		if len(v) == 4 {
			return net.IP(append([]byte(nil), v...))
		}
	}
	return nil
}

func buildEdges(localDevice string, peers map[string]*bgpPeer, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for _, peer := range peers {
		if peer.state != bgpStateEstablished {
			continue
		}
		if peer.remoteIP == nil {
			continue
		}

		if len(allowedNets) > 0 && !snmputil.IPInNets(peer.remoteIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				ReportingDevice: localDevice,
				NeighbourHint:   peer.remoteIP.String(),
				LastSeen:        now,
			})
			continue
		}

		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			DstDevice:      peer.remoteIP.String(),
			DiscoveryProto: "bgp",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceLow,
			Adjacency:      discovery.AdjacencyUnknown,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ip",
			ObservedAt:     now,
		})
	}
	return edges, oos
}
