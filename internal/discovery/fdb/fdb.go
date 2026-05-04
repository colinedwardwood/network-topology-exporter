// Package fdb implements FDB-based topology discovery.
//
// Specification sources (public, no vendor source code consulted):
//   - RFC 4188 — BRIDGE-MIB dot1dTpFdbTable. SNMP walk: 1.3.6.1.2.1.17.4.3
//
// LD-09: this header lists the only sources consulted. Add citations before
// extending to additional sub-OIDs.
package fdb

import (
	"context"

	"github.com/owner-tbd/network-topology-exporter/internal/discovery"
)

// Walker is the FDB discovery module. Implementation lands per the v1 plan.
type Walker struct{}

// Walk returns the FDB-discovered edges for the device at host.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
