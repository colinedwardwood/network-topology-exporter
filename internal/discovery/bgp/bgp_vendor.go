// Vendor-specific BGP4-V2 walkers. Each vendor publishes its IPv6-capable
// BGP peer table under its own enterprise OID with its own column numbers
// AND its own index format. The dispatcher in Walk picks one based on the
// resolved sysObjectID vendor string.
//
// Three vendor MIBs are verified from real-device captures landed in
// 2026-05-16 (see lab/cisco-iol-bgp/captures/ and lab/arista-ceos-bgp/
// captures/):
//
//   - Cisco CISCO-BGP4-MIB::cbgpPeer2Table at 1.3.6.1.4.1.9.9.187.1.2.5
//     Index: <addrType>.<addrLen>.<addrBytes...>  (3+ components)
//     RemoteAddr is INDEX-ENCODED — there is no separate column for it.
//
//   - Arista enterprise BGP4V2 at 1.3.6.1.4.1.30065.4.1.1.2
//     Index: <peerInst>.<addrType>.<addrLen>.<addrBytes...>  (4+ components)
//     RemoteAddr is INDEX-ENCODED. peerInstance differentiates this from
//     Cisco's narrower index.
//
//   - Juniper BGP4-V2-MIB-JUNIPER::jnxBgpM2PeerTable at 1.3.6.1.4.1.2636.5.1.1.2
//     Nokia TIMETRA-BGP-MIB::tBgpPeerTable at 1.3.6.1.4.1.6527.3.1.2.13.2
//     Column numbers and index format are transcribed from vendor MIB docs
//     and remain UNVERIFIED against real devices (no lab access yet). The
//     `vendor_juniper` and `vendor_nokia` walkers ship with best-effort
//     configuration; their column constants should be confirmed before any
//     operator relies on them. Tracked as part of issue #1 in milestone v1.3.1.
//
// The shared "IETF draft form" walker that previously lived at
// 1.3.6.1.3.5.1.1.2 has been removed: real-device probing (Arista 4.36
// specifically) shows the experimental-tree OID is not implemented by any
// vendor tested. Devices that historically would have implemented the
// IETF draft re-published the same table under their enterprise OID. See
// issue #31 for the full investigation.

package bgp

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

// vendorIndexDecoder extracts the BGP peer's remote IP address from the
// index suffix of one row in a vendor's peer table. Each vendor's table
// uses a different index layout (see file header); the spec carries its
// own decoder.
//
// The suffix is the dot-separated remainder of the OID after .<root>.1.<col>.
// E.g. for OID .1.3.6.1.4.1.9.9.187.1.2.5.1.3.1.4.10.0.0.2 with
// root 1.3.6.1.4.1.9.9.187.1.2.5 and col 3, the suffix is "1.4.10.0.0.2".
//
// Returns (nil, false) on any malformed input. Callers increment the
// outcomeMalformedIndex per-row counter on false return; if every unique
// row in the table fails decoding, the walk-level outcome rolls up to
// outcomeWalkerDrift (issue #27).
type vendorIndexDecoder func(suffix string) (net.IP, bool)

// vendorTableSpec describes one vendor's BGP4-V2-equivalent peer table.
type vendorTableSpec struct {
	name        string
	root        string             // table root OID
	colState    int                // column index whose value is the BGP state enum
	colRemoteAs int                // column index whose value is the peer AS number
	decodeIndex vendorIndexDecoder // per-vendor index parser; returns peer IP

	// verified == true means real-device captures landed in lab/ confirm
	// this spec's column numbers and index format. False means the spec
	// is transcribed from vendor MIB documentation but not validated
	// against real hardware; operators should treat the walker's output
	// with appropriate skepticism. Issue #1 tracks the remaining lab work.
	verified bool
}

