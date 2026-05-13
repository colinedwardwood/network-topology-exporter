// Package cdp discovers Cisco-proprietary neighbour relationships via CDP.
//
// Specification sources:
//   - CISCO-CDP-MIB (vendor-published) — cdpCacheTable at
//     1.3.6.1.4.1.9.9.23.1.2.1.1. Index is (ifIndex, neighbourIndex).
//     cdpCacheDeviceId (.6), cdpCacheDevicePort (.7), cdpCachePlatform (.8),
//     cdpCacheAddress (.4) / cdpCacheAddressType (.3).
//   - RFC 2863 — IF-MIB ifXTable. ifName (1.3.6.1.2.1.31.1.1.1.1.{ifIndex})
//     maps the ifIndex from the CDP OID index to a human-readable port name.
//
// Design references:
//   - arXiv:1709.02209, 2017 — BFS via cdpCacheTable is equivalent to
//     lldpRemTable for Cisco-only environments.
//
// CDP is Cisco-proprietary; only Cisco devices respond. If LLDP is also
// enabled and both protocols report the same link, the graph layer ranks
// LLDP above CDP (lower PrecedenceRank) per the LD-10 ladder.
package cdp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidCDPCacheTable = "1.3.6.1.4.1.9.9.23.1.2.1"
	precedenceRank   = 3
)

// cdpCacheTable column numbers (CISCO-CDP-MIB).
const (
	colAddressType = 3
	colAddress     = 4
	colDeviceID    = 6
	colDevicePort  = 7
)

type cacheKey struct{ ifIndex, neighIndex int }

type cacheEntry struct {
	addrType int
	addr     []byte
	deviceID string
	devPort  string
}

// Walk returns CDP-discovered edges for the device at p.IP. localDevice is
// the sysName from the SNMP SYSTEM walk.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("cdp %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	ifNames, err := snmputil.WalkIfNamesWithFallback(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("cdp ifname %s: %w", p.IP, err)
	}

	entries, err := walkCacheTable(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("cdp cache %s: %w", p.IP, err)
	}

	return buildEdges(localDevice, ifNames, entries, allowedNets)
}

func walkCacheTable(ctx context.Context, client *gsnmp.GoSNMP) (map[cacheKey]*cacheEntry, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidCDPCacheTable)
	if err != nil {
		return nil, err
	}

	const prefix = "." + oidCDPCacheTable + ".1."
	entries := make(map[cacheKey]*cacheEntry)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		// suffix: col.ifIndex.neighIndex
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok {
			continue
		}
		ifIdx, neighStr, ok := snmputil.SplitOIDComponent(rest)
		if !ok {
			continue
		}
		if ifIdx <= 0 {
			continue
		}
		neighIdx, err := strconv.Atoi(neighStr)
		if err != nil {
			continue
		}
		if neighIdx <= 0 {
			continue
		}

		k := cacheKey{ifIdx, neighIdx}
		e := entries[k]
		if e == nil {
			e = &cacheEntry{}
			entries[k] = e
		}
		switch col {
		case colAddressType:
			e.addrType = snmputil.PDUInt(pdu)
		case colAddress:
			e.addr = snmputil.PDUBytes(pdu)
		case colDeviceID:
			e.deviceID = snmputil.NormaliseName(snmputil.PDUString(pdu))
		case colDevicePort:
			e.devPort = snmputil.PDUString(pdu)
		}
	}
	return entries, nil
}

func buildEdges(localDevice string, ifNames map[int]string, entries map[cacheKey]*cacheEntry, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour
	now := time.Now()

	for k, e := range entries {
		if e.deviceID == "" || e.devPort == "" {
			continue
		}

		localPort := ifNames[k.ifIndex]
		if localPort == "" {
			localPort = fmt.Sprintf("if%d", k.ifIndex)
		}

		// LD-11: cdpCacheAddress with addrType 1 (IPv4) gives the neighbor IP.
		remIP := cdpNeighborIP(e)
		if remIP == nil && len(allowedNets) > 0 {
			// Cannot validate scope for non-IP neighbor; skip when scope filtering is active.
			slog.Debug("cdp: skipping non-IP neighbor when scope filtering is active",
				"device", localDevice, "neighbor", e.deviceID)
			continue
		}
		if remIP != nil && len(allowedNets) > 0 && !snmputil.IPInNets(remIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "cdp",
				ReportingDevice: localDevice,
				ReportingPort:   localPort,
				NeighbourHint:   e.deviceID,
				FirstSeen:       now,
				LastSeen:        now,
			})
			continue
		}

		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        localPort,
			DstDevice:      e.deviceID,
			DstPort:        e.devPort,
			DiscoveryProto: "cdp",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceHigh,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ethernet",
			ObservedAt:     now,
		})
	}

	return edges, oos, nil
}

// cdpNeighborIP extracts the IP address from cdpCacheAddress. addrType 1 is
// IPv4 (4-byte addr); addrType 20 is IPv6 (16-byte addr) per CISCO-CDP-MIB.
// Returns nil for all other address types or unexpected lengths.
func cdpNeighborIP(e *cacheEntry) net.IP {
	switch {
	case e.addrType == 1 && len(e.addr) == 4:
		return append(net.IP{}, e.addr...)
	case e.addrType == 20 && len(e.addr) == 16:
		return append(net.IP{}, e.addr...)
	}
	return nil
}
