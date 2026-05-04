// Package cdp implements CDP-based topology discovery.
//
// Specification sources (public, no vendor source code consulted):
//   - CISCO-CDP-MIB (vendor-published, public). SNMP walk: 1.3.6.1.4.1.9.9.23.1.2 (cdpCacheTable)
//
// LD-09: this header lists the only sources consulted. Add citations before
// extending to additional sub-OIDs.
package cdp

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker is the CDP discovery module. Implementation lands per the v1 plan.
type Walker struct{}

// Walk returns the CDP-discovered edges for the device at host.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
