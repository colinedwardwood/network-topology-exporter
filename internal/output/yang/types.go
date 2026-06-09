// Package yang renders the reconciled topology graph as RFC 8345/8346
// YANG-JSON (RFC 7951 encoding). See docs/operator/yang-topology.md for the
// normative mapping and docs/superpowers/specs/2026-06-09-yang-emission-design.md.
package yang

// Document is the RFC 7951 top level: {"ietf-network:networks": {...}}.
type Document struct {
	Networks Networks `json:"ietf-network:networks"`
}

// Networks is the RFC 8345 list of networks under ietf-network:networks.
type Networks struct {
	Network []Network `json:"network"`
}

// NetworkTypes carries the RFC 8346 l3-unicast presence marker. The empty
// struct marshals to {} — the correct RFC 7951 encoding for a presence
// container. l3-node-attributes are intentionally omitted (not collected).
type NetworkTypes struct {
	L3Unicast struct{} `json:"ietf-l3-unicast-topology:l3-unicast-topology"`
}

// Network is a single RFC 8345 network with its nodes and links.
type Network struct {
	NetworkID    string       `json:"network-id"`
	NetworkTypes NetworkTypes `json:"network-types"`
	Node         []Node       `json:"node,omitempty"`
	Link         []Link       `json:"ietf-network-topology:link,omitempty"`
}

// Node is an RFC 8345 node augmented with ntx-topology device attributes.
type Node struct {
	NodeID           string             `json:"node-id"`
	TerminationPoint []TerminationPoint `json:"ietf-network-topology:termination-point,omitempty"`
	// ntx-topology node augmentation
	Vendor    string `json:"ntx-topology:vendor,omitempty"`
	Model     string `json:"ntx-topology:model,omitempty"`
	OSVersion string `json:"ntx-topology:os-version,omitempty"`
	Site      string `json:"ntx-topology:site,omitempty"`
}

// TerminationPoint is an RFC 8345 termination point on a node.
type TerminationPoint struct {
	TPID string `json:"tp-id"`
}

// Source is the RFC 8345 source endpoint of a link.
type Source struct {
	SourceNode string `json:"source-node"`
	SourceTP   string `json:"source-tp,omitempty"`
}

// Destination is the RFC 8345 destination endpoint of a link.
type Destination struct {
	DestNode string `json:"dest-node"`
	DestTP   string `json:"dest-tp,omitempty"`
}

// Link is an RFC 8345 link augmented with ntx-topology discovery attributes.
type Link struct {
	LinkID      string      `json:"link-id"`
	Source      Source      `json:"source"`
	Destination Destination `json:"destination"`
	// ntx-topology link augmentation
	DiscoveryProtocol string `json:"ntx-topology:discovery-protocol,omitempty"`
	LinkKind          string `json:"ntx-topology:link-kind,omitempty"`
	Confidence        string `json:"ntx-topology:confidence,omitempty"`
	Adjacency         string `json:"ntx-topology:adjacency,omitempty"`
}
