// Package arp implements ARP-based topology discovery.
//
// Specification sources (public, no vendor source code consulted):
//   - RFC 4293 — IP-MIB ipNetToPhysicalTable. SNMP walk: 1.3.6.1.2.1.4.35
//
// LD-09: this header lists the only sources consulted. Add citations before
// extending to additional sub-OIDs.
package arp

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker is the ARP discovery module. Implementation lands per the v1 plan.
type Walker struct{}

// Walk returns the ARP-discovered edges for the device at host.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
