// Package graph implements the LD-10 reconciliation and diff logic.
//
// LD-10 (network-o11y-dev/docs/ARCHITECTURE.md §LD-10) defines a precedence
// ladder over discovery sources and a "surface, don't resolve" conflict
// model. This package is the seam where that policy is enforced:
//
//   - Reconcile takes the per-cycle Edge slice produced by every discovery
//     module concatenated together and returns the same edges with their
//     PrecedenceRank, Confidence, and Direction fields populated and any
//     duplicates collapsed by precedence.
//
//   - Diff compares two reconciled cycles and emits EdgeChange records for
//     the metrics layer (TopologyChangeTotal counter + Loki push events).
//
//   - Conflicts records same-cycle disagreements (two different sources
//     report a different DstDevice/DstPort for the same SrcDevice:SrcPort)
//     so the metrics layer can emit TopologyConflictTotal. Conflicts are
//     surfaced, never silently resolved.
//
// Implementation lands per the v1 plan; this stub keeps the import path
// live and the type schema fixed so cmd/topology-exporter can wire it in
// incrementally.
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

// ConflictKind classifies the disagreement type. Mirrors the
// `conflict_type` label on TopologyConflictTotal per LD-10.
type ConflictKind string

const (
	// ConflictPortNameMismatch — sources agree on the neighbour device but
	// disagree on the port-name encoding (e.g. CDP says "Eth1/1", LLDP says
	// "Ethernet1/1").
	ConflictPortNameMismatch ConflictKind = "port_name_mismatch"

	// ConflictNeighbourDisagreement — sources name different neighbour
	// devices for the same local port. Most serious case.
	ConflictNeighbourDisagreement ConflictKind = "neighbour_disagreement"

	// ConflictDirectionAsymmetry — A's LLDP names B as a neighbour but B's
	// LLDP doesn't name A. Common with mixed-vendor environments and
	// occasional vendor LLDP bugs.
	ConflictDirectionAsymmetry ConflictKind = "direction_asymmetry"

	// ConflictDocumentedVsObserved — manual / NetBox topology disagrees with
	// observed discovery. Highest-value signal for keeping documentation
	// honest.
	ConflictDocumentedVsObserved ConflictKind = "documented_vs_observed"
)

// Conflict records one disagreement between sources within a single cycle.
type Conflict struct {
	SrcDevice string
	SrcPort   string
	Kind      ConflictKind
	Sources   []string         // discovery_proto values that disagreed (e.g. ["lldp", "cdp"])
	Edges     []discovery.Edge // the conflicting edge observations, kept for the Loki event payload
}

// Reconcile collapses per-cycle observations from multiple discovery sources
// into a single ranked edge set per LD-10. It returns the ranked edges, plus
// the conflicts surfaced for the TopologyConflictTotal counter. Stub for
// v0.1; the precedence ladder lives in code, not config, because changing
// the ladder is a breaking change to the metric schema.
func Reconcile(_ []discovery.Edge) (edges []discovery.Edge, conflicts []Conflict) {
	return nil, nil
}

// Diff returns the changes between two reconciled edge sets. Stub for v0.1.
func Diff(_, _ []discovery.Edge) []EdgeChange {
	return nil
}
