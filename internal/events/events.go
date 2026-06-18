// Package events writes topology change records as structured log lines.
//
// Change events are emitted to the process logger (slog, JSON to stderr) so
// operators can ship them to any log aggregator — Loki, Elasticsearch, Splunk —
// using the collector agent already present in their stack. There is no
// bespoke HTTP push here: that coupling belongs in the shipping agent, not in
// the exporter.
//
// The counter (network_topology_change_total) is what alerts fire on.
// The log line carries the full before/after edge record so the operator can
// answer "which edge changed?" without joining metric series.
package events

import (
	"context"
	"log/slog"

	"github.com/grafana/network-topology-exporter/internal/graph"
)

// Logger writes EdgeChange records to a slog.Logger.
type Logger struct {
	log *slog.Logger
}

// New returns a Logger that writes change events to l.
func New(l *slog.Logger) *Logger {
	return &Logger{log: l}
}

// EmitConflicts writes one structured log line per Conflict at Warn level.
// Conflicts indicate source disagreements and warrant operator attention, but
// the exporter can still proceed — hence Warn rather than Error.
func (l *Logger) EmitConflicts(ctx context.Context, conflicts []graph.Conflict) {
	for _, c := range conflicts {
		l.log.Log(ctx, slog.LevelWarn, "topology conflict",
			"conflict_type", c.Kind,
			"src_device", c.SrcDevice,
			"src_port", c.SrcPort,
			"sources", c.Sources,
			"edge_count", len(c.Edges),
			"edges", c.Edges,
		)
	}
}

// Emit writes one structured log line per EdgeChange. The log level is Info
// for added/updated edges and Warn for removed edges so removal events stand
// out in operator dashboards without requiring a separate alert channel.
func (l *Logger) Emit(ctx context.Context, changes []graph.EdgeChange) {
	for _, c := range changes {
		lvl := slog.LevelInfo
		if c.Kind == graph.ChangeRemoved {
			lvl = slog.LevelWarn
		}

		args := []any{
			"change_kind", c.Kind,
		}
		if c.Before != nil {
			args = append(args,
				"before_src_device", c.Before.SrcDevice,
				"before_src_port", c.Before.SrcPort,
				"before_dst_device", c.Before.DstDevice,
				"before_dst_port", c.Before.DstPort,
				"before_proto", c.Before.DiscoveryProto,
				"before_direction", c.Before.Direction,
			)
		}
		if c.After != nil {
			args = append(args,
				"after_src_device", c.After.SrcDevice,
				"after_src_port", c.After.SrcPort,
				"after_dst_device", c.After.DstDevice,
				"after_dst_port", c.After.DstPort,
				"after_proto", c.After.DiscoveryProto,
				"after_direction", c.After.Direction,
			)
		}
		l.log.Log(ctx, lvl, "topology change", args...)
	}
}
