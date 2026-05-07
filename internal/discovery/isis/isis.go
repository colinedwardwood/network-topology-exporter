// Package isis infers L3 topology from IS-IS adjacencies.
//
// # Specification sources
//
//   - RFC 4444 — Management Information Base for Intermediate System to
//     Intermediate System (IS-IS). OID base 1.3.6.1.2.1.138. The adjacency
//     table (isisISAdjTable, 1.3.6.1.2.1.138.1.6.1) holds one row per IS-IS
//     adjacency; isisISAdjState (.2) gives the adjacency state. The adjacency
//     IP address table (isisISAdjIPAddrTable, 1.3.6.1.2.1.138.1.6.2) holds
//     the IP addresses associated with each adjacency.
//
// # Critical implementation notes
//
//  1. Only adjacencies in state up(3) are emitted as edges. States down(1),
//     initializing(2), and failed(4) represent incomplete or stale adjacencies
//     and are filtered before edge construction.
//
//  2. IPv6 adjacency IP entries use addrType=2; this implementation only
//     handles addrType=1 (IPv4, addrLen=4). IPv6 entries are silently skipped.
//
//  3. The OID suffix for isisISAdjIPAddrEntry has the form
//     {sysInst}.{circIdx}.{adjIdx}.{addrType}.{addrLen}.{addr octets}.
//     The adjKey is extracted by splitting on "." and dropping the last 6
//     tail components (addrType + addrLen + 4 octets). Using LastIndex(".1.4.")
//     is unsafe when adjIdx==4 and the IP starts with 1.4.x.x.
package isis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidISISAdjState  = "1.3.6.1.2.1.138.1.6.1.1.2"
	oidISISAdjIPAddr = "1.3.6.1.2.1.138.1.6.2.1.2"
	// precedenceRank 5: IS-IS ranked above OSPF (6) because it is more commonly
	// the primary IGP on service-provider networks and carries richer TE data.
	// Ladder: LLDP=2, CDP=3, FDB=4, IS-IS=5, OSPF=6, BGP=7, MPLS-TE=8.
	precedenceRank = 5
	isisAdjStateUp = 3
)

// Walk returns IS-IS adjacency edges for the device at p.IP. Only adjacencies
// in state up(3) produce edges. Neighbours outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("isis %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	states, err := walkAdjStates(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("isis adjState %s: %w", p.IP, err)
	}

	edges, oos, err := walkAdjIPAddrs(ctx, client, localDevice, states, allowedNets)
	if err != nil {
		return nil, nil, fmt.Errorf("isis adjIPAddr %s: %w", p.IP, err)
	}
	return edges, oos, nil
}

func walkAdjStates(ctx context.Context, client *gsnmp.GoSNMP) (map[string]int, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidISISAdjState)
	if err != nil {
		return nil, err
	}
	const prefix = "." + oidISISAdjState + "."
	states := make(map[string]int, len(pdus))
	for _, pdu := range pdus {
		adjKey, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		states[adjKey] = snmputil.PDUInt(pdu)
	}
	return states, nil
}

func walkAdjIPAddrs(ctx context.Context, client *gsnmp.GoSNMP, localDevice string, states map[string]int, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidISISAdjIPAddr)
	if err != nil {
		return nil, nil, err
	}
	const prefix = "." + oidISISAdjIPAddr + "."
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		parts := strings.Split(suffix, ".")
		const ipv4TailLen = 6
		if len(parts) <= ipv4TailLen || parts[len(parts)-ipv4TailLen] != "1" || parts[len(parts)-ipv4TailLen+1] != "4" {
			continue
		}
		adjKey := strings.Join(parts[:len(parts)-ipv4TailLen], ".")
		state, known := states[adjKey]
		if !known || state != isisAdjStateUp {
			continue
		}
		ip := snmputil.PDUIPv4(pdu)
		if ip == nil {
			continue
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(ip, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				ReportingDevice: localDevice,
				NeighbourHint:   ip.String(),
				LastSeen:        now,
			})
			continue
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			DstDevice:      ip.String(),
			DiscoveryProto: "isis",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ip",
			ObservedAt:     now,
		})
	}
	return edges, oos, nil
}
