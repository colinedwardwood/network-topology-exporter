// Package graph implements the LD-10 reconciliation and diff logic. It's
// the seam where the precedence-ladder policy from
// network-o11y-dev/docs/ARCHITECTURE.md §LD-10 actually executes.
//
// Reconcile takes the per-cycle Edge slice — every discovery module's
// output concatenated together — and returns the same edges with their
// PrecedenceRank, Confidence, and Direction populated, duplicates collapsed
// by rank. Diff compares two reconciled cycles and produces EdgeChange
// records for the change counter and the structured log event stream.
// Conflict records same-cycle disagreements (two sources naming different
// neighbours for the same local port, for example) — surfaced, never
// silently resolved.
//
// Pure functions, no I/O.
//
// # Design references
//
//   - Bejerano, Breitbart, Garofalakis, Rastogi — "Physical Topology
//     Discovery for Large Multisubnet Networks", IEEE INFOCOM 2003. The
//     foundational paper for handling conflicting edge observations from
//     multiple sources. The insight that L2 protocols (LLDP, FDB) should
//     supersede L3 inference (BGP, OSPF) for the same physical link underpins
//     the LD-10 precedence ladder.
//     https://ieeexplore.ieee.org/document/1208686
//   - Breitbart et al. — "The NetInventory System", IEEE/ACM ToN 2004.
//     System paper for the above; documents how conflicting sources are
//     ranked and how disagreements (ConflictNeighbourDisagreement) are
//     surfaced rather than silently resolved. The conflict types modelled
//     here (port_name_mismatch, direction_asymmetry, etc.) map directly to
//     the disagreement categories described in that paper.
//     https://dl.acm.org/doi/abs/10.1109/TNET.2004.828963
//   - prometheus/snmp_exporter (Apache 2.0) — confirms that port name
//     encoding differences between LLDP and CDP (the LldpPortId subtype
//     issue in lldp/lldp.go) are a known real-world source of
//     ConflictPortNameMismatch conflicts that require normalisation before
//     reconciliation. Source: collector/collector.go combinedTypeMapping.
package graph

import (
	"fmt"
	"slices"
	"strings"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

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
	Edges     []discovery.Edge // the conflicting edge observations, kept for the log payload
}

// Reconcile collapses per-cycle observations from multiple discovery sources
// into a single ranked edge set.
//
// Algorithm:
//  1. Group edges by their canonical EdgeKey (endpoint-sorted so A→B and B→A
//     land in the same bucket).
//  2. Within each bucket, detect bidirectionality: if both endpoint devices
//     appear as the SrcDevice of at least one observation, the link is
//     DirectionBidirectional. Otherwise DirectionUnidirectional.
//  3. Select the edge with the lowest PrecedenceRank (1=highest priority).
//     When multiple edges tie at the winning rank, prefer the one from the
//     canonical (alphabetically-first) side so output is deterministic.
//  4. Normalise the chosen edge's SrcDevice/SrcPort/DstDevice/DstPort to the
//     canonical order so callers and the snapshot see consistent identifiers.
//  5. Sort the result by EdgeKey so output order is deterministic across calls.
//
// Conflict surfacing (v0.2+): NeighbourDisagreement and PortNameMismatch are
// detected between different EdgeKey groups that share the same
// (SrcDevice, SrcPort). Returning an empty conflicts slice is valid for v0.1.
func Reconcile(edges []discovery.Edge) ([]discovery.Edge, []Conflict) {
	if len(edges) == 0 {
		return nil, nil
	}

	type group struct {
		byRank  map[int][]discovery.Edge
		minRank int
		sides   map[string]bool // SrcDevice values that reported this link
	}

	groups := make(map[EdgeKey]*group, len(edges))
	for i := range edges {
		e := &edges[i]
		k := Key(*e)
		g, ok := groups[k]
		if !ok {
			g = &group{
				byRank:  make(map[int][]discovery.Edge),
				minRank: e.PrecedenceRank,
				sides:   make(map[string]bool),
			}
			groups[k] = g
		}
		g.sides[e.SrcDevice] = true
		g.byRank[e.PrecedenceRank] = append(g.byRank[e.PrecedenceRank], *e)
		if e.PrecedenceRank < g.minRank {
			g.minRank = e.PrecedenceRank
		}
	}

	result := make([]discovery.Edge, 0, len(groups))
	for k, g := range groups {
		bidirectional := len(g.sides) >= 2
		candidates := g.byRank[g.minRank]

		// Prefer the observation from the canonical (A) side for determinism.
		chosen := candidates[0]
		for _, c := range candidates {
			if c.SrcDevice == k.SrcDevice {
				chosen = c
				break
			}
		}

		// Normalise to canonical endpoint order.
		if chosen.SrcDevice != k.SrcDevice {
			chosen.SrcDevice, chosen.DstDevice = k.SrcDevice, k.DstDevice
			chosen.SrcPort, chosen.DstPort = k.SrcPort, k.DstPort
		}
		if bidirectional {
			chosen.Direction = discovery.DirectionBidirectional
		} else {
			chosen.Direction = discovery.DirectionUnidirectional
		}
		result = append(result, chosen)
	}

	slices.SortFunc(result, func(a, b discovery.Edge) int {
		return compareEdgeKey(Key(a), Key(b))
	})

	return result, nil
}

