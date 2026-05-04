// Package graph reconciles per-cycle Device and Edge sets and produces a diff
// stream (added, removed, changed) consumed by the metrics and events layers.
//
// Implementation lands per the v1 plan; this stub keeps the import path live
// so cmd/topology-exporter can wire it in incrementally.
package graph

import "github.com/colinedwardwood/network-topology-exporter/internal/discovery"

// ChangeKind describes a topology mutation between two cycles.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeRemoved ChangeKind = "removed"
	ChangeUpdated ChangeKind = "updated"
)

// EdgeChange is one entry in a per-cycle diff.
type EdgeChange struct {
	Kind   ChangeKind
	Before *discovery.Edge
	After  *discovery.Edge
}

// Diff returns the changes between two edge sets. Stub for v0.1.
func Diff(_, _ []discovery.Edge) []EdgeChange {
	return nil
}
