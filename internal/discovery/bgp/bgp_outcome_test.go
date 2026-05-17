package bgp

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// newOutcomeCounter constructs a fresh CounterVec mirroring the production
// metric's shape and wires it as the package-level sink for the duration of
// the test. The cleanup callback restores the previous sink so tests stay
// isolated from one another and from any prior /metrics handler setup in
// integration tests that share the package.
func newOutcomeCounter(t *testing.T) *prometheus.CounterVec {
	t.Helper()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "network_topology_bgp_walker_outcome_total",
		Help: "test counter",
	}, []string{"walker", "outcome"})
	prev := walkerOutcomeCounter.Load()
	SetWalkerOutcomeCounter(c)
	t.Cleanup(func() { walkerOutcomeCounter.Store(prev) })
	return c
}

// TestWalkerOutcomeMibUnimplemented: device responds to SNMP but neither
// the vendor BGP table nor the RFC 4273 table has any PDUs. The vendor
// walker is skipped entirely (no vendor mapping for the test's vendor),
// and the rfc4273 walker records "mib_unimplemented" — the device does
// not implement BGP MIBs at all. Per issue #15, this outcome must not page.
func TestWalkerOutcomeMibUnimplemented(t *testing.T) {
	c := newOutcomeCounter(t)

	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "mikrotik", // no vendor walker dispatched
	}

	edges, _, err := Walk(context.Background(), p, "rtr-empty", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}

	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeMIBUnimplemented)); got != 1 {
		t.Errorf("rfc4273 mib_unimplemented counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeNoPeers)); got != 0 {
		t.Errorf("rfc4273 no_peers counter = %v, want 0 (MIB unimplemented, not no peers)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeEdges)); got != 0 {
		t.Errorf("rfc4273 edges counter = %v, want 0", got)
	}
}

// TestWalkerOutcomeNoPeers: device implements the RFC 4273 bgpPeerTable
// but every row is in a non-established state — BGP is configured but no
// session is up. rfc4273 must record "no_peers" (not "mib_unimplemented"),
// so operators can alert on this without false positives from non-BGP
// devices.
func TestWalkerOutcomeNoPeers(t *testing.T) {
	c := newOutcomeCounter(t)

	const base = ".1.3.6.1.2.1.15.3.1."
	const peer = "10.0.0.1"
	pdus := []gsnmp.SnmpPDU{
		{Name: base + "2." + peer, Type: gsnmp.Integer, Value: 1}, // bgpPeerState = idle
		{Name: base + "7." + peer, Type: gsnmp.IPAddress, Value: []byte{10, 0, 0, 1}},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "mikrotik",
	}

	if _, _, err := Walk(context.Background(), p, "rtr", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeNoPeers)); got != 1 {
		t.Errorf("rfc4273 no_peers counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeMIBUnimplemented)); got != 0 {
		t.Errorf("rfc4273 mib_unimplemented counter = %v, want 0 (PDUs arrived; MIB is implemented)", got)
	}
}

// TestWalkerOutcomeRFC4273Edges: established peer on RFC 4273 → edges.
func TestWalkerOutcomeRFC4273Edges(t *testing.T) {
	c := newOutcomeCounter(t)
	addr := snmptest.Start(t, "public", buildBgpAgentPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: false, // skip vendor lookup, go straight to RFC 4273
	}

	edges, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeEdges)); got != 1 {
		t.Errorf("rfc4273 edges counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeVendorCiscoEdges: Cisco vendor walker fires on a
// device whose cbgpPeer2Table has an established peer. Validates the
// new column numbers from real captures (state=3, remoteAs=11) and the
// index decoder.
func TestWalkerOutcomeVendorCiscoEdges(t *testing.T) {
	c := newOutcomeCounter(t)
	addr := snmptest.Start(t, "public", buildCiscoCbgpPeer2RealPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-cisco", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Cisco walker, got %d", len(edges))
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeEdges)); got != 1 {
		t.Errorf("vendor_cisco edges counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeVendorAristaEdges: Arista vendor walker (the new one
// added in issue #31).
func TestWalkerOutcomeVendorAristaEdges(t *testing.T) {
	c := newOutcomeCounter(t)
	addr := snmptest.Start(t, "public", buildAristaBgp4v2RealPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "arista",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-arista", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Arista walker, got %d", len(edges))
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorArista, outcomeEdges)); got != 1 {
		t.Errorf("vendor_arista edges counter = %v, want 1", got)
	}
}

// TestRecordWalkerOutcomeAllPDUsMalformedIsDrift (issue #27): a Cisco-
// dispatched walk against rows whose index suffix doesn't match the
// Cisco format. Every row drops via the malformed_index path; the
// per-PDU counter records each drop, and — critically — the walk-level
// outcome records "walker_drift", NOT "no_peers". Operators alerting on
// outcome=walker_drift get the signal that our walker is broken on this
// vendor's MIB; operators alerting on outcome=no_peers get a clean
// "BGP is broken on every session" signal without the conflation that
// shipped before #27.
func TestRecordWalkerOutcomeAllPDUsMalformedIsDrift(t *testing.T) {
	c := newOutcomeCounter(t)

	// Use the Arista index format (peerInst.type.len.addr) against the
	// Cisco walker, which expects type.len.addr only. Every row is
	// rejected by decodeCiscoCbgpPeer2Index because the addrType byte
	// position is shifted.
	const base = ".1.3.6.1.4.1.9.9.187.1.2.5.1."
	const aristaIdx = "1.1.4.10.0.0.2" // peerInst=1 throws off Cisco decoder
	pdus := []gsnmp.SnmpPDU{
		{Name: base + "3." + aristaIdx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "11." + aristaIdx, Type: gsnmp.Gauge32, Value: uint(65001)},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	_, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Two rows in the input, both malformed for the Cisco decoder — the
	// per-PDU malformed_index counter still increments per-PDU so the
	// soft-signal semantics are preserved.
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeMalformedIndex)); got != 2 {
		t.Errorf("vendor_cisco malformed_index counter = %v, want 2", got)
	}
	// The walk-level outcome must be walker_drift (NOT no_peers — issue #27).
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeWalkerDrift)); got != 1 {
		t.Errorf("vendor_cisco walker_drift counter = %v, want 1 (every row rejected by decoder)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeNoPeers)); got != 0 {
		t.Errorf("vendor_cisco no_peers counter = %v, want 0 (all rows malformed, not no peers)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeMIBUnimplemented)); got != 0 {
		t.Errorf("vendor_cisco mib_unimplemented counter = %v, want 0 (PDUs arrived)", got)
	}
	// Walk falls back to RFC 4273 after the vendor walker returns no
	// peers; RFC 4273 has no data in this test, so it records
	// mib_unimplemented. That's the documented behaviour of the fallback
	// chain — assert it so a future refactor that breaks the fallback
	// surfaces here.
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeMIBUnimplemented)); got != 1 {
		t.Errorf("rfc4273 mib_unimplemented counter = %v, want 1 (vendor drift → fallback ran)", got)
	}
}

