// Shared utilities for the vendor-specific BGP walkers in bgp_vendor.go.
//
// This file previously hosted a "v2_draft" walker that probed the IETF
// draft form `bgp4V2PeerTable` at OID 1.3.6.1.3.5.1.1.2. Real-device
// captures landed in 2026-05-16 (lab/cisco-iol-bgp/ and lab/arista-ceos-bgp/)
// showed that no production vendor implements the draft at this OID:
// Arista, the canonical IETF-draft-aligned vendor in the original design,
// publishes its BGP4V2 MIB under its enterprise OID
// (1.3.6.1.4.1.30065.4.1.1.2) instead. The v2_draft walker was therefore
// removed in issue #31 — fallback chain now goes directly from
// vendor-specific table to RFC 4273.
//
// What remains here are utilities the vendor walkers share: the peer
// struct, the InetAddress index parsers (read a one-byte addrType, a
// one-byte addrLen, then addrLen bytes), generic OID-suffix splitting,
// and the log-truncation helper.

package bgp

import (
	"fmt"
	"strconv"

	gsnmp "github.com/gosnmp/gosnmp"
	"net"
)

// vendorPeer accumulates the columns we need for one row of any vendor's
// BGP peer table. Keyed in the walker map by the full encoded index
// string so concurrent rows for different peer addresses don't collide.
//
// Note: vendor peer tables (Cisco cbgpPeer2Table, Arista BGP4V2, etc.)
// encode the peer's remote IP address IN THE INDEX, not as a separate
// column — so there's no remoteAddr field at the column-read site. The
// peerIP field is populated by the spec's decodeIndex function before
// any column values are read.
type vendorPeer struct {
	state    int
	remoteAs int
	peerIP   net.IP
}

// InetAddressType values, RFC 4001 §3.
const (
	inetAddrTypeUnknown = 0
	inetAddrTypeIPv4    = 1
	inetAddrTypeIPv6    = 2
)

// readInetAddrAt reads one (addrType, addrLen, addrBytes...) triplet
// from parts starting at pos. Returns the parsed IP, the position after
// the triplet, and ok=true on success.
//
// Used by per-vendor index decoders in bgp_vendor.go. The triplet is
// the dot-encoded representation of an RFC 4001 InetAddress IndexValue.
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
//
// Currently unused by production walker code (peer IPs come from the
// index in every vendor table tested). Retained because it remains a
// correct decoder for tests and any future vendor whose remote-address
// column actually exists.
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

// truncateForLog returns s clipped to max RUNES (not bytes), appending an
// ellipsis if truncation occurred. Used for malformed-index log fields
// to keep log volume bounded — a misbehaving device could otherwise emit
// unbounded OID strings.
//
// Rune-aware to avoid corrupting multi-byte UTF-8 if a future caller
// passes a non-ASCII payload (current callers pass dot-separated OID
// suffixes which are ASCII, but the contract is rune-safe).
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Iterate runes up to max; if we exit early the string is too long.
	runes := 0
	byteIdx := 0
	for i := range s {
		if runes >= max {
			byteIdx = i
			break
		}
		runes++
	}
	if byteIdx == 0 {
		return s
	}
	return s[:byteIdx] + "..."
}
