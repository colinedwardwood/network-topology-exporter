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

	edges, oos, _ := buildEdges("router-a", rows, nil)
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

// buildEdges: twoWay(4) neighbour also produces an edge.
func TestBuildEdgesTwoWayNeighbour(t *testing.T) {
	rows := map[string]*nbrRow{
		"10.0.0.2.0": {nbrIP: net.ParseIP("10.0.0.2").To4(), state: stateTwoWay},
	}
	edges, oos, _ := buildEdges("router-b", rows, nil)
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
	edges, oos, _ := buildEdges("router-a", rows, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for down neighbour, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope, got %d", len(oos))
	}
}

// buildEdges: exchangeStart(5) is not an active adjacency and produces no edge.
// This is the value that was previously (incorrectly) used for stateTwoWay.
func TestBuildEdgesFiltersExchangeStartNeighbour(t *testing.T) {
	rows := map[string]*nbrRow{
		"192.0.2.1.0": {nbrIP: net.ParseIP("192.0.2.1").To4(), state: 5},
	}
	edges, oos, _ := buildEdges("router-a", rows, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for exchangeStart neighbour, got %d", len(edges))
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
	edges, oos, _ := buildEdges("router-a", rows, nil)
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
	edges, oos, _ := buildEdges("router-a", rows, []*net.IPNet{allow})
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
		Community: []byte("public"),
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
		{Name: base + "1." + idx, Type: gsnmp.IPAddress, Value: "192.0.2.1"},
		{Name: base + "6." + idx, Type: gsnmp.Integer, Value: stateFull},
	}
}

// Walk: Open fails when the connection cannot be established.
func TestWalkOpenFails(t *testing.T) {
	p := snmputil.Params{
		IP:        net.ParseIP("127.0.0.1"),
		Port:      161,
		Community: []byte("public"),
		Timeout:   time.Nanosecond,
	}
	_, _, err := Walk(context.Background(), p, "router-a", nil)
	if err == nil {
		t.Fatal("expected error when Open fails, got nil")
	}
}

// Walk: walkOspfNbrTable fails when the context is already cancelled. This also
// covers walkOspfNbrTable's internal BulkWalk-error return.
func TestWalkOspfNbrTableFails(t *testing.T) {
	pdus := buildOspfAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: []byte("public"),
		Timeout:   3 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := Walk(ctx, p, "router-a", nil)
	if err == nil {
		t.Fatal("expected error when walkOspfNbrTable fails, got nil")
	}
}

// walkOspfNbrTable: PDUs with OIDs that fail parseNbrOID are silently skipped.
// Also covers the truncated-bytes path for ospfNbrIpAddr PDUs (PDUIPv4 returns
// nil for []byte values that are not exactly 4 bytes).
func TestWalkOspfNbrTableSkipsOIDs(t *testing.T) {
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"

	pdus := []gsnmp.SnmpPDU{
		// Valid PDU (covers normal path).
		{Name: base + "6." + idx, Type: gsnmp.Integer, Value: stateFull},
		// PDU within BulkWalk root (.1.3.6.1.2.1.14.10) but wrong prefix for
		// parseNbrOID (.1.3.6.1.2.1.14.10.2. vs .1.3.6.1.2.1.14.10.1.) — triggers
		// the !ok continue in walkOspfNbrTable.
		{Name: ".1.3.6.1.2.1.14.10.2.1.192.0.2.1.0", Type: gsnmp.Integer, Value: int(8)},
		// ospfNbrIpAddr PDU with a 2-byte raw value — PDUIPv4 returns nil for
		// []byte values that are not exactly 4 bytes, so nbrIP stays nil.
		{Name: base + "1." + idx, Type: gsnmp.OctetString, Value: []byte{192, 0}},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: []byte("public"), Timeout: 3 * time.Second}
	client, err := snmputil.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	rows, _, err := walkOspfNbrTable(context.Background(), client)
	if err != nil {
		t.Fatalf("walkOspfNbrTable: %v", err)
	}
	// One row keyed by "192.0.2.1.0": state=full, nbrIP=nil (2-byte addr rejected).
	if len(rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(rows))
	}
	for _, row := range rows {
		if row.nbrIP != nil {
			t.Errorf("expected nil nbrIP for truncated address, got %v", row.nbrIP)
		}
	}
}

// buildEdges: rows with nil nbrIP are silently skipped.
func TestBuildEdgesNilNbrIP(t *testing.T) {
	rows := map[string]*nbrRow{
		"10.0.0.1.0": {nbrIP: nil, state: stateFull},
	}
	edges, oos, _ := buildEdges("router-a", rows, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for nil nbrIP, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope for nil nbrIP, got %d", len(oos))
	}
}

