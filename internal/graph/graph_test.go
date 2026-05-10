package graph

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// LD-14: a unidirectional edge expires once it has been unconfirmed for ttl
// consecutive cycles.
func TestAgeUnconfirmedExpiresAfterTTL(t *testing.T) {
	edge := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		Direction: discovery.DirectionUnidirectional,
	}
	ages := map[EdgeKey]int{}
	for cycle := 1; cycle <= 2; cycle++ {
		expired := AgeUnconfirmed([]discovery.Edge{edge}, ages, 3)
		if len(expired) != 0 {
			t.Fatalf("cycle %d: expected no expirations yet, got %v", cycle, expired)
		}
	}
	expired := AgeUnconfirmed([]discovery.Edge{edge}, ages, 3)
	if len(expired) != 1 {
		t.Fatalf("cycle 3: expected one expiration, got %v", expired)
	}
	if expired[0] != Key(edge) {
		t.Errorf("expired key = %#v, want %#v", expired[0], Key(edge))
	}
}

// LD-14: a bidirectional confirmation resets the counter so an edge that
// was briefly unidirectional doesn't get evicted on the next bidirectional
// observation.
func TestAgeUnconfirmedResetsOnBidirectional(t *testing.T) {
	uni := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		Direction: discovery.DirectionUnidirectional,
	}
	bi := uni
	bi.Direction = discovery.DirectionBidirectional

	ages := map[EdgeKey]int{}
	AgeUnconfirmed([]discovery.Edge{uni}, ages, 3)
	AgeUnconfirmed([]discovery.Edge{uni}, ages, 3)

	if got := ages[Key(uni)]; got != 2 {
		t.Fatalf("counter after two unconfirmed cycles = %d, want 2", got)
	}
	if expired := AgeUnconfirmed([]discovery.Edge{bi}, ages, 3); len(expired) != 0 {
		t.Errorf("bidirectional cycle should not expire, got %v", expired)
	}
	if _, ok := ages[Key(bi)]; ok {
		t.Errorf("counter should be cleared after bidirectional confirmation")
	}
}

// LD-14: an edge that disappears entirely (no longer reported by any
// protocol) is dropped from the counter map so it doesn't leak.
func TestAgeUnconfirmedClearsAbsentKeys(t *testing.T) {
	edge := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		Direction: discovery.DirectionUnidirectional,
	}
	ages := map[EdgeKey]int{Key(edge): 2}
	AgeUnconfirmed(nil, ages, 3)
	if _, ok := ages[Key(edge)]; ok {
		t.Fatal("absent edge should be cleared from counter map")
	}
}

// LD-14 regression: an edge that reappears after expiry must start a fresh
// counter, not immediately expire again. Before the fix, AgeUnconfirmed did
// not delete(ages, k) after appending to expired, so ages[k] remained at ttl.
// On the next cycle, ages[k]++ produced ttl+1 >= ttl → permanently expired.
func TestAgeUnconfirmedReappearsAfterExpiry(t *testing.T) {
	edge := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		Direction: discovery.DirectionUnidirectional,
	}
	ages := map[EdgeKey]int{}
	const ttl = 2

	// Advance to expiry.
	AgeUnconfirmed([]discovery.Edge{edge}, ages, ttl) // ages[k] = 1
	expired := AgeUnconfirmed([]discovery.Edge{edge}, ages, ttl)
	if len(expired) != 1 {
		t.Fatalf("expected expiry at ttl=%d, got %v", ttl, expired)
	}

	// Counter must be absent so a reappearing edge gets a fresh start.
	if _, ok := ages[Key(edge)]; ok {
		t.Fatal("ages key must be deleted after expiry")
	}

	// Edge reappears next cycle — must not expire immediately.
	expired = AgeUnconfirmed([]discovery.Edge{edge}, ages, ttl)
	if len(expired) != 0 {
		t.Fatalf("reappeared edge must not re-expire on first cycle back, got %v", expired)
	}
	if got := ages[Key(edge)]; got != 1 {
		t.Fatalf("counter after reappearance = %d, want 1 (fresh)", got)
	}
}

// AgeUnconfirmed returns nil immediately when ages is nil (nil means no
// lifecycle tracking is active for this call).
func TestAgeUnconfirmedNilAges(t *testing.T) {
	edge := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		Direction: discovery.DirectionUnidirectional,
	}
	if expired := AgeUnconfirmed([]discovery.Edge{edge}, nil, 3); expired != nil {
		t.Errorf("nil ages should return nil, got %v", expired)
	}
}

// Key normalisation: the same physical link reported with src/dst swapped
// must produce the same EdgeKey so the lifecycle counter accumulates against
// one entry, not two.
func TestKeyIsSymmetric(t *testing.T) {
	a := discovery.Edge{SrcDevice: "a", SrcPort: "Gi0/1", DstDevice: "b", DstPort: "Gi0/2"}
	b := discovery.Edge{SrcDevice: "b", SrcPort: "Gi0/2", DstDevice: "a", DstPort: "Gi0/1"}
	if !reflect.DeepEqual(Key(a), Key(b)) {
		t.Fatalf("Key(a)=%#v Key(b)=%#v should match", Key(a), Key(b))
	}
}

