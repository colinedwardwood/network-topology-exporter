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
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	defer func() { _ = client.Conn.Close() }()

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

// buildEdges converts the raw SNMP walks into topology edges.
//
// TTL liveness: lldpRemTable entries are aged out by the LLDP agent per
// IEEE 802.1AB-2016 §9.6.3. Expired entries are removed before our walk,
// so no explicit TTL field check is needed here.
func buildEdges(localDevice string, locPorts map[int]locPort, remEntries map[remKey]*remEntry, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour
	now := time.Now()

	for k, rem := range remEntries {
		// IEEE 802.1AB Table 8-2: chassis ID subtypes are 1–7.
		if rem.chassisSubtype < 1 || rem.chassisSubtype > 7 {
			slog.Debug("lldp: invalid chassis ID subtype; skipping entry",
				"device", localDevice, "subtype", rem.chassisSubtype)
			continue
		}

		// IEEE 802.1AB Table 8-3: port ID subtypes are 1–7.
		if rem.portSubtype < 1 || rem.portSubtype > 7 {
			slog.Debug("lldp: invalid port ID subtype; skipping entry",
				"device", localDevice, "subtype", rem.portSubtype)
			continue
		}

		// MAC chassis ID (subtype 4) must be exactly 6 bytes.
		if rem.chassisSubtype == chassisSubtypeMACAddress && len(rem.chassisID) != 6 {
			slog.Debug("lldp: MAC chassis ID wrong length; skipping entry",
				"device", localDevice, "got", len(rem.chassisID), "want", 6)
			continue
		}

		// Network-address chassis ID (subtype 5): first byte is IANA address family
		// (1=IPv4, 2=IPv6); total length must be 5 (1+4 for IPv4) or 17 (1+16 for IPv6).
		if rem.chassisSubtype == chassisSubtypeNetworkAddress {
			if len(rem.chassisID) == 0 {
				slog.Debug("lldp: zero-length network-address chassis ID; skipping entry",
					"device", localDevice)
				continue
			}
			if len(rem.chassisID) < 2 ||
				(rem.chassisID[0] == 1 && len(rem.chassisID) != 5) ||
				(rem.chassisID[0] == 2 && len(rem.chassisID) != 17) ||
				(rem.chassisID[0] != 1 && rem.chassisID[0] != 2) {
				slog.Debug("lldp: malformed network-address chassis ID; skipping entry",
					"device", localDevice, "family", rem.chassisID[0], "len", len(rem.chassisID))
				continue
			}
		}

		// MAC port ID (subtype 3) must be exactly 6 bytes.
		if rem.portSubtype == portSubtypeMACAddress && len(rem.portID) != 6 {
			slog.Debug("lldp: MAC port ID wrong length; skipping entry",
				"device", localDevice, "got", len(rem.portID), "want", 6)
			continue
		}

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
			slog.Debug("lldp: unable to resolve neighbour device or port, skipping edge",
				"local_device", localDevice, "local_port", localPort,
				"rem_device", remDevice, "rem_port", remPort)
			continue
		}

		// LD-11: if the neighbor exposes a network-address chassis ID that
		// falls outside the allow-list, record it and skip the edge.
		if remIP := extractChassisIP(rem.chassisSubtype, rem.chassisID); remIP != nil {
			// Devices that have no assigned IP advertise the unspecified address
			// (0.0.0.0 for IPv4, :: for IPv6); skip them to avoid emitting an
			// edge with an invalid DstDevice.
			if remIP.IsUnspecified() {
				slog.Debug("lldp: unspecified chassis IP; skipping entry",
					"device", localDevice, "ip", remIP)
				continue
			}
			if len(allowedNets) > 0 && !snmputil.IPInNets(remIP, allowedNets) {
				oos = append(oos, discovery.OutOfScopeNeighbour{
					Proto:           "lldp",
					ReportingDevice: localDevice,
					ReportingPort:   localPort,
					NeighbourHint:   remDevice,
					FirstSeen:       now,
					LastSeen:        now,
				})
				continue
			}
		}

		// Phase 1 identity annotation: when the peer uses a MAC chassis ID and
		// also advertises a sysName, record the MAC so the synthesis layer can
		// build a MAC→sysName index for FDB edge resolution (Phase 2).
		var metadata map[string]string
		if rem.chassisSubtype == chassisSubtypeMACAddress && rem.sysName != "" {
			metadata = map[string]string{
				discovery.MetadataKeyPeerChassisMac: fmtMAC(rem.chassisID),
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
			Metadata:       metadata,
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
		s := strings.TrimRight(string(raw), "\x00")
		if !utf8.ValidString(s) {
			return hex.EncodeToString(raw)
		}
		return s
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
		s := strings.TrimRight(string(raw), "\x00")
		if !utf8.ValidString(s) {
			return hex.EncodeToString(raw)
		}
		return s
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
