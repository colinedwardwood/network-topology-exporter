// Package fdb infers L2 topology from the bridge forwarding database.
//
// # Specification sources
//
//   - RFC 4188 — Definitions of Managed Objects for Bridges (Bridge MIB v2,
//     SMIv2). Obsoletes RFC 1493. Defines dot1dTpFdbTable (OID
//     1.3.6.1.2.1.17.4.3): dot1dTpFdbAddress (.1) is the learned MAC,
//     dot1dTpFdbPort (.2) is the bridge port number, dot1dTpFdbStatus (.3)
//     is the entry state (learned=3, self=4, mgmt=5). RFC 4188 §14.8.2 also
//     defines dot1dStpPortTable (OID 1.3.6.1.2.1.17.2.15): dot1dStpPortState
//     (.3) is the STP port state (disabled=1, blocking=2, listening=3,
//     learning=4, forwarding=5, broken=6). Only forwarding(5) ports pass
//     traffic; entries on all other states are suppressed.
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
//
//  5. Ports absent from dot1dStpPortTable are treated as forwarding. Some
//     devices do not populate STP state for all ports (access ports on
//     non-STP VLANs, management ports). Absence of an STP entry is not a
//     signal of blocking.
//
// Precedence rank: 4. Ranked below LLDP(2), CDP(3) and above OSPF(5), BGP(6).
// FDB edges carry no protocol-verified identity (only MACs), so they are
// treated as weaker evidence than control-plane adjacencies. When LLDP or CDP
// reports the same link, their edge wins.
package fdb

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidFdbTable         = "1.3.6.1.2.1.17.4.3"
	oidBasePortTable    = "1.3.6.1.2.1.17.1.4"
	oidStpPortTable     = "1.3.6.1.2.1.17.2.15"
	oidQBridgeFdbTable  = "1.3.6.1.2.1.17.7.1.2.2"
	oidVlanCurrentTable = "1.3.6.1.2.1.17.7.1.4.2"
	precedenceRank      = 4
	fdbStatusLearned    = 3
	stpStateForwarding  = 5
)

// dot1dTpFdbTable column numbers (RFC 4188).
const (
	colFdbAddress = 1
	colFdbPort    = 2
	colFdbStatus  = 3
)

// dot1qTpFdbTable column numbers (RFC 4363).
const (
	colQBridgePort   = 2
	colQBridgeStatus = 3
)

// dot1dBasePortTable column numbers (RFC 4188).
const colBasePortIfIndex = 2

// dot1dStpPortTable column numbers (RFC 4188 §14.8.2).
const colStpPortState = 3

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
	defer func() { _ = client.Conn.Close() }()

	entries, err := walkFdbTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb table %s: %w", p.IP, err)
	}
	// Q-BRIDGE walk failures are non-fatal: devices that implement only B-MIB
	// return an empty result or a no-such-object error, both of which are fine.
	if err := walkQBridgeFdbTable(ctx, client, entries); err != nil {
		slog.Debug("fdb: Q-BRIDGE walk failed; using B-MIB only", "device", p.IP, "err", err)
	}
	maxVlans := p.MaxVlans
	if maxVlans <= 0 {
		maxVlans = 100 // default when not set by caller
	}
	walkVlanCommunityFdbs(ctx, p, client, entries, maxVlans)
	bridgePorts, err := walkBasePortTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb baseport %s: %w", p.IP, err)
	}
	stpStates, err := walkStpPortStates(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb stpport %s: %w", p.IP, err)
	}
	ifNames, err := snmputil.WalkIfNamesWithFallback(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("fdb ifname %s: %w", p.IP, err)
	}
	return buildEdges(localDevice, entries, bridgePorts, ifNames, stpStates), nil, nil
}

func walkFdbTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*fdbEntry, error) {
	entries := make(map[string]*fdbEntry)
	return entries, walkFdbTableInto(ctx, client, entries)
}

func walkFdbTableInto(ctx context.Context, client *gsnmp.GoSNMP, entries map[string]*fdbEntry) error {
	pdus, err := snmputil.BulkWalk(ctx, client, oidFdbTable)
	if err != nil {
		return err
	}
	const prefix = ".1.3.6.1.2.1.17.4.3.1."
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
	return nil
}

