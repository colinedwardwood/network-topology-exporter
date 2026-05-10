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
// # Critical implementation notes
//
//  1. Only adjacencies in state full(8) or twoWay(5) are emitted as edges.
//     States init(3), attempt(2), and down(1) represent incomplete or stale
//     adjacencies and must be filtered before edge construction.
//
//  2. LD-11 CIDR scope enforcement is applied here: ospfNbrIpAddr is an IPv4
//     address and can be checked against the allow-list. Neighbours outside
//     the list are surfaced as OutOfScopeNeighbour and never polled.
//
//  3. RFC 4750 OSPF-MIB is not widely implemented on modern network OS.
//     Cisco IOS-XR, Juniper Junos, and Arista EOS have varying levels of
//     OSPF MIB support. Treat empty walk results as normal, not as an error.
package ospf

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidOspfNbrTable = "1.3.6.1.2.1.14.10"
	// precedenceRank 6: ranked above IS-IS (5) is intentional — IS-IS is the
	// primary IGP on service-provider networks and carries richer TE data.
	// Ladder: LLDP=2, CDP=3, FDB=4, IS-IS=5, OSPF=6, BGP=7, MPLS-TE=8.
	precedenceRank = 6
)

// ospfNbrTable column numbers (RFC 4750 §11.2).
const (
	colNbrIPAddr = 1
	colNbrState  = 6
)

const (
	stateTwoWay = 5
	stateFull   = 8
)

type nbrRow struct {
	nbrIP net.IP
	state int
}

// Walk returns OSPF neighbour edges for the device at p.IP. Only neighbours
// in state full(8) or twoWay(5) produce edges. Neighbours outside allowedNets
// go to the OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("ospf %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	rows, err := walkOspfNbrTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("ospf nbrTable %s: %w", p.IP, err)
	}
	edges, oos := buildEdges(localDevice, rows, allowedNets)
	return edges, oos, nil
}

func walkOspfNbrTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*nbrRow, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidOspfNbrTable)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.14.10.1."
	rows := make(map[string]*nbrRow)
	for _, pdu := range pdus {
		col, key, ok := parseNbrOID(pdu.Name, prefix)
		if !ok {
			continue
		}
		row := rows[key]
		if row == nil {
			row = &nbrRow{}
			rows[key] = row
		}
		switch col {
		case colNbrIPAddr:
			if b := snmputil.PDUBytes(pdu); len(b) == 4 {
				ip := make(net.IP, 4)
				copy(ip, b)
				row.nbrIP = ip
			}
		case colNbrState:
			row.state = snmputil.PDUInt(pdu)
		}
	}
	return rows, nil
}

func buildEdges(localDevice string, rows map[string]*nbrRow, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for _, row := range rows {
		if row.nbrIP == nil {
			continue
		}
		if row.state != stateFull && row.state != stateTwoWay {
			continue
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(row.nbrIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				ReportingDevice: localDevice,
				NeighbourHint:   row.nbrIP.String(),
				LastSeen:        now,
			})
			continue
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			DstDevice:      row.nbrIP.String(),
			DiscoveryProto: "ospf",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ip",
			ObservedAt:     now,
		})
	}
	return edges, oos
}

// parseNbrOID extracts the column number and composite row key from an
// ospfNbrTable OID suffix. The suffix after the table prefix has the form
// "<col>.<ip0>.<ip1>.<ip2>.<ip3>.<addressLessIndex>".
func parseNbrOID(oid, prefix string) (col int, key string, ok bool) {
	if !strings.HasPrefix(oid, prefix) {
		return 0, "", false
	}
	rest := oid[len(prefix):]
	dotIdx := strings.IndexByte(rest, '.')
	if dotIdx < 0 {
		return 0, "", false
	}
	col, err := strconv.Atoi(rest[:dotIdx])
	if err != nil {
		return 0, "", false
	}
	key = rest[dotIdx+1:]
	// key must be "<ip0>.<ip1>.<ip2>.<ip3>.<addrLessIdx>" — exactly 4 dots.
	if strings.Count(key, ".") != 4 {
		return 0, "", false
	}
	parts := strings.SplitN(key, ".", 5)
	// Validate all 4 IP octets are in 0-255.
	for _, octet := range parts[:4] {
		v, err := strconv.Atoi(octet)
		if err != nil || v < 0 || v > 255 {
			return "", "", false
		}
	}
	return col, key, true
}
