package events

import (
	"context"
	"log/slog"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
)

// captureHandler records every slog.Record for inspection.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func newTestLogger() (*Logger, *captureHandler) {
	h := &captureHandler{}
	return New(slog.New(h)), h
}

func makeEdge(src, dst string) *discovery.Edge {
	return &discovery.Edge{
		SrcDevice: src, SrcPort: "eth0",
		DstDevice: dst, DstPort: "eth1",
		DiscoveryProto: "lldp",
		Direction:      discovery.DirectionUnidirectional,
	}
}

// Emit: ChangeAdded logs at Info level.
func TestEmitAdded(t *testing.T) {
	l, h := newTestLogger()

	l.Emit(context.Background(), []graph.EdgeChange{
		{Kind: graph.ChangeAdded, After: makeEdge("a", "b")},
	})

	if len(h.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.records))
	}
	if h.records[0].Level != slog.LevelInfo {
		t.Errorf("Level = %v, want Info", h.records[0].Level)
	}
}

// Emit: ChangeRemoved logs at Warn level.
func TestEmitRemoved(t *testing.T) {
	l, h := newTestLogger()

	l.Emit(context.Background(), []graph.EdgeChange{
		{Kind: graph.ChangeRemoved, Before: makeEdge("a", "b")},
	})

	if len(h.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.records))
	}
	if h.records[0].Level != slog.LevelWarn {
		t.Errorf("Level = %v, want Warn", h.records[0].Level)
	}
}

// Emit: ChangeUpdated includes both before and after fields.
func TestEmitUpdated(t *testing.T) {
	l, h := newTestLogger()

	l.Emit(context.Background(), []graph.EdgeChange{
		{Kind: graph.ChangeUpdated, Before: makeEdge("a", "b"), After: makeEdge("a", "b")},
	})

	if len(h.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.records))
	}

	hasBefore, hasAfter := false, false
	h.records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "before_src_device" {
			hasBefore = true
		}
		if a.Key == "after_src_device" {
			hasAfter = true
		}
		return true
	})
	if !hasBefore || !hasAfter {
		t.Error("expected before_src_device and after_src_device attrs in log record")
	}
}

// Emit: empty changes list emits nothing.
func TestEmitEmpty(t *testing.T) {
	l, h := newTestLogger()
	l.Emit(context.Background(), nil)
	if len(h.records) != 0 {
		t.Errorf("expected 0 records for empty changes, got %d", len(h.records))
	}
}

// EmitConflicts: one conflict produces one Warn log line with expected fields.
func TestEmitConflicts(t *testing.T) {
	l, h := newTestLogger()

	c := graph.Conflict{
		Kind:      graph.ConflictNeighbourDisagreement,
		SrcDevice: "sw-01",
		SrcPort:   "Gi0/1",
		Sources:   []string{"lldp", "cdp"},
		Edges: []discovery.Edge{
			{
				SrcDevice: "sw-01", SrcPort: "Gi0/1",
				DstDevice: "sw-02", DstPort: "Gi0/2",
				DiscoveryProto: "lldp",
			},
		},
	}
	l.EmitConflicts(context.Background(), []graph.Conflict{c})

	if len(h.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(h.records))
	}
	if h.records[0].Level != slog.LevelWarn {
		t.Errorf("Level = %v, want Warn", h.records[0].Level)
	}
	hasDevice := false
	hasEdges := false
	h.records[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "src_device" {
			hasDevice = true
		}
		if a.Key == "edges" {
			hasEdges = true
		}
		return true
	})
	if !hasDevice {
		t.Error("expected src_device attr in conflict log record")
	}
	if !hasEdges {
		t.Error("expected edges attr in conflict log record")
	}
}

// EmitConflicts: empty slice emits nothing.
func TestEmitConflictsEmpty(t *testing.T) {
	l, h := newTestLogger()
	l.EmitConflicts(context.Background(), nil)
	if len(h.records) != 0 {
		t.Errorf("expected 0 records for empty conflicts, got %d", len(h.records))
	}
}
