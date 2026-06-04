package fdb

import (
	"context"
	"errors"
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"
)

// errInjected is the canned failure used by the sub-walk seams to simulate a
// BulkWalk error that the in-process SNMP test agent cannot otherwise produce
// (it returns empty results, never transport errors).
var errInjected = errors.New("injected sub-walk failure")

// TestDegradedQBridgeWalkFailedEmptyBMIB: a Q-BRIDGE walk error on a device
// whose B-MIB walk produced NO usable entries means the device depended on
// Q-BRIDGE — emit discovery_degraded_total{module="fdb",
// reason="qbridge_walk_failed"} exactly once.
func TestDegradedQBridgeWalkFailedEmptyBMIB(t *testing.T) {
	fake := &fakeWalkerMetrics{}
	// Only sysDescr — the B-MIB FDB walk yields zero entries.
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("q-bridge-dependent")},
	}
	p := fdbOutcomeParams(t, pdus, fake)

	orig := walkQBridgeFdbTableFn
	t.Cleanup(func() { walkQBridgeFdbTableFn = orig })
	walkQBridgeFdbTableFn = func(_ context.Context, _ *gsnmp.GoSNMP, _ map[string]*fdbEntry) error {
		return errInjected
	}

	if _, _, err := Walk(context.Background(), p, "sw-01", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := fake.countDegraded(walkerFDB, reasonQBridgeWalkFailed); got != 1 {
		t.Errorf("degraded qbridge_walk_failed = %d, want 1", got)
	}
}

// TestDegradedQBridgeWalkFailedBMIBHadEntries is the false-alert guard: a
// Q-BRIDGE walk error on a device whose B-MIB ALREADY produced usable entries
// is benign (the topology came from B-MIB) and must NOT increment the degraded
// counter. This is the most important test of issue #100.
func TestDegradedQBridgeWalkFailedBMIBHadEntries(t *testing.T) {
	fake := &fakeWalkerMetrics{}
	// buildFdbAgentPDUs populates one learned B-MIB entry → the map is
	// non-empty before the Q-BRIDGE walk runs.
	p := fdbOutcomeParams(t, buildFdbAgentPDUs(), fake)

	orig := walkQBridgeFdbTableFn
	t.Cleanup(func() { walkQBridgeFdbTableFn = orig })
	walkQBridgeFdbTableFn = func(_ context.Context, _ *gsnmp.GoSNMP, _ map[string]*fdbEntry) error {
		return errInjected
	}

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from B-MIB, got %d", len(edges))
	}
	if got := fake.countDegraded(walkerFDB, reasonQBridgeWalkFailed); got != 0 {
		t.Errorf("degraded qbridge_walk_failed = %d, want 0 (B-MIB already had entries — false-alert guard)", got)
	}
}

// TestDegradedBMIBOnlyDeviceQuiet: a legitimate B-MIB-only device whose
// Q-BRIDGE walk returns cleanly (no rows, no error) must stay completely
// quiet — no degraded increment.
func TestDegradedBMIBOnlyDeviceQuiet(t *testing.T) {
	fake := &fakeWalkerMetrics{}
	// buildFdbAgentPDUs has B-MIB rows but no Q-BRIDGE rows; the real
	// Q-BRIDGE walk returns cleanly with zero rows.
	p := fdbOutcomeParams(t, buildFdbAgentPDUs(), fake)

	if _, _, err := Walk(context.Background(), p, "sw-01", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := fake.countDegraded(walkerFDB, reasonQBridgeWalkFailed); got != 0 {
		t.Errorf("degraded qbridge_walk_failed = %d, want 0 (clean B-MIB-only device)", got)
	}
	if got := fake.countDegraded(walkerFDB, reasonVLANWalkFailed); got != 0 {
		t.Errorf("degraded vlan_walk_failed = %d, want 0 (clean B-MIB-only device)", got)
	}
}

// TestDegradedVLANWalkFailed: a per-VLAN community walk that fails increments
// discovery_degraded_total{module="fdb", reason="vlan_walk_failed"}. One VLAN
// is discovered via dot1qVlanCurrentTable; the per-VLAN FDB walk is forced to
// error via the walkFdbTableInto seam.
func TestDegradedVLANWalkFailed(t *testing.T) {
	fake := &fakeWalkerMetrics{}
	// One active VLAN (id 10) in dot1qVlanCurrentTable so discoverVlanIDs
	// returns a non-empty list and the per-VLAN walk is attempted.
	vlanCurrentBase := ".1.3.6.1.2.1.17.7.1.4.2.1."
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("ios-vlan-device")},
		// dot1qVlanCurrentEgressPorts: col 4, timeMark 0, vlanId 10.
		{Name: vlanCurrentBase + "4.0.10", Type: gsnmp.OctetString, Value: []byte{0x80}},
	}
	p := fdbOutcomeParams(t, pdus, fake)

	// The base B-MIB walk uses walkFdbTableInto directly (not the seam), so
	// overriding the seam fails ONLY the per-VLAN session walk.
	orig := walkFdbTableIntoFn
	t.Cleanup(func() { walkFdbTableIntoFn = orig })
	walkFdbTableIntoFn = func(_ context.Context, _ *gsnmp.GoSNMP, _ map[string]*fdbEntry) (bool, error) {
		return false, errInjected
	}

	if _, _, err := Walk(context.Background(), p, "sw-01", nil); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got := fake.countDegraded(walkerFDB, reasonVLANWalkFailed); got != 1 {
		t.Errorf("degraded vlan_walk_failed = %d, want 1", got)
	}
}
