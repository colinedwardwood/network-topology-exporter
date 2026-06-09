// Package ospf infers L3 topology from OSPF neighbour adjacencies.
//
// # Specification sources
//
//   - RFC 4750 — OSPF Version 2 Management Information Base. OID base
//     1.3.6.1.2.1.14. ospfNbrTable (1.3.6.1.2.1.14.10) contains one row per
//     OSPF neighbour; the relevant fields are ospfNbrIpAddr (.1),
//     ospfNbrRtrId (.3), ospfNbrState (.6), and ospfNbrHelloSuppressed (.14).
//     A neighbour in state full(8) or twoWay(4) is an active adjacency.
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
//  1. Only adjacencies in state full(8) or twoWay(4) are emitted as edges.
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
	stateTwoWay = 4
	stateFull   = 8
)

// walkerOSPF is the fixed walker label for network_topology_walker_outcome_total
// (issue #98). The outcome label values and the {walker, outcome} forwarder live
// in snmputil (snmputil.Outcome*, snmputil.RecordProtocolWalkerOutcome).
const walkerOSPF = "ospf"

type nbrRow struct {
	nbrIP net.IP
	state int
}

// Walk returns OSPF neighbour edges for the device at p.IP. Only neighbours
// in state full(8) or twoWay(4) produce edges. Neighbours outside allowedNets
// go to the OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, release, err := snmputil.Acquire(p)
	if err != nil {
		snmputil.RecordProtocolWalkerOutcome(&p, walkerOSPF, snmputil.OutcomeError)
		return nil, nil, fmt.Errorf("ospf %s: %w", p.IP, err)
	}
	defer release()

	// ospfNbrTable is the base table for the outcome accounting: hadPDUs
	// distinguishes "MIB unimplemented" (zero PDUs — common on modern OS that
	// don't ship the RFC 4750 MIB) from a device that does report neighbours.
	rows, hadPDUs, err := walkOspfNbrTable(ctx, client)
	if err != nil {
		snmputil.RecordProtocolWalkerOutcome(&p, walkerOSPF, snmputil.OutcomeError)
		return nil, nil, fmt.Errorf("ospf nbrTable %s: %w", p.IP, err)
	}
	edges, oos, decoded := buildEdges(localDevice, rows, allowedNets)

	// Terminal outcome classification (edges / mib_unimplemented / no_neighbours
	// / walker_drift) lives in snmputil.ClassifyNeighbourOutcome, shared with the
	// LLDP/CDP/FDB walkers.
	snmputil.RecordProtocolWalkerOutcome(&p, walkerOSPF, snmputil.ClassifyNeighbourOutcome(len(edges), hadPDUs, decoded))

	return edges, oos, nil
}

func walkOspfNbrTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*nbrRow, bool, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidOspfNbrTable)
	if err != nil {
		return nil, false, err
	}
	hadPDUs := len(pdus) > 0
	const prefix = ".1.3.6.1.2.1.14.10.1."
	rows := make(map[string]*nbrRow)
	for _, pdu := range pdus {
		col, key, ok := parseNbrOID(pdu.Name, prefix)
		if !ok {
			snmputil.ReportDecodeIssue(ctx, walkerOSPF, oidOspfNbrTable, "oid_suffix_malformed", 1)
			continue
		}
		row := rows[key]
		if row == nil {
			row = &nbrRow{}
			rows[key] = row
		}
		switch col {
		case colNbrIPAddr:
			row.nbrIP = snmputil.PDUIPv4(pdu)
			if row.nbrIP == nil {
				snmputil.ReportDecodeIssue(ctx, walkerOSPF, oidOspfNbrTable, "nbr_ip_undecodable", 1)
			}
		case colNbrState:
			row.state = snmputil.PDUInt(pdu)
		}
	}
	return rows, hadPDUs, nil
}

// buildEdges returns (edges, oos, decoded). decoded reports whether at least
// one neighbour row decoded cleanly — i.e. carried a usable (non-unspecified,
// non-link-local, non-loopback) neighbour IP — regardless of its adjacency
// state or scope membership. The Walk-level outcome accounting uses it to
// distinguish "no_neighbours" (rows decoded, none usable) from "walker_drift"
// (PDUs arrived but every row was decoder-rejected). See issue #98.
func buildEdges(localDevice string, rows map[string]*nbrRow, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, bool) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour
	var decoded bool

	for _, row := range rows {
		if row.nbrIP == nil {
			continue
		}
		// Unnumbered P2P links report 0.0.0.0 as ospfNbrIpAddr; skip them to
		// avoid emitting an edge with DstDevice="0.0.0.0".
		if row.nbrIP.IsUnspecified() || row.nbrIP.IsLinkLocalUnicast() || row.nbrIP.IsLoopback() {
			continue
		}
		// The row carries a usable neighbour IP: it decoded cleanly. A
		// non-adjacent state or out-of-scope address below is a usability
		// filter, not decoder drift (issue #98).
		decoded = true
		if row.state != stateFull && row.state != stateTwoWay {
			continue
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(row.nbrIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "ospf",
				ReportingDevice: localDevice,
				NeighbourHint:   row.nbrIP.String(),
				FirstSeen:       now,
				LastSeen:        now,
			})
			continue
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			DstDevice:      row.nbrIP.String(),
			DiscoveryProto: discovery.DiscoveryProtocolOSPF,
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       discovery.LinkKindIP,
			ObservedAt:     now,
		})
	}
	return edges, oos, decoded
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
			return 0, "", false
		}
	}
	// Validate addressLessIndex is a non-negative integer.
	if _, err := strconv.Atoi(parts[4]); err != nil {
		return 0, "", false
	}
	return col, key, true
}