// parseQBridgeIndex parses a Q-BRIDGE OID instance suffix of the form
// "{fdbId}.{mac1}.{mac2}.{mac3}.{mac4}.{mac5}.{mac6}". At least 7 dot-separated
// components are required. The last six are the MAC octets; the remainder is the
// fdbId (VLAN forwarding-database identifier), which is ignored by the caller.
func parseQBridgeIndex(rest string) (key string, mac []byte, ok bool) {
	parts := strings.Split(rest, ".")
	if len(parts) < 7 {
		return "", nil, false
	}
	macParts := parts[len(parts)-6:]
	macBytes := make([]byte, 6)
	for i, p := range macParts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return "", nil, false
		}
		macBytes[i] = byte(v)
	}
	return strings.Join(macParts, "."), macBytes, true
}

func walkQBridgeFdbTable(ctx context.Context, client *gsnmp.GoSNMP, entries map[string]*fdbEntry) error {
	pdus, err := snmputil.BulkWalk(ctx, client, oidQBridgeFdbTable)
	if err != nil {
		return err
	}
	const prefix = ".1.3.6.1.2.1.17.7.1.2.2.1."
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || rest == "" {
			continue
		}
		if col != colQBridgePort && col != colQBridgeStatus {
			continue
		}
		macKey, macBytes, ok := parseQBridgeIndex(rest)
		if !ok {
			continue
		}
		e := entries[macKey]
		if e == nil {
			e = &fdbEntry{}
			entries[macKey] = e
		}
		switch col {
		case colQBridgePort:
			e.port = snmputil.PDUInt(pdu)
		case colQBridgeStatus:
			e.status = snmputil.PDUInt(pdu)
		}
		if e.mac == nil {
			e.mac = macBytes
		}
	}
	return nil
}

// discoverVlanIDs walks dot1qVlanCurrentTable and returns a deduplicated,
// sorted list of active VLAN IDs. Returns nil on any error — the VLAN
// community walk is a best-effort IOS-only path, not a required step.
func discoverVlanIDs(ctx context.Context, client *gsnmp.GoSNMP) []int {
	pdus, err := snmputil.BulkWalk(ctx, client, oidVlanCurrentTable)
	if err != nil {
		return nil
	}
	const prefix = ".1.3.6.1.2.1.17.7.1.4.2.1."
	seen := make(map[int]struct{})
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		// OID instance suffix: {col}.{timeMark}.{vlanId}
		_, rest, ok := snmputil.SplitOIDComponent(suffix) // skip col
		if !ok || rest == "" {
			continue
		}
		_, vlanStr, ok := snmputil.SplitOIDComponent(rest) // skip timeMark
		if !ok || vlanStr == "" {
			continue
		}
		vlanID, err := strconv.Atoi(vlanStr)
		if err != nil || vlanID < 1 || vlanID > 4094 {
			continue
		}
		seen[vlanID] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// maxVlanConcurrency is the maximum number of concurrent per-VLAN SNMP sessions
// opened during a VLAN community FDB walk. Caps resource use on devices with
// 100+ VLANs while still providing meaningful parallelism.
const maxVlanConcurrency = 8

// walkVlanCommunityFdbs uses VLAN community-string indexing (community@vlanId)
// to walk dot1dTpFdbTable for each active VLAN on classic Cisco IOS devices.
// These devices maintain one BRIDGE-MIB instance per VLAN and expose it only
// through community-string indexing; Q-BRIDGE is not available on IOS 12.x/15.x.
// Entries already present in the map (from B-MIB or Q-BRIDGE) are not overwritten.
// maxVlans caps the number of VLANs iterated; if the discovered VLAN list is
// longer, a warning is logged and the remaining VLANs are skipped.
// Per-VLAN walks run in parallel, bounded by maxVlanConcurrency.
func walkVlanCommunityFdbs(ctx context.Context, p snmputil.Params, client *gsnmp.GoSNMP, entries map[string]*fdbEntry, maxVlans int) {
	if p.V3 || p.Community == "" {
		return
	}
	vlanIDs := discoverVlanIDs(ctx, client)
	if len(vlanIDs) > maxVlans {
		slog.WarnContext(ctx, "fdb: VLAN community walk truncated at max_vlans limit; increase fdb.max_vlans to see all VLANs",
			"discovered", len(vlanIDs), "max_vlans", maxVlans)
		vlanIDs = vlanIDs[:maxVlans]
	}

	type result struct {
		vlanEntries map[string]*fdbEntry
	}

	results := make([]result, len(vlanIDs))
	sem := make(chan struct{}, maxVlanConcurrency)
	var wg sync.WaitGroup

	for i, vlanID := range vlanIDs {
		wg.Add(1)
		go func(idx, vlan int) {
			defer wg.Done()

			// Acquire semaphore slot; respect context cancellation while waiting.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			vp := p
			vp.Community = fmt.Sprintf("%s@%d", p.Community, vlan)
			vlanClient, err := snmputil.Open(vp)
			if err != nil {
				slog.Debug("fdb: VLAN community open failed", "device", vp.IP, "vlan", vlan, "err", err)
				return
			}
			defer func() { _ = vlanClient.Conn.Close() }()
			vlanEntries := make(map[string]*fdbEntry)
			if err := walkFdbTableInto(ctx, vlanClient, vlanEntries); err != nil {
				slog.Debug("fdb: VLAN community walk incomplete", "device", vp.IP, "vlan", vlan, "err", err)
			}
			results[idx] = result{vlanEntries: vlanEntries}
		}(i, vlanID)
	}

	wg.Wait()

	// Merge per-goroutine maps into entries; don't overwrite existing keys.
	for _, r := range results {
		for key, e := range r.vlanEntries {
			if _, exists := entries[key]; !exists {
				entries[key] = e
			}
		}
	}
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

// walkStpPortStates reads dot1dStpPortTable (RFC 4188 §14.8.2) and returns a
// map of bridge port number → STP state. Only column 3 (dot1dStpPortState)
// is consumed. Ports absent from the table are treated as forwarding by the
// caller; this function never synthesises entries.
func walkStpPortStates(ctx context.Context, client *gsnmp.GoSNMP) (map[int]int, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidStpPortTable)
	if err != nil {
		return nil, err
	}
	const prefix = ".1.3.6.1.2.1.17.2.15.1."
	states := make(map[int]int) // bridgePort → STP state
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, portStr, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || col != colStpPortState {
			continue
		}
		portNum, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		states[portNum] = snmputil.PDUInt(pdu)
	}
	return states, nil
}

