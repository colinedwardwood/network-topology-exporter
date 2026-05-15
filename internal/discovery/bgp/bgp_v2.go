// BGP4-V2 walker. Targets the IETF draft form of the BGP-4 MIB
// (`draft-ietf-idr-bgp4-mibv2`) at root OID 1.3.6.1.3.5.1.1.2 (bgp4V2PeerTable).
// The draft never reached IETF standardisation but is implemented natively by
// Arista EOS and is a common shape across other vendors that re-publish under
// their own enterprise OID — see bgp_vendor.go for vendor-specific tables.
//
// Why this MIB and not RFC 4273:
//
//	RFC 4273 bgpPeerTable encodes the remote address as an IpAddress (4-byte)
//	type. It physically cannot represent an IPv6 peer. The v2 draft fixes this
//	with an InetAddressType+InetAddress index pair that supports both families.
//
// Index decoding:
//
//	bgp4V2PeerTable index = peerInstance.localAddrType.localAddrLen.localAddr...
//	                       remoteAddrType.remoteAddrLen.remoteAddr...
//	where each address is length-prefixed (4 bytes for IPv4, 16 bytes for IPv6).
//	The decoder below parses the full index — empty or short indices are dropped
//	silently rather than producing partial peers.
//
// Vendor coverage notes:
//
//   - Arista: implements this OID natively. Primary target.
//   - Cisco / Juniper / Nokia: re-publish under enterprise OIDs. See bgp_vendor.go.
//   - Devices that respond to neither this nor the vendor tables fall back to
//     the RFC 4273 walker in bgp.go.
package bgp

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
	// oidBgp4V2PeerTable is the root OID of bgp4V2PeerTable as defined in
	// draft-ietf-idr-bgp4-mibv2. Subtree 1.3.6.1.3.5.1.1.2 (the .2 selects
	// the peerTable from the bgp4V2 module root at .1.3.6.1.3.5).
	oidBgp4V2PeerTable = "1.3.6.1.3.5.1.1.2"

	// bgp4V2PeerTable columns, draft §6.
	colBgp4V2PeerState          = 13 // bgp4V2PeerState — same semantics as RFC 4273 (established=6)
	colBgp4V2PeerRemoteAs       = 11
	colBgp4V2PeerRemoteAddrType = 8
	colBgp4V2PeerRemoteAddr     = 9
	colBgp4V2PeerLocalAddrType  = 5
	colBgp4V2PeerLocalAddr      = 6
)

// InetAddressType values, RFC 4001 §3.
const (
	inetAddrTypeUnknown = 0
	inetAddrTypeIPv4    = 1
	inetAddrTypeIPv6    = 2
)

// bgp4V2Peer accumulates the columns we need for one row of bgp4V2PeerTable.
// Keyed in the walker map by the full encoded index string so concurrent rows
// for different (localAddr, remoteAddr) pairs do not collide.
type bgp4V2Peer struct {
	state         int
	remoteAddr    net.IP
	remoteAs      int
	indexLocalIP  net.IP // parsed from the index suffix
	indexRemoteIP net.IP // parsed from the index suffix
}

// walkBGP4V2PeerTable walks bgp4V2PeerTable. Returns (peers, ok) where ok is
// false if the table returned no rows — the caller uses this to short-circuit
// to vendor-specific or RFC 4273 fallbacks.
func walkBGP4V2PeerTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*bgp4V2Peer, bool, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidBgp4V2PeerTable)
	if err != nil {
		return nil, false, err
	}
	if len(pdus) == 0 {
		return nil, false, nil
	}

	const prefix = "." + oidBgp4V2PeerTable + ".1."
	peers := make(map[string]*bgp4V2Peer)

	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || rest == "" {
			continue
		}

		// rest is the row index — same for every column of one peer row.
		peer := peers[rest]
		if peer == nil {
			localIP, remoteIP, ok := decodeBgp4V2Index(rest)
			if !ok {
				// Malformed index. The MIB v2 draft is widely but not uniformly
				// implemented; some vendors emit rows with truncated indices on
				// peers that were never fully negotiated. Drop the row rather
				// than fabricate a peer with zero addresses.
				continue
			}
			peer = &bgp4V2Peer{indexLocalIP: localIP, indexRemoteIP: remoteIP}
			peers[rest] = peer
		}

		switch col {
		case colBgp4V2PeerState:
			peer.state = snmputil.PDUInt(pdu)
		case colBgp4V2PeerRemoteAs:
			peer.remoteAs = snmputil.PDUInt(pdu)
		case colBgp4V2PeerRemoteAddr:
			// Prefer the column value over the index for the remote IP —
			// some implementations zero-pad the index but emit the real
			// address in the column.
			if ip := pduInetAddress(pdu); ip != nil {
				peer.remoteAddr = ip
			}
		}
	}
	if len(peers) == 0 {
		return nil, false, nil
	}
	return peers, true, nil
}

