// Package bgp infers L3 topology from BGP peer adjacencies.
//
// # Specification sources
//
//   - RFC 4273 — Definitions of Managed Objects for BGP-4. OID base
//     1.3.6.1.2.1.15. bgpPeerTable (1.3.6.1.2.1.15.3) contains one row per BGP
//     peer; the relevant fields are bgpPeerState (.2), bgpPeerRemoteAddr (.7),
//     and bgpPeerRemoteAs (.9). A peer in state established(6) is an active
//     adjacency. RFC 4273 supersedes the 1994 RFC 1657 BGP4-MIB; the table
//     structure is identical so deployed devices implementing either RFC walk
//     the same OIDs.
//
// # Design references
//
//   - Donnet, Friedman — "Internet Topology Discovery: A Survey", IEEE
//     Communications Surveys and Tutorials, vol. 9 no. 4, 2007. Places BGP
//     table analysis in the broader context of AS-level topology discovery;
//     confirms that BGP adjacency is a logical relationship, not evidence of
//     physical proximity. https://hal.science/hal-01151820
//   - Motamedi, Rejaie, Willinger — "A Survey of Techniques for Internet
//     Topology Discovery", IEEE COMST 2015. Surveys BGP-based topology methods
//     and their limitations; the key lesson is that BGP peers are often not
//     directly connected at L2.
//     https://ieeexplore.ieee.org/document/6970764/
//   - prometheus/snmp_exporter (Apache 2.0) — BulkWalk table subtree,
//     group columns by index key.
//     https://github.com/prometheus/snmp_exporter
//
// # Notes
//
//   - BGP adjacency means the two routers exchange BGP UPDATE messages; it
//     does not mean they are physically adjacent. iBGP sessions routinely
//     span multiple hops through intermediate switches. This module emits
//     edges with Confidence=low and Adjacency=unknown to reflect this.
//   - RFC 4273 BGP4-MIB SNMP support is deprecated or incomplete in many
//     modern router OS versions (Cisco IOS-XR, Arista EOS, Juniper Junos
//     post-18.x). SNMP BGP walks may return empty results on modern gear.
//     Streaming telemetry (gNMI) is the preferred path for BGP adjacency
//     on those platforms, but is out of scope for v1.
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

// recordWalkerOutcome forwards a {walker, outcome} observation to the
// metrics sink carried on Params. Walker outcome accounting is dependency-
// injected — the bgp package no longer holds a package-level counter handle.
// See snmputil.WalkerMetrics for the interface and cmd/topology-exporter/
// main.go for the adapter that wraps Metrics.BGPWalkerOutcomeTotal.
//
// nil-safety: drop the increment, don't crash. p may be nil in unit tests
// that exercise recordWalkerOutcome directly, and p.WalkerMetrics may be
// nil when discovery cycles run before main has finished wiring (or in
// tests that intentionally inject no sink to verify the drop path).
func recordWalkerOutcome(p *snmputil.Params, walker, outcome string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordWalkerOutcome(walker, outcome)
}

// Walker label constants. Keep these in sync with the metric's documented
// label set in internal/metrics/metrics.go and docs/metrics.md.
//
// The "v2_draft" label that previously identified the (now removed)
// IETF-draft-OID walker was retired in issue #31. Operators alerting on
// network_topology_bgp_walker_outcome_total{walker="v2_draft"} must
// migrate to one of the remaining labels (typically vendor_arista or
// rfc4273 depending on the device fleet).
const (
	walkerVendorCisco   = "vendor_cisco"
	walkerVendorArista  = "vendor_arista"
	walkerVendorJuniper = "vendor_juniper"
	walkerVendorNokia   = "vendor_nokia"
	walkerRFC4273       = "rfc4273"
)

