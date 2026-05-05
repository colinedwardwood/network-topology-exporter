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

	edges := buildEdges("sw-01", entries, bridgePorts, ifNames)
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

	edges := buildEdges("sw-01", entries, bridgePorts, ifNames)
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

	edges := buildEdges("sw", entries, bridgePorts, ifNames)
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

	edges := buildEdges("sw", entries, bridgePorts, ifNames)
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

	edges := buildEdges("sw", entries, bridgePorts, ifNames)
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

	edges := buildEdges("sw", entries, bridgePorts, ifNames)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].SrcPort != "7" {
		t.Errorf("SrcPort = %q, want \"7\" (fallback to ifIndex)", edges[0].SrcPort)
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
// ifXTable.ifName (1.3.6.1.2.1.31.1.1.1.1.{ifIndex}):
//
//	ifIndex 2 → "GigabitEthernet0/1"
func buildFdbAgentPDUs() []gsnmp.SnmpPDU {
	macSuffix := "0.10.187.204.221.238"
	fdbBase := ".1.3.6.1.2.1.17.4.3.1."
	basePortBase := ".1.3.6.1.2.1.17.1.4.1."
	ifNameBase := ".1.3.6.1.2.1.31.1.1.1.1."

	return []gsnmp.SnmpPDU{
		// dot1dTpFdbTable
		{Name: fdbBase + "1." + macSuffix, Type: gsnmp.OctetString, Value: []byte{0, 10, 187, 204, 221, 238}},
		{Name: fdbBase + "2." + macSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: fdbBase + "3." + macSuffix, Type: gsnmp.Integer, Value: fdbStatusLearned},

		// dot1dBasePortTable: bridge port 1 → ifIndex 2
		{Name: basePortBase + "2.1", Type: gsnmp.Integer, Value: 2},

		// ifXTable.ifName: ifIndex 2 → "GigabitEthernet0/1"
		{Name: ifNameBase + "2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/1")},
	}
}