// Vendor table specifications.
var (
	// Cisco: real-device verified against vrnetlab/cisco_iol:L2-17.12.1
	// (Cisco IOS-XE 17.12.1, ADVENTERPRISEK9-M) on 2026-05-16. Captures at
	// lab/cisco-iol-bgp/captures/r{1,2}_cisco_cbgpPeer2Table.txt.
	// Cross-confirmed against real Cisco IOS-XE hardware on 2026-05-30
	// via a colleague-supplied snmpwalk over four BGP sessions (two IPv4,
	// two IPv6, all in state established): column 3 returned INTEGER 6
	// for all rows, column 11 returned Gauge32 values consistent with
	// public AS numbers, and the index encoding (1.4.<v4> / 2.16.<v6>)
	// matched IOL byte-for-byte. See PR closing #58 for the column-match
	// table.
	ciscoCbgpPeer2Spec = vendorTableSpec{
		name:        "cisco-cbgpPeer2Table",
		root:        "1.3.6.1.4.1.9.9.187.1.2.5",
		colState:    3,  // cbgpPeer2State — confirmed (real value 6=established)
		colRemoteAs: 11, // cbgpPeer2RemoteAs — confirmed (real value 65001)
		decodeIndex: decodeCiscoCbgpPeer2Index,
		verified:    true,
	}

	// Arista: real-device verified against ceos:4.36.0F on 2026-05-16.
	// Captures at lab/arista-ceos-bgp/captures/r{1,2}_arista_bgp4v2.txt.
	// The columns map to the IETF draft form's semantics but Arista
	// publishes them under its enterprise OID, not at 1.3.6.1.3.5.
	aristaBgp4v2Spec = vendorTableSpec{
		name:        "arista-bgp4v2",
		root:        "1.3.6.1.4.1.30065.4.1.1.2",
		colState:    13, // bgp4V2PeerState — confirmed
		colRemoteAs: 10, // bgp4V2PeerRemoteAs — confirmed
		decodeIndex: decodeAristaBgp4v2Index,
		verified:    true,
	}

	// Juniper: column numbers + index format transcribed from
	// BGP4-V2-MIB-JUNIPER documentation. NOT verified against a real
	// device (Juniper vMX/vSRX images require a Juniper account; lab
	// blocked on that). Walker ships but operators should not yet rely
	// on it. Issue #1 tracks the verification.
	juniperJnxBgpM2PeerSpec = vendorTableSpec{
		name:        "juniper-jnxBgpM2PeerTable",
		root:        "1.3.6.1.4.1.2636.5.1.1.2.1.1",
		colState:    2,                         // jnxBgpM2PeerState — UNVERIFIED
		colRemoteAs: 13,                        // jnxBgpM2PeerRemoteAs — UNVERIFIED
		decodeIndex: decodeBgp4v2InstanceIndex, // best guess; same shape as Arista
		verified:    false,
	}

	// Nokia: same caveat as Juniper. SR-OS / SR Linux licensing blocks
	// lab access.
	nokiaTBgpPeerSpec = vendorTableSpec{
		name:        "nokia-tBgpPeerTable",
		root:        "1.3.6.1.4.1.6527.3.1.2.13.2",
		colState:    3,                         // tBgpPeerOperState — UNVERIFIED
		colRemoteAs: 7,                         // tBgpPeerRemoteAS — UNVERIFIED
		decodeIndex: decodeBgp4v2InstanceIndex, // best guess; same shape as Arista
		verified:    false,
	}
)

// vendorSpecFor returns the vendor table spec for the given canonical vendor
// string, or nil if no vendor-specific walker is available. Mapping aligns
// with snmputil.VendorFromObjectID's outputs.
func vendorSpecFor(vendor string) *vendorTableSpec {
	switch vendor {
	case "cisco":
		return &ciscoCbgpPeer2Spec
	case "arista":
		return &aristaBgp4v2Spec
	case "juniper":
		return &juniperJnxBgpM2PeerSpec
	case "nokia", "alcatel-lucent":
		// Pre-acquisition Alcatel-Lucent 7705/7750 still ships TIMETRA-BGP-MIB.
		return &nokiaTBgpPeerSpec
	default:
		return nil
	}
}

// decodeCiscoCbgpPeer2Index parses the index suffix of one cbgpPeer2Table
// row. Format: <addrType>.<addrLen>.<addrBytes...>
//
// addrType uses the RFC 4001 InetAddressType enum: 1=ipv4, 2=ipv6.
// addrLen is the byte length of the address (4 for IPv4, 16 for IPv6).
//
// Real captures for ipv4 peer 10.0.0.2: ".1.4.10.0.0.2"
//
// Returns (peerIP, true) on success; (nil, false) on malformed input.
func decodeCiscoCbgpPeer2Index(suffix string) (net.IP, bool) {
	parts, err := splitOIDParts(suffix)
	if err != nil {
		return nil, false
	}
	ip, _, ok := readInetAddrAt(parts, 0)
	return ip, ok
}

// decodeAristaBgp4v2Index parses the index suffix of one row in Arista's
// enterprise BGP4V2 peer table. Format: <peerInstance>.<addrType>.<addrLen>.<addrBytes...>
//
// peerInstance differentiates multiple BGP instances on the same router;
// always 1 in single-instance deployments. We don't use the value (we
// emit one Edge per peer regardless of instance) but the format requires
// us to skip it before the InetAddress triplet.
//
// Real captures for ipv4 peer 10.0.0.2 with instance 1: ".1.1.4.10.0.0.2"
func decodeAristaBgp4v2Index(suffix string) (net.IP, bool) {
	parts, err := splitOIDParts(suffix)
	if err != nil || len(parts) < 1 {
		return nil, false
	}
	// Skip peerInstance at parts[0].
	ip, _, ok := readInetAddrAt(parts, 1)
	return ip, ok
}