// decodeBgp4V2Index parses a bgp4V2PeerTable index suffix.
// Format: peerInstance . localAddrType . localAddrLen . localAddr... .
//
//	remoteAddrType . remoteAddrLen . remoteAddr...
//
// Each numeric component is dot-separated. localAddr / remoteAddr expand to
// addrLen dot-separated bytes.
//
// Returns the parsed (localIP, remoteIP) and true on success. Returns false
// if the suffix is malformed (truncated, length mismatch, unknown family).
func decodeBgp4V2Index(suffix string) (localIP, remoteIP net.IP, ok bool) {
	parts, err := splitOIDParts(suffix)
	if err != nil {
		return nil, nil, false
	}
	// Minimum: peerInstance(1) + localAddrType(1) + localAddrLen(1) +
	//          localAddrBytes(>=4) + remoteAddrType(1) + remoteAddrLen(1) +
	//          remoteAddrBytes(>=4) = 12
	if len(parts) < 12 {
		return nil, nil, false
	}
	pos := 1 // skip peerInstance

	localIP, pos, ok = readInetAddrAt(parts, pos)
	if !ok {
		return nil, nil, false
	}
	remoteIP, pos, ok = readInetAddrAt(parts, pos)
	if !ok {
		return nil, nil, false
	}
	if pos != len(parts) {
		// Trailing junk after the remote address. Possible vendor quirk but
		// we reject to keep the decoder strict.
		return nil, nil, false
	}
	return localIP, remoteIP, true
}

// readInetAddrAt reads one (addrType, addrLen, addrBytes...) triplet from
// parts starting at pos. Returns the parsed IP, the position after the
// triplet, and ok=true on success.
func readInetAddrAt(parts []int, pos int) (net.IP, int, bool) {
	if pos+2 > len(parts) {
		return nil, 0, false
	}
	addrType := parts[pos]
	addrLen := parts[pos+1]
	pos += 2

	var want int
	switch addrType {
	case inetAddrTypeIPv4:
		want = 4
	case inetAddrTypeIPv6:
		want = 16
	default:
		// Unknown / DNS / IPv4z / IPv6z — out of scope.
		return nil, 0, false
	}
	if addrLen != want {
		return nil, 0, false
	}
	if pos+want > len(parts) {
		return nil, 0, false
	}

	ip := make(net.IP, want)
	for i := 0; i < want; i++ {
		b := parts[pos+i]
		if b < 0 || b > 255 {
			return nil, 0, false
		}
		ip[i] = byte(b)
	}
	return ip, pos + want, true
}

// splitOIDParts parses a dot-separated OID suffix into integer parts.
// Empty input returns an empty slice with no error.
func splitOIDParts(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	out := make([]int, 0, 16)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			if i == start {
				return nil, fmt.Errorf("empty OID component at %d", start)
			}
			n, err := strconv.Atoi(s[start:i])
			if err != nil {
				return nil, err
			}
			out = append(out, n)
			start = i + 1
		}
	}
	return out, nil
}

// pduInetAddress decodes an InetAddress PDU value to a net.IP. The MIB
// declares InetAddress as OCTET STRING (4 or 16 bytes); gosnmp surfaces it
// as []byte under either OctetString or ObjectIdentifier types depending on
// the device implementation. Returns nil if the bytes do not form a valid
// IPv4 or IPv6 address.
func pduInetAddress(pdu gsnmp.SnmpPDU) net.IP {
	if b, ok := pdu.Value.([]byte); ok {
		switch len(b) {
		case 4:
			ip := make(net.IP, 4)
			copy(ip, b)
			return ip
		case 16:
			ip := make(net.IP, 16)
			copy(ip, b)
			return ip
		}
	}
	if s, ok := pdu.Value.(string); ok {
		// Some implementations surface OCTET STRING as Go string.
		if ip := net.ParseIP(s); ip != nil {
			return ip
		}
	}
	return nil
}

// walkAndBuildV2Edges runs the v2 walk and converts peers to edges. Returns
// (edges, oos, ok) where ok=false means no v2-shaped data was found; the
// caller should try vendor fallbacks or RFC 4273.
func walkAndBuildV2Edges(
	ctx context.Context,
	client *gsnmp.GoSNMP,
	localDevice string,
	allowedNets []*net.IPNet,
) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, bool, error) {
	peers, ok, err := walkBGP4V2PeerTable(ctx, client)
	if err != nil {
		return nil, nil, false, err
	}
	if !ok {
		return nil, nil, false, nil
	}
	edges, oos := buildV2Edges(localDevice, peers, allowedNets)
	return edges, oos, true, nil
}

// buildV2Edges converts a bgp4V2Peer map into edges + OOS observations using
// the same precedence, confidence, and metadata conventions as the RFC 4273
// path. The remote IP preference order is (column value > index value); when
// both are unset the row is dropped with a debug log.
func buildV2Edges(localDevice string, peers map[string]*bgp4V2Peer, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for idx, peer := range peers {
		if peer.state != bgpStateEstablished {
			continue
		}

		remoteIP := peer.remoteAddr
		if remoteIP == nil {
			remoteIP = peer.indexRemoteIP
		}
		if remoteIP == nil {
			slog.Debug("bgp v2: peer missing remote address, skipping", "local_device", localDevice, "index", idx)
			continue
		}
		if remoteIP.IsUnspecified() || remoteIP.IsLinkLocalUnicast() {
			continue
		}

		if len(allowedNets) > 0 && !snmputil.IPInNets(remoteIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "bgp",
				ReportingDevice: localDevice,
				NeighbourHint:   remoteIP.String(),
				FirstSeen:       now,
				LastSeen:        now,
			})
			continue
		}

		var metadata map[string]string
		if peer.remoteAs > 0 {
			metadata = map[string]string{metaKeyRemoteAs: strconv.Itoa(peer.remoteAs)}
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			DstDevice:      remoteIP.String(),
			DiscoveryProto: "bgp",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceLow,
			Adjacency:      discovery.AdjacencyUnknown,
			PrecedenceRank: precedenceRank,
			LinkKind:       "ip",
			ObservedAt:     now,
			Metadata:       metadata,
		})
	}
	return edges, oos
}