// Walker outcome label values for network_topology_bgp_walker_outcome_total.
// The set is closed — only these values are emitted. Operators alert on
// these label values; adding/removing is a breaking change tracked in
// CHANGELOG. Keep in sync with the metric's documented label set in
// internal/metrics/metrics.go and docs/metrics.md.
//
// Operational meaning (the four-bucket categorisation that issue #27 settled):
//
//   - outcomeEdges            — success: ≥1 peer reached bgpStateEstablished
//     and produced a discovery.Edge.
//   - outcomeMIBUnimplemented — BulkWalk returned zero PDUs; the device does
//     not implement the table at all. Expected on non-BGP devices, MUST NOT
//     page.
//   - outcomeNoPeers          — PDUs arrived AND at least one row decoded
//     cleanly, but no peer reached bgpStateEstablished. The device speaks
//     BGP, the MIB is implemented, every session is down. SHOULD page —
//     this is the canonical "BGP broken" signal.
//   - outcomeMalformedIndex   — per-row counter incremented inside the
//     walker for each row whose index suffix was rejected by the spec's
//     decodeIndex. Soft signal — a non-zero rate means walker drift on
//     this vendor's MIB but at least one peer still decoded. Warn-level.
//   - outcomeWalkerDrift      — PDUs arrived but EVERY row was rejected
//     by decodeIndex; zero peers assembled. Operationally distinct from
//     mib_unimplemented (the device DOES implement the MIB, our decoder
//     just doesn't match) and from no_peers (which assumes at least one
//     row decoded cleanly and was simply in a non-Established state).
//     Page-level signal that the walker is broken on this vendor.
//   - outcomeError            — the SNMP walk itself errored; the next
//     walker in the fallback chain will be tried.
const (
	outcomeEdges            = "edges"
	outcomeMIBUnimplemented = "mib_unimplemented"
	outcomeNoPeers          = "no_peers"
	outcomeMalformedIndex   = "malformed_index"
	outcomeWalkerDrift      = "walker_drift"
	outcomeError            = "error"
)

// vendorWalkerLabel maps a vendorTableSpec.name to its outcome counter label.
// Centralised so test assertions match the dispatcher exactly.
func vendorWalkerLabel(specName string) string {
	switch specName {
	case ciscoCbgpPeer2Spec.name:
		return walkerVendorCisco
	case aristaBgp4v2Spec.name:
		return walkerVendorArista
	case juniperJnxBgpM2PeerSpec.name:
		return walkerVendorJuniper
	case nokiaTBgpPeerSpec.name:
		return walkerVendorNokia
	default:
		return "vendor_unknown"
	}
}

const (
	oidBgpPeerTable = "1.3.6.1.2.1.15.3"
	// precedenceRank 7: ranked below OSPF (6) and IS-IS (5).
	// Ladder: LLDP=2, CDP=3, FDB=4, IS-IS=5, OSPF=6, BGP=7, MPLS-TE=8.
	precedenceRank = 7
)

// bgpPeerTable column numbers (RFC 4273 §3).
const (
	colBgpPeerState      = 2
	colBgpPeerRemoteAddr = 7
	colBgpPeerRemoteAs   = 9
)

const bgpStateEstablished = 6

const metaKeyRemoteAs = "bgp.remote_as"

type bgpPeer struct {
	state    int
	remoteIP net.IP
	remoteAs int
}