// decodeBgp4v2InstanceIndex is a best-effort decoder for vendors whose
// MIB documentation describes an Arista-style peerInstance-prefixed index
// but whose real-device behavior is not verified. Used by the Juniper
// and Nokia specs pending lab access. If a verified capture shows a
// different format, swap in a vendor-specific decoder like
// decodeAristaBgp4v2Index above.
func decodeBgp4v2InstanceIndex(suffix string) (net.IP, bool) {
	return decodeAristaBgp4v2Index(suffix)
}

// walkVendorPeerTable runs a generic walk of a vendor-specific peer table.
// Each vendor's spec carries its own index decoder (spec.decodeIndex)
// because real-device captures showed the index format differs across
// Cisco / Arista / IETF-draft / Juniper / Nokia.
//
// Returns (peers, hadPDUs, allRowsMalformed, ok, err):
//   - hadPDUs reports whether BulkWalk produced any PDUs at all. Used by
//     the caller to distinguish outcomeMIBUnimplemented (zero PDUs) from
//     all the "PDUs arrived but no peers assembled" cases.
//   - allRowsMalformed=true means at least one unique row index was
//     attempted AND every attempted decode failed. Used by the caller to
//     record outcomeWalkerDrift (issue #27) — operationally distinct from
//     outcomeNoPeers, which assumes at least one row decoded cleanly and
//     was simply in a non-Established state.
//   - ok=false means no peer rows were assembled; the caller should fall
//     back to RFC 4273.
//
// Per-row decode failures are also surfaced via the per-row
// outcomeMalformedIndex counter (one increment per PDU whose index could
// not be decoded; same index decoded multiple times by repeated column
// PDUs is counted each time, preserving the existing soft-signal semantics).
func walkVendorPeerTable(ctx context.Context, p *snmputil.Params, client *gsnmp.GoSNMP, spec vendorTableSpec) (map[string]*vendorPeer, bool, bool, bool, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, spec.root)
	if err != nil {
		return nil, false, false, false, err
	}
	if len(pdus) == 0 {
		return nil, false, false, false, nil
	}

	prefix := "." + spec.root + ".1."
	peers := make(map[string]*vendorPeer)
	// failedIndexes tracks UNIQUE row indexes whose decode attempt failed.
	// We need this — not just the per-PDU malformed_index count — to
	// detect the issue #27 walker_drift case: "every unique row attempted
	// was rejected". Multiple PDUs per row (one per column) would otherwise
	// double-count; we want a row-level signal.
	failedIndexes := make(map[string]struct{})

	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, rest, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || rest == "" {
			continue
		}

		peer := peers[rest]
		if peer == nil {
			peerIP, ok := spec.decodeIndex(rest)
			if !ok {
				recordWalkerOutcome(p, vendorWalkerLabel(spec.name), outcomeMalformedIndex)
				failedIndexes[rest] = struct{}{}
				slog.Debug("bgp vendor: malformed index, dropping row",
					"walker", vendorWalkerLabel(spec.name),
					"vendor_table", spec.name,
					"index_suffix", truncateForLog(rest, 50))
				continue
			}
			peer = &vendorPeer{peerIP: peerIP}
			peers[rest] = peer
		}

		switch col {
		case spec.colState:
			peer.state = snmputil.PDUInt(pdu)
		case spec.colRemoteAs:
			peer.remoteAs = snmputil.PDUInt(pdu)
		}
	}
	if len(peers) == 0 {
		// PDUs arrived but no peer row was assembled. Two sub-cases:
		//   - failedIndexes non-empty AND peers empty → every row we tried
		//     to decode was rejected. The MIB is implemented but the
		//     walker is broken on this vendor → walker_drift (issue #27).
		//   - failedIndexes empty AND peers empty → no row had a valid
		//     prefix/column split. Treat as the existing no_peers case
		//     for backward compatibility (the prefix split is structural,
		//     not a decoder mismatch).
		allRowsMalformed := len(failedIndexes) > 0
		return nil, true, allRowsMalformed, false, nil
	}
	return peers, true, false, true, nil
}

