package metrics

// RejectReason names a wire-level spoke-push reject reason. Values are
// surfaced both as the `reason` field in the federation pushRejection JSON
// response (docs/operator/federation.md) and as the `reason` label on the
// network_topology_graph_updates_rejected_total counter. The set is closed —
// only the constants declared below are valid label values.
//
// This type lives in the metrics package (rather than internal/federation)
// because internal/federation already imports internal/metrics; locating it
// here lets both packages reference the same typed constants without a new
// package or an import cycle. Local-mode admission control in
// cmd/topology-exporter also reuses these values for the same counter.
//
// Wire format guarantee: the underlying string for each constant is the
// stable label/JSON value. Renaming a constant is a Go-only change; changing
// its string literal is a breaking observability change and must be paired
// with a CHANGELOG note and an update to docs/operator/federation.md.
type RejectReason string

// String implements fmt.Stringer so RejectReason values format identically
// to their underlying wire string in log lines and %v formatting.
func (r RejectReason) String() string { return string(r) }

// Valid reports whether r is one of the declared RejectReason constants.
// Use in defense-in-depth checks at the metric emission site so a future
// caller cannot smuggle in an unregistered series via an untyped string
// conversion.
func (r RejectReason) Valid() bool {
	switch r {
	case RejectReasonStaleGeneration,
		RejectReasonSizeBudgetExceeded,
		RejectReasonInvalidLabelKey,
		RejectReasonInvalidLabelValue,
		RejectReasonStructuralInvalid:
		return true
	}
	return false
}

// The reject-reason enum. Two flavours share this namespace:
//
//   - Post-transport-accept rejects emitted by the hub's tryPublishMetrics
//     (stale_generation, size_budget_exceeded) — the payload was
//     syntactically valid but the resulting combined graph could not be
//     applied.
//   - Pre-publish validation rejects emitted by the hub's
//     validateSpokePayload (invalid_label_key, invalid_label_value,
//     structural_invalid) — the payload contained data that would corrupt
//     /metrics line protocol if accepted, or violated structural
//     invariants. These are the hub's defense against a spoke (legitimate
//     or compromised) injecting newlines/quotes/reserved names that mTLS
//     cannot prevent.
//
// New values are added only in a release that ships emission code + tests;
// see docs/operator/federation.md "Spoke push response contract".
const (
	RejectReasonStaleGeneration    RejectReason = "stale_generation"
	RejectReasonSizeBudgetExceeded RejectReason = "size_budget_exceeded"
	RejectReasonInvalidLabelKey    RejectReason = "invalid_label_key"
	RejectReasonInvalidLabelValue  RejectReason = "invalid_label_value"
	// RejectReasonStructuralInvalid covers semantic-validation failures that
	// are not specifically about Prometheus label key/value safety: empty
	// required identifiers (device_id, edge endpoints), duplicate device IDs,
	// self-edges, oversize fields beyond the per-field byte cap, and invalid
	// UTF-8 in fields that are not themselves label values. Before issue #19
	// these paths returned plain fmt.Errorf and were mislabeled as
	// invalid_label_value by the handlePush fallthrough; the dedicated reason
	// gives operators accurate signals and lets dashboards distinguish
	// structural shape errors (almost always a buggy spoke build) from
	// label-injection attempts (potentially a compromised spoke).
	RejectReasonStructuralInvalid RejectReason = "structural_invalid"
)
