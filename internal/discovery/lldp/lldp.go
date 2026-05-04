// Package lldp implements LLDP neighbor discovery against LLDP-MIB.
//
// Specification sources (public, no vendor source code consulted):
//   - IEEE 802.1AB-2016 — Station and Media Access Control Connectivity Discovery
//   - RFC 4957 — LLDP MIB
//   - LLDP-MIB.mib (IETF, public)
//
// SNMP walk: 1.0.8802.1.1.2.1.4 (lldpRemoteSystemsData)
//
// LD-09: spec citations above are the only sources consulted for this module.
// Any extension (e.g. MED, capability negotiation) must add the corresponding
// IEEE 802.1AB amendment or RFC reference here before shipping.
package lldp

import (
	"context"

	"github.com/owner-tbd/network-topology-exporter/internal/discovery"
)

// Walker discovers LLDP neighbors for one device and emits Edge records.
type Walker struct{}

// Walk returns the LLDP neighbor edges for the device at host. v0.1 returns
// no edges; the implementation lands Day 2 per the v1 plan.
func (w *Walker) Walk(_ context.Context, _ string) ([]discovery.Edge, error) {
	return nil, nil
}
