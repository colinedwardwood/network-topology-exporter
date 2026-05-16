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
	"sync/atomic"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

// walkerOutcomeCounter is the package-level sink for BGP walker outcome
// observations. It is set once at process startup by main via
// SetWalkerOutcomeCounter and is safe to read from any goroutine after that —
// the atomic.Pointer guarantees a happens-before edge between Set and Load
// across goroutines, and Load returning nil is the documented "not wired"
// state used in tests that don't spin up the full process.
//
// Why a package-level setter rather than threading the counter through
// snmputil.Params or the Walk signature: the project's module dispatch in
// cmd/topology-exporter/main.go invokes every protocol walker through a
// shared func signature (ctx, params, deviceID, allowedNets) → (edges, oos,
// error). Adding a metrics handle to either the signature or Params bleeds
// observability plumbing into every other module that doesn't need it.
// Option A keeps the change scoped to this package and matches how
// non-call-site config is already handled by other modules that need
// process-wide singletons. The trade-off is that tests must either set the
// counter explicitly (and reset it in cleanup) or accept nil (which the
// helpers below handle without panicking).
var walkerOutcomeCounter atomic.Pointer[prometheus.CounterVec]

// SetWalkerOutcomeCounter wires the package's outcome counter. Call once at
// startup before any Walk invocation; subsequent calls overwrite the previous
// value. Passing nil disables outcome accounting (useful in tests).
func SetWalkerOutcomeCounter(c *prometheus.CounterVec) {
	walkerOutcomeCounter.Store(c)
}

// recordWalkerOutcome increments the {walker, outcome} counter if one is wired.
// Safe to call when no counter is set — the call is a cheap atomic load + nil
// check, so production paths can call it unconditionally.
func recordWalkerOutcome(walker, outcome string) {
	if c := walkerOutcomeCounter.Load(); c != nil {
		c.WithLabelValues(walker, outcome).Inc()
	}
}

// Walker label constants. Keep these in sync with the metric's documented
// label set in internal/metrics/metrics.go.
const (
	walkerV2Draft       = "v2_draft"
	walkerVendorCisco   = "vendor_cisco"
	walkerVendorJuniper = "vendor_juniper"
	walkerVendorNokia   = "vendor_nokia"
	walkerRFC4273       = "rfc4273"
)

// vendorWalkerLabel maps a vendorTableSpec.name to its outcome counter label.
// Centralised so test assertions match the dispatcher exactly.
func vendorWalkerLabel(specName string) string {
	switch specName {
	case ciscoCbgpPeer2Spec.name:
		return walkerVendorCisco
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
//     jnxBgpM2PeerTable, Nokia tBgpPeerTable) selected by p.Vendor. If
//     non-empty, used exclusively. Surfaces IPv6 sessions that RFC 4273
//     cannot represent.
//  3. RFC 4273 bgpPeerTable — final fallback, IPv4-only.
//
// When p.UseBGPV2MIB is false, only step 3 runs. This kill-switch exists so
// operators who hit a vendor regression in the v2 walker can revert to the
// pre-v1.3.0 IPv4-only behaviour with one config flag.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("bgp %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	// v2Err holds a v2 draft walk error so we can promote it to Warn iff a
	// later walker succeeds. Per issue #8: a silently-discarded v2 error
	// while RFC 4273 limps along masks the real failure (vendor MIB column
	// drift) — log at Warn when the fallback chain papered over a v2 error.
	var v2Err error
	// vendorErr / vendorSpec capture the vendor-walk error for the same
	// promotion rationale.
	var vendorErr error
	var vendorSpec *vendorTableSpec

	if p.UseBGPV2MIB {
		// Step 1: try the IETF draft form first.
		edges, oos, ok, err := walkAndBuildV2Edges(ctx, client, localDevice, allowedNets)
		if err != nil {
			// A walk error here doesn't fail the module — the device may simply
			// not implement the draft. Stash the error and fall through; if a
			// later walker succeeds we promote this to Warn (see end of fn).
			v2Err = err
		} else if ok {
			return edges, oos, nil
		}

		// Step 2: try the vendor-specific table.
		if spec := vendorSpecFor(resolveVendor(ctx, p, client)); spec != nil {
			vendorSpec = spec
			edges, oos, ok, err := walkAndBuildVendorEdges(ctx, client, *spec, localDevice, allowedNets)
			if err != nil {
				vendorErr = err
			} else if ok {
				if v2Err != nil {
					slog.Warn("bgp v2: draft walk error, vendor table succeeded", "target", p.IP, "error", v2Err, "vendor_table", spec.name)
				}
				return edges, oos, nil
			}
		}
	}

	// Step 3 (always-on fallback): RFC 4273 bgpPeerTable.
	//
	// Outcome accounting (issue #15): the RFC 4273 path now distinguishes
	// "mib_unimplemented" (BulkWalk produced zero PDUs — device does not
	// support the RFC 4273 BGP4-MIB at all, expected for non-BGP devices)
	// from "no_peers" (PDUs arrived but no peer reached established —
	// device implements the MIB but BGP is down). Operators alerting on
	// the legacy "empty" outcome should switch to "no_peers".
	peers, hadPDUs, err := walkBgpPeerTable(ctx, client)
	if err != nil {
		recordWalkerOutcome(walkerRFC4273, "error")
		return nil, nil, fmt.Errorf("bgp peer table %s: %w", p.IP, err)
	}

	edges, oos := buildEdges(localDevice, peers, allowedNets)
	if len(edges) > 0 {
		recordWalkerOutcome(walkerRFC4273, "edges")
	} else if hadPDUs {
		recordWalkerOutcome(walkerRFC4273, "no_peers")
	} else {
		recordWalkerOutcome(walkerRFC4273, "mib_unimplemented")
	}

	// Promote earlier errors to Warn now that a later walker delivered. If
	// neither v2 nor the vendor walker errored, this block is a no-op.
	if v2Err != nil {
		slog.Warn("bgp v2: draft walk error, RFC 4273 fallback succeeded", "target", p.IP, "error", v2Err)
	}
	if vendorErr != nil && vendorSpec != nil {
		slog.Warn("bgp v2: vendor walk error, RFC 4273 fallback succeeded", "target", p.IP, "vendor_table", vendorSpec.name, "error", vendorErr)
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
