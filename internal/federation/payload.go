// Package federation implements LD-15–LD-20 multi-instance coordination.
// Three modes beyond standalone are supported: uncoordinated (boundary metrics
// only), spoke (push to hub after each cycle), and hub (pure aggregator).
package federation

import (
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// SpokePayload is the wire type pushed from a spoke to the hub after each
// discovery cycle. Devices and Edges are pre-reconciled by the spoke per
// LD-17; OutOfScope carries the boundary observations the hub needs to detect
// cross-domain bidirectionality via name matching. Ages carries the LD-14
// unconfirmed-link counters so they survive spoke restarts.
type SpokePayload struct {
	SpokeID    string                          `json:"spoke_id"`
	CycleAt    time.Time                       `json:"cycle_at"`
	Devices    []discovery.Device              `json:"devices"`
	Edges      []discovery.Edge                `json:"edges"`
	OutOfScope []discovery.OutOfScopeNeighbour `json:"out_of_scope"`
	Ages       map[string]int                  `json:"ages"` // EdgeKeyString → consecutive unconfirmed cycles (LD-14)
}
