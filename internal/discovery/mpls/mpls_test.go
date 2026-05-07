package mpls

import (
	"context"
	"fmt"
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

// Walk: tunnelIdx=42 → SrcPort formatted as "te-tunnel42".
func TestWalkTunnelIdxFormatting(t *testing.T) {
	oid := tunnelOID("42", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].SrcPort != "te-tunnel42" {
		t.Errorf("SrcPort = %q, want te-tunnel42", edges[0].SrcPort)
	}
}

// parseTunnelSuffix: non-integer tunnel index → ok=false, entry skipped.
// SNMP OIDs can only carry integers so this path is exercised via the helper
// directly rather than through the SNMP test server.
func TestParseTunnelSuffixNonIntegerIdx(t *testing.T) {
	// "abc" as the tunnel index; remaining components are otherwise valid.
	suffix := "abc.1.10.0.0.1.192.0.2.1"
	_, _, ok := parseTunnelSuffix(suffix)
	if ok {
		t.Error("expected ok=false for non-integer tunnel index, got true")
	}
}

// parseTunnelSuffix: valid suffix with tunnelIdx=42 → correct index and egress IP.
func TestParseTunnelSuffixValid(t *testing.T) {
	suffix := "42.1.10.0.0.1.192.0.2.1"
	idx, egressIP, ok := parseTunnelSuffix(suffix)
	if !ok {
		t.Fatal("expected ok=true for valid suffix, got false")
	}
	if idx != 42 {
		t.Errorf("tunnelIdx = %d, want 42", idx)
	}
	if egressIP.String() != "192.0.2.1" {
		t.Errorf("egressIP = %q, want 192.0.2.1", egressIP.String())
	}
}

// adminStatusPDUs returns PDUs for mplsTunnelAdminStatus for the given
// tunnelIdx and adminStatus value. The tunnel instance, ingress, and egress
// addresses are fixed: instance=1, ingress=10.0.0.1, egress=192.0.2.1 —
// matching the oper-status OIDs used in the existing tests.
func adminStatusPDUs(tunnelIdx, adminStatus int) []gsnmp.SnmpPDU {
	oid := fmt.Sprintf(".1.3.6.1.2.1.10.166.3.2.2.1.13.%d.1.10.0.0.1.192.0.2.1", tunnelIdx)
	return []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.Integer, Value: adminStatus},
	}
}

// Walk: admin status PDU present → Metadata["mpls_te.admin_status"] is set correctly.
func TestWalkPopulatesAdminStatus(t *testing.T) {
	operOID := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := append(
		[]gsnmp.SnmpPDU{{Name: operOID, Type: gsnmp.Integer, Value: mplsTunnelOperUp}},
		adminStatusPDUs(1, 1)..., // adminStatus up(1)
	)
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Metadata == nil {
		t.Fatal("Metadata is nil, want non-nil")
	}
	got := e.Metadata["mpls_te.admin_status"]
	if got != "up" {
		t.Errorf("Metadata[mpls_te.admin_status] = %q, want %q", got, "up")
	}
}

// Walk: no admin status PDUs present → Metadata["mpls_te.admin_status"] == "unknown".
func TestWalkAdminStatusMissingIsUnknown(t *testing.T) {
	operOID := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: operOID, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Metadata == nil {
		t.Fatal("Metadata is nil, want non-nil")
	}
	got := e.Metadata["mpls_te.admin_status"]
	if got != "unknown" {
		t.Errorf("Metadata[mpls_te.admin_status] = %q, want %q", got, "unknown")
	}
}

