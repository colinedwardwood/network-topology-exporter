package app

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/grafana/network-topology-exporter/internal/discovery"
)

// TestDeduplicateOOS verifies duplicate (device, port, hint) neighbours are
// collapsed to the first occurrence while insertion order is preserved.
func TestDeduplicateOOS(t *testing.T) {
	in := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "a", ReportingPort: "1", NeighbourHint: "h1", Proto: "lldp"},
		{ReportingDevice: "a", ReportingPort: "1", NeighbourHint: "h1", Proto: "cdp"}, // dup of first
		{ReportingDevice: "a", ReportingPort: "2", NeighbourHint: "h1", Proto: "lldp"},
	}
	out := DeduplicateOOS(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Proto != "lldp" {
		t.Errorf("first kept proto = %q, want lldp (first occurrence)", out[0].Proto)
	}
	if out[1].ReportingPort != "2" {
		t.Errorf("second kept port = %q, want 2", out[1].ReportingPort)
	}
}

// TestMergeOOSFirstSeen restores the original FirstSeen for known neighbours and
// leaves the cycle's fresh timestamp for newly-seen ones.
func TestMergeOOSFirstSeen(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	prev := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "a", ReportingPort: "1", NeighbourHint: "h", FirstSeen: old},
	}
	cur := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "a", ReportingPort: "1", NeighbourHint: "h", FirstSeen: fresh}, // known
		{ReportingDevice: "b", ReportingPort: "2", NeighbourHint: "x", FirstSeen: fresh}, // new
	}
	out := MergeOOSFirstSeen(cur, prev)
	if !out[0].FirstSeen.Equal(old) {
		t.Errorf("known neighbour FirstSeen = %v, want restored %v", out[0].FirstSeen, old)
	}
	if !out[1].FirstSeen.Equal(fresh) {
		t.Errorf("new neighbour FirstSeen = %v, want fresh %v", out[1].FirstSeen, fresh)
	}
	// Ensure the input slice was not mutated.
	if !cur[0].FirstSeen.Equal(fresh) {
		t.Errorf("input slice mutated: cur[0].FirstSeen = %v", cur[0].FirstSeen)
	}
}

// TestDeduplicateDevices keeps the first Device per ID in input order.
func TestDeduplicateDevices(t *testing.T) {
	in := []discovery.Device{
		{ID: "sw-a", Vendor: "first"},
		{ID: "sw-b"},
		{ID: "sw-a", Vendor: "second"}, // dup ID
	}
	out := DeduplicateDevices(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Vendor != "first" {
		t.Errorf("kept vendor = %q, want first occurrence", out[0].Vendor)
	}
}

// TestCollectDegradedReasons gathers the unique reason set across degraded
// edges, defaulting empty/missing reasons to "unknown" and ignoring
// non-degraded edges.
func TestCollectDegradedReasons(t *testing.T) {
	edges := []discovery.Edge{
		{Metadata: map[string]string{discovery.MetadataKeyDegraded: "true", discovery.MetadataKeyDegradedReason: "timeout, partial"}},
		{Metadata: map[string]string{discovery.MetadataKeyDegraded: "true", discovery.MetadataKeyDegradedReason: "timeout"}}, // dup reason
		{Metadata: map[string]string{discovery.MetadataKeyDegraded: "true"}},                                                 // empty reason -> unknown
		{Metadata: map[string]string{discovery.MetadataKeyDegraded: "false"}},                                                // not degraded
		{Metadata: nil}, // no metadata
	}
	got := CollectDegradedReasons(edges)
	want := map[string]bool{"timeout": true, "partial": true, "unknown": true}
	if len(got) != len(want) {
		t.Fatalf("reasons = %v, want keys %v", got, want)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected reason %q in %v", r, got)
		}
	}
}

// TestResolveEdgeDstDevicesIPResolution resolves an IP DstDevice to its sysName
// and keeps an unresolved IP edge as-is.
func TestResolveEdgeDstDevicesIPResolution(t *testing.T) {
	ipToID := map[string]string{"10.0.0.2": "sw-b"}
	edges := []discovery.Edge{
		{SrcDevice: "sw-a", DstDevice: "10.0.0.2", DiscoveryProto: discovery.DiscoveryProtocolBGP},
		{SrcDevice: "sw-a", DstDevice: "10.0.0.9", DiscoveryProto: discovery.DiscoveryProtocolBGP}, // unresolved IP kept
	}
	out := ResolveEdgeDstDevices(slogDiscard(), edges, ipToID, map[string]string{}, nil)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (unresolved IP edge is kept)", len(out))
	}
	if out[0].DstDevice != "sw-b" {
		t.Errorf("resolved dst = %q, want sw-b", out[0].DstDevice)
	}
	if out[1].DstDevice != "10.0.0.9" {
		t.Errorf("unresolved IP dst = %q, want 10.0.0.9 unchanged", out[1].DstDevice)
	}
}