// EdgeKeyString / EdgeKeyFromString: round-trip a canonical key through the
// snapshot's pipe-delimited encoding.
func TestEdgeKeyStringSerialization(t *testing.T) {
	k := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	s := EdgeKeyString(k)
	if s != "sw-a|Gi0/1|sw-b|Gi0/2" {
		t.Fatalf("EdgeKeyString = %q, want sw-a|Gi0/1|sw-b|Gi0/2", s)
	}
	got, err := EdgeKeyFromString(s)
	if err != nil {
		t.Fatalf("EdgeKeyFromString: %v", err)
	}
	if got != k {
		t.Errorf("round-trip mismatch: got %#v, want %#v", got, k)
	}
}

// EdgeKeyFromString returns an error when the input has fewer than four
// pipe-separated components.
func TestEdgeKeyFromStringError(t *testing.T) {
	if _, err := EdgeKeyFromString("a|b|c"); err == nil {
		t.Error("expected error for key with only 3 pipe-separated components")
	}
}

// Reconcile: a link reported by both endpoints becomes DirectionBidirectional.
func TestReconcileBidirectionality(t *testing.T) {
	fromA := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", PrecedenceRank: 2,
		Direction: discovery.DirectionUnidirectional,
	}
	fromB := discovery.Edge{
		SrcDevice: "b", SrcPort: "Gi0/2",
		DstDevice: "a", DstPort: "Gi0/1",
		DiscoveryProto: "lldp", PrecedenceRank: 2,
		Direction: discovery.DirectionUnidirectional,
	}
	edges, _ := Reconcile([]discovery.Edge{fromA, fromB})
	if len(edges) != 1 {
		t.Fatalf("expected 1 reconciled edge, got %d", len(edges))
	}
	if edges[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", edges[0].Direction)
	}
}

// Reconcile: when the same link is reported by two protocols at different
// ranks, the lower-rank (higher-priority) observation wins.
func TestReconcilePrecedenceRank(t *testing.T) {
	lldp := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", PrecedenceRank: 2,
	}
	fdb := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "fdb", PrecedenceRank: 4,
	}
	edges, _ := Reconcile([]discovery.Edge{fdb, lldp})
	if len(edges) != 1 {
		t.Fatalf("expected 1 reconciled edge, got %d", len(edges))
	}
	if edges[0].DiscoveryProto != "lldp" {
		t.Errorf("proto = %q, want lldp (lower rank wins)", edges[0].DiscoveryProto)
	}
}

// Reconcile: canonical endpoint order is stable regardless of which side
// was passed first.
func TestReconcileCanonicalOrder(t *testing.T) {
	// "b" < "a" alphabetically so the canonical key has b as SrcDevice.
	e := discovery.Edge{
		SrcDevice: "z-device", SrcPort: "Gi1",
		DstDevice: "a-device", DstPort: "Gi2",
		DiscoveryProto: "lldp", PrecedenceRank: 2,
	}
	edges, _ := Reconcile([]discovery.Edge{e})
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	// Canonical order: a-device < z-device → a-device is SrcDevice.
	if edges[0].SrcDevice != "a-device" {
		t.Errorf("SrcDevice = %q, want a-device (canonical order)", edges[0].SrcDevice)
	}
}

// Diff: a new edge appears as ChangeAdded.
func TestDiffAdded(t *testing.T) {
	e := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", Direction: discovery.DirectionBidirectional,
	}
	changes := Diff(nil, []discovery.Edge{e})
	if len(changes) != 1 || changes[0].Kind != ChangeAdded {
		t.Fatalf("expected 1 added change, got %v", changes)
	}
}

// Diff: an edge that disappears appears as ChangeRemoved.
func TestDiffRemoved(t *testing.T) {
	e := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", Direction: discovery.DirectionBidirectional,
	}
	changes := Diff([]discovery.Edge{e}, nil)
	if len(changes) != 1 || changes[0].Kind != ChangeRemoved {
		t.Fatalf("expected 1 removed change, got %v", changes)
	}
}

// Diff: a direction change (uni→bi) is ChangeUpdated.
func TestDiffDirectionChange(t *testing.T) {
	before := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", Direction: discovery.DirectionUnidirectional,
	}
	after := before
	after.Direction = discovery.DirectionBidirectional

	changes := Diff([]discovery.Edge{before}, []discovery.Edge{after})
	if len(changes) != 1 || changes[0].Kind != ChangeUpdated {
		t.Fatalf("expected 1 updated change, got %v", changes)
	}
}

// Diff: identical edge sets produce no changes.
func TestDiffNoChange(t *testing.T) {
	e := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", Direction: discovery.DirectionBidirectional,
	}
	if changes := Diff([]discovery.Edge{e}, []discovery.Edge{e}); len(changes) != 0 {
		t.Errorf("expected no changes for identical graphs, got %v", changes)
	}
}

