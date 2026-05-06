package fdb

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// buildEdges: one MAC on a port → AdjacencyDirect, ConfidenceMedium.
func TestBuildEdgesDirectAdjacency(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.10.187.204.221.238": {mac: []byte{0, 10, 187, 204, 221, 238}, port: 1, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{1: 2}
	ifNames := map[int]string{2: "GigabitEthernet0/1"}

	edges := buildEdges("sw-01", entries, bridgePorts, ifNames, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.SrcDevice != "sw-01" {
		t.Errorf("SrcDevice = %q, want sw-01", e.SrcDevice)
	}
	if e.SrcPort != "GigabitEthernet0/1" {
		t.Errorf("SrcPort = %q, want GigabitEthernet0/1", e.SrcPort)
	}
	if e.DstDevice != "00:0a:bb:cc:dd:ee" {
		t.Errorf("DstDevice = %q, want 00:0a:bb:cc:dd:ee", e.DstDevice)
	}
	if e.Adjacency != discovery.AdjacencyDirect {
		t.Errorf("Adjacency = %q, want direct", e.Adjacency)
	}
	if e.Confidence != discovery.ConfidenceMedium {
		t.Errorf("Confidence = %q, want medium", e.Confidence)
	}
	if e.DiscoveryProto != "fdb" {
		t.Errorf("DiscoveryProto = %q, want fdb", e.DiscoveryProto)
	}
	if e.PrecedenceRank != precedenceRank {
		t.Errorf("PrecedenceRank = %d, want %d", e.PrecedenceRank, precedenceRank)
	}
}

// buildEdges: multiple MACs on a port → AdjacencyIndirect, ConfidenceLow.
func TestBuildEdgesIndirectAdjacency(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 1, status: fdbStatusLearned},
		"0.1.2.3.4.6": {mac: []byte{0, 1, 2, 3, 4, 6}, port: 1, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{1: 3}
	ifNames := map[int]string{3: "Ethernet1/1"}

	edges := buildEdges("sw-01", entries, bridgePorts, ifNames, nil)
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	for _, e := range edges {
		if e.Adjacency != discovery.AdjacencyIndirect {
			t.Errorf("Adjacency = %q, want indirect", e.Adjacency)
		}
		if e.Confidence != discovery.ConfidenceLow {
			t.Errorf("Confidence = %q, want low", e.Confidence)
		}
	}
}

// buildEdges: status=self(4) and status=mgmt(5) entries are filtered out.
func TestBuildEdgesFiltersNonLearned(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 1, status: 4}, // self
		"0.1.2.3.4.6": {mac: []byte{0, 1, 2, 3, 4, 6}, port: 1, status: 5}, // mgmt
		"0.1.2.3.4.7": {mac: []byte{0, 1, 2, 3, 4, 7}, port: 1, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{1: 2}
	ifNames := map[int]string{2: "Gi0/1"}

	edges := buildEdges("sw", entries, bridgePorts, ifNames, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (only learned), got %d", len(edges))
	}
	if edges[0].DstDevice != "00:01:02:03:04:07" {
		t.Errorf("DstDevice = %q, want 00:01:02:03:04:07", edges[0].DstDevice)
	}
}

// buildEdges: MAC with wrong byte length is skipped.
func TestBuildEdgesSkipsInvalidMAC(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3":     {mac: []byte{0, 1, 2, 3}, port: 1, status: fdbStatusLearned}, // 4 bytes, not 6
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 1, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{1: 2}
	ifNames := map[int]string{2: "Gi0/1"}

	edges := buildEdges("sw", entries, bridgePorts, ifNames, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (invalid MAC skipped), got %d", len(edges))
	}
}

// buildEdges: FDB entry whose bridge port has no base-port mapping is skipped.
func TestBuildEdgesMissingBridgePort(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 99, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{} // port 99 not present
	ifNames := map[int]string{}

	edges := buildEdges("sw", entries, bridgePorts, ifNames, nil)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges when bridge port unmapped, got %d", len(edges))
	}
}

// buildEdges: falls back to ifIndex string when ifName is missing.
func TestBuildEdgesFallbackPortName(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 1, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{1: 7}
	ifNames := map[int]string{} // ifIndex 7 has no name

	edges := buildEdges("sw", entries, bridgePorts, ifNames, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].SrcPort != "7" {
		t.Errorf("SrcPort = %q, want \"7\" (fallback to ifIndex)", edges[0].SrcPort)
	}
}

