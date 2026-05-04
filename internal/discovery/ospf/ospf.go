// Package ospf implements OSPF-based topology discovery.
//
// Specification sources (public, no vendor source code consulted):
//   - RFC 4750 — OSPF Version 2 MIB. SNMP walk: 1.3.6.1.2.1.14 (ospf)
//
// LD-09: this header lists the only sources consulted. Add citations before
// extending to additional sub-OIDs.
package ospf

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker is the OSPF discovery module. Implementation lands per the v1 plan.
type Walker struct{}

// Walk returns the OSPF-discovered edges for the device at host.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