// AgesToEdgeKeys round-trips a string map through the snapshot encoding.
func TestAgesToEdgeKeys(t *testing.T) {
	in := map[string]int{
		"sw-a|Gi0/1|sw-b|Gi0/2": 2,
		"sw-c|Gi0/3|sw-d|Gi0/4": 1,
	}
	got := AgesToEdgeKeys(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	k1 := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	if got[k1] != 2 {
		t.Errorf("ages[%v] = %d, want 2", k1, got[k1])
	}
	k2 := EdgeKey{SrcDevice: "sw-c", SrcPort: "Gi0/3", DstDevice: "sw-d", DstPort: "Gi0/4"}
	if got[k2] != 1 {
		t.Errorf("ages[%v] = %d, want 1", k2, got[k2])
	}
}

// AgesToEdgeKeys silently drops entries whose key is malformed.
func TestAgesToEdgeKeysMalformed(t *testing.T) {
	in := map[string]int{
		"sw-a|Gi0/1|sw-b|Gi0/2": 1,
		"bad-key":               99,
	}
	got := AgesToEdgeKeys(in)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (malformed key must be dropped)", len(got))
	}
}

// EdgeKeysToAges round-trips an EdgeKey map through the snapshot encoding.
func TestEdgeKeysToAges(t *testing.T) {
	k1 := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	k2 := EdgeKey{SrcDevice: "sw-c", SrcPort: "Gi0/3", DstDevice: "sw-d", DstPort: "Gi0/4"}
	in := map[EdgeKey]int{k1: 3, k2: 1}
	got := EdgeKeysToAges(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got["sw-a|Gi0/1|sw-b|Gi0/2"] != 3 {
		t.Errorf("ages[sw-a|...] = %d, want 3", got["sw-a|Gi0/1|sw-b|Gi0/2"])
	}
	if got["sw-c|Gi0/3|sw-d|Gi0/4"] != 1 {
		t.Errorf("ages[sw-c|...] = %d, want 1", got["sw-c|Gi0/3|sw-d|Gi0/4"])
	}
}

// Reconcile: two sources naming different neighbours for the same local port
// produce a ConflictNeighbourDisagreement and both edges are preserved.
func TestReconcileConflictNeighbourDisagreement(t *testing.T) {
	lldpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "Gi0/1",
		DstDevice: "sw-02", DstPort: "Gi0/1",
		DiscoveryProto: "lldp", PrecedenceRank: 1,
	}
	cdpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "Gi0/1",
		DstDevice: "sw-03", DstPort: "Gi0/1",
		DiscoveryProto: "cdp", PrecedenceRank: 1,
	}

	edges, conflicts := Reconcile([]discovery.Edge{lldpEdge, cdpEdge})

	if len(edges) != 2 {
		t.Fatalf("expected 2 reconciled edges, got %d", len(edges))
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	c := conflicts[0]
	if c.Kind != ConflictNeighbourDisagreement {
		t.Errorf("conflict kind = %q, want %q", c.Kind, ConflictNeighbourDisagreement)
	}
	if c.SrcDevice != "sw-01" {
		t.Errorf("conflict SrcDevice = %q, want sw-01", c.SrcDevice)
	}
	if c.SrcPort != "Gi0/1" {
		t.Errorf("conflict SrcPort = %q, want Gi0/1", c.SrcPort)
	}
	if !slices.Contains(c.Sources, "lldp") {
		t.Errorf("Sources %v missing lldp", c.Sources)
	}
	if !slices.Contains(c.Sources, "cdp") {
		t.Errorf("Sources %v missing cdp", c.Sources)
	}
}

func TestReconcileConflictNeighbourDisagreementUsesReportedLocalPort(t *testing.T) {
	lldpEdge := discovery.Edge{
		SrcDevice: "zz-sw", SrcPort: "Gi0/1",
		DstDevice: "aa-neighbour", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", PrecedenceRank: 1,
	}
	cdpEdge := discovery.Edge{
		SrcDevice: "zz-sw", SrcPort: "Gi0/1",
		DstDevice: "bb-neighbour", DstPort: "Gi0/3",
		DiscoveryProto: "cdp", PrecedenceRank: 1,
	}

	_, conflicts := Reconcile([]discovery.Edge{lldpEdge, cdpEdge})

	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.Kind != ConflictNeighbourDisagreement {
		t.Fatalf("conflict kind = %q, want %q", c.Kind, ConflictNeighbourDisagreement)
	}
	if c.SrcDevice != "zz-sw" || c.SrcPort != "Gi0/1" {
		t.Fatalf("conflict local endpoint = %s/%s, want zz-sw/Gi0/1", c.SrcDevice, c.SrcPort)
	}
}

