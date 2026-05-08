// Package discovery defines the Device and Edge value types every protocol
// module produces. Concrete walking logic lives in
// internal/discovery/<protocol>/.
//
// # Graph model reference
//
// The abstract structure (nodes, links, termination-points) follows the
// conceptual model in RFC 8345 — "A YANG Data Model for Network Topologies"
// (Clemm et al., 2018, https://datatracker.ietf.org/doc/html/rfc8345).
// RFC 8345 is the current IETF standard representation for topology data;
// the Device/Edge naming here is equivalent to its node/link vocabulary.
//
// # Edge schema
//
// The Edge schema is shaped by LD-10 (network-o11y-dev/docs/ARCHITECTURE.md):
// every edge carries the source protocol, direction, confidence bucket,
// direct-vs-indirect adjacency classification, and precedence rank so the
// graph layer can reconcile without re-deriving that information.
//
// The Confidence and Adjacency fields are informed by:
//   - Bejerano, Breitbart, Garofalakis, Rastogi — "Physical Topology
//     Discovery for Large Multisubnet Networks", IEEE INFOCOM 2003. The
//     direct/indirect classification (one MAC on a port = direct; many MACs
//     = indirect) is the core contribution of that paper.
//     https://ieeexplore.ieee.org/document/1208686
//   - Breitbart et al. — "The NetInventory System", IEEE/ACM ToN 2004.
//     Describes ranking multiple protocol sources for the same physical link;
//     the precedence ladder in LD-10 follows this approach.
//     https://dl.acm.org/doi/abs/10.1109/TNET.2004.828963
package discovery

import (
	"fmt"
	"strings"
	"time"
)

const (
	// MetadataKeyDegraded marks an edge emitted in degraded mode.
	// The OTLP exporter prepends "network.topology." when emitting attributes,
	// so this bare key avoids double-prefixing (e.g. "network.topology.network.topology.degraded").
	MetadataKeyDegraded = "degraded"
	// MetadataKeyDegradedReason carries the degraded-mode reason code.
	MetadataKeyDegradedReason = "degraded_reason"

	DegradedReasonRequiredTablePartialDecode = "required_table_partial_decode"
	DegradedReasonMissingSrcPortMapping      = "missing_srcport_mapping"
	DegradedReasonMissingAdminStatusWalk     = "missing_admin_status_walk"
	DegradedReasonInvalidAdminStatusDecode   = "invalid_admin_status_decode"
)

// PolicyError marks a module-level policy failure with a machine-readable reason.
type PolicyError struct {
	Module string
	Reason string
	Err    error
}

func (e *PolicyError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s policy failure: %s", e.Module, e.Reason)
	}
	return fmt.Sprintf("%s policy failure: %s: %v", e.Module, e.Reason, e.Err)
}

func (e *PolicyError) Unwrap() error { return e.Err }

// JoinReasonCodes canonicalizes a reason slice into a deduplicated comma list.
func JoinReasonCodes(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	seen := make(map[string]bool)
	ordered := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		ordered = append(ordered, reason)
	}
	return strings.Join(ordered, ",")
}

// Device is the inventory record for one network node.
type Device struct {
	ID        string // sysName (normalised lowercase); fallback: management IP
	Vendor    string
	Model     string
	OSVersion string
	Site      string // from per-target enrichment config, not SNMP
	Uptime    time.Duration
	Labels    map[string]string // free-form labels from per-target enrichment config
}

// Direction records whether a link was confirmed from both endpoints (the
// other end's discovery protocol agrees the link exists) or only one end.
// Bidirectional confirmation is a strong signal; unidirectional links survive
// in the graph but at lower precedence per LD-10.
type Direction string

// Direction values: confirmed from both endpoints or only one.
const (
	DirectionBidirectional  Direction = "bidirectional"
	DirectionUnidirectional Direction = "unidirectional"
)

// Confidence is the coarse three-bucket classification used by LD-10. v1 does
// not learn confidence from history; the bucket is a function of the source
// protocol and direction. v2 may layer a learned score on top without
// changing this enum.
type Confidence string

// Confidence bucket values: high for LLDP/CDP direct, medium for FDB indirect, low for heuristics.
const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Adjacency is the NetXMS-style direct-vs-indirect classification: a port
// with exactly one MAC in its bridge FDB is direct (probably points at a
// real device); a port with multiple MACs is indirect (downstream switch,
// hypervisor host, aggregator). Emitted to Prometheus as the `link_type`
// label per LD-10.
type Adjacency string

// Adjacency values: direct means one MAC on the port; indirect means multiple (downstream switch, hypervisor).
const (
	AdjacencyDirect   Adjacency = "direct"
	AdjacencyIndirect Adjacency = "indirect"
	AdjacencyUnknown  Adjacency = "unknown" // for sources that can't tell (BGP, OSPF, manual)
)

// Edge is one observation of a link between two devices, made by a single
// discovery protocol. Multiple Edge values can describe the same physical
// link from different sources; reconciliation per LD-10 is the graph
// package's job, not the discovery module's.
type Edge struct {
	SrcDevice string
	SrcPort   string
	DstDevice string
	DstPort   string

	// LD-10 reconciliation labels. The metric layer maps these directly onto
	// `network_topology_edge_info` labels.
	DiscoveryProto string // "lldp" | "cdp" | "bgp" | "ospf" | "fdb"
	Direction      Direction
	Confidence     Confidence
	Adjacency      Adjacency
	PrecedenceRank int // 1 = highest priority. See LD-10 ladder.

	// LinkKind describes the link's transport semantics, independent of how it
	// was discovered: "ethernet" for L2 LLDP/CDP/FDB observations, "ibgp" or
	// "ebgp" for BGP4-MIB peer adjacencies, "ospf-area" for OSPF-MIB peers,
	// "logical" for tunnel / VPN endpoints, etc. Not part of the LD-10 label
	// set; consumed by dashboards that want to filter by transport.
	LinkKind string

	// LD-14 lifecycle. ObservedAt timestamps the cycle this Edge was emitted;
	// the graph layer tracks UnconfirmedCycles internally and removes a link
	// once it has been unidirectional for DiscoveryConfig.UnconfirmedLinkTTLCycles
	// in a row. The discovery module fills ObservedAt and leaves the counter
	// at zero — the counter is graph-layer state, not per-cycle state.
	ObservedAt time.Time

	// Metadata holds protocol-specific attributes that don't fit the core schema.
	// Keys and values are free-form strings. Nil when no extra metadata is present.
	Metadata map[string]string
}

// OutOfScopeNeighbour is the LD-11 surface for neighbours discovered via
// LLDP/CDP whose IP falls outside the configured CIDR allow-list. The
// exporter records the report but never polls the neighbour.
type OutOfScopeNeighbour struct {
	ReportingDevice string
	ReportingPort   string
	NeighbourHint   string // chassis-id, hostname, or IP — whatever the source gave us
	Proto           string // discovery protocol that reported this neighbour (lldp, cdp, …)
	FirstSeen       time.Time
	LastSeen        time.Time
}

// Graph is the reconciled view of the network at one point in time. It's
// what the snapshot writer serialises and what the metric collector reads
// to build /metrics responses.
type Graph struct {
	Devices    []Device
	Edges      []Edge
	OutOfScope []OutOfScopeNeighbour
}
