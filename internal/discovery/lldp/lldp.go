// Package lldp implements LLDP neighbour discovery against LLDP-MIB.
//
// Specification sources:
//   - IEEE 802.1AB-2016 — Station and Media Access Control Connectivity
//     Discovery. Defines lldpRemTable (1.0.8802.1.1.2.1.4.1.1) and
//     lldpLocPortTable (1.0.8802.1.1.2.1.3.7.1). Table 8-2 (chassis ID
//     subtypes) and Table 8-3 (port ID subtypes) define the encoding.
//   - RFC 4957 / IETF LLDP-MIB — SNMP surface for the above.
//
// Design references:
//   - prometheus/snmp_exporter (Apache 2.0) — the LldpPortId combined-type
//     dispatch pattern (subtype read before ID bytes). No code copied.
//   - arXiv:1709.02209, 2017 — BFS via lldpRemTable is the validated
//     discovery algorithm; Walk is one step of that loop.
//
// lldpRemPortId is a combined type: the bytes in column 7 mean different
// things depending on lldpRemPortIdSubtype (column 6) in the same row.
// Skipping the subtype read produces garbage port names on Arista, Juniper,
// and most AP vendors that use MAC (subtype 3) instead of interface name
// (subtype 5).
package lldp

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidLocPortTable = "1.0.8802.1.1.2.1.3.7"
	oidRemTable     = "1.0.8802.1.1.2.1.4.1"
	precedenceRank  = 2
)

// LldpPortIdSubtype values from IEEE 802.1AB Table 8-3.
const (
	portSubtypeInterfaceAlias = 1
	portSubtypePortComponent  = 2
	portSubtypeMACAddress     = 3
	portSubtypeNetworkAddress = 4
	portSubtypeInterfaceName  = 5
	portSubtypeAgentCircuitID = 6
	portSubtypeLocal          = 7
)

// LldpChassisIdSubtype values from IEEE 802.1AB Table 8-2.
const (
	chassisSubtypeMACAddress     = 4
	chassisSubtypeNetworkAddress = 5
)

// lldpLocPortTable column numbers (IEEE 802.1AB §9.5.4).
const (
	colLocPortIDSubtype = 2
	colLocPortID        = 3
	colLocPortDesc      = 4
)

// lldpRemTable column numbers (IEEE 802.1AB §9.5.5).
const (
	colRemChassisIDSubtype = 4
	colRemChassisID        = 5
	colRemPortIDSubtype    = 6
	colRemPortID           = 7
	colRemPortDesc         = 8
	colRemSysName          = 9
)

type locPort struct {
	idSubtype int
	id        []byte
	desc      string
}

type remKey struct{ portNum, remIndex int }

type remEntry struct {
	chassisSubtype int
	chassisID      []byte
	portSubtype    int
	portID         []byte
	portDesc       string
	sysName        string
}

// Walk returns LLDP-discovered edges for the device at p.IP. localDevice is
// the sysName from the SNMP SYSTEM walk; it becomes SrcDevice in all edges.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("lldp %s: %w", p.IP, err)
	}
	defer client.Conn.Close()

	locPorts, err := walkLocPorts(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("lldp locport %s: %w", p.IP, err)
	}

	remEntries, err := walkRemEntries(ctx, client)
	if err != nil {
		return nil, nil, fmt.Errorf("lldp remtable %s: %w", p.IP, err)
	}

	return buildEdges(localDevice, locPorts, remEntries, allowedNets)
}

func walkLocPorts(ctx context.Context, client *gsnmp.GoSNMP) (map[int]locPort, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidLocPortTable)
	if err != nil {
		return nil, err
	}

	const prefix = ".1.0.8802.1.1.2.1.3.7.1."
	ports := make(map[int]locPort)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		// suffix: col.portNum
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok {
			continue
		}
		portNum, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		lp := ports[portNum]
		switch col {
		case colLocPortIDSubtype:
			lp.idSubtype = snmputil.PDUInt(pdu)
		case colLocPortID:
			lp.id = snmputil.PDUBytes(pdu)
		case colLocPortDesc:
			lp.desc = snmputil.PDUString(pdu)
		}
		ports[portNum] = lp
	}
	return ports, nil
}