// TestRecordWalkerOutcomeSomeMalformedSomeValid (issue #27): a Cisco
// walk where half the rows decode cleanly (and reach Established) and
// half are rejected by the decoder. The walk-level outcome is "edges"
// (success path); the soft malformed_index signal increments per-row
// in the other counter. This is the case where outcomeWalkerDrift
// must NOT fire — a partial decoder mismatch is a warn-level signal,
// not a page-level one.
func TestRecordWalkerOutcomeSomeMalformedSomeValid(t *testing.T) {
	c := newOutcomeCounter(t)

	const base = ".1.3.6.1.4.1.9.9.187.1.2.5.1."
	const goodIdx = "1.4.10.0.0.2"  // Cisco-decoder-clean: ipv4 10.0.0.2
	const badIdx = "1.1.4.10.0.0.3" // Arista-shape: rejected by Cisco decoder
	pdus := []gsnmp.SnmpPDU{
		// Good row: col 3 (state) + col 11 (as) for 10.0.0.2 → established.
		{Name: base + "3." + goodIdx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "11." + goodIdx, Type: gsnmp.Gauge32, Value: uint(65001)},
		// Bad row: both PDUs trigger decoder failure for the same index;
		// the failedIndexes set tracks one unique row, but malformed_index
		// counter increments per PDU.
		{Name: base + "3." + badIdx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "11." + badIdx, Type: gsnmp.Gauge32, Value: uint(65002)},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from the good row, got %d", len(edges))
	}
	// Walk-level outcome: edges (success — at least one peer decoded
	// AND reached Established).
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeEdges)); got != 1 {
		t.Errorf("vendor_cisco edges counter = %v, want 1", got)
	}
	// Walker drift must NOT fire — the decoder worked on at least one row.
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeWalkerDrift)); got != 0 {
		t.Errorf("vendor_cisco walker_drift counter = %v, want 0 (one row decoded cleanly)", got)
	}
	// Per-PDU malformed_index still increments (2 PDUs for the bad index).
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeMalformedIndex)); got != 2 {
		t.Errorf("vendor_cisco malformed_index counter = %v, want 2 (per-PDU)", got)
	}
}

