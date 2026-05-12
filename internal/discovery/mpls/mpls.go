// Package mpls infers MPLS-TE tunnel topology from MPLS-TE-MIB tables.
//
// Invariants:
// - Only operStatus up(1) emits edges.
// - operStatus decode errors are hard-fail (required signal).
// - adminStatus is optional metadata; failures degrade metadata only.
// - tunnel OID suffix must parse as {idx,inst,ingress4,egress4}.
package mpls

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	oidMplsTunnelOperStatus  = "1.3.6.1.2.1.10.166.3.2.2.1.17"
	oidMplsTunnelAdminStatus = "1.3.6.1.2.1.10.166.3.2.2.1.13"
	mplsTunnelOperUp         = 1
	metaKeyAdminStatus       = "mpls_te.admin_status"
	requiredMinValidRows     = 0
	requiredMaxInvalidRatio  = 0.50
	// precedenceRank 8: lowest priority in the graph merge ladder.
	// Ladder: LLDP=2, CDP=3, FDB=4, IS-IS=5, OSPF=6, BGP=7, MPLS-TE=8.
	// Higher rank = lower precedence in graph merge.
	precedenceRank = 8
)

// Walk returns MPLS-TE tunnel edges for the device at p.IP. Only tunnels with
// operStatus up(1) produce edges. Egress LSR IPs outside allowedNets go to the
// OutOfScopeNeighbour slice; pass nil to skip scope enforcement.
func Walk(ctx context.Context, p snmputil.Params, localDevice string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error) {
	client, err := snmputil.Open(p)
	if err != nil {
		return nil, nil, fmt.Errorf("mpls_te %s: %w", p.IP, err)
	}
	defer func() { _ = client.Conn.Close() }()

	operStatuses, operStats, err := snmputil.WalkToIntMapStrict(ctx, client, "mpls_te", oidMplsTunnelOperStatus)
	if err != nil {
		return nil, nil, fmt.Errorf("mpls_te tunnel table %s: %w", p.IP, err)
	}
	if _, hardFailReason := snmputil.EvaluateRequiredTablePolicy(operStats, snmputil.RequiredTablePolicy{
		MinValidRows:    requiredMinValidRows,
		MaxInvalidRatio: requiredMaxInvalidRatio,
	}); hardFailReason != "" {
		return nil, nil, &discovery.PolicyError{
			Module: "mpls_te",
			Reason: hardFailReason,
			Err:    fmt.Errorf("operStatus stats: valid=%d total=%d invalid=%d ratio=%.3f", operStats.ValidRows, operStats.TotalRows, operStats.InvalidRows, operStats.InvalidRatio),
		}
	}

	adminStatuses, adminStats, adminErr := snmputil.WalkToIntMapStrict(ctx, client, "mpls_te", oidMplsTunnelAdminStatus)
	degradedReasons := make([]string, 0, 2)
	if degraded, _ := snmputil.EvaluateRequiredTablePolicy(operStats, snmputil.RequiredTablePolicy{
		MinValidRows:    requiredMinValidRows,
		MaxInvalidRatio: requiredMaxInvalidRatio,
	}); degraded {
		degradedReasons = append(degradedReasons, discovery.DegradedReasonRequiredTablePartialDecode)
	}
	if adminErr != nil {
		slog.Debug("mpls_te: admin status walk failed; admin_status will be unknown", "device", p.IP, "err", adminErr)
		adminStatuses = nil
		degradedReasons = append(degradedReasons, discovery.DegradedReasonMissingAdminStatusWalk)
	} else if adminStats.InvalidRows > 0 {
		slog.Debug(
			"mpls_te: admin status decode anomalies; admin_status may be unknown",
			"device", p.IP,
			"decode_failures", adminStats.DecodeFailures,
			"trim_failures", adminStats.TrimFailures,
		)
		degradedReasons = append(degradedReasons, discovery.DegradedReasonInvalidAdminStatusDecode)
	}
	degradedReason := discovery.JoinReasonCodes(degradedReasons)

	now := time.Now()
	var edges []discovery.Edge
	var oos []discovery.OutOfScopeNeighbour

	for suffix, operStatus := range operStatuses {
		if operStatus != mplsTunnelOperUp {
			continue
		}
		tunnelIdx, egressIP, ok := parseTunnelSuffix(suffix)
		if !ok {
			continue
		}
		if egressIP.IsUnspecified() || egressIP.IsLinkLocalUnicast() {
			continue
		}
		if len(allowedNets) > 0 && !snmputil.IPInNets(egressIP, allowedNets) {
			oos = append(oos, discovery.OutOfScopeNeighbour{
				Proto:           "mpls_te",
				ReportingDevice: localDevice,
				ReportingPort:   fmt.Sprintf("te-tunnel%d", tunnelIdx),
				NeighbourHint:   egressIP.String(),
				FirstSeen:       now,
				LastSeen:        now,
			})
			continue
		}
		adminStatus := adminStatuses[suffix] // 0 if absent or decode-filtered
		metadata := map[string]string{
			metaKeyAdminStatus: mplsAdminStatusString(adminStatus),
		}
		if degradedReason != "" {
			metadata[discovery.MetadataKeyDegraded] = "true"
			metadata[discovery.MetadataKeyDegradedReason] = degradedReason
		}
		edges = append(edges, discovery.Edge{
			SrcDevice:      localDevice,
			SrcPort:        fmt.Sprintf("te-tunnel%d", tunnelIdx),
			DstDevice:      egressIP.String(),
			DiscoveryProto: "mpls_te",
			Direction:      discovery.DirectionUnidirectional,
			Confidence:     discovery.ConfidenceMedium,
			Adjacency:      discovery.AdjacencyUnknown,
			PrecedenceRank: precedenceRank,
			LinkKind:       "mpls-te",
			ObservedAt:     now,
			Metadata:       metadata,
		})
	}
	return edges, oos, nil
}

// mplsAdminStatusString converts a mplsTunnelAdminStatus integer value to a
// human-readable string. Values are defined in RFC 3812: up(1), down(2),
// testing(3). Zero indicates the value was absent from the SNMP walk.
func mplsAdminStatusString(v int) string {
	switch v {
	case 1:
		return "up"
	case 2:
		return "down"
	case 3:
		return "testing"
	default:
		return "unknown"
	}
}

// parseTunnelSuffix parses the OID suffix that follows the mplsTunnelOperStatus
// column prefix. The suffix must have exactly 10 dot-separated components:
// tunnelIdx, tunnelInstance, ig0..ig3, eg0..eg3. Returns the tunnel index as an
// integer and the egress LSR IPv4 address. Returns ok=false for any malformed
// suffix, including a non-integer tunnel index.
func parseTunnelSuffix(suffix string) (tunnelIdx int, egressIP net.IP, ok bool) {
	suffix = strings.TrimPrefix(suffix, ".")
	parts := strings.Split(suffix, ".")
	if len(parts) != 10 {
		return 0, nil, false
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, nil, false
	}
	if idx <= 0 {
		return 0, nil, false
	}
	ip, ipOK := parseIPFromParts(parts[6:10])
	if !ipOK {
		return 0, nil, false
	}
	return idx, ip, true
}

// parseIPFromParts converts 4 decimal-string octets into an IPv4 net.IP.
func parseIPFromParts(parts []string) (net.IP, bool) {
	if len(parts) != 4 {
		return nil, false
	}
	b := make([]byte, 4)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return nil, false
		}
		b[i] = byte(v)
	}
	return net.IP(b), true
}