// Reconcile: two edges for the same device pair with different port-name
// encodings ("Gi0/1" vs "GigabitEthernet0/1", "Eth0/1" vs "Ethernet0/1")
// normalise to the same physical link — one collapsed edge, no false-positive
// PortNameMismatch conflict. A genuine PortNameMismatch (different dst ports
// after normalisation) is tested by TestReconcilePortNameMismatchFiresForDifferentPorts.
func TestReconcileConflictPortNameMismatch(t *testing.T) {
	lldpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "Gi0/1",
		DstDevice: "sw-02", DstPort: "Eth0/1",
		DiscoveryProto: "lldp", PrecedenceRank: 1,
	}
	cdpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "GigabitEthernet0/1",
		DstDevice: "sw-02", DstPort: "Ethernet0/1",
		DiscoveryProto: "cdp", PrecedenceRank: 1,
	}

	edges, conflicts := Reconcile([]discovery.Edge{lldpEdge, cdpEdge})

	// After normalisation, "Gi0/1"=="GigabitEthernet0/1" and "Eth0/1"=="Ethernet0/1"
	// so both observations collapse to a single edge. Both report from sw-01 so
	// the direction remains unidirectional — bidirectionality requires a reverse
	// observation from sw-02, which this test intentionally omits.
	if len(edges) != 1 {
		t.Fatalf("expected 1 reconciled edge (encoding variants collapse), got %d", len(edges))
	}
	for _, c := range conflicts {
		if c.Kind == ConflictPortNameMismatch {
			t.Errorf("unexpected PortNameMismatch for mere encoding difference: %+v", c)
		}
	}
}

// Reconcile: the same edge from two different protocols must not produce
// conflicts — they are collapsed normally with the higher-priority proto winning.
func TestReconcileNoConflictSamePort(t *testing.T) {
	lldpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "Gi0/1",
		DstDevice: "sw-02", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", PrecedenceRank: 1,
	}
	cdpEdge := discovery.Edge{
		SrcDevice: "sw-01", SrcPort: "Gi0/1",
		DstDevice: "sw-02", DstPort: "Gi0/2",
		DiscoveryProto: "cdp", PrecedenceRank: 2,
	}

	edges, conflicts := Reconcile([]discovery.Edge{lldpEdge, cdpEdge})

	if len(edges) != 1 {
		t.Fatalf("expected 1 reconciled edge, got %d", len(edges))
	}
	if edges[0].DiscoveryProto != "lldp" {
		t.Errorf("proto = %q, want lldp (lower rank wins)", edges[0].DiscoveryProto)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts for same-port same-neighbour edges, got %v", conflicts)
	}
}

func TestReconcileNilInput(t *testing.T) {
	edges, conflicts := Reconcile(nil)
	if edges != nil {
		t.Errorf("Reconcile(nil) edges = %v, want nil", edges)
	}
	if conflicts != nil {
		t.Errorf("Reconcile(nil) conflicts = %v, want nil", conflicts)
	}
}

func TestReconcileEmptyInput(t *testing.T) {
	edges, conflicts := Reconcile([]discovery.Edge{})
	if len(edges) != 0 {
		t.Errorf("Reconcile([]) edges = %v, want empty", edges)
	}
	if len(conflicts) != 0 {
		t.Errorf("Reconcile([]) conflicts = %v, want empty", conflicts)
	}
}

// ── Port name normalisation ───────────────────────────────────────────────

