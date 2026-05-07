// Package isis infers L3 topology from IS-IS adjacency tables.
//
// Invariants:
// - Only adjacency state up(3) emits edges.
// - adjKey is derived by dropping the IPv4 tail from isisISAdjIPAddr OID suffix.
// - adjState decode errors are hard-fail (required signal).
// - circuit/ifDescr joins are optional; failures degrade SrcPort enrichment.
package isis

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidISISAdjState    = "1.3.6.1.2.1.138.1.6.1.1.2"
	oidISISAdjIPAddr   = "1.3.6.1.2.1.138.1.6.2.1.2"
	oidISISCircIfIndex = "1.3.6.1.2.1.138.1.4.1.1.3"
	// precedenceRank 5: IS-IS ranked above OSPF (6) because it is more commonly
	// the primary IGP on service-provider networks and carries richer TE data.
	// Ladder: LLDP=2, CDP=3, FDB=4, IS-IS=5, OSPF=6, BGP=7, MPLS-TE=8.
	precedenceRank = 5
	isisAdjStateUp = 3
)

// Walk returns IS-IS adjacency edges for the device at p.IP. Only adjacencies
// in state up(3) produce edges. Neighbours outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("isis %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	states, err := walkAdjStates(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("isis adjState %s: %w", p.IP, err)
	}

	var circIfNames map[string]string
	var degradedReason string
	if len(states) > 0 {
		circIfNames, err = walkCircuitIfNames(ctx, client)
		if err != nil {
			slog.Debug("isis: circuit ifName walk failed; SrcPort will be empty", "device", p.IP, "err", err)
			degradedReason = "missing_srcport_mapping"
		}
	}

	edges, oos, err := walkAdjIPAddrs(ctx, client, localDevice, states, circIfNames, degradedReason, allowedNets)
	if err != nil {
		return nil, nil, fmt.Errorf("isis adjIPAddr %s: %w", p.IP, err)
	}
	return edges, oos, nil
}

func walkAdjStates(ctx context.Context, client *gsnmp.GoSNMP) (map[string]int, error) {
	states, stats, err := snmputil.WalkToIntMapStrict(ctx, client, "isis", oidISISAdjState)
	if err != nil {
		return nil, err
	}
	if stats.DecodeFailures > 0 || stats.TrimFailures > 0 {
		return nil, fmt.Errorf("strict decode failed for isis adjacency state (decode=%d trim=%d)", stats.DecodeFailures, stats.TrimFailures)
	}
	return states, nil
}

// walkCircuitIfNames returns a map from "{sysInst}.{circIdx}" to the interface
// name string, built by joining isisISCircIfIndex (circuit → ifIndex) with
// ifDescr (ifIndex → interface name).
func walkCircuitIfNames(ctx context.Context, client *gsnmp.GoSNMP) (map[string]string, error) {
	circIfIndex, stats, err := snmputil.WalkToIntMapStrict(ctx, client, "isis", oidISISCircIfIndex)
	if err != nil {
		return nil, err
	}
	if stats.DecodeFailures > 0 || stats.TrimFailures > 0 {
		return nil, fmt.Errorf("strict decode failed for isis circuit ifIndex map (decode=%d trim=%d)", stats.DecodeFailures, stats.TrimFailures)
	}
	ifNames, err := snmputil.WalkIfDescr(ctx, client)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(circIfIndex))
	for key, ifIdx := range circIfIndex {
		if name, ok := ifNames[ifIdx]; ok {
			result[key] = name
		}
	}
	return result, nil
}

func walkAdjIPAddrs(ctx context.Context, client *gsnmp.GoSNMP, localDevice string, states map[string]int, circIfNames map[string]string, degradedReason string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidISISAdjIPAddr)
	if err != nil {
		return nil, nil, err
	}
	const prefix = "." + oidISISAdjIPAddr + "."
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		parts := strings.Split(suffix, ".")
		const ipv4TailLen = 6
		if len(parts) <= ipv4TailLen || parts[len(parts)-ipv4TailLen] != "1" || parts[len(parts)-ipv4TailLen+1] != "4" {
			continue
		}
		adjKey := strings.Join(parts[:len(parts)-ipv4TailLen], ".")
		state, known := states[adjKey]
		if !known || state != isisAdjStateUp {
			continue
		}
		ip := snmputil.PDUIPv4(pdu)
		if ip == nil {
			continue
		}
		// Derive circuit key: adjKey is "{sysInst}.{circIdx}.{adjIdx}"; circuit
		// key is the first two components.
		adjParts := strings.SplitN(adjKey, ".", 3)
		var circKey string
		if len(adjParts) >= 2 {
			circKey = adjParts[0] + "." + adjParts[1]
		}
		ifName := circIfNames[circKey]
		edgeDegradedReason := degradedReason
		if edgeDegradedReason == "" && circKey != "" && ifName == "" {
			edgeDegradedReason = "missing_srcport_mapping"
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(ip, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				ReportingDevice: localDevice,
				NeighbourHint:   ip.String(),
				LastSeen:        now,
			})
			continue
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        ifName,
			DstDevice:      ip.String(),
			DiscoveryProto: "isis",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ip",
			ObservedAt:     now,
			Metadata:       isisMetadata(edgeDegradedReason),
		})
	}
	return edges, oos, nil
}

func isisMetadata(degradedReason string) map[string]string {
	if degradedReason == "" {
		return nil
	}
	return map[string]string{
		discovery.MetadataKeyDegraded:       "true",
		discovery.MetadataKeyDegradedReason: degradedReason,
	}
}
