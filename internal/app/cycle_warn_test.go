package app

import "testing"

// TestMaybeWarnLargeTopologyUpwardCrossing fires the warning only on the upward
// crossing of the edge threshold, honours the cooldown, and threads the
// returned state correctly across cycles.
func TestMaybeWarnLargeTopologyUpwardCrossing(t *testing.T) {
	log := slogDiscard()
	above := LargeTopologyEdgeThreshold + 1
	below := LargeTopologyEdgeThreshold

	// Below threshold: no warning, state stays below.
	nowAbove, last := MaybeWarnLargeTopology(log, below, 10, false, 1, -LargeTopologyWarnCooldownCycles)
	if nowAbove {
		t.Error("below threshold should report nowAbove=false")
	}
	if last != -LargeTopologyWarnCooldownCycles {
		t.Errorf("lastWarnCycle = %d, want unchanged", last)
	}

	// Upward crossing past cooldown: warns, lastWarnCycle advances to cycle 5.
	nowAbove, last = MaybeWarnLargeTopology(log, above, 10, false, 5, -LargeTopologyWarnCooldownCycles)
	if !nowAbove {
		t.Error("crossing should report nowAbove=true")
	}
	if last != 5 {
		t.Errorf("lastWarnCycle = %d, want 5 (warned this cycle)", last)
	}

	// Already above (prevAbove true): no re-warn, lastWarnCycle unchanged.
	_, last2 := MaybeWarnLargeTopology(log, above, 10, true, 6, 5)
	if last2 != 5 {
		t.Errorf("lastWarnCycle = %d, want 5 (no re-warn while still above)", last2)
	}

	// Upward crossing but within cooldown window: suppressed.
	_, last3 := MaybeWarnLargeTopology(log, above, 10, false, 10, 5)
	if last3 != 5 {
		t.Errorf("lastWarnCycle = %d, want 5 (suppressed by cooldown)", last3)
	}
}