func TestNormalizePortName(t *testing.T) {
	cases := []struct{ in, want string }{
		// Cisco long → short
		{"GigabitEthernet0/1", "Gi0/1"},
		{"FastEthernet0/1", "Fa0/1"},
		{"TenGigabitEthernet1/0/1", "Te1/0/1"},
		{"TwoGigabitEthernet0/1", "Tw0/1"},
		{"HundredGigE0/0/0", "Hu0/0/0"},
		{"HundredGigabitEthernet0/0/0", "Hu0/0/0"},
		{"FortyGigabitEthernet0/0/0", "Fo0/0/0"},
		{"TwentyFiveGigE0/0/0", "Twe0/0/0"},
		{"Ethernet1/1", "Eth1/1"},
		{"Management0", "Mgmt0"},
		{"Port-channel1", "Po1"},
		{"Loopback0", "Lo0"},
		{"Tunnel1", "Tu1"},
		{"Vlan10", "Vl10"},
		// Already-short forms — idempotent
		{"Gi0/1", "Gi0/1"},
		{"Fa0/1", "Fa0/1"},
		{"Te1/0/1", "Te1/0/1"},
		{"Po1", "Po1"},
		// Case-insensitive prefix matching
		{"gigabitethernet0/1", "Gi0/1"},
		{"GIGABITETHERNET0/1", "Gi0/1"},
		// Junos-style — pass through unchanged
		{"ge-0/0/0", "ge-0/0/0"},
		{"xe-1/0/1", "xe-1/0/1"},
		{"et-0/0/0", "et-0/0/0"},
	}
	for _, tc := range cases {
		got := NormalizePortName(tc.in)
		if got != tc.want {
			t.Errorf("NormalizePortName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestReconcileCollapsesPortNameEncodings verifies that LLDP "Gi0/1" and CDP
// "GigabitEthernet0/1" are treated as the same physical port, producing one
// bidirectional edge with the winning observation's original port names.
func TestReconcileCollapsesPortNameEncodings(t *testing.T) {
	edges := []discovery.Edge{
		{
			SrcDevice: "sw-a", SrcPort: "Gi0/1",
			DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		{
			SrcDevice: "sw-b", SrcPort: "GigabitEthernet0/2",
			DstDevice: "sw-a", DstPort: "GigabitEthernet0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 2,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
	}
	result, conflicts := Reconcile(edges)
	if len(result) != 1 {
		t.Fatalf("edge count = %d, want 1 (encoding variants should collapse)", len(result))
	}
	if result[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", result[0].Direction)
	}
	// Winning observation is LLDP (rank 1 < rank 2); original port names preserved.
	if result[0].SrcPort != "Gi0/1" || result[0].DstPort != "Gi0/2" {
		t.Errorf("ports = (%s, %s), want (Gi0/1, Gi0/2)", result[0].SrcPort, result[0].DstPort)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none (encoding differences are not conflicts)", conflicts)
	}
}

// TestReconcilePortNameMismatchFiresForDifferentPorts — previously this
// verified that the devicePair check produced a ConflictPortNameMismatch when
// LLDP and CDP reported different dst ports for the same normalised src port.
// That check has been removed: it fired false positives for every LAG bond
// (parallel member links between the same device pair). Two observations from
// sw-a:Gi0/1 to sw-b on different dst ports still produce two distinct
// reconciled edges; neither PortNameMismatch nor NeighbourDisagreement fires
// because the neighbour device (sw-b) is identical in both observations.
func TestReconcilePortNameMismatchFiresForDifferentPorts(t *testing.T) {
	edges := []discovery.Edge{
		{
			SrcDevice: "sw-a", SrcPort: "Gi0/1",
			DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		{
			// Same src port (different encoding), different dst port.
			SrcDevice: "sw-a", SrcPort: "GigabitEthernet0/1",
			DstDevice: "sw-b", DstPort: "GigabitEthernet0/3",
			DiscoveryProto: "cdp", PrecedenceRank: 2,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
	}
	result, conflicts := Reconcile(edges)
	// Two distinct normalised EdgeKeys → two reconciled edges.
	if len(result) != 2 {
		t.Errorf("expected 2 reconciled edges (different dst ports = different links), got %d", len(result))
	}
	// No PortNameMismatch: the devicePair check has been removed to prevent
	// false positives on LAG parallel member links.
	for _, c := range conflicts {
		if c.Kind == ConflictPortNameMismatch {
			t.Errorf("unexpected ConflictPortNameMismatch after devicePair detection removal: %+v", c)
		}
	}
}

// TestReconcileMultiLinkLAGNoFalsePositive verifies that two parallel member
// links between the same device pair (a LAG bond) do not produce a false
// positive ConflictPortNameMismatch. Previously the devicePair check fired
// because Gi0/1 ≠ Gi0/2, polluting the network_topology_conflict_total counter
// in spine-leaf fabrics.
func TestReconcileMultiLinkLAGNoFalsePositive(t *testing.T) {
	edges := []discovery.Edge{
		// Member link 1 — seen from both sides.
		{
			SrcDevice: "sw-a", SrcPort: "Gi0/1",
			DstDevice: "sw-b", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		{
			SrcDevice: "sw-b", SrcPort: "Gi0/1",
			DstDevice: "sw-a", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		// Member link 2 — seen from both sides.
		{
			SrcDevice: "sw-a", SrcPort: "Gi0/2",
			DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		{
			SrcDevice: "sw-b", SrcPort: "Gi0/2",
			DstDevice: "sw-a", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
	}
	result, conflicts := Reconcile(edges)
	if len(result) != 2 {
		t.Fatalf("expected 2 reconciled edges for 2-member LAG, got %d: %v", len(result), result)
	}
	for _, e := range result {
		if e.Direction != discovery.DirectionBidirectional {
			t.Errorf("edge %s:%s↔%s:%s direction = %q, want bidirectional",
				e.SrcDevice, e.SrcPort, e.DstDevice, e.DstPort, e.Direction)
		}
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts for LAG parallel links, got %d: %v", len(conflicts), conflicts)
	}
}

// TestDiffNilInputs verifies that Diff(nil, nil) returns nil without panicking.
func TestDiffNilInputs(t *testing.T) {
	changes := Diff(nil, nil)
	if changes != nil {
		t.Errorf("Diff(nil, nil) = %v, want nil", changes)
	}
}

// TestDiffRemovedEdge verifies that Diff returns a change with Kind=Removed
// when the before set contains an edge not present in after.
func TestDiffRemovedEdge(t *testing.T) {
	before := discovery.Edge{
		SrcDevice: "a", SrcPort: "Gi0/1",
		DstDevice: "b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", Direction: discovery.DirectionBidirectional,
	}
	changes := Diff([]discovery.Edge{before}, nil)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if changes[0].Kind != ChangeRemoved {
		t.Errorf("Kind = %q, want %q", changes[0].Kind, ChangeRemoved)
	}
	if changes[0].Before == nil {
		t.Error("Before must be non-nil for a removed edge")
	}
}

// TestCompareEdgeKeyLess exercises compareEdgeKey via Key() sorting for the
// case where a.SrcDevice < b.SrcDevice — Key() must pick the lexically smaller
// device as SrcDevice.
func TestCompareEdgeKeyLess(t *testing.T) {
	// "aaa" < "zzz" so the canonical key should have aaa as SrcDevice.
	e := discovery.Edge{SrcDevice: "zzz", SrcPort: "Gi0/1", DstDevice: "aaa", DstPort: "Gi0/2"}
	k := Key(e)
	if k.SrcDevice != "aaa" {
		t.Errorf("SrcDevice = %q, want aaa (compareEdgeKey must return negative for a<b)", k.SrcDevice)
	}
}

// TestCompareEdgeKeyGreater exercises compareEdgeKey via Key() for the case
// where a.SrcDevice > b.SrcDevice — Key() picks the lexically smaller device.
func TestCompareEdgeKeyGreater(t *testing.T) {
	// "mmm" < "zzz" so the canonical key has mmm as SrcDevice.
	e := discovery.Edge{SrcDevice: "zzz", SrcPort: "Gi0/1", DstDevice: "mmm", DstPort: "Gi0/2"}
	k := Key(e)
	if k.SrcDevice != "mmm" {
		t.Errorf("SrcDevice = %q, want mmm (compareEdgeKey must return positive for a>b)", k.SrcDevice)
	}
}

// TestCompareEdgeKeyAllFieldsEqual verifies the equal case: two identical
// EdgeKeys must be considered the same edge (no swap needed in Key()).
func TestCompareEdgeKeyAllFieldsEqual(t *testing.T) {
	e := discovery.Edge{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-a", DstPort: "Gi0/1"}
	k := Key(e)
	if k.SrcDevice != "sw-a" || k.SrcPort != "Gi0/1" || k.DstDevice != "sw-a" || k.DstPort != "Gi0/1" {
		t.Errorf("Key for identical endpoints = %#v, expected no change", k)
	}
}

// TestCompareEdgeKeySrcPortDiffers exercises the SrcPort comparison branch in
// compareEdgeKey: same SrcDevice, a.SrcPort < b.SrcPort.
func TestCompareEdgeKeySrcPortDiffers(t *testing.T) {
	// Both have the same device pair. Key must pick the form where
	// the src port sorts first lexically.
	// Edge: sw-a:Gi0/1 → sw-a:Gi0/2 — device tie-breaks on port; Gi0/1 < Gi0/2.
	a := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-a", DstPort: "Gi0/2"}
	b := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/2", DstDevice: "sw-a", DstPort: "Gi0/1"}
	// Reconcile uses compareEdgeKey to pick canonical form; verify via normalizedGroupKey
	// indirectly by passing this through Reconcile with two observations.
	e := discovery.Edge{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-a", DstPort: "Gi0/2",
		DiscoveryProto: "lldp", PrecedenceRank: 1}
	k := Key(e)
	// Key must resolve to the form where a ≤ b (i.e., Gi0/1 appears as SrcPort).
	if compareEdgeKey(a, b) >= 0 {
		t.Errorf("compareEdgeKey(a, b) = %d; expected negative (Gi0/1 < Gi0/2)",
			compareEdgeKey(a, b))
	}
	_ = k
}

// TestCompareEdgeKeyDstDeviceDiffers exercises both DstDevice comparison branches:
// same SrcDevice + SrcPort, with a.DstDevice > b.DstDevice (positive) and
// a.DstDevice < b.DstDevice (negative).
func TestCompareEdgeKeyDstDeviceDiffers(t *testing.T) {
	// a.DstDevice "zzz" > b.DstDevice "aaa" → positive.
	a := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "zzz", DstPort: "Gi0/2"}
	b := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "aaa", DstPort: "Gi0/2"}
	if got := compareEdgeKey(a, b); got <= 0 {
		t.Errorf("compareEdgeKey(a, b) = %d; expected positive (zzz > aaa)", got)
	}
	// a.DstDevice "aaa" < b.DstDevice "zzz" → negative.
	c := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "aaa", DstPort: "Gi0/2"}
	d := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "zzz", DstPort: "Gi0/2"}
	if got := compareEdgeKey(c, d); got >= 0 {
		t.Errorf("compareEdgeKey(c, d) = %d; expected negative (aaa < zzz)", got)
	}
}

// TestCompareEdgeKeyDstPortDiffers exercises the DstPort comparison branches
// in compareEdgeKey: same SrcDevice, SrcPort, and DstDevice but differing DstPort.
func TestCompareEdgeKeyDstPortDiffers(t *testing.T) {
	// a.DstPort < b.DstPort → should return negative.
	a := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/1"}
	b := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	if got := compareEdgeKey(a, b); got >= 0 {
		t.Errorf("compareEdgeKey(a,b) = %d; expected negative (Gi0/1 < Gi0/2 for DstPort)", got)
	}
	// a.DstPort > b.DstPort → should return positive.
	c := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/9"}
	d := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	if got := compareEdgeKey(c, d); got <= 0 {
		t.Errorf("compareEdgeKey(c,d) = %d; expected positive (Gi0/9 > Gi0/2 for DstPort)", got)
	}
}

// TestCompareEdgeKeyDefault exercises the default return 0 case: all fields equal.
func TestCompareEdgeKeyDefault(t *testing.T) {
	k := EdgeKey{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2"}
	if got := compareEdgeKey(k, k); got != 0 {
		t.Errorf("compareEdgeKey(k, k) = %d; expected 0 for equal keys", got)
	}
}

// TestReconcileConflictSortOrder exercises the conflict-sorting path by
// generating multiple conflicts with different SrcDevice and SrcPort values,
// verifying they are returned in lexical order.
func TestReconcileConflictSortOrder(t *testing.T) {
	// Two conflicts: one on zzz-sw:Gi0/1, one on aaa-sw:Gi0/1.
	// After sorting, aaa-sw should come first.
	edges := []discovery.Edge{
		// Conflict on zzz-sw:Gi0/1 — lldp says sw-02, cdp says sw-03.
		{SrcDevice: "zzz-sw", SrcPort: "Gi0/1", DstDevice: "sw-02", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1},
		{SrcDevice: "zzz-sw", SrcPort: "Gi0/1", DstDevice: "sw-03", DstPort: "Gi0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 1},
		// Conflict on aaa-sw:Gi0/1 — lldp says sw-04, cdp says sw-05.
		{SrcDevice: "aaa-sw", SrcPort: "Gi0/1", DstDevice: "sw-04", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1},
		{SrcDevice: "aaa-sw", SrcPort: "Gi0/1", DstDevice: "sw-05", DstPort: "Gi0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 1},
	}
	_, conflicts := Reconcile(edges)
	if len(conflicts) < 2 {
		t.Fatalf("expected at least 2 conflicts, got %d", len(conflicts))
	}
	if conflicts[0].SrcDevice != "aaa-sw" {
		t.Errorf("first conflict SrcDevice = %q, want aaa-sw (sorted order)", conflicts[0].SrcDevice)
	}
	if conflicts[1].SrcDevice != "zzz-sw" {
		t.Errorf("second conflict SrcDevice = %q, want zzz-sw (sorted order)", conflicts[1].SrcDevice)
	}
}

// TestReconcileConflictSortOrderSameDevice exercises the SrcPort tie-break in
// conflict sorting: two conflicts on the same device but different ports.
func TestReconcileConflictSortOrderSameDevice(t *testing.T) {
	edges := []discovery.Edge{
		// Conflict on sw-a:Gi0/2 — lldp says sw-x, cdp says sw-y.
		{SrcDevice: "sw-a", SrcPort: "Gi0/2", DstDevice: "sw-x", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1},
		{SrcDevice: "sw-a", SrcPort: "Gi0/2", DstDevice: "sw-y", DstPort: "Gi0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 1},
		// Conflict on sw-a:Gi0/1 — lldp says sw-m, cdp says sw-n.
		{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-m", DstPort: "Gi0/1",
			DiscoveryProto: "lldp", PrecedenceRank: 1},
		{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-n", DstPort: "Gi0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 1},
	}
	_, conflicts := Reconcile(edges)
	if len(conflicts) < 2 {
		t.Fatalf("expected at least 2 conflicts, got %d", len(conflicts))
	}
	// Gi0/1 < Gi0/2, so Gi0/1 should be first.
	if conflicts[0].SrcPort != "Gi0/1" {
		t.Errorf("first conflict SrcPort = %q, want Gi0/1 (sorted by SrcPort)", conflicts[0].SrcPort)
	}
	if conflicts[1].SrcPort != "Gi0/2" {
		t.Errorf("second conflict SrcPort = %q, want Gi0/2 (sorted by SrcPort)", conflicts[1].SrcPort)
	}
}

// TestReconcileConflictSeenKeyDedup exercises the seenKey deduplication guard
// in the conflict-detection loop. We need the same group key to appear more
// than once in portNeighbours[portKey] while also having a different DstDevice
// observation to trigger conflict detection. Two observations that normalize
// to the same EdgeKey (same group) plus one observation with a different
// DstDevice achieves this.
func TestReconcileConflictSeenKeyDedup(t *testing.T) {
	// Two observations from sw-a:Gi0/1 → sw-b:Gi0/2 (same EdgeKey, rank 1)
	// and one from sw-a:Gi0/1 → sw-c:Gi0/1 (different EdgeKey, rank 1).
	// The same EdgeKey appears twice in portNeighbours[{sw-a,Gi0/1}], triggering
	// the seenKey[k] dedup branch.
	edges := []discovery.Edge{
		// Two observations mapping to the same group/EdgeKey (same src+dst pair, diff proto).
		{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1},
		{SrcDevice: "sw-a", SrcPort: "GigabitEthernet0/1", DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "cdp", PrecedenceRank: 1},
		// Third observation — different neighbor, triggers the conflict.
		{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-c", DstPort: "Gi0/1",
			DiscoveryProto: "ospf", PrecedenceRank: 1},
	}
	_, conflicts := Reconcile(edges)
	// There should be at least one NeighbourDisagreement conflict.
	found := false
	for _, c := range conflicts {
		if c.Kind == ConflictNeighbourDisagreement && c.SrcDevice == "sw-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ConflictNeighbourDisagreement for sw-a, got %v", conflicts)
	}
}

// TestReconcileNeighbourDisagreementAcrossEncodings verifies that
// NeighbourDisagreement fires when the same port (under different encodings)
// names different neighbor devices.
func TestReconcileNeighbourDisagreementAcrossEncodings(t *testing.T) {
	edges := []discovery.Edge{
		{
			SrcDevice: "sw-a", SrcPort: "Gi0/1",
			DstDevice: "sw-b", DstPort: "Gi0/2",
			DiscoveryProto: "lldp", PrecedenceRank: 1,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
		{
			// Same src port (long encoding), different neighbor — genuine disagreement.
			SrcDevice: "sw-a", SrcPort: "GigabitEthernet0/1",
			DstDevice: "sw-c", DstPort: "Gi0/1",
			DiscoveryProto: "cdp", PrecedenceRank: 2,
			Direction: discovery.DirectionUnidirectional, LinkKind: "ethernet",
		},
	}
	_, conflicts := Reconcile(edges)
	found := false
	for _, c := range conflicts {
		if c.Kind == ConflictNeighbourDisagreement {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ConflictNeighbourDisagreement for same port naming different neighbors, got %v", conflicts)
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────────

// makeEdges generates n raw (pre-reconciliation) edge observations with
// bidirectional coverage: for each logical link A→B we emit both the A-side
// and the B-side observation so that Reconcile has to detect bidirectionality.
//
// Device IDs are distributed across ~sqrt(n) devices using modular arithmetic
// so the result is a realistic mesh rather than a star topology. Port names
// cycle through GigabitEthernet0/0 … GigabitEthernet0/47.
func makeEdges(n int) []discovery.Edge {
	numDevices := int(math.Ceil(math.Sqrt(float64(n))))
	if numDevices < 2 {
		numDevices = 2
	}
	edges := make([]discovery.Edge, 0, n*2)
	for i := 0; i < n; i++ {
		srcIdx := i % numDevices
		dstIdx := (i + 1) % numDevices
		if srcIdx == dstIdx {
			dstIdx = (dstIdx + 1) % numDevices
		}
		srcDevice := fmt.Sprintf("sw-%03d", srcIdx)
		dstDevice := fmt.Sprintf("sw-%03d", dstIdx)
		srcPort := fmt.Sprintf("GigabitEthernet0/%d", i%48)
		dstPort := fmt.Sprintf("GigabitEthernet0/%d", (i+1)%48)

		// A-side observation.
		edges = append(edges, discovery.Edge{
			SrcDevice:      srcDevice,
			SrcPort:        srcPort,
			DstDevice:      dstDevice,
			DstPort:        dstPort,
			DiscoveryProto: "lldp",
			PrecedenceRank: 2,
		})
		// B-side observation — lets Reconcile detect bidirectionality.
		edges = append(edges, discovery.Edge{
			SrcDevice:      dstDevice,
			SrcPort:        dstPort,
			DstDevice:      srcDevice,
			DstPort:        srcPort,
			DiscoveryProto: "lldp",
			PrecedenceRank: 2,
		})
	}
	return edges
}

func BenchmarkReconcile100Edges(b *testing.B) {
	edges := makeEdges(100)
	b.ResetTimer()
	for b.Loop() {
		out, _ := Reconcile(edges)
		_ = out
	}
}

func BenchmarkReconcile1000Edges(b *testing.B) {
	edges := makeEdges(1000)
	b.ResetTimer()
	for b.Loop() {
		out, _ := Reconcile(edges)
		_ = out
	}
}

func BenchmarkReconcile10000Edges(b *testing.B) {
	edges := makeEdges(10000)
	b.ResetTimer()
	for b.Loop() {
		out, _ := Reconcile(edges)
		_ = out
	}
}

func BenchmarkDiff100Edges(b *testing.B) {
	raw := makeEdges(100)
	before, _ := Reconcile(raw)

	// Slightly modified copy: flip the direction of the first edge so Diff
	// has at least one ChangeUpdated to detect, and drop the last edge so
	// there is one ChangeRemoved.
	afterRaw := makeEdges(100)
	after, _ := Reconcile(afterRaw)
	if len(after) > 0 {
		after[0].Direction = discovery.DirectionUnidirectional
	}
	if len(after) > 1 {
		after = after[:len(after)-1]
	}

	b.ResetTimer()
	for b.Loop() {
		changes := Diff(before, after)
		_ = changes
	}
}

func BenchmarkDiff1000Edges(b *testing.B) {
	raw := makeEdges(1000)
	before, _ := Reconcile(raw)

	afterRaw := makeEdges(1000)
	after, _ := Reconcile(afterRaw)
	if len(after) > 0 {
		after[0].Direction = discovery.DirectionUnidirectional
	}
	if len(after) > 1 {
		after = after[:len(after)-1]
	}

	b.ResetTimer()
	for b.Loop() {
		changes := Diff(before, after)
		_ = changes
	}
}
