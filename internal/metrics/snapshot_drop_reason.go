package metrics

// SnapshotDropReason is the closed-enum label value for
// SnapshotDropsTotal's `reason` field. Each value describes a distinct
// layer at which a snapshot was lost; the two values together let an
// operator distinguish "upstream cycle is outpacing the writer"
// (queue_full) from "writer goroutine itself is stalled"
// (write_in_flight). Both ultimately surface the same operational
// condition (storage stalled).
type SnapshotDropReason string

// String returns the underlying wire value, satisfying fmt.Stringer.
func (r SnapshotDropReason) String() string { return string(r) }

// Valid reports whether r is one of the declared SnapshotDropReason
// constants. Used by tests to pin the closed-enum contract.
func (r SnapshotDropReason) Valid() bool {
	switch r {
	case SnapshotDropReasonQueueFull,
		SnapshotDropReasonWriteInFlight:
		return true
	}
	return false
}

// SnapshotDropReason values.
const (
	// SnapshotDropReasonQueueFull means the caller could not enqueue a new
	// snapshot because the bounded snapshot channel was full — the writer
	// goroutine is still working on (or stuck on) the previous one.
	SnapshotDropReasonQueueFull SnapshotDropReason = "queue_full"

	// SnapshotDropReasonWriteInFlight means the writer goroutine pulled a
	// new snapshot off the channel but found its previous write still
	// in-flight (from an earlier timed-out write goroutine). The new
	// snapshot is dropped rather than spawning a second concurrent writer.
	SnapshotDropReasonWriteInFlight SnapshotDropReason = "write_in_flight"
)
