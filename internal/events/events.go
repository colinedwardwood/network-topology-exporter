// Package events pushes topology change events to Loki via the standard
// /loki/api/v1/push API. There is no bespoke event channel — the consumer
// queries Loki with their existing tooling.
//
// Implementation lands per the v1 plan; this stub fixes the public surface.
package events

import (
	"context"

	"github.com/owner-tbd/network-topology-exporter/internal/graph"
)

// Pusher pushes graph diffs to Loki. Construction takes the configured Loki
// URL and label set from internal/config.
type Pusher struct{}

// Push emits one log line per change. Stub.
func (p *Pusher) Push(_ context.Context, _ []graph.EdgeChange) error {
	return nil
}
