package bgp

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

func TestBuildEdgesEstablishedPeer(t *testing.T) {
	peers := map[string]*bgpPeer{
		"10.0.0.1": {state: bgpStateEstablished, remoteIP: net.ParseIP("10.0.0.1").To4(), remoteAS: 65001},
	}

	edges, oos := buildEdges("rtr-01", peers, nil)
	if len(oos) != 0 {
		t.Fatalf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.SrcDevice != "rtr-01" {
		t.Errorf("SrcDevice = %q, want rtr-01", e.SrcDevice)
	}
	if e.DstDevice != "10.0.0.1" {
		t.Errorf("DstDevice = %q, want 10.0.0.1", e.DstDevice)
	}
	if e.SrcPort != "" {
		t.Errorf("SrcPort = %q, want empty", e.SrcPort)
	}
	if e.DiscoveryProto != "bgp" {
		t.Errorf("DiscoveryProto = %q, want bgp", e.DiscoveryProto)
	}
	if e.Direction != discovery.DirectionUnidirectional {
		t.Errorf("Direction = %q, want unidirectional", e.Direction)
	}
	if e.Confidence != discovery.ConfidenceLow {
		t.Errorf("Confidence = %q, want low", e.Confidence)
	}
	if e.Adjacency != discovery.AdjacencyUnknown {
		t.Errorf("Adjacency = %q, want unknown", e.Adjacency)
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

// buildEdges: peers in non-established states are filtered out.
func TestBuildEdgesFiltersNonEstablished(t *testing.T) {
	peers := map[string]*bgpPeer{
		"10.0.0.1": {state: 3, remoteIP: net.ParseIP("10.0.0.1").To4(), remoteAS: 65001}, // active
		"10.0.0.2": {state: 1, remoteIP: net.ParseIP("10.0.0.2").To4(), remoteAS: 65002}, // idle
		"10.0.0.3": {state: bgpStateEstablished, remoteIP: net.ParseIP("10.0.0.3").To4(), remoteAS: 65003},
	}

	edges, oos := buildEdges("rtr-01", peers, nil)
	if len(oos) != 0 {
		t.Fatalf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (only established peer), got %d", len(edges))
	}
	if edges[0].DstDevice != "10.0.0.3" {
		t.Errorf("DstDevice = %q, want 10.0.0.3", edges[0].DstDevice)
	}
}

// buildEdges: two established peers → two edges.
func TestBuildEdgesMultiplePeers(t *testing.T) {
	peers := map[string]*bgpPeer{
		"192.168.1.1": {state: bgpStateEstablished, remoteIP: net.ParseIP("192.168.1.1").To4(), remoteAS: 65010},
		"192.168.1.2": {state: bgpStateEstablished, remoteIP: net.ParseIP("192.168.1.2").To4(), remoteAS: 65011},
	}

	edges, oos := buildEdges("rtr-02", peers, nil)
	if len(oos) != 0 {
		t.Fatalf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	seen := make(map[string]bool)
	for _, e := range edges {
		seen[e.DstDevice] = true
		if e.SrcDevice != "rtr-02" {
			t.Errorf("SrcDevice = %q, want rtr-02", e.SrcDevice)
		}
	}
	if !seen["192.168.1.1"] || !seen["192.168.1.2"] {
		t.Errorf("missing expected peers in edges: %v", edges)
	}
}

// buildEdges: peer outside allowedNets goes to OutOfScopeNeighbour.
func TestBuildEdgesOutOfScope(t *testing.T) {
	_, allowNet, _ := net.ParseCIDR("10.0.0.0/24")
	peers := map[string]*bgpPeer{
		"10.0.0.1":   {state: bgpStateEstablished, remoteIP: net.ParseIP("10.0.0.1").To4(), remoteAS: 65001},
		"172.16.0.1": {state: bgpStateEstablished, remoteIP: net.ParseIP("172.16.0.1").To4(), remoteAS: 65002},
	}

	edges, oos := buildEdges("rtr-01", peers, []*net.IPNet{allowNet})
	if len(edges) != 1 {
		t.Fatalf("expected 1 in-scope edge, got %d", len(edges))
	}
	if edges[0].DstDevice != "10.0.0.1" {
		t.Errorf("DstDevice = %q, want 10.0.0.1", edges[0].DstDevice)
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope entry, got %d", len(oos))
	}
	if oos[0].NeighbourHint != "172.16.0.1" {
		t.Errorf("NeighbourHint = %q, want 172.16.0.1", oos[0].NeighbourHint)
	}
}

// Walk: end-to-end with a fake agent. One established peer → one edge.
func TestWalkEndToEnd(t *testing.T) {
	pdus := buildBgpAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "rtr-01", nil)
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
	if e.SrcDevice != "rtr-01" {
		t.Errorf("SrcDevice = %q, want rtr-01", e.SrcDevice)
	}
	if e.DstDevice != "10.0.0.1" {
		t.Errorf("DstDevice = %q, want 10.0.0.1", e.DstDevice)
	}
	if e.DiscoveryProto != "bgp" {
		t.Errorf("DiscoveryProto = %q, want bgp", e.DiscoveryProto)
	}
	if e.Adjacency != discovery.AdjacencyUnknown {
		t.Errorf("Adjacency = %q, want unknown", e.Adjacency)
	}
}

// buildBgpAgentPDUs builds the minimal PDU set for one established BGP peer.
//
// bgpPeerTable (1.3.6.1.2.1.15.3.1.{col}.{ip1}.{ip2}.{ip3}.{ip4}):
//
//	col 2 (state)      = 6 (established)
//	col 7 (remoteAddr) = 10.0.0.1
//	col 9 (remoteAs)   = 65001
func buildBgpAgentPDUs() []gsnmp.SnmpPDU {
	const base = ".1.3.6.1.2.1.15.3.1."
	const peer = "10.0.0.1"

	return []gsnmp.SnmpPDU{
		{Name: base + "2." + peer, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "7." + peer, Type: gsnmp.IPAddress, Value: []byte{10, 0, 0, 1}},
		{Name: base + "9." + peer, Type: gsnmp.Integer, Value: 65001},
	}
}

// Walk: Open fails when the connection cannot be established.
func TestWalkOpenFails(t *testing.T) {
	p := snmputil.Params{
		IP:        net.ParseIP("127.0.0.1"),
		Port:      161,
		Community: "public",
		Timeout:   time.Nanosecond,
	}
	_, _, err := Walk(context.Background(), p, "rtr-01", nil)
	if err == nil {
		t.Fatal("expected error when Open fails, got nil")
	}
}

// Walk: walkBgpPeerTable fails when the context is already cancelled. This also
// covers walkBgpPeerTable's internal BulkWalk-error return.
func TestWalkBgpPeerTableFails(t *testing.T) {
	pdus := buildBgpAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := Walk(ctx, p, "rtr-01", nil)
	if err == nil {
		t.Fatal("expected error when walkBgpPeerTable fails, got nil")
	}
}

// walkBgpPeerTable: PDUs with too-short OID suffixes are silently skipped.
// Covers the two continue paths (TrimOIDPrefix failure and empty ipKey).
func TestWalkBgpPeerTableSkipsShortOIDs(t *testing.T) {
	const base = ".1.3.6.1.2.1.15.3.1."
	const peer = "10.0.0.1"

	pdus := []gsnmp.SnmpPDU{
		// Continue A: within BulkWalk root but wrong prefix — TrimOIDPrefix fails.
		// The root is 1.3.6.1.2.1.15.3; the prefix trimmed is .1.3.6.1.2.1.15.3.1.
		// A PDU at .1.3.6.1.2.1.15.3.2.2.10.0.0.1 is in the root subtree but does
		// not start with .1.3.6.1.2.1.15.3.1.
		{Name: ".1.3.6.1.2.1.15.3.2.2.10.0.0.1", Type: gsnmp.Integer, Value: bgpStateEstablished},
		// Continue B (ipKey == ""): suffix = "2" (col only, no IP key).
		// OID = prefix+"2" → suffix="2", SplitOIDComponent("2") → ok=true, ipKey="".
		{Name: base + "2", Type: gsnmp.Integer, Value: bgpStateEstablished},
		// Valid PDU for the established peer.
		{Name: base + "2." + peer, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "7." + peer, Type: gsnmp.IPAddress, Value: []byte{10, 0, 0, 1}},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := snmputil.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	peers, err := walkBgpPeerTable(context.Background(), client)
	if err != nil {
		t.Fatalf("walkBgpPeerTable: %v", err)
	}
	if len(peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(peers))
	}
}

// pduIP: []byte branch with a 4-byte value returns the IPv4 address.
// pduIP: []byte branch with a non-4-byte value returns nil.
// pduIP: return nil is reached when the byte slice length is not 4.
func TestPduIPBytesBranch(t *testing.T) {
	// 4-byte []byte → valid IPv4.
	pdu4 := gsnmp.SnmpPDU{Value: []byte{192, 168, 1, 1}}
	ip := pduIP(pdu4)
	if ip == nil || ip.String() != "192.168.1.1" {
		t.Errorf("4-byte []byte: got %v, want 192.168.1.1", ip)
	}

	// 2-byte []byte → nil (len != 4 → falls to return nil).
	pdu2 := gsnmp.SnmpPDU{Value: []byte{10, 0}}
	if ip := pduIP(pdu2); ip != nil {
		t.Errorf("2-byte []byte: got %v, want nil", ip)
	}
}

// buildEdges: established peer with nil remoteIP is skipped silently.
func TestBuildEdgesNilRemoteIP(t *testing.T) {
	peers := map[string]*bgpPeer{
		"10.0.0.1": {state: bgpStateEstablished, remoteIP: nil},
	}
	edges, oos := buildEdges("rtr-01", peers, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for nil remoteIP, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope for nil remoteIP, got %d", len(oos))
	}
}