// Diff returns the changes between two reconciled edge sets. Both slices must
// come from Reconcile (canonical endpoint order, one entry per EdgeKey) so the
// key lookups are stable.
func Diff(before, after []discovery.Edge) []EdgeChange {
	if len(before) == 0 && len(after) == 0 {
		return nil
	}

	beforeMap := make(map[EdgeKey]discovery.Edge, len(before))
	for _, e := range before {
		beforeMap[Key(e)] = e
	}

	var changes []EdgeChange

	for i := range after {
		a := &after[i]
		k := Key(*a)
		b, exists := beforeMap[k]
		if !exists {
			aCopy := *a
			changes = append(changes, EdgeChange{Kind: ChangeAdded, After: &aCopy})
		} else if edgeMateriallyChanged(b, *a) {
			bCopy := b
			aCopy := *a
			changes = append(changes, EdgeChange{Kind: ChangeUpdated, Before: &bCopy, After: &aCopy})
		}
		delete(beforeMap, k)
	}

	for k := range beforeMap {
		b := beforeMap[k]
		bCopy := b
		changes = append(changes, EdgeChange{Kind: ChangeRemoved, Before: &bCopy})
	}

	return changes
}

// edgeMateriallyChanged returns true when two edges describing the same
// physical link (same EdgeKey) differ in a way that warrants a log event and
// metric increment. Timestamp-only churn is not a change.
func edgeMateriallyChanged(before, after discovery.Edge) bool {
	return before.Direction != after.Direction ||
		before.DiscoveryProto != after.DiscoveryProto ||
		before.Confidence != after.Confidence ||
		before.PrecedenceRank != after.PrecedenceRank ||
		before.LinkKind != after.LinkKind
}

// EdgeKey is the LD-14 lifecycle key. Two Edge observations describe the
// same physical link when they share an EdgeKey, regardless of which
// discovery protocol produced them. The graph layer tracks the
// consecutive-unconfirmed-cycle counter against this key in the snapshot.
type EdgeKey struct {
	SrcDevice string
	SrcPort   string
	DstDevice string
	DstPort   string
}

// Key returns the LD-14 lifecycle key for an Edge. Endpoints are sorted so
// the same physical link emits the same key regardless of which side the
// discovery protocol named first.
func Key(e discovery.Edge) EdgeKey {
	a := EdgeKey{SrcDevice: e.SrcDevice, SrcPort: e.SrcPort, DstDevice: e.DstDevice, DstPort: e.DstPort}
	b := EdgeKey{SrcDevice: e.DstDevice, SrcPort: e.DstPort, DstDevice: e.SrcDevice, DstPort: e.SrcPort}
	if compareEdgeKey(a, b) <= 0 {
		return a
	}
	return b
}

// EdgeKeyString serialises an EdgeKey to the pipe-delimited format used in
// snapshot.File.UnconfirmedAges ("srcDevice|srcPort|dstDevice|dstPort").
// The key must be in canonical order (from Key()) for round-trips to be
// stable.
func EdgeKeyString(k EdgeKey) string {
	return k.SrcDevice + "|" + k.SrcPort + "|" + k.DstDevice + "|" + k.DstPort
}

// EdgeKeyFromString parses the pipe-delimited snapshot format back into an
// EdgeKey. Returns an error if s does not have exactly three separators.
func EdgeKeyFromString(s string) (EdgeKey, error) {
	parts := strings.SplitN(s, "|", 4)
	if len(parts) != 4 {
		return EdgeKey{}, fmt.Errorf("graph: invalid edge key %q (want srcDevice|srcPort|dstDevice|dstPort)", s)
	}
	return EdgeKey{SrcDevice: parts[0], SrcPort: parts[1], DstDevice: parts[2], DstPort: parts[3]}, nil
}

// AgeUnconfirmed advances the LD-14 lifecycle counter by one cycle for every
// edge in current that is unidirectional, and returns the keys to remove
// because their counter has reached ttl. ages is the persisted counter map
// from the snapshot (LD-13); it's mutated in place so the caller can write
// it back to the next snapshot. A bidirectional observation resets the
// counter to zero — bidirectional links never time out via this path.
//
// ttl mirrors discovery.unconfirmed_link_ttl_cycles from config (default 3).
func AgeUnconfirmed(current []discovery.Edge, ages map[EdgeKey]int, ttl int) []EdgeKey {
	if ages == nil {
		return nil
	}
	seen := make(map[EdgeKey]bool, len(current))
	var expired []EdgeKey
	for _, e := range current {
		k := Key(e)
		seen[k] = true
		if e.Direction == discovery.DirectionBidirectional {
			delete(ages, k)
			continue
		}
		ages[k]++
		if ages[k] >= ttl {
			expired = append(expired, k)
			delete(ages, k) // reset so a reappearing edge gets a fresh counter
		}
	}
	// Edges that were unconfirmed last cycle but absent this cycle don't
	// linger in the counter map.
	for k := range ages {
		if !seen[k] {
			delete(ages, k)
		}
	}
	return expired
}

func compareEdgeKey(a, b EdgeKey) int {
	switch {
	case a.SrcDevice < b.SrcDevice:
		return -1
	case a.SrcDevice > b.SrcDevice:
		return 1
	case a.SrcPort < b.SrcPort:
		return -1
	case a.SrcPort > b.SrcPort:
		return 1
	case a.DstDevice < b.DstDevice:
		return -1
	case a.DstDevice > b.DstDevice:
		return 1
	case a.DstPort < b.DstPort:
		return -1
	case a.DstPort > b.DstPort:
		return 1
	default:
		return 0
	}
}