// buildEdges: port in STP forwarding state produces an edge.
func TestBuildEdgesStpForwardingAllowed(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 2, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{2: 4}
	ifNames := map[int]string{4: "Ethernet0/2"}
	stpStates := map[int]int{2: stpStateForwarding}

	edges := buildEdges("sw", entries, bridgePorts, ifNames, stpStates)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge for forwarding STP port, got %d", len(edges))
	}
}

// buildEdges: port in STP blocking state produces no edges.
func TestBuildEdgesStpBlockingFiltered(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 3, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{3: 5}
	ifNames := map[int]string{5: "Ethernet0/3"}
	stpStates := map[int]int{3: 2} // blocking(2)

	edges := buildEdges("sw", entries, bridgePorts, ifNames, stpStates)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for blocking STP port, got %d", len(edges))
	}
}

// buildEdges: port absent from stpStates produces an edge (treat as forwarding).
func TestBuildEdgesStpAbsentAllowed(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 4, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{4: 6}
	ifNames := map[int]string{6: "Ethernet0/4"}
	stpStates := map[int]int{} // port 4 not present

	edges := buildEdges("sw", entries, bridgePorts, ifNames, stpStates)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge when port absent from STP table, got %d", len(edges))
	}
}

// buildEdges: port in STP learning state (not yet forwarding) is filtered.
func TestBuildEdgesStpLearningFiltered(t *testing.T) {
	entries := map[string]*fdbEntry{
		"0.1.2.3.4.5": {mac: []byte{0, 1, 2, 3, 4, 5}, port: 5, status: fdbStatusLearned},
	}
	bridgePorts := map[int]int{5: 7}
	ifNames := map[int]string{7: "Ethernet0/5"}
	stpStates := map[int]int{5: 4} // learning(4)

	edges := buildEdges("sw", entries, bridgePorts, ifNames, stpStates)
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for learning STP port, got %d", len(edges))
	}
}

// Walk: end-to-end with a fake agent. One learned MAC on one port → one direct edge.
func TestWalkEndToEnd(t *testing.T) {
	pdus := buildFdbAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope entries: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	e := edges[0]
	if e.SrcDevice != "sw-01" {
		t.Errorf("SrcDevice = %q, want sw-01", e.SrcDevice)
	}
	if e.SrcPort != "GigabitEthernet0/1" {
		t.Errorf("SrcPort = %q, want GigabitEthernet0/1", e.SrcPort)
	}
	if e.DstDevice != "00:0a:bb:cc:dd:ee" {
		t.Errorf("DstDevice = %q, want 00:0a:bb:cc:dd:ee", e.DstDevice)
	}
	if e.Adjacency != discovery.AdjacencyDirect {
		t.Errorf("Adjacency = %q, want direct", e.Adjacency)
	}
}

func TestParseQBridgeIndex(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantMAC []byte
		wantOK  bool
	}{
		{
			name:    "valid fdbId=1",
			input:   "1.0.10.187.204.221.238",
			wantKey: "0.10.187.204.221.238",
			wantMAC: []byte{0, 10, 187, 204, 221, 238},
			wantOK:  true,
		},
		{
			name:    "valid fdbId=100",
			input:   "100.0.1.2.3.4.5",
			wantKey: "0.1.2.3.4.5",
			wantMAC: []byte{0, 1, 2, 3, 4, 5},
			wantOK:  true,
		},
		{
			name:   "too short — only 6 components",
			input:  "1.2.3.4.5.6",
			wantOK: false,
		},
		{
			name:   "non-numeric component",
			input:  "1.x.1.2.3.4.5",
			wantOK: false,
		},
		{
			name:   "out of range octet",
			input:  "1.0.1.2.3.4.999",
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, mac, ok := parseQBridgeIndex(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if len(mac) != len(tc.wantMAC) {
				t.Fatalf("mac len = %d, want %d", len(mac), len(tc.wantMAC))
			}
			for i := range tc.wantMAC {
				if mac[i] != tc.wantMAC[i] {
					t.Errorf("mac[%d] = %d, want %d", i, mac[i], tc.wantMAC[i])
				}
			}
		})
	}
}

