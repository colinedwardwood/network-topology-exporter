package graph

import (
	"reflect"
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