// TestResolveEdgeDstDevicesSuppressesUnresolvedFDBMAC drops an FDB edge whose
// MAC peer cannot be resolved and increments the suppressed counter, while a
// non-FDB MAC edge is retained.
func TestResolveEdgeDstDevicesSuppressesUnresolvedFDBMAC(t *testing.T) {
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_suppressed_total"})
	macToID := map[string]string{"aa:bb:cc:dd:ee:01": "sw-known"}
	edges := []discovery.Edge{
		{SrcDevice: "sw-a", DstDevice: "aa:bb:cc:dd:ee:01", DiscoveryProto: discovery.DiscoveryProtocolFDB},  // resolved
		{SrcDevice: "sw-a", DstDevice: "aa:bb:cc:dd:ee:99", DiscoveryProto: discovery.DiscoveryProtocolFDB},  // unresolved FDB -> suppressed
		{SrcDevice: "sw-a", DstDevice: "aa:bb:cc:dd:ee:88", DiscoveryProto: discovery.DiscoveryProtocolLLDP}, // unresolved non-FDB -> kept as MAC
	}
	out := ResolveEdgeDstDevices(slogDiscard(), edges, map[string]string{}, macToID, counter)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (one FDB MAC suppressed)", len(out))
	}
	if out[0].DstDevice != "sw-known" {
		t.Errorf("resolved FDB dst = %q, want sw-known", out[0].DstDevice)
	}
	// The LLDP MAC edge is kept (normalised to canonical MAC form, unchanged value).
	if out[1].DiscoveryProto != discovery.DiscoveryProtocolLLDP {
		t.Errorf("second kept edge proto = %q, want lldp", out[1].DiscoveryProto)
	}
	if got := testutil.ToFloat64(counter); got != 1 {
		t.Errorf("suppressed counter = %v, want 1", got)
	}
}

// TestSynthesizeEdgesBackfillsFDBPort builds the LLDP chassis-MAC index, resolves
// an FDB MAC peer to a sysName via that index, and backfills the FDB edge's
// DstPort from the matching LLDP observation.
func TestSynthesizeEdgesBackfillsFDBPort(t *testing.T) {
	lldp := discovery.Edge{
		SrcDevice:      "sw-a",
		SrcPort:        "Gi0/1",
		DstDevice:      "sw-b",
		DstPort:        "Gi0/2",
		DiscoveryProto: discovery.DiscoveryProtocolLLDP,
		Metadata:       map[string]string{discovery.MetadataKeyPeerChassisMac: "aa:bb:cc:dd:ee:ff"},
	}
	fdb := discovery.Edge{
		SrcDevice:      "sw-a",
		SrcPort:        "Gi0/1",
		DstDevice:      "aa:bb:cc:dd:ee:ff", // MAC -> resolves to sw-b via LLDP index
		DiscoveryProto: discovery.DiscoveryProtocolFDB,
	}
	out := SynthesizeEdges(slogDiscard(), []discovery.Edge{lldp, fdb}, map[string]string{}, map[string]string{}, nil)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	var fdbOut *discovery.Edge
	for i := range out {
		if out[i].DiscoveryProto == discovery.DiscoveryProtocolFDB {
			fdbOut = &out[i]
		}
	}
	if fdbOut == nil {
		t.Fatal("FDB edge not present in output")
	}
	if fdbOut.DstDevice != "sw-b" {
		t.Errorf("FDB dst = %q, want resolved sw-b", fdbOut.DstDevice)
	}
	if fdbOut.DstPort != "Gi0/2" {
		t.Errorf("FDB dst port = %q, want backfilled Gi0/2", fdbOut.DstPort)
	}
}

// TestSynthesizeEdgesARPFallback resolves a MAC via the ARP table when no LLDP
// chassis annotation provides the mapping.
func TestSynthesizeEdgesARPFallback(t *testing.T) {
	ipToID := map[string]string{"10.0.0.2": "sw-b"}
	arpMACToIP := map[string]string{"aa:bb:cc:dd:ee:ff": "10.0.0.2"}
	fdb := discovery.Edge{
		SrcDevice:      "sw-a",
		SrcPort:        "Gi0/1",
		DstDevice:      "aa:bb:cc:dd:ee:ff",
		DiscoveryProto: discovery.DiscoveryProtocolFDB,
	}
	out := SynthesizeEdges(slogDiscard(), []discovery.Edge{fdb}, ipToID, arpMACToIP, nil)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].DstDevice != "sw-b" {
		t.Errorf("ARP-resolved dst = %q, want sw-b", out[0].DstDevice)
	}
}