// Walk: end-to-end with B-MIB and Q-BRIDGE PDUs. B-MIB contributes one MAC,
// Q-BRIDGE contributes a second MAC on the same port → two direct edges.
func TestWalkEndToEndQBridge(t *testing.T) {
	pdus := buildQBridgeAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope entries: %v", oos)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d: %v", len(edges), edges)
	}

	seen := make(map[string]bool)
	for _, e := range edges {
		seen[e.DstDevice] = true
	}
	if !seen["00:0a:bb:cc:dd:ee"] {
		t.Errorf("expected DstDevice 00:0a:bb:cc:dd:ee in edges")
	}
	if !seen["00:aa:bb:cc:dd:01"] {
		t.Errorf("expected DstDevice 00:aa:bb:cc:dd:01 in edges")
	}
}

// TestDiscoverVlanIDs verifies that discoverVlanIDs parses dot1qVlanCurrentTable
// OID suffixes and returns a sorted, deduplicated slice of valid VLAN IDs.
func TestDiscoverVlanIDs(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		// .1.3.6.1.2.1.17.7.1.4.2.3.0.1 (col=3, timeMark=0, vlanId=1)
		{Name: ".1.3.6.1.2.1.17.7.1.4.2.3.0.1", Type: gsnmp.Integer, Value: 1},
		// .1.3.6.1.2.1.17.7.1.4.2.3.0.10 (col=3, timeMark=0, vlanId=10)
		{Name: ".1.3.6.1.2.1.17.7.1.4.2.3.0.10", Type: gsnmp.Integer, Value: 10},
		// .1.3.6.1.2.1.17.7.1.4.2.3.0.100 (col=3, timeMark=0, vlanId=100)
		{Name: ".1.3.6.1.2.1.17.7.1.4.2.3.0.100", Type: gsnmp.Integer, Value: 100},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}
	client, err := snmputil.Open(p)
	if err != nil {
		t.Fatalf("snmputil.Open: %v", err)
	}
	defer client.Conn.Close()

	ids := discoverVlanIDs(context.Background(), client)
	want := []int{1, 10, 100}
	if len(ids) != len(want) {
		t.Fatalf("discoverVlanIDs returned %v, want %v", ids, want)
	}
	for i, v := range want {
		if ids[i] != v {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], v)
		}
	}
}

// TestWalkVlanCommunityFdbDiscovery verifies that walkVlanCommunityFdbs discovers
// VLANs via dot1qVlanCurrentTable and then walks dot1dTpFdbTable for each VLAN
// using community-string indexing (community@vlanId).
func TestWalkVlanCommunityFdbDiscovery(t *testing.T) {
	vlanTablePDUs := []gsnmp.SnmpPDU{
		// dot1qVlanCurrentTable: VLAN 10 active
		{Name: ".1.3.6.1.2.1.17.7.1.4.2.3.0.10", Type: gsnmp.Integer, Value: 10},
		// dot1dBasePortTable: bridge port 1 → ifIndex 2
		{Name: ".1.3.6.1.2.1.17.1.4.1.2.1", Type: gsnmp.Integer, Value: 2},
		// ifXTable.ifName: ifIndex 2 → "GigabitEthernet0/2"
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/2")},
	}

	// B-MIB FDB PDUs for MAC 00:aa:bb:cc:dd:ee on bridge port 1 (learned)
	vlan10PDUs := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.17.4.3.1.1.0.170.187.204.221.238", Type: gsnmp.OctetString, Value: []byte{0, 170, 187, 204, 221, 238}},
		{Name: ".1.3.6.1.2.1.17.4.3.1.2.0.170.187.204.221.238", Type: gsnmp.Integer, Value: 1},
		{Name: ".1.3.6.1.2.1.17.4.3.1.3.0.170.187.204.221.238", Type: gsnmp.Integer, Value: fdbStatusLearned},
	}

	communities := map[string][]gsnmp.SnmpPDU{
		"public":    vlanTablePDUs,
		"public@10": vlan10PDUs,
	}
	addr := snmptest.StartMultiCommunity(t, communities)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	found := false
	for _, e := range edges {
		if e.DstDevice == "00:aa:bb:cc:dd:ee" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected edge with DstDevice 00:aa:bb:cc:dd:ee, got %v", edges)
	}
}