func walkRemEntries(ctx context.Context, client *gsnmp.GoSNMP) (map[remKey]*remEntry, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidRemTable)
	if err != nil {
		return nil, err
	}

	const prefix = ".1.0.8802.1.1.2.1.4.1.1."
	entries := make(map[remKey]*remEntry)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		// suffix: col.timeMark.portNum.remIndex
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok {
			continue
		}
		// skip timeMark — it's a stability timestamp, not a topology field
		_, rest, ok = snmputil.SplitOIDComponent(rest)
		if !ok {
			continue
		}
		portNum, remStr, ok := snmputil.SplitOIDComponent(rest)
		if !ok {
			continue
		}
		remIndex, err := strconv.Atoi(remStr)
		if err != nil {
			continue
		}

		k := remKey{portNum, remIndex}
		e := entries[k]
		if e == nil {
			e = &remEntry{}
			entries[k] = e
		}
		switch col {
		case colRemChassisIDSubtype:
			e.chassisSubtype = snmputil.PDUInt(pdu)
		case colRemChassisID:
			e.chassisID = snmputil.PDUBytes(pdu)
		case colRemPortIDSubtype:
			e.portSubtype = snmputil.PDUInt(pdu)
		case colRemPortID:
			e.portID = snmputil.PDUBytes(pdu)
		case colRemPortDesc:
			e.portDesc = snmputil.PDUString(pdu)
		case colRemSysName:
			e.sysName = snmputil.NormaliseName(snmputil.PDUString(pdu))
		}
	}
	return entries, nil
}

func buildEdges(localDevice string, locPorts map[int]locPort, remEntries map[remKey]*remEntry, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour
	now := time.Now()

	for k, rem := range remEntries {
		if len(rem.chassisID) == 0 || len(rem.portID) == 0 {
			continue
		}

		localPort := resolveLocalPort(k.portNum, locPorts)
		remDevice := resolveRemDevice(rem)
		remPort := decodePortID(rem.portSubtype, rem.portID)
		if remPort == "" {
			remPort = rem.portDesc
		}
		if remDevice == "" || remPort == "" {
			continue
		}

		// LD-11: if the neighbor exposes a network-address chassis ID that
		// falls outside the allow-list, record it and skip the edge.
		if remIP := extractChassisIP(rem.chassisSubtype, rem.chassisID); remIP != nil {
			if len(allowedNets) > 0 && !snmputil.IPInNets(remIP, allowedNets) {
				oos = append(oos, discovery.OutOfScopeNeighbour{
					ReportingDevice: localDevice,
					ReportingPort:   localPort,
					NeighbourHint:   remDevice,
					FirstSeen:       now,
					LastSeen:        now,
				})
				continue
			}
		}

		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        localPort,
			DstDevice:      remDevice,
			DstPort:        remPort,
			DiscoveryProto: "lldp",
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

func resolveLocalPort(portNum int, locPorts map[int]locPort) string {
	lp, ok := locPorts[portNum]
	if !ok {
		return strconv.Itoa(portNum)
	}
	if s := decodePortID(lp.idSubtype, lp.id); s != "" {
		return s
	}
	if lp.desc != "" {
		return lp.desc
	}
	return strconv.Itoa(portNum)
}

func resolveRemDevice(rem *remEntry) string {
	if rem.sysName != "" {
		return rem.sysName
	}
	return decodeChassisID(rem.chassisSubtype, rem.chassisID)
}

// decodePortID decodes an LLDP port ID given its subtype (IEEE 802.1AB Table 8-3).
func decodePortID(subtype int, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	switch subtype {
	case portSubtypeMACAddress:
		return fmtMAC(raw)
	case portSubtypeNetworkAddress:
		return fmtNetAddr(raw)
	default:
		return strings.TrimRight(string(raw), "\x00")
	}
}

// decodeChassisID decodes an LLDP chassis ID given its subtype (IEEE 802.1AB Table 8-2).
func decodeChassisID(subtype int, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	switch subtype {
	case chassisSubtypeMACAddress:
		return fmtMAC(raw)
	case chassisSubtypeNetworkAddress:
		return fmtNetAddr(raw)
	default:
		return strings.TrimRight(string(raw), "\x00")
	}
}

// extractChassisIP returns the IP from a networkAddress chassis ID, or nil.
func extractChassisIP(subtype int, raw []byte) net.IP {
	if subtype != chassisSubtypeNetworkAddress || len(raw) < 2 {
		return nil
	}
	// First byte is IANA address family: 1=IPv4, 2=IPv6.
	switch raw[0] {
	case 1:
		if len(raw) == 5 {
			return append(net.IP{}, raw[1:5]...)
		}
	case 2:
		if len(raw) == 17 {
			return append(net.IP{}, raw[1:17]...)
		}
	}
	return nil
}

func fmtMAC(b []byte) string {
	if len(b) != 6 {
		return hex.EncodeToString(b)
	}
	return net.HardwareAddr(b).String()
}

func fmtNetAddr(raw []byte) string {
	if ip := extractChassisIP(chassisSubtypeNetworkAddress, raw); ip != nil {
		return ip.String()
	}
	return hex.EncodeToString(raw)
}