// walkAndBuildVendorEdges runs the vendor-specific walk and converts the
// resulting peers map into discovery.Edge / OutOfScopeNeighbour values.
// The caller picks the spec via vendorSpecFor before invoking this.
//
// Outcome accounting (issues #15, #27): records exactly one of
// {edges, no_peers, mib_unimplemented, walker_drift, error} per walker
// invocation, plus one malformed_index increment per dropped row inside
// walkVendorPeerTable. The five buckets are mutually exclusive at the
// walker-invocation level (the per-row malformed_index counter is the
// only one that may co-occur).
//
// Bucket selection logic:
//   - walk errored                                              → error
//   - zero PDUs returned                                        → mib_unimplemented
//   - PDUs returned, ≥1 peer assembled, ≥1 in Established       → edges
//   - PDUs returned, ≥1 peer assembled, none in Established     → no_peers
//   - PDUs returned, ≥1 row attempted decoding, all rows failed → walker_drift  (issue #27)
//
// walker_drift exists because the previous code lumped "every row was
// rejected by decodeIndex" into no_peers — operationally that conflated
// "BGP is broken on every session" with "our walker is broken on this
// vendor's MIB", which require very different alerting responses.
func walkAndBuildVendorEdges(
	ctx context.Context,
	p *snmputil.Params,
	client *gsnmp.GoSNMP,
	spec vendorTableSpec,
	localDevice string,
	allowedNets []*net.IPNet,
) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, bool, error) {
	walker := vendorWalkerLabel(spec.name)
	peers, hadPDUs, allRowsMalformed, ok, err := walkVendorPeerTable(ctx, p, client, spec)
	if err != nil {
		recordWalkerOutcome(p, walker, outcomeError)
		return nil, nil, false, err
	}
	if !ok {
		switch {
		case !hadPDUs:
			recordWalkerOutcome(p, walker, outcomeMIBUnimplemented)
		case allRowsMalformed:
			recordWalkerOutcome(p, walker, outcomeWalkerDrift)
		default:
			recordWalkerOutcome(p, walker, outcomeNoPeers)
		}
		return nil, nil, false, nil
	}
	edges, oos := buildVendorEdges(localDevice, peers, allowedNets)
	if len(edges) > 0 {
		recordWalkerOutcome(p, walker, outcomeEdges)
	} else {
		recordWalkerOutcome(p, walker, outcomeNoPeers)
	}
	return edges, oos, true, nil
}

// buildVendorEdges converts a vendorPeer map into edges + OOS observations
// using the same precedence, confidence, and metadata conventions as the
// RFC 4273 path. Every vendor walker uses this — the peer IP comes from
// the index decoder (no separate column), so this is simpler than the
// retired buildV2Edges helper.
func buildVendorEdges(localDevice string, peers map[string]*vendorPeer, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for idx, peer := range peers {
		if peer.state != bgpStateEstablished {
			continue
		}
		if peer.peerIP == nil {
			slog.Debug("bgp vendor: peer missing IP (index decoder returned nil), skipping",
				"local_device", localDevice, "index", idx)
			continue
		}
		if peer.peerIP.IsUnspecified() || peer.peerIP.IsLinkLocalUnicast() {
			continue
		}

		if len(allowedNets) > 0 && !snmputil.IPInNets(peer.peerIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "bgp",
				ReportingDevice: localDevice,
				NeighbourHint:   peer.peerIP.String(),
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
			DstDevice:      peer.peerIP.String(),
			DiscoveryProto: discovery.DiscoveryProtocolBGP,
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceLow,
			Adjacency:      discovery.AdjacencyUnknown,
			PrecedenceRank: precedenceRank,
			LinkKind:       discovery.LinkKindIP,
			ObservedAt:     now,
			Metadata:       metadata,
		})
	}
	return edges, oos
}

// resolveVendor returns the canonical vendor string for the SNMP target,
// preferring the pre-resolved p.Vendor (populated by the discovery loop after
// sys-group resolution) and falling back to an in-flight sysObjectID lookup
// when Vendor is empty. The fallback path costs one extra SNMP GET; it exists
// so that the bgp package remains self-contained in tests that don't run the
// full discovery loop.
func resolveVendor(ctx context.Context, p snmputil.Params, client *gsnmp.GoSNMP) string {
	if p.Vendor != "" {
		return p.Vendor
	}
	return snmpSysObjectIDVendor(ctx, client)
}

// snmpSysObjectIDVendor does an SNMP GET of 1.3.6.1.2.1.1.2.0 (sysObjectID)
// and maps the result through snmputil.VendorFromObjectID. Returns "unknown"
// on any failure — never an error, since vendor resolution is best-effort.
func snmpSysObjectIDVendor(_ context.Context, client *gsnmp.GoSNMP) string {
	const oidSysObjectID = "1.3.6.1.2.1.1.2.0"
	pkt, err := client.Get([]string{oidSysObjectID})
	if err != nil || pkt == nil || len(pkt.Variables) == 0 {
		return "unknown"
	}
	v := pkt.Variables[0]
	if v.Type != gsnmp.ObjectIdentifier {
		return "unknown"
	}
	s, ok := v.Value.(string)
	if !ok {
		return "unknown"
	}
	return snmputil.VendorFromObjectID(s)
}