// Walk: invalid admin status PDU type degrades metadata but keeps edge.
func TestWalkAdminStatusDecodeFailureIsDegraded(t *testing.T) {
	operOID := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	adminOID := ".1.3.6.1.2.1.10.166.3.2.2.1.13.1.1.10.0.0.1.192.0.2.1"
	pdus := []gsnmp.SnmpPDU{
		{Name: operOID, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: adminOID, Type: gsnmp.OctetString, Value: []byte("bad")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Metadata["mpls_te.admin_status"] != "unknown" {
		t.Errorf("Metadata[mpls_te.admin_status] = %q, want unknown", e.Metadata["mpls_te.admin_status"])
	}
	if e.Metadata["network.topology.degraded"] != "true" {
		t.Errorf("Metadata[network.topology.degraded] = %q, want true", e.Metadata["network.topology.degraded"])
	}
	if e.Metadata["network.topology.degraded_reason"] != "invalid_admin_status_decode" {
		t.Errorf("Metadata[network.topology.degraded_reason] = %q, want invalid_admin_status_decode", e.Metadata["network.topology.degraded_reason"])
	}
}

// Walk: invalid oper status PDU type is a hard failure.
func TestWalkOperStatusDecodeFailureIsHardFail(t *testing.T) {
	oid := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	pdus := []gsnmp.SnmpPDU{
		{Name: oid, Type: gsnmp.OctetString, Value: []byte("bad")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	_, _, err := Walk(context.Background(), p, "router-a", nil)
	if err == nil {
		t.Fatal("expected hard-fail error for oper status decode failure, got nil")
	}
}

// Walk: mixed valid+invalid operStatus rows below threshold degrades but still emits valid edges.
func TestWalkOperStatusPartialDecodeDegraded(t *testing.T) {
	validOID := tunnelOID("1", "1", "10.0.0.1", "192.0.2.1")
	invalidOID := tunnelOID("2", "1", "10.0.0.1", "192.0.2.2")
	pdus := []gsnmp.SnmpPDU{
		{Name: validOID, Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: invalidOID, Type: gsnmp.OctetString, Value: []byte("bad")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}

	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].Metadata["network.topology.degraded"] != "true" {
		t.Fatalf("expected degraded metadata, got %+v", edges[0].Metadata)
	}
	if edges[0].Metadata["network.topology.degraded_reason"] != "required_table_partial_decode" {
		t.Errorf("degraded_reason = %q, want required_table_partial_decode", edges[0].Metadata["network.topology.degraded_reason"])
	}
}

// Walk: required-table invalid ratio above threshold hard-fails.
func TestWalkOperStatusInvalidRatioExceededHardFail(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: tunnelOID("1", "1", "10.0.0.1", "192.0.2.1"), Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: tunnelOID("2", "1", "10.0.0.1", "192.0.2.2"), Type: gsnmp.OctetString, Value: []byte("bad")},
		{Name: tunnelOID("3", "1", "10.0.0.1", "192.0.2.3"), Type: gsnmp.OctetString, Value: []byte("bad")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}

	_, _, err := Walk(context.Background(), p, "router-a", nil)
	if err == nil {
		t.Fatal("expected hard-fail error for invalid ratio exceeded, got nil")
	}
}

// Walk: deterministic behavior across noisy cycles (degraded -> hard-fail -> recover).
func TestWalkOperStatusNoisyCyclesDeterministic(t *testing.T) {
	run := func(pdus []gsnmp.SnmpPDU) error {
		addr := snmptest.Start(t, "public", pdus)
		ip, port := snmptest.ParseAddr(addr)
		p := snmputil.Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
		_, _, err := Walk(context.Background(), p, "router-a", nil)
		return err
	}

	// Cycle 1: partial decode anomaly below threshold => no hard-fail.
	err := run([]gsnmp.SnmpPDU{
		{Name: tunnelOID("1", "1", "10.0.0.1", "192.0.2.1"), Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: tunnelOID("2", "1", "10.0.0.1", "192.0.2.2"), Type: gsnmp.OctetString, Value: []byte("bad")},
	})
	if err != nil {
		t.Fatalf("cycle1 unexpected error: %v", err)
	}

	// Cycle 2: invalid ratio exceeds threshold => hard-fail.
	err = run([]gsnmp.SnmpPDU{
		{Name: tunnelOID("1", "1", "10.0.0.1", "192.0.2.1"), Type: gsnmp.Integer, Value: mplsTunnelOperUp},
		{Name: tunnelOID("2", "1", "10.0.0.1", "192.0.2.2"), Type: gsnmp.OctetString, Value: []byte("bad")},
		{Name: tunnelOID("3", "1", "10.0.0.1", "192.0.2.3"), Type: gsnmp.OctetString, Value: []byte("bad")},
	})
	if err == nil {
		t.Fatal("cycle2 expected hard-fail error, got nil")
	}

	// Cycle 3: clean data => no hard-fail.
	err = run([]gsnmp.SnmpPDU{
		{Name: tunnelOID("1", "1", "10.0.0.1", "192.0.2.1"), Type: gsnmp.Integer, Value: mplsTunnelOperUp},
	})
	if err != nil {
		t.Fatalf("cycle3 unexpected recovery error: %v", err)
	}
}
