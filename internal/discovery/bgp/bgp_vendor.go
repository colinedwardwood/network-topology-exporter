// Vendor-specific BGP4-V2 walkers. Each vendor that re-publishes the IETF
// draft form under its own enterprise OID has a thin walker here. The
// dispatcher in v2Walk picks one based on the resolved vendor string.
//
// Table OIDs and rationale:
//
//   - Cisco CISCO-BGP4-MIB::cbgpPeer2Table at 1.3.6.1.4.1.9.9.187.1.2.5
//     This is the long-form name; we walk it with the same InetAddress
//     index decoder used for the IETF draft form because the index encoding
//     is identical (peerType+peerAddrType+peerAddr is the table index).
//
//   - Juniper BGP4-V2-MIB-JUNIPER::jnxBgpM2PeerTable at 1.3.6.1.4.1.2636.5.1.1.2
//     Same index shape as Cisco / IETF.
//
//   - Nokia TIMETRA-BGP-MIB::tBgpPeerTable at 1.3.6.1.4.1.6527.3.1.2.13.2
//     Nokia uses a different column numbering but the same InetAddress index.
//
// All three share the same decoder; only the root OID and column numbers
// differ. To keep the code clean we factor the walk into a generic helper
// parameterised by a vendorTableSpec struct.

package bgp

import (
	"context"
	"log/slog"
	"net"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

// vendorTableSpec describes one vendor's BGP4-V2-equivalent peer table.
// Column numbers refer to the position under the table's row OID (i.e. the
// suffix after .<root>.1.).
type vendorTableSpec struct {
	name          string
	root          string // table root OID
	colState      int
	colRemoteAddr int
	colRemoteAs   int
}

// Vendor table specifications. Column numbers verified against each vendor's
// published MIB module (cited in the package doc); changes need a MIB diff,
// not just code review.
var (
	ciscoCbgpPeer2Spec = vendorTableSpec{
		name:          "cisco-cbgpPeer2Table",
		root:          "1.3.6.1.4.1.9.9.187.1.2.5",
		colState:      3,  // cbgpPeer2State
		colRemoteAddr: 11, // cbgpPeer2RemoteAddr
		colRemoteAs:   13, // cbgpPeer2RemoteAs
	}

	juniperJnxBgpM2PeerSpec = vendorTableSpec{
		name:          "juniper-jnxBgpM2PeerTable",
		root:          "1.3.6.1.4.1.2636.5.1.1.2.1.1",
		colState:      2,  // jnxBgpM2PeerState
		colRemoteAddr: 11, // jnxBgpM2PeerRemoteAddr
		colRemoteAs:   13, // jnxBgpM2PeerRemoteAs
	}

	nokiaTBgpPeerSpec = vendorTableSpec{
		name:          "nokia-tBgpPeerTable",
		root:          "1.3.6.1.4.1.6527.3.1.2.13.2",
		colState:      3, // tBgpPeerOperState
		colRemoteAddr: 6, // tBgpPeerRemoteAddress
		colRemoteAs:   7, // tBgpPeerRemoteAS
	}
)

// vendorSpecFor returns the vendor table spec for the given canonical vendor
// string, or nil if no vendor-specific walker is available.
func vendorSpecFor(vendor string) *vendorTableSpec {
	switch vendor {
	case "cisco":
		return &ciscoCbgpPeer2Spec
	case "juniper":
		return &juniperJnxBgpM2PeerSpec
	case "nokia", "alcatel-lucent":
		// Pre-acquisition Alcatel-Lucent 7705/7750 still ships TIMETRA-BGP-MIB.
		return &nokiaTBgpPeerSpec
	default:
		return nil
	}
}

// walkVendorPeerTable runs a generic walk of a vendor-specific peer table
// using the same InetAddress index decoder as bgp4V2PeerTable. Returns
// (peers, hadPDUs, ok, err) where hadPDUs reports whether BulkWalk produced
// any PDUs at all (used by the caller to distinguish "MIB not implemented"
// from "MIB implemented but no peers"); ok=false means no peer rows were
// assembled and the caller should fall back to RFC 4273.
func walkVendorPeerTable(ctx context.Context, client *gsnmp.GoSNMP, spec vendorTableSpec) (map[string]*bgp4V2Peer, bool, bool, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, spec.root)
	if err != nil {
		return nil, false, false, err
	}
	if len(pdus) == 0 {
		return nil, false, false, nil
	}

	prefix := "." + spec.root + ".1."
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

		peer := peers[rest]
		if peer == nil {
			localIP, remoteIP, ok := decodeBgp4V2Index(rest)
			if !ok {
				recordWalkerOutcome(vendorWalkerLabel(spec.name), "malformed_index")
				slog.Debug("bgp vendor: malformed index, dropping row", "walker", vendorWalkerLabel(spec.name), "vendor_table", spec.name, "index_suffix", truncateForLog(rest, 50))
				continue
			}
			peer = &bgp4V2Peer{indexLocalIP: localIP, indexRemoteIP: remoteIP}
			peers[rest] = peer
		}

		switch col {
		case spec.colState:
			peer.state = snmputil.PDUInt(pdu)
		case spec.colRemoteAs:
			peer.remoteAs = snmputil.PDUInt(pdu)
		case spec.colRemoteAddr:
			if ip := pduInetAddress(pdu); ip != nil {
				peer.remoteAddr = ip
			}
		}
	}
	if len(peers) == 0 {
		// PDUs arrived but every row was rejected by the index decoder, or
		// none matched the expected prefix. MIB is implemented; signal with
		// hadPDUs=true so the caller records "no_peers".
		return nil, true, false, nil
	}
	return peers, true, true, nil
}

// walkAndBuildVendorEdges runs the vendor-specific walk and converts peers
// to edges. Mirrors walkAndBuildV2Edges; the caller is responsible for
// picking the spec.
//
// Outcome accounting (issue #15): see walkAndBuildV2Edges for the
// mib_unimplemented vs no_peers split rationale.
func walkAndBuildVendorEdges(
	ctx context.Context,
	client *gsnmp.GoSNMP,
	spec vendorTableSpec,
	localDevice string,
	allowedNets []*net.IPNet,
) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, bool, error) {
	walker := vendorWalkerLabel(spec.name)
	peers, hadPDUs, ok, err := walkVendorPeerTable(ctx, client, spec)
	if err != nil {
		recordWalkerOutcome(walker, "error")
		return nil, nil, false, err
	}
	if !ok {
		if hadPDUs {
			recordWalkerOutcome(walker, "no_peers")
		} else {
			recordWalkerOutcome(walker, "mib_unimplemented")
		}
		return nil, nil, false, nil
	}
	edges, oos := buildV2Edges(localDevice, peers, allowedNets)
	if len(edges) > 0 {
		recordWalkerOutcome(walker, "edges")
	} else {
		recordWalkerOutcome(walker, "no_peers")
	}
	return edges, oos, true, nil
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