// Walk returns BGP-peer edges for the device at p.IP. Only peers in
// state established(6) produce edges. Peers outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
//
// Walker selection (when p.UseBGPV2MIB is true, which is the default):
//
//  1. bgp4V2PeerTable (IETF draft form) — covers Arista natively and any
//     other vendor that implements the draft. If non-empty, used exclusively.
//  2. Vendor-specific peer table (Cisco cbgpPeer2Table, Juniper
//     jnxBgpM2PeerTable, Nokia tBgpPeerTable, Arista enterprise BGP4V2)
//     selected by p.Vendor. If non-empty, used exclusively. Surfaces IPv6
//     sessions that RFC 4273 cannot represent.
//  2. RFC 4273 bgpPeerTable — final fallback, IPv4-only.
//
// When p.UseBGPV2MIB is false, only step 2 runs. This kill-switch exists so
// operators who hit a vendor regression in the vendor walker can revert
// to the pre-v1.3.0 IPv4-only behaviour with one config flag.
//
// History: a "v2_draft" walker that probed the IETF draft form OID
// 1.3.6.1.3.5.1.1.2 was previously Step 1 of this chain. Issue #31
// removed it after real-device captures showed no vendor implements the
// draft at that OID; each vendor publishes under its enterprise OID
// instead. See plans/bgp4v2-ipv6.md for the design history.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("bgp %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	// vendorErr / vendorSpec capture a vendor-walk error so we can promote
	// it to Warn iff RFC 4273 succeeds afterwards. Per issue #8: a silently
	// discarded vendor error while RFC 4273 limps along masks the real
	// failure (vendor MIB column drift); the Warn surfaces it once we know
	// the device at least responds to BGP-related SNMP.
	var vendorErr error
	var vendorSpec *vendorTableSpec

	if p.UseBGPV2MIB {
		// Step 1: try the vendor-specific table.
		if spec := vendorSpecFor(resolveVendor(ctx, p, client)); spec != nil {
			vendorSpec = spec
			edges, oos, ok, err := walkAndBuildVendorEdges(ctx, &p, client, *spec, localDevice, allowedNets)
			if err != nil {
				vendorErr = err
			} else if ok {
				return edges, oos, nil
			}
		}
	}

	// Step 2 (always-on fallback): RFC 4273 bgpPeerTable.
	//
	// Outcome accounting (issue #15): the RFC 4273 path distinguishes
	// "mib_unimplemented" (BulkWalk produced zero PDUs — device does not
	// support the RFC 4273 BGP4-MIB at all, expected for non-BGP devices)
	// from "no_peers" (PDUs arrived but no peer reached established —
	// device implements the MIB but BGP is down).
	peers, hadPDUs, err := walkBgpPeerTable(ctx, client)
	if err != nil {
		recordWalkerOutcome(&p, walkerRFC4273, outcomeError)
		return nil, nil, fmt.Errorf("bgp peer table %s: %w", p.IP, err)
	}

	edges, oos := buildEdges(localDevice, peers, allowedNets)
	if len(edges) > 0 {
		recordWalkerOutcome(&p, walkerRFC4273, outcomeEdges)
	} else if hadPDUs {
		recordWalkerOutcome(&p, walkerRFC4273, outcomeNoPeers)
	} else {
		recordWalkerOutcome(&p, walkerRFC4273, outcomeMIBUnimplemented)
	}

	// Promote a stashed vendor-walker error to Warn now that RFC 4273
	// delivered. If the vendor path didn't error, this is a no-op.
	if vendorErr != nil && vendorSpec != nil {
		slog.Warn("bgp vendor: walk error, RFC 4273 fallback succeeded",
			"target", p.IP, "vendor_table", vendorSpec.name, "error", vendorErr)
	}
	return edges, oos, nil
}

// walkBgpPeerTable walks the RFC 4273 bgpPeerTable. Returns (peers, hadPDUs,
// err) where hadPDUs reports whether the underlying BulkWalk produced any
// PDUs at all. The caller uses hadPDUs to distinguish "MIB not implemented"
// (zero PDUs — record outcome="mib_unimplemented") from "MIB implemented but
// no established peers" (PDUs arrived but every peer was filtered out —
// record outcome="no_peers"). See issue #15.
func walkBgpPeerTable(ctx context.Context, client *gsnmp.GoSNMP) (map[string]*bgpPeer, bool, error) {
	pdus, err := snmputil.BulkWalk(ctx, client, oidBgpPeerTable)
	if err != nil {
		return nil, false, err
	}
	hadPDUs := len(pdus) > 0
	const prefix = ".1.3.6.1.2.1.15.3.1."
	peers := make(map[string]*bgpPeer)
	for _, pdu := range pdus {
		suffix, ok := snmputil.TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		col, ipKey, ok := snmputil.SplitOIDComponent(suffix)
		if !ok || ipKey == "" {
			continue
		}
		peer := peers[ipKey]
		if peer == nil {
			peer = &bgpPeer{}
			peers[ipKey] = peer
		}
		switch col {
		case colBgpPeerState:
			peer.state = snmputil.PDUInt(pdu)
		case colBgpPeerRemoteAddr:
			peer.remoteIP = snmputil.PDUIPv4(pdu)
		case colBgpPeerRemoteAs:
			peer.remoteAs = snmputil.PDUInt(pdu)
		}
	}
	return peers, hadPDUs, nil
}

func buildEdges(localDevice string, peers map[string]*bgpPeer, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for ipKey, peer := range peers {
		if peer.state != bgpStateEstablished {
			continue
		}
		if peer.remoteIP == nil {
			slog.Debug("bgp: peer missing remote address, skipping", "local_device", localDevice, "peer_key", ipKey)
			continue
		}
		if peer.remoteIP.IsUnspecified() || peer.remoteIP.IsLinkLocalUnicast() {
			continue
		}

		if len(allowedNets) > 0 && !snmputil.IPInNets(peer.remoteIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "bgp",
				ReportingDevice: localDevice,
				NeighbourHint:   peer.remoteIP.String(),
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
			DstDevice:      peer.remoteIP.String(),
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
