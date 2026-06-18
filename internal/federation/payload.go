// Package federation implements LD-15–LD-20 multi-instance coordination.
// Three modes beyond standalone are supported: uncoordinated (boundary metrics
// only), spoke (push to hub after each cycle), and hub (pure aggregator).
package federation

import (
	"time"

	"github.com/grafana/network-topology-exporter/internal/discovery"
)

// SpokePayload is the wire type pushed from a spoke to the hub after each
// discovery cycle. Devices and Edges are pre-reconciled by the spoke per
// LD-17; OutOfScope carries the boundary observations the hub needs to detect
// cross-domain bidirectionality via name matching.
//
// The LD-14 unconfirmed-link counters are deliberately NOT part of the wire
// type: age lifecycle runs inside each spoke's own discovery cycle, and the
// counters survive spoke restarts via the spoke's local snapshot
// (snapshot.File.UnconfirmedAges) — the hub aggregates pre-reconciled graphs
// and has no age-merge step. An `ages` field was carried here historically but
// was never read by the hub; it was removed rather than left as a misleading
// half-implemented contract. Hubs ignore unknown JSON fields, so payloads from
// older spokes that still send `ages` remain accepted.
type SpokePayload struct {
	SpokeID    string                          `json:"spoke_id"`
	CycleAt    time.Time                       `json:"cycle_at"`
	Devices    []discovery.Device              `json:"devices"`
	Edges      []discovery.Edge                `json:"edges"`
	OutOfScope []discovery.OutOfScopeNeighbour `json:"out_of_scope"`
}