// buildEdges applies the Bejerano direct/indirect classification: a port with
// exactly one learned MAC is AdjacencyDirect (one device); a port with multiple
// MACs is AdjacencyIndirect (downstream switch or trunk). Ports with a known
// STP state other than forwarding(5) are skipped entirely — their FDB entries
// are stale and do not represent active forwarding paths. Ports absent from
// stpStates are passed through; not all devices populate STP state for every
// port (e.g. access ports on non-STP VLANs).
func buildEdges(localDevice string, entries map[string]*fdbEntry, bridgePorts map[int]int, ifNames map[int]string, stpStates map[int]int) []discovery.Edge {
	portMACs := make(map[int][]net.HardwareAddr)
	for _, e := range entries {
		if e.status != fdbStatusLearned || len(e.mac) != 6 {
			continue
		}
		if state, ok := stpStates[e.port]; ok && state != stpStateForwarding {
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
			slog.Debug("fdb: bridge port has no ifIndex mapping", "bridge_port", bridgePort)
			continue
		}
		localPort := ifNames[ifIdx]
		if localPort == "" {
			localPort = fmt.Sprintf("if%d", ifIdx)
		}

		// AdjacencyIndirect ports (len(macs) > 1) are downstream switch trunks or
		// access ports with many hosts. Emitting one edge per MAC would create an
		// unbounded number of Prometheus series on large switches (cardinality bomb).
		// Without L3 ARP correlation the MAC cannot be mapped to a device identity
		// anyway, so these edges would be misleading. Suppress them entirely.
		if len(macs) != 1 {
			continue
		}

		rawMAC := macs[0].String()
		slog.Debug("fdb: direct peer",
			"local_device", localDevice, "local_port", localPort,
			"dst_device", rawMAC)
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        localPort,
			DstDevice:      rawMAC,
			DiscoveryProto: "fdb",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ethernet",
			ObservedAt:     now,
		})
	}
	return edges
}
