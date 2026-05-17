package metrics

import "testing"

// TestRejectReasonWireValuesPinned locks the underlying string for every
// RejectReason constant to its on-wire value. These strings appear in:
//
//   - The `reason` field of the federation pushRejection JSON response
//     (docs/operator/federation.md "Spoke push response contract").
//   - The `reason` label on the
//     network_topology_graph_updates_rejected_total Prometheus counter.
//
// Changing any value here is a breaking observability change: operator
// dashboards, alert rules, and spoke retry policies branch on these strings.
// Failures of this test should be paired with a CHANGELOG entry and a docs
// update, not silently fixed by updating `want`.
func TestRejectReasonWireValuesPinned(t *testing.T) {
	cases := []struct {
		r    RejectReason
		want string
	}{
		{RejectReasonStaleGeneration, "stale_generation"},
		{RejectReasonSizeBudgetExceeded, "size_budget_exceeded"},
		{RejectReasonInvalidLabelKey, "invalid_label_key"},
		{RejectReasonInvalidLabelValue, "invalid_label_value"},
		{RejectReasonStructuralInvalid, "structural_invalid"},
	}
	for _, c := range cases {
		if string(c.r) != c.want {
			t.Errorf("RejectReason(%s) = %q, want %q (wire-format contract: see docs/operator/federation.md)",
				c.want, string(c.r), c.want)
		}
		if !c.r.Valid() {
			t.Errorf("RejectReason(%q).Valid() = false, want true", string(c.r))
		}
	}
}

// TestRejectReasonValidRejectsUnknown ensures Valid() returns false for
// strings that look like reject reasons but are not declared constants —
// the defense-in-depth check at metric emission sites must catch typos.
func TestRejectReasonValidRejectsUnknown(t *testing.T) {
	for _, s := range []string{
		"",
		"size_budet_exceeded", // typo of size_budget_exceeded
		"invalid_label",
		"unknown",
		"STALE_GENERATION", // wrong case
	} {
		if RejectReason(s).Valid() {
			t.Errorf("RejectReason(%q).Valid() = true, want false", s)
		}
	}
}
