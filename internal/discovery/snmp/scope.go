package snmp

import (
	"net"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// OutOfScope applies the LD-11 discovery-scope filter to one neighbour
// address: it reports true when scope filtering is active (allowedNets
// non-empty) and ip falls outside every allowed network. An empty
// allowedNets means scope enforcement is off, so nothing is ever out of
// scope. This is the single scope predicate shared by every protocol
// walker — a change to scope semantics (catch-all handling, v6 mapping)
// happens here once, not in seven walkers.
func OutOfScope(ip net.IP, allowedNets []*net.IPNet) bool {
	return len(allowedNets) > 0 && !IPInNets(ip, allowedNets)
}

// NewOutOfScopeNeighbour builds the canonical LD-11 out-of-scope
// observation record. now is stamped into both FirstSeen and LastSeen —
// first/last collapsing across cycles is the reconciler's job, not the
// walker's. reportingPort may be empty for protocols with no local-port
// notion at the peering layer (bgp, ospf). hint is whatever identity the
// protocol can offer for the unpolled neighbour: an IP for the routing
// protocols, an advertised system/device name for lldp/cdp.
func NewOutOfScopeNeighbour(proto, reportingDevice, reportingPort, hint string, now time.Time) discovery.OutOfScopeNeighbour {
	return discovery.OutOfScopeNeighbour{
		Proto:           proto,
		ReportingDevice: reportingDevice,
		ReportingPort:   reportingPort,
		NeighbourHint:   hint,
		FirstSeen:       now,
		LastSeen:        now,
	}
}
