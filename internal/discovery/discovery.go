// Package discovery defines the cross-module Device and Edge value types that
// every discovery module produces. Concrete walking logic lives in
// internal/discovery/<protocol>/.
package discovery

import "time"

// Device is the inventory record for one network node.
type Device struct {
	ID           string            // stable identifier (typically <site>/<host>)
	Vendor       string
	Model        string
	OSVersion    string
	Site         string
	ParentDevice string            // for topology-aware suppression (TS-09)
	Uptime       time.Duration
	Labels       map[string]string // free-form site / role / environment labels
}

// Edge is one directional link between two devices, as observed by a single
// discovery protocol. Edges from different protocols (LLDP, CDP, BGP, OSPF)
// are emitted independently; the consuming graph layer is responsible for
// reconciling them.
type Edge struct {
	SrcDevice      string
	SrcPort        string
	DstDevice      string
	DstPort        string
	DiscoveryProto string // "lldp" | "cdp" | "bgp" | "ospf" | "arp" | "fdb"
	LinkType       string // "ethernet" | "logical" | "ibgp" | ...
}
