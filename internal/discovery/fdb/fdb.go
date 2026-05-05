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
//
//  4. LD-11 CIDR scope enforcement is not applied here: FDB entries carry only
//     a MAC address, not an IP. Without ARP resolution (excluded by note 2),
//     there is no IP address to test against the allow-list.
package fdb

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidFdbTable      = "1.3.6.1.2.1.17.4.3"
	oidBasePortTable = "1.3.6.1.2.1.17.1.4"
	oidIfName        = "1.3.6.1.2.1.31.1.1.1.1"
	precedenceRank   = 4
	fdbStatusLearned = 3
)

// dot1dTpFdbTable column numbers (RFC 4188).
const (
	colFdbAddress = 1
	colFdbPort    = 2
	colFdbStatus  = 3
)

// dot1dBasePortTable column numbers (RFC 4188).
const colBasePortIfIndex = 2

type fdbEntry struct {
	mac    []byte
	port   int
	status int
}

// Walk returns FDB-derived edges for the device at p.IP. localDevice is the
// sysName from the SNMP SYSTEM walk. allowedNets is accepted for interface
// compatibility but not applied — FDB entries carry no IP address to check.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, _ []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb %s: %w", p.IP, err)
	}
	defer client.Conn.Close()

	entries, err := walkFdbTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb table %s: %w", p.IP, err)
	}
	bridgePorts, err := walkBasePortTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb baseport %s: %w", p.IP, err)
	}
	ifNames, err := walkIfNames(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb ifname %s: %w", p.IP, err)
	}
	return buildEdges(localDevice, entries, bridgePorts, ifNames), nil, nil
}

func walkFdbTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*fdbEntry, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidFdbTable)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.17.4.3.1."
	entries := make(map[string]*fdbEntry)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, macKey, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || macKey == "" {
			continue
		}
		e := entries[macKey]
		if e == nil {
			e = &fdbEntry{}
			entries[macKey] = e
		}
		switch col {
		case colFdbAddress:
			e.mac = snmputil.PDUBytes(pdu)
		case colFdbPort:
			e.port = snmputil.PDUInt(pdu)
		case colFdbStatus:
			e.status = snmputil.PDUInt(pdu)
		}
	}
	return entries, nil
}

func walkBasePortTable(ctx context.Context, client *gsnmp.GoSNMP) (map[int]int, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidBasePortTable)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.17.1.4.1."
	ports := make(map[int]int) // bridgePort → ifIndex
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, portStr, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || col != colBasePortIfIndex {
			continue
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		ports[portNum] = snmputil.PDUInt(pdu)
	}
	return ports, nil
}

func walkIfNames(ctx context.Context, client *gsnmp.GoSNMP) (map[int]string, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidIfName)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.31.1.1.1.1."
	names := make(map[int]string, len(pdus))
	for _, pdu := range pdus {
		idxStr, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		names[idx] = snmputil.PDUString(pdu)
	}
	return names, nil
}

// buildEdges applies the Bejerano direct/indirect classification: a port with
// exactly one learned MAC is AdjacencyDirect (one device); a port with multiple
// MACs is AdjacencyIndirect (downstream switch or trunk).
func buildEdges(localDevice string, entries map[string]*fdbEntry, bridgePorts map[int]int, ifNames map[int]string) []discovery.Edge {
	portMACs := make(map[int][]net.HardwareAddr)
	for _, e := range entries {
		if e.status != fdbStatusLearned || len(e.mac) != 6 {
			continue
		}
		mac := make(net.HardwareAddr, 6)
		copy(mac, e.mac)
		portMACs[e.port] = append(portMACs[e.port], mac)
	}

	now := time.Now()
	var edges []discovery.Edge
	for bridgePort, macs := range portMACs {
		ifIdx, ok := bridgePorts[bridgePort]
		if !ok {
			continue
		}
		localPort := ifNames[ifIdx]
		if localPort == "" {
			localPort = strconv.Itoa(ifIdx)
		}

		adjacency := discovery.AdjacencyDirect
		confidence := discovery.ConfidenceMedium
		if len(macs) > 1 {
			adjacency = discovery.AdjacencyIndirect
			confidence = discovery.ConfidenceLow
		}

		for _, mac := range macs {
			edges = append(edges, discovery.Edge{
				SrcDevice:      localDevice,
				SrcPort:        localPort,
				DstDevice:      mac.String(),
				DiscoveryProto: "fdb",
				Direction:      discovery.DirectionUnidirectional,
				Confidence:     confidence,
				Adjacency:      adjacency,
				PrecedenceRank: precedenceRank,
				LinkKind:       "ethernet",
				ObservedAt:     now,
			})
		}
	}
	return edges
}
