// Package bgp implements BGP-based topology discovery.
//
// Specification sources (public, no vendor source code consulted):
//   - RFC 1657 — BGP4-MIB. SNMP walk: 1.3.6.1.2.1.15 (bgp)
//
// LD-09: this header lists the only sources consulted. Add citations before
// extending to additional sub-OIDs.
package bgp

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker is the BGP discovery module. Implementation lands per the v1 plan.
type Walker struct{}

// Walk returns the BGP-discovered edges for the device at host.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
