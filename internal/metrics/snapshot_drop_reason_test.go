package metrics

import "testing"

// TestSnapshotDropReasonWireValuesPinned locks the underlying string for every
// SnapshotDropReason constant to its on-wire value. These strings appear as
// the `reason` label on network_topology_snapshot_drops_total; alert rules
// and dashboards branch on them. Failures of this test should be paired
// with a CHANGELOG entry and a docs update, not silently fixed by updating
// `want`.
func TestSnapshotDropReasonWireValuesPinned(t *testing.T) {
	cases := []struct {
		r    SnapshotDropReason
		want string
	}{
		{SnapshotDropReasonQueueFull, "queue_full"},
		{SnapshotDropReasonWriteInFlight, "write_in_flight"},
	}
	for _, c := range cases {
		if string(c.r) != c.want {
			t.Errorf("SnapshotDropReason(%s) = %q, want %q (wire-format contract)",
				c.want, string(c.r), c.want)
		}
		if !c.r.Valid() {
			t.Errorf("SnapshotDropReason(%q).Valid() = false, want true", string(c.r))
		}
	}
}

// TestSnapshotDropReasonValidRejectsUnknown ensures Valid() returns false for
// strings that look like reasons but are not declared constants.
func TestSnapshotDropReasonValidRejectsUnknown(t *testing.T) {
	for _, s := range []string{
		"",
		"queue-full",      // hyphens, not underscores
		"WRITE_IN_FLIGHT", // wrong case
		"timeout",         // we deliberately do NOT count timeouts as drops
		"unknown",
	} {
		if SnapshotDropReason(s).Valid() {
			t.Errorf("SnapshotDropReason(%q).Valid() = true, want false", s)
		}
	}
}
