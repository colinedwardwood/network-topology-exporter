// Package discovery defines the cross-module Device and Edge value types that
// every discovery module produces. Concrete walking logic lives in
// internal/discovery/<protocol>/.
//
// The Edge schema mirrors the LD-10 reconciliation labels in the parent
// network-o11y-dev repo (docs/ARCHITECTURE.md §LD-10): every edge carries
// the source protocol, direction, confidence bucket, NetXMS-style adjacency
// classification, and precedence rank, so the graph layer can rank without
// having to recover that information from elsewhere.
package discovery

import "time"

// Device is the inventory record for one network node.
type Device struct {
	ID           string // stable identifier (typically <site>/<host>)
	Vendor       string
	Model        string
	OSVersion    string
	Site         string
	ParentDevice string // for topology-aware suppression (TS-09)
	Uptime       time.Duration
	Labels       map[string]string // free-form site / role / environment labels
}

// Direction records whether a link was confirmed from both endpoints (the
// other end's discovery protocol agrees the link exists) or only one end.
// Bidirectional confirmation is a strong signal; unidirectional links survive
// in the graph but at lower precedence per LD-10.
type Direction string

const (
	DirectionBidirectional Direction = "bidirectional"
	DirectionUnidirectional Direction = "unidirectional"
)

// Confidence is the coarse three-bucket classification used by LD-10. v1 does
// not learn confidence from history; the bucket is a function of the source
// protocol and direction. v2 may layer a learned score on top without
// changing this enum.
type Confidence string

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
	DiscoveryProto string     // "lldp" | "cdp" | "fdp" | "bgp" | "ospf" | "arp" | "fdb" | "netbox"
	Direction      Direction
	Confidence     Confidence
	Adjacency      Adjacency
	PrecedenceRank int // 1 = manual/NetBox; 9 = ARP inference. See LD-10 ladder.

	// LinkKind describes the link's transport semantics, independent of how it
	// was discovered: "ethernet" for L2 LLDP/CDP/FDB observations, "ibgp" or
	// "ebgp" for BGP4-MIB peer adjacencies, "ospf-area" for OSPF-MIB peers,
	// "logical" for tunnel / VPN endpoints, etc. Not part of the LD-10 label
	// set; consumed by dashboards that want to filter by transport.
	LinkKind string
}