// buildQBridgeAgentPDUs builds PDUs with one B-MIB entry and one Q-BRIDGE entry.
//
// dot1dTpFdbTable: MAC 00:0a:bb:cc:dd:ee on bridge port 1 (learned)
// dot1qTpFdbTable: MAC 00:aa:bb:cc:dd:01 on bridge port 1, VLAN FDB ID 10 (learned)
// dot1dBasePortTable: bridge port 1 → ifIndex 2
// dot1dStpPortTable: bridge port 1 → forwarding(5)
// ifXTable.ifName: ifIndex 2 → "GigabitEthernet0/1"
func buildQBridgeAgentPDUs() []gsnmp.SnmpPDU {
	fdbBase      := ".1.3.6.1.2.1.17.4.3.1."
	qBridgeBase  := ".1.3.6.1.2.1.17.7.1.2.2."
	basePortBase := ".1.3.6.1.2.1.17.1.4.1."
	stpPortBase  := ".1.3.6.1.2.1.17.2.15.1."
	ifNameBase   := ".1.3.6.1.2.1.31.1.1.1.1."

	bmibSuffix   := "0.10.187.204.221.238"
	// Q-BRIDGE index: fdbId=10, MAC=00:aa:bb:cc:dd:01
	qSuffix := "10.0.170.187.204.221.1"

	return []gsnmp.SnmpPDU{
		// dot1dTpFdbTable: MAC 00:0a:bb:cc:dd:ee
		{Name: fdbBase + "1." + bmibSuffix, Type: gsnmp.OctetString, Value: []byte{0, 10, 187, 204, 221, 238}},
		{Name: fdbBase + "2." + bmibSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: fdbBase + "3." + bmibSuffix, Type: gsnmp.Integer, Value: fdbStatusLearned},

		// dot1qTpFdbTable: MAC 00:aa:bb:cc:dd:01, VLAN FDB ID 10
		{Name: qBridgeBase + "2." + qSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: qBridgeBase + "3." + qSuffix, Type: gsnmp.Integer, Value: fdbStatusLearned},

		// dot1dBasePortTable: bridge port 1 → ifIndex 2
		{Name: basePortBase + "2.1", Type: gsnmp.Integer, Value: 2},

		// dot1dStpPortTable: bridge port 1 → forwarding(5)
		{Name: stpPortBase + "3.1", Type: gsnmp.Integer, Value: stpStateForwarding},

		// ifXTable.ifName: ifIndex 2 → "GigabitEthernet0/1"
		{Name: ifNameBase + "2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/1")},
	}
}

// buildFdbAgentPDUs builds the minimal PDU set for one learned FDB entry.
//
// dot1dTpFdbTable (1.3.6.1.2.1.17.4.3.1.{col}.{mac_octets}):
//
//	col 1 (address) = 00:0a:bb:cc:dd:ee  → OID suffix 0.10.187.204.221.238
//	col 2 (port)    = 1 (bridge port 1)
//	col 3 (status)  = 3 (learned)
//
// dot1dBasePortTable (1.3.6.1.2.1.17.1.4.1.2.{portNum}):
//
//	port 1 → ifIndex 2
//
// dot1dStpPortTable (1.3.6.1.2.1.17.2.15.1.3.{portNum}):
//
//	port 1 → forwarding(5)
//
// ifXTable.ifName (1.3.6.1.2.1.31.1.1.1.1.{ifIndex}):
//
//	ifIndex 2 → "GigabitEthernet0/1"
func buildFdbAgentPDUs() []gsnmp.SnmpPDU {
	macSuffix := "0.10.187.204.221.238"
	fdbBase := ".1.3.6.1.2.1.17.4.3.1."
	basePortBase := ".1.3.6.1.2.1.17.1.4.1."
	stpPortBase := ".1.3.6.1.2.1.17.2.15.1."
	ifNameBase := ".1.3.6.1.2.1.31.1.1.1.1."

	return []gsnmp.SnmpPDU{
		// dot1dTpFdbTable
		{Name: fdbBase + "1." + macSuffix, Type: gsnmp.OctetString, Value: []byte{0, 10, 187, 204, 221, 238}},
		{Name: fdbBase + "2." + macSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: fdbBase + "3." + macSuffix, Type: gsnmp.Integer, Value: fdbStatusLearned},

		// dot1dBasePortTable: bridge port 1 → ifIndex 2
		{Name: basePortBase + "2.1", Type: gsnmp.Integer, Value: 2},

		// dot1dStpPortTable: bridge port 1 → forwarding(5)
		{Name: stpPortBase + "3.1", Type: gsnmp.Integer, Value: stpStateForwarding},

		// ifXTable.ifName: ifIndex 2 → "GigabitEthernet0/1"
		{Name: ifNameBase + "2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/1")},
	}
}
