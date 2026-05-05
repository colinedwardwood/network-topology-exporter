package ospf

import (
	"context"
	"net"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// buildEdges: full(8) neighbour → one edge with all fields validated.
func TestBuildEdgesEstablishedNeighbour(t *testing.T) {
	rows := map[string]*nbrRow{
		"192.0.2.1.0": {nbrIP: net.ParseIP("192.0.2.1").To4(), state: stateFull},
	}

	edges, oos := buildEdges("router-a", rows, nil)
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.SrcDevice != "router-a" {
		t.Errorf("SrcDevice = %q, want router-a", e.SrcDevice)
	}
	if e.DstDevice != "192.0.2.1" {
		t.Errorf("DstDevice = %q, want 192.0.2.1", e.DstDevice)
	}
	if e.SrcPort != "" {
		t.Errorf("SrcPort = %q, want empty", e.SrcPort)
	}
	if e.DiscoveryProto != "ospf" {
		t.Errorf("DiscoveryProto = %q, want ospf", e.DiscoveryProto)
	}
	if e.Direction != discovery.DirectionUnidirectional {
		t.Errorf("Direction = %q, want unidirectional", e.Direction)
	}
	if e.Confidence != discovery.ConfidenceMedium {
		t.Errorf("Confidence = %q, want medium", e.Confidence)
	}
	if e.Adjacency != discovery.AdjacencyDirect {
		t.Errorf("Adjacency = %q, want direct", e.Adjacency)
	}
	if e.PrecedenceRank != precedenceRank {
		t.Errorf("PrecedenceRank = %d, want %d", e.PrecedenceRank, precedenceRank)
	}
	if e.LinkKind != "ip" {
		t.Errorf("LinkKind = %q, want ip", e.LinkKind)
	}
	if e.ObservedAt.IsZero() {
		t.Error("ObservedAt is zero")
	}
}

// buildEdges: twoWay(5) neighbour also produces an edge.
func TestBuildEdgesTwoWayNeighbour(t *testing.T) {
	rows := map[string]*nbrRow{
		"10.0.0.2.0": {nbrIP: net.ParseIP("10.0.0.2").To4(), state: stateTwoWay},
	}
	edges, oos := buildEdges("router-b", rows, nil)
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].DstDevice != "10.0.0.2" {
		t.Errorf("DstDevice = %q, want 10.0.0.2", edges[0].DstDevice)
	}
}

// buildEdges: down(1) state is not an active adjacency and produces no edge.
func TestBuildEdgesFiltersDownNeighbour(t *testing.T) {
	rows := map[string]*nbrRow{
		"192.0.2.1.0": {nbrIP: net.ParseIP("192.0.2.1").To4(), state: 1},
	}
	edges, oos := buildEdges("router-a", rows, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for down neighbour, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// buildEdges: two full neighbours → two edges.
func TestBuildEdgesMultipleNeighbours(t *testing.T) {
	rows := map[string]*nbrRow{
		"192.0.2.1.0": {nbrIP: net.ParseIP("192.0.2.1").To4(), state: stateFull},
		"10.0.0.2.0":  {nbrIP: net.ParseIP("10.0.0.2").To4(), state: stateFull},
	}
	edges, oos := buildEdges("router-a", rows, nil)
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	seen := make(map[string]bool)
	for _, e := range edges {
		seen[e.DstDevice] = true
	}
	if !seen["192.0.2.1"] {
		t.Error("missing edge to 192.0.2.1")
	}
	if !seen["10.0.0.2"] {
		t.Error("missing edge to 10.0.0.2")
	}
}

// buildEdges: neighbour outside allowedNets → OutOfScopeNeighbour, not Edge.
func TestBuildEdgesOutOfScope(t *testing.T) {
	_, allow, _ := net.ParseCIDR("172.16.0.0/12")
	rows := map[string]*nbrRow{
		"192.0.2.1.0": {nbrIP: net.ParseIP("192.0.2.1").To4(), state: stateFull},
	}
	edges, oos := buildEdges("router-a", rows, []*net.IPNet{allow})
	if len(edges) != 0 {
		t.Errorf("expected 0 in-scope edges, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope entry, got %d", len(oos))
	}
	if oos[0].NeighbourHint != "192.0.2.1" {
		t.Errorf("NeighbourHint = %q, want 192.0.2.1", oos[0].NeighbourHint)
	}
	if oos[0].ReportingDevice != "router-a" {
		t.Errorf("ReportingDevice = %q, want router-a", oos[0].ReportingDevice)
	}
}

// Walk: end-to-end with a fake agent. One full neighbour → one direct edge.
func TestWalkEndToEnd(t *testing.T) {
	pdus := buildOspfAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "router-x", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	if edges[0].DstDevice != "192.0.2.1" {
		t.Errorf("DstDevice = %q, want 192.0.2.1", edges[0].DstDevice)
	}
	if edges[0].SrcDevice != "router-x" {
		t.Errorf("SrcDevice = %q, want router-x", edges[0].SrcDevice)
	}
	if edges[0].Adjacency != discovery.AdjacencyDirect {
		t.Errorf("Adjacency = %q, want direct", edges[0].Adjacency)
	}
}

// buildOspfAgentPDUs builds the minimal PDU set for one full OSPF adjacency.
//
// ospfNbrTable (1.3.6.1.2.1.14.10.1.{col}.{ip0}.{ip1}.{ip2}.{ip3}.{addrLessIdx}):
//
//	col 1 (ospfNbrIpAddr) = 192.0.2.1  → OID suffix 192.0.2.1.0
//	col 6 (ospfNbrState)  = 8 (full)
func buildOspfAgentPDUs() []gsnmp.SnmpPDU {
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"

	return []gsnmp.SnmpPDU{
		{Name: base + "1." + idx, Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
		{Name: base + "6." + idx, Type: gsnmp.Integer, Value: stateFull},
	}
}
