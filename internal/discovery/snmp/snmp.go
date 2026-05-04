// Package snmp implements the SYSTEM-group walk that anchors device
// inventory.
//
// Specification sources (public, no vendor source code consulted):
//   - RFC 1907 — Management Information Base for SNMPv2 (sysDescr, sysObjectID,
//     sysUpTime, sysName)
//   - IANA Enterprise Numbers (vendor identification from sysObjectID prefix)
//
// Pattern provenance: the SNMP walker uses gosnmp/gosnmp (BSD-2-Clause)
// directly. The "module emits Device + Edge values, exporter coordinates"
// shape is patterned after prometheus/snmp_exporter (Apache 2.0); no code is
// copied — only the structural pattern.
//
// LD-09: any extension of this module (vendor-specific MIB walks, hardware
// inventory) must add the source spec / vendor MIB citation in this header
// before shipping.
package snmp

import (
	"context"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Walker reads the SNMP SYSTEM group and emits one Device per target.
type Walker struct {
	// fields land here when gosnmp wiring goes in (Day 2 per the v1 plan).
}

// Walk discovers a single device. v0.1 returns a hard-coded test record so the
// exporter publishes a valid network_device_info series end to end before any
// real SNMP walk lands.
func (w *Walker) Walk(_ context.Context, host string) (*discovery.Device, error) {
	return &discovery.Device{
		ID:        host,
		Vendor:    "unknown",
		Model:     "unknown",
		OSVersion: "unknown",
		Site:      "lab",
	}, nil
}
