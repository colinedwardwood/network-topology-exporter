// Package mpls infers MPLS-TE tunnel topology from the MPLS-TE-MIB.
//
// # Specification sources
//
//   - RFC 3812 — Multiprotocol Label Switching (MPLS) Traffic Engineering
//     (TE) Management Information Base (MIB). OID base 1.3.6.1.2.1.10.166.3.
//     mplsTunnelTable (1.3.6.1.2.1.10.166.3.2.2) contains one row per MPLS-TE
//     tunnel; mplsTunnelOperStatus (.1.17) gives the operational status.
//     The table index encodes tunnelIndex, tunnelInstance, ingressLSRId, and
//     egressLSRId; the ingress and egress LSR IDs are IPv4 addresses.
//
// # Critical implementation notes
//
//  1. Only tunnels with mplsTunnelOperStatus == up(1) are emitted as edges.
//     Other status values (down, testing, unknown, dormant, notPresent,
//     lowerLayerDown) indicate the tunnel is not actively forwarding traffic.
//
//  2. The OID suffix after the mplsTunnelOperStatus column prefix has exactly
//     10 dot-separated components: tunnelIdx, tunnelInstance, ig0..ig3, eg0..eg3.
//     Entries with a different component count are silently skipped.
//
//  3. SrcPort encodes the tunnel index as "te-tunnel{idx}" so the graph layer
//     can distinguish multiple tunnels to the same egress LSR.
package mpls

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidMplsTunnelOperStatus = "1.3.6.1.2.1.10.166.3.2.2.1.17"
	mplsTunnelOperUp        = 1
	// Rank ladder: LLDP/CDP=1-2, OSPF=5, BGP=6, MPLS-TE=7, IS-IS=8.
	// Higher rank = lower precedence in graph merge.
	precedenceRank = 7
)

// Walk returns MPLS-TE tunnel edges for the device at p.IP. Only tunnels with
// operStatus up(1) produce edges. Egress LSR IPs outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("mpls_te %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	pdus, err := snmputil.BulkWalk(ctx, client, oidMplsTunnelOperStatus)
	if err != nil {
		return nil, nil, fmt.Errorf("mpls_te tunnel table %s: %w", p.IP, err)
	}

	const prefix = "." + oidMplsTunnelOperStatus + "."
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		if snmputil.PDUInt(pdu) != mplsTunnelOperUp {
			continue
		}
		tunnelIdx, egressIP, ok := parseTunnelSuffix(suffix)
		if !ok {
			continue
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(egressIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				ReportingDevice: localDevice,
				NeighbourHint:   egressIP.String(),
				LastSeen:        now,
			})
			continue
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        fmt.Sprintf("te-tunnel%d", tunnelIdx),
			DstDevice:      egressIP.String(),
			DiscoveryProto: "mpls_te",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "mpls-te",
			ObservedAt:     now,
		})
	}
	return edges, oos, nil
}

// parseTunnelSuffix parses the OID suffix that follows the mplsTunnelOperStatus
// column prefix. The suffix must have exactly 10 dot-separated components:
// tunnelIdx, tunnelInstance, ig0..ig3, eg0..eg3. Returns the tunnel index as an
// integer and the egress LSR IPv4 address. Returns ok=false for any malformed
// suffix, including a non-integer tunnel index.
func parseTunnelSuffix(suffix string) (tunnelIdx int, egressIP net.IP, ok bool) {
	suffix = strings.TrimPrefix(suffix, ".")
	parts := strings.Split(suffix, ".")
	if len(parts) != 10 {
		return 0, nil, false
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, nil, false
	}
	ip, ipOK := parseIPFromParts(parts[6:10])
	if !ipOK {
		return 0, nil, false
	}
	return idx, ip, true
}

// parseIPFromParts converts 4 decimal-string octets into an IPv4 net.IP.
func parseIPFromParts(parts []string) (net.IP, bool) {
	if len(parts) != 4 {
		return nil, false
	}
	b := make([]byte, 4)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return nil, false
		}
		b[i] = byte(v)
	}
	return net.IP(b), true
}
