package isis

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

const (
	adjStateBase = ".1.3.6.1.2.1.138.1.6.1.1.2."
	adjIPBase    = ".1.3.6.1.2.1.138.1.6.2.1.2."
	adjKey       = "0.1.1"
	neighborIP   = "192.0.2.1"
)

// Walk: up adjacency (state=3) with in-scope neighbour → one edge.
func TestWalkUpAdjacencyInScope(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	e := edges[0]
	if e.SrcDevice != "router-a" {
		t.Errorf("SrcDevice = %q, want router-a", e.SrcDevice)
	}
	if e.DstDevice != neighborIP {
		t.Errorf("DstDevice = %q, want %s", e.DstDevice, neighborIP)
	}
	if e.DiscoveryProto != "isis" {
		t.Errorf("DiscoveryProto = %q, want isis", e.DiscoveryProto)
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

// Walk: down adjacency (state=1) → no edge, no OOS.
func TestWalkDownAdjacency(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: 1},
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for down adjacency, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// Walk: initializing adjacency (state=2) → no edge, no OOS.
func TestWalkInitializingAdjacency(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: 2},
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for initializing adjacency, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// Walk: up adjacency with out-of-scope neighbour → OutOfScopeNeighbour, no edge.
func TestWalkUpAdjacencyOutOfScope(t *testing.T) {
	_, allow, _ := net.ParseCIDR("172.16.0.0/12")
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", []*net.IPNet{allow})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 in-scope edges, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope entry, got %d", len(oos))
	}
	if oos[0].NeighbourHint != neighborIP {
		t.Errorf("NeighbourHint = %q, want %s", oos[0].NeighbourHint, neighborIP)
	}
	if oos[0].ReportingDevice != "router-a" {
		t.Errorf("ReportingDevice = %q, want router-a", oos[0].ReportingDevice)
	}
}

// Walk: IP entry exists with no matching state entry → no edge (treat as not-up).
func TestWalkMissingStateEntry(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		// No state entry for adjKey; only an IP address entry.
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for missing state, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// Walk: empty walk (no PDUs) → empty results, no error.
func TestWalkEmpty(t *testing.T) {
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for empty walk, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope for empty walk, got %d", len(oos))
	}
}

// TestWalkAdjIPAddrsAdjKeyExtraction verifies the tail-count adjKey extraction
// for two cases:
//
//  1. Normal: adjIdx=1, IP=10.0.0.1 → adjKey "0.1.1", edge produced.
//  2. Ambiguous: adjIdx=4, IP=1.4.5.6 → the old LastIndex approach would
//     match the wrong ".1.4." and produce adjKey "0.1.4.1" (missing in the
//     state map → edge dropped). The new tail-count approach correctly
//     produces adjKey "0.1.4" and emits the edge.
func TestWalkAdjIPAddrsAdjKeyExtraction(t *testing.T) {
	cases := []struct {
		name      string
		stateKey  string // key stored in the states map (state=up)
		ipSuffix  string // OID suffix appended to adjIPBase
		ipBytes   []byte // raw IPv4 bytes for the PDU value
		wantEdge  bool
		wantDstIP string
	}{
		{
			name:      "normal adjIdx=1 IP=10.0.0.1",
			stateKey:  "0.1.1",
			ipSuffix:  "0.1.1.1.4.10.0.0.1",
			ipBytes:   []byte{10, 0, 0, 1},
			wantEdge:  true,
			wantDstIP: "10.0.0.1",
		},
		{
			name:      "ambiguous adjIdx=4 IP=1.4.5.6",
			stateKey:  "0.1.4",
			ipSuffix:  "0.1.4.1.4.1.4.5.6",
			ipBytes:   []byte{1, 4, 5, 6},
			wantEdge:  true,
			wantDstIP: "1.4.5.6",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pdus := []gsnmp.SnmpPDU{
				{Name: adjStateBase + tc.stateKey, Type: gsnmp.Integer, Value: isisAdjStateUp},
				{Name: adjIPBase + tc.ipSuffix, Type: gsnmp.OctetString, Value: tc.ipBytes},
			}
			addr := snmptest.Start(t, "public", pdus)
			ip, port := snmptest.ParseAddr(addr)

			p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
			edges, _, err := Walk(context.Background(), p, "router-a", nil)
			if err != nil {
				t.Fatalf("Walk: %v", err)
			}
			if tc.wantEdge {
				if len(edges) != 1 {
					t.Fatalf("expected 1 edge, got %d", len(edges))
				}
				if edges[0].DstDevice != tc.wantDstIP {
					t.Errorf("DstDevice = %q, want %s", edges[0].DstDevice, tc.wantDstIP)
				}
			} else if len(edges) != 0 {
				t.Errorf("expected 0 edges, got %d", len(edges))
			}
		})
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
	_, _, err := Walk(context.Background(), p, "router-a", nil)
	if err == nil {
		t.Fatal("expected error when Open fails, got nil")
	}
}

// Walk: cancelled context → error returned from walkAdjStates.
func TestWalkCancelledContext(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: isisAdjStateUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := Walk(ctx, p, "router-a", nil)
	if err == nil {
		t.Fatal("expected error when context is cancelled, got nil")
	}
}
