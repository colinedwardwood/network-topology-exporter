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

// TestWalkerOutcomeMibUnimplemented: device responds but neither bgp4V2PeerTable
// nor the RFC 4273 table has any PDUs. BulkWalk returns zero PDUs for both
// roots, so v2_draft and rfc4273 both record "mib_unimplemented" — the device
// does not speak BGP at all. Per issue #15, this is operationally distinct
// from "no_peers" (MIB implemented, no peers established) and must not page.
func TestWalkerOutcomeMibUnimplemented(t *testing.T) {
	c := newOutcomeCounter(t)

	// snmptest with an unrelated PDU so BulkWalk against the BGP roots
	// returns no rows. Use a sysDescr-style PDU outside both BGP roots.
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
		Vendor:      "arista", // no vendor walker dispatched for arista
	}

	edges, _, err := Walk(context.Background(), p, "rtr-empty", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}

	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "mib_unimplemented")); got != 1 {
		t.Errorf("v2_draft mib_unimplemented counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "mib_unimplemented")); got != 1 {
		t.Errorf("rfc4273 mib_unimplemented counter = %v, want 1", got)
	}
	// Neither "no_peers" nor "edges" should be set for either walker.
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "no_peers")); got != 0 {
		t.Errorf("v2_draft no_peers counter = %v, want 0 (MIB unimplemented, not no peers)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "no_peers")); got != 0 {
		t.Errorf("rfc4273 no_peers counter = %v, want 0 (MIB unimplemented, not no peers)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "edges")); got != 0 {
		t.Errorf("v2_draft edges counter = %v, want 0", got)
	}
}

// TestWalkerOutcomeNoPeers: device implements the RFC 4273 bgpPeerTable but
// every row is in a non-established state — BGP is configured but no session
// has come up. rfc4273 must record "no_peers" (not "mib_unimplemented"), so
// operators can alert on this without false positives from non-BGP devices.
// v2_draft still records "mib_unimplemented" because the device doesn't
// expose the draft form.
func TestWalkerOutcomeNoPeers(t *testing.T) {
	c := newOutcomeCounter(t)

	// RFC 4273 row in idle state (1) — MIB implemented, peer not established.
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
		Vendor:      "arista",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-no-peers", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges (no established peers), got %d", len(edges))
	}

	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "no_peers")); got != 1 {
		t.Errorf("rfc4273 no_peers counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "mib_unimplemented")); got != 0 {
		t.Errorf("rfc4273 mib_unimplemented counter = %v, want 0 (MIB IS implemented)", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "mib_unimplemented")); got != 1 {
		t.Errorf("v2_draft mib_unimplemented counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeMalformedIndex: feed bgp4V2PeerTable rows whose index does
// not decode (truncated). The walker drops the row and increments the
// malformed_index counter. PDUs did arrive (so the MIB is implemented) but
// no peer was assembled, so v2_draft "no_peers" is also recorded — distinct
// from "mib_unimplemented" (issue #15).
func TestWalkerOutcomeMalformedIndex(t *testing.T) {
	c := newOutcomeCounter(t)

	// Build a v2 row with a truncated index — the decoder rejects suffixes
	// shorter than the 12-part minimum. Use a single state column under a
	// 3-part index so the walker sees a "row" but cannot decode it.
	const base = ".1.3.6.1.3.5.1.1.2.1."
	const badIdx = "0.1.4" // localAddrType=1, localAddrLen=4, then nothing
	pdus := []gsnmp.SnmpPDU{
		{Name: base + "13." + badIdx, Type: gsnmp.Integer, Value: bgpStateEstablished},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "arista",
	}

	_, _, err := Walk(context.Background(), p, "rtr-malformed", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "malformed_index")); got != 1 {
		t.Errorf("v2_draft malformed_index counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeError: when the SNMP walk itself errors (the v2 BulkWalk
// returns a non-nil error), v2_draft "error" is recorded and the walker
// promotes the prior Debug log to Warn iff RFC 4273 fallback succeeds. We
// drive the error by cancelling the context before the walk; the walk then
// fails for both v2 and RFC 4273, so v2_draft="error" and rfc4273="error"
// should both be present.
func TestWalkerOutcomeError(t *testing.T) {
	c := newOutcomeCounter(t)

	// snmptest agent that returns no PDUs at all; we cancel the context
	// before the walks dispatch so BulkWalk hits a context error.
	addr := snmptest.Start(t, "public", []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("x")},
	})
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "arista",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so BulkWalk's first read errors

	_, _, _ = Walk(ctx, p, "rtr-err", nil)

	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "error")); got != 1 {
		t.Errorf("v2_draft error counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "error")); got != 1 {
		t.Errorf("rfc4273 error counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeRFC4273Edges: v2 returns empty, RFC 4273 returns one
// established peer. rfc4273 "edges" should be recorded.
func TestWalkerOutcomeRFC4273Edges(t *testing.T) {
	c := newOutcomeCounter(t)

	addr := snmptest.Start(t, "public", buildBgpAgentPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "arista",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-rfc", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RFC 4273, got %d", len(edges))
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "edges")); got != 1 {
		t.Errorf("rfc4273 edges counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "mib_unimplemented")); got != 1 {
		t.Errorf("v2_draft mib_unimplemented counter = %v, want 1", got)
	}
}

// TestWalkerOutcomeV2DraftEdges: v2 returns one established IPv6 peer; the
// vendor and RFC 4273 walkers must NOT be invoked. Counter records
// v2_draft="edges" exactly once.
func TestWalkerOutcomeV2DraftEdges(t *testing.T) {
	c := newOutcomeCounter(t)

	addr := snmptest.Start(t, "public", buildV2IPv6PeerPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
	}

	if _, _, err := Walk(context.Background(), p, "rtr-v2", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "edges")); got != 1 {
		t.Errorf("v2_draft edges counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "edges")); got != 0 {
		t.Errorf("rfc4273 must not have been invoked; got %v", got)
	}
}

// TestVendorWalkerLabel sanity-checks the vendor → walker-label mapping so a
// new vendor spec must wire its label or the test breaks.
func TestVendorWalkerLabel(t *testing.T) {
	cases := []struct {
		specName string
		want     string
	}{
		{ciscoCbgpPeer2Spec.name, walkerVendorCisco},
		{juniperJnxBgpM2PeerSpec.name, walkerVendorJuniper},
		{nokiaTBgpPeerSpec.name, walkerVendorNokia},
		{"unknown-spec", "vendor_unknown"},
	}
	for _, c := range cases {
		t.Run(c.specName, func(t *testing.T) {
			if got := vendorWalkerLabel(c.specName); got != c.want {
				t.Errorf("vendorWalkerLabel(%q) = %q, want %q", c.specName, got, c.want)
			}
		})
	}
}

// TestVendorWalkerEdges: Cisco cbgpPeer2Table returns one row; vendor_cisco
// "edges" should be recorded, v2_draft should be "empty", rfc4273 untouched.
func TestVendorWalkerEdges(t *testing.T) {
	c := newOutcomeCounter(t)

	addr := snmptest.Start(t, "public", buildCiscoCbgpPeer2PDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	if _, _, err := Walk(context.Background(), p, "rtr-cisco", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerVendorCisco, "edges")); got != 1 {
		t.Errorf("vendor_cisco edges counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerV2Draft, "mib_unimplemented")); got != 1 {
		t.Errorf("v2_draft mib_unimplemented counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(c.WithLabelValues(walkerRFC4273, "edges")); got != 0 {
		t.Errorf("rfc4273 must not have been invoked; got %v", got)
	}
}

// TestRecordWalkerOutcomeNilSafe verifies that recordWalkerOutcome is a no-op
// when no counter is wired. Guards against a regression in the production
// flag-check that would crash any process without main wired up (e.g. an
// imported test that doesn't call SetWalkerOutcomeCounter).
func TestRecordWalkerOutcomeNilSafe(t *testing.T) {
	prev := walkerOutcomeCounter.Load()
	SetWalkerOutcomeCounter(nil)
	t.Cleanup(func() { walkerOutcomeCounter.Store(prev) })
	// Must not panic.
	recordWalkerOutcome(walkerV2Draft, "empty")
}

// TestTruncateForLog covers the log-bound helper directly so the malformed-
// index code path has explicit coverage of its only formatting choice.
func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("short", 50); got != "short" {
		t.Errorf("short string = %q, want passthrough", got)
	}
	long := "0.1.4.192.0.2.1.1.4.192.0.2.2.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99.99"
	got := truncateForLog(long, 50)
	if len(got) != 53 { // 50 + len("...")
		t.Errorf("len = %d, want 53", len(got))
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("missing ellipsis suffix: %q", got)
	}
}
