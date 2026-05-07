package mpls

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

// tunnelOID builds an mplsTunnelOperStatus OID for the given parameters.
// ingressIP and egressIP are dotted-decimal IPv4 strings.
func tunnelOID(tunnelIdx, tunnelInst, ingressIP, egressIP string) string {
	const base = ".1.3.6.1.2.1.10.166.3.2.2.1.17."
	return base + tunnelIdx + "." + tunnelInst + "." + dotToOID(ingressIP) + "." + dotToOID(egressIP)
}

func dotToOID(ip string) string {
	// "192.0.2.1" → "192.0.2.1" (already dotted decimal; suitable as OID suffix)
	return ip
}

// Walk: up tunnel (operStatus=1) with in-scope egress → one edge.
func TestWalkUpTunnelInScope(t *testing.T) {
	oid := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
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
	if e.SrcPort != "te-tunnel1" {
		t.Errorf("SrcPort = %q, want te-tunnel1", e.SrcPort)
	}
	if e.DstDevice != "192.0.2.1" {
		t.Errorf("DstDevice = %q, want 192.0.2.1", e.DstDevice)
	}
	if e.DstPort != "" {
		t.Errorf("DstPort = %q, want empty", e.DstPort)
	}
	if e.DiscoveryProto != "mpls_te" {
		t.Errorf("DiscoveryProto = %q, want mpls_te", e.DiscoveryProto)
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
	if e.LinkKind != "mpls-te" {
		t.Errorf("LinkKind = %q, want mpls-te", e.LinkKind)
	}
	if e.ObservedAt.IsZero() {
		t.Error("ObservedAt is zero")
	}
}

// Walk: down tunnel (operStatus=2) → no edge.
func TestWalkDownTunnel(t *testing.T) {
	oid := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.Integer, Value: 2},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for down tunnel, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// Walk: malformed OID (wrong number of components) → skip, no error.
func TestWalkMalformedOID(t *testing.T) {
	// Only 8 components after the prefix (missing 2 egress octets).
	const malformed = ".1.3.6.1.2.1.10.166.3.2.2.1.17.1.1.10.0.0.1.192.0"
	pdus := []gsnmp.SnmpPDU{
		{Name: malformed, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for malformed OID, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// Walk: up tunnel with out-of-scope egress → OutOfScopeNeighbour, no edge.
func TestWalkUpTunnelOutOfScope(t *testing.T) {
	_, allow, _ := net.ParseCIDR("172.16.0.0/12")
	oid := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
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
	if oos[0].NeighbourHint != "192.0.2.1" {
		t.Errorf("NeighbourHint = %q, want 192.0.2.1", oos[0].NeighbourHint)
	}
	if oos[0].ReportingDevice != "router-a" {
		t.Errorf("ReportingDevice = %q, want router-a", oos[0].ReportingDevice)
	}
}

// Walk: multiple tunnels to same egress with different indices → multiple edges.
func TestWalkMultipleTunnelsSameEgress(t *testing.T) {
	oid1 := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	oid2 := tunnelOID("2", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid1, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: oid2, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, oos, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d: %v", len(edges), edges)
	}
	ports := make(map[string]bool)
	for _, e := range edges {
		if e.DstDevice != "192.0.2.1" {
			t.Errorf("DstDevice = %q, want 192.0.2.1", e.DstDevice)
		}
		ports[e.SrcPort] = true
	}
	if !ports["te-tunnel1"] {
		t.Error("missing edge with SrcPort te-tunnel1")
	}
	if !ports["te-tunnel2"] {
		t.Error("missing edge with SrcPort te-tunnel2")
	}
}

// Walk: empty walk → empty results, no error.
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