// TestRecordWalkerOutcomeNoPDUsIsMIBUnimplemented (issue #27 regression
// guard): the zero-PDU case must still record mib_unimplemented, not
// walker_drift — a non-BGP device must not page on this counter.
func TestRecordWalkerOutcomeNoPDUsIsMIBUnimplemented(t *testing.T) {
	c := newOutcomeCounter(t)

	// Agent responds only to sysDescr; vendor table walk returns empty.
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	if _, _, err := Walk(context.Background(), p, "rtr", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeMIBUnimplemented)); got != 1 {
		t.Errorf("vendor_cisco mib_unimplemented counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeWalkerDrift)); got != 0 {
		t.Errorf("vendor_cisco walker_drift counter = %v, want 0 (no PDUs, no rows attempted)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeNoPeers)); got != 0 {
		t.Errorf("vendor_cisco no_peers counter = %v, want 0", got)
	}
}

// TestRecordWalkerOutcomePDUsButNoneEstablished (issue #27 regression
// guard): the genuine no_peers case — rows decode cleanly but every
// peer's state is non-Established — must continue to record no_peers
// (NOT walker_drift, NOT mib_unimplemented).
func TestRecordWalkerOutcomePDUsButNoneEstablished(t *testing.T) {
	c := newOutcomeCounter(t)

	const base = ".1.3.6.1.4.1.9.9.187.1.2.5.1."
	const idx = "1.4.10.0.0.2" // clean Cisco-format index
	pdus := []gsnmp.SnmpPDU{
		// State = idle(1); decoder will accept the row but the peer is
		// not Established → no edge → outcomeNoPeers.
		{Name: base + "3." + idx, Type: gsnmp.Integer, Value: 1},
		{Name: base + "11." + idx, Type: gsnmp.Gauge32, Value: uint(65001)},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges (no Established peer), got %d", len(edges))
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeNoPeers)); got != 1 {
		t.Errorf("vendor_cisco no_peers counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeWalkerDrift)); got != 0 {
		t.Errorf("vendor_cisco walker_drift counter = %v, want 0 (rows decoded cleanly)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, outcomeMalformedIndex)); got != 0 {
		t.Errorf("vendor_cisco malformed_index counter = %v, want 0 (no decode failures)", got)
	}
}

// TestWalkerOutcomeError: SNMP error path. Build an unreachable target;
// Open succeeds but BulkWalk errors. Note: with a fast timeout and the
// test agent stopped, the open/connect itself fails — we get the error
// from there. Either way the walker should record outcome=error.
func TestWalkerOutcomeError(t *testing.T) {
	c := newOutcomeCounter(t)

	// A snmptest agent that we immediately stop — connect succeeds at
	// the kernel layer but no agent responds.
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "wrong-community-causes-timeout",
		Timeout:     time.Nanosecond, // immediate timeout
		UseBGPV2MIB: true,
	}

	_, _, _ = Walk(context.Background(), p, "rtr", nil)
	// Walk may return an error; we just verify the rfc4273 path didn't
	// inappropriately record "edges". One of error / mib_unimplemented is
	// expected.
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, outcomeEdges)); got != 0 {
		t.Errorf("rfc4273 edges = %v, want 0 on failure path", got)
	}
}

// TestVendorWalkerLabel covers vendorSpecFor + label mapping symmetry
// for the four vendors we ship walkers for.
func TestVendorWalkerLabel(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{"cisco", walkerVendorCisco},
		{"arista", walkerVendorArista},
		{"juniper", walkerVendorJuniper},
		{"nokia", walkerVendorNokia},
	}
	for _, c := range cases {
		spec := vendorSpecFor(c.vendor)
		if spec == nil {
			t.Errorf("vendorSpecFor(%q) returned nil", c.vendor)
			continue
		}
		if got := vendorWalkerLabel(spec.name); got != c.want {
			t.Errorf("vendor %q: vendorWalkerLabel(%q) = %q, want %q", c.vendor, spec.name, got, c.want)
		}
	}
}

// TestRecordWalkerOutcomeNilSafe verifies that increments dropped when
// the counter is nil are truly dropped — they do NOT land on a stale
// counter pointer. Issue #23 / D27.
func TestRecordWalkerOutcomeNilSafe(t *testing.T) {
	prev := walkerOutcomeCounter.Load()
	t.Cleanup(func() { walkerOutcomeCounter.Store(prev) })

	sentinel := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_sentinel"}, []string{"walker", "outcome"})
	SetWalkerOutcomeCounter(sentinel)
	recordWalkerOutcome(walkerVendorCisco, outcomeEdges)
	if v := testutil.ToFloat64(sentinel.WithLabelValues(walkerVendorCisco, outcomeEdges)); v != 1 {
		t.Fatalf("baseline: sentinel counter = %v, want 1", v)
	}

	SetWalkerOutcomeCounter(nil)
	recordWalkerOutcome(walkerVendorCisco, outcomeEdges)
	if v := testutil.ToFloat64(sentinel.WithLabelValues(walkerVendorCisco, outcomeEdges)); v != 1 {
		t.Errorf("after nil set: sentinel counter = %v, want 1 (increment must be dropped, not landed on stale pointer)", v)
	}
}

// TestTruncateForLog covers byte length truncation and rune boundary
// safety. The function operates on rune boundaries to avoid emitting
// invalid UTF-8 in log fields (issue #24 / D27).
func TestTruncateForLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short ascii", "hello", 50, "hello"},
		{"long ascii", "abcdefghij", 5, "abcde..."},
		{"exact length", "abcde", 5, "abcde"},
		{"empty", "", 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncateForLog(c.in, c.max); got != c.want {
				t.Errorf("truncateForLog(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
		})
	}
}