// buildEdges: full(8) neighbour with link-local nbrIP (169.254.x.x) → no edge, no OOS.
func TestBuildEdgesFiltersLinkLocalNbrIP(t *testing.T) {
	rows := map[string]*nbrRow{
		"169.254.1.1.0": {nbrIP: net.ParseIP("169.254.1.1").To4(), state: stateFull},
	}
	edges, oos, _ := buildEdges("router-a", rows, nil)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for link-local nbrIP, got %d", len(edges))
	}
	if len(oos) != 0 {
		t.Errorf("expected 0 out-of-scope for link-local nbrIP, got %d", len(oos))
	}
}

// parseNbrOID: OID without the expected prefix returns false.
func TestParseNbrOIDNoPrefix(t *testing.T) {
	const prefix = ".1.3.6.1.2.1.14.10.1."
	_, _, ok := parseNbrOID(".1.3.6.1.2.1.99.0.0.1", prefix)
	if ok {
		t.Error("expected ok=false for OID with wrong prefix, got true")
	}
}

// parseNbrOID: OID whose suffix after the prefix has no dot returns false.
func TestParseNbrOIDNoDot(t *testing.T) {
	const prefix = ".1.3.6.1.2.1.14.10.1."
	// OID = prefix + "1" → rest = "1", no dot → dotIdx < 0 → false.
	_, _, ok := parseNbrOID(prefix+"1", prefix)
	if ok {
		t.Error("expected ok=false for OID with no dot after column, got true")
	}
}

// parseNbrOID: OID whose key part has the wrong number of dots returns false.
func TestParseNbrOIDWrongDotCount(t *testing.T) {
	const prefix = ".1.3.6.1.2.1.14.10.1."
	// OID = prefix + "1.192.0.2.1" → key = "192.0.2.1" (3 dots, not 4) → false.
	_, _, ok := parseNbrOID(prefix+"1.192.0.2.1", prefix)
	if ok {
		t.Error("expected ok=false for key with 3 dots (need 4), got true")
	}
}

// parseNbrOID: valid OID returns the column and key correctly.
func TestParseNbrOIDValid(t *testing.T) {
	const prefix = ".1.3.6.1.2.1.14.10.1."
	// OID = prefix + "6.192.0.2.1.0" → col=6, key="192.0.2.1.0" (4 dots) → true.
	col, key, ok := parseNbrOID(prefix+"6.192.0.2.1.0", prefix)
	if !ok {
		t.Fatal("expected ok=true for valid OID, got false")
	}
	if col != 6 {
		t.Errorf("col = %d, want 6", col)
	}
	if key != "192.0.2.1.0" {
		t.Errorf("key = %q, want 192.0.2.1.0", key)
	}
}

// ---------- decode-issue reporter tests (issue #99) ----------

// walkOspfNbrTable: a PDU with a malformed neighbour-OID suffix and an
// ospfNbrIpAddr PDU that does not decode as IPv4 each report a decode issue to
// the reporter installed on ctx, tagged with the ospfNbrTable root OID.
func TestWalkOspfNbrTableReportsDecodeIssues(t *testing.T) {
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"

	pdus := []gsnmp.SnmpPDU{
		// Malformed suffix: within the BulkWalk root but parseNbrOID rejects the
		// ".2." sub-table prefix → oid_suffix_malformed.
		{Name: ".1.3.6.1.2.1.14.10.2.1.192.0.2.1.0", Type: gsnmp.Integer, Value: int(8)},
		// ospfNbrIpAddr with a 2-byte raw value → PDUIPv4 returns nil →
		// nbr_ip_undecodable.
		{Name: base + "1." + idx, Type: gsnmp.OctetString, Value: []byte{192, 0}},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{IP: ip, Port: port, Community: []byte("public"), Timeout: 3 * time.Second}
	client, err := snmputil.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	var issues []snmputil.DecodeIssue
	ctx := snmputil.ContextWithDecodeIssueReporter(context.Background(), func(i snmputil.DecodeIssue) {
		issues = append(issues, i)
	})

	if _, _, err := walkOspfNbrTable(ctx, client); err != nil {
		t.Fatalf("walkOspfNbrTable: %v", err)
	}

	got := map[string]int{}
	for _, i := range issues {
		if i.Module != walkerOSPF {
			t.Errorf("issue module = %q, want ospf", i.Module)
		}
		if string(i.OID) != oidOspfNbrTable {
			t.Errorf("issue OID = %q, want %q", i.OID, oidOspfNbrTable)
		}
		got[i.Reason]++
	}
	if got["oid_suffix_malformed"] != 1 {
		t.Errorf("oid_suffix_malformed count = %d, want 1 (issues: %v)", got["oid_suffix_malformed"], issues)
	}
	if got["nbr_ip_undecodable"] != 1 {
		t.Errorf("nbr_ip_undecodable count = %d, want 1 (issues: %v)", got["nbr_ip_undecodable"], issues)
	}
}
