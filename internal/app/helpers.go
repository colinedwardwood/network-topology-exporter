// Package app holds the discovery-engine runtime that cmd/topology-exporter
// instantiates: the discovery loop scheduler, single-cycle orchestration,
// credential-probe logic, and pure cycle-helper functions.
//
// httpx (sibling subpackage) holds HTTP handler factories; this package
// holds everything else that used to live in cmd/topology-exporter/main.go
// before it was extracted.
package app

import (
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// DeduplicateOOS removes duplicate OutOfScopeNeighbour entries that arise when
// multiple discovery protocols (e.g. LLDP and CDP) both observe the same
// out-of-scope neighbour on the same device/port. Uniqueness is keyed on
// (ReportingDevice, ReportingPort, NeighbourHint); the first occurrence is kept
// so the Proto field reflects the first protocol that reported the neighbour.
// The returned slice preserves insertion order.
func DeduplicateOOS(oos []discovery.OutOfScopeNeighbour) []discovery.OutOfScopeNeighbour {
	type oosKey struct {
		ReportingDevice string
		ReportingPort   string
		NeighbourHint   string
	}
	seen := make(map[oosKey]struct{}, len(oos))
	out := make([]discovery.OutOfScopeNeighbour, 0, len(oos))
	for _, n := range oos {
		k := oosKey{n.ReportingDevice, n.ReportingPort, n.NeighbourHint}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, n)
	}
	return out
}

// MergeOOSFirstSeen preserves FirstSeen timestamps across collection cycles.
// Each cycle, OutOfScopeNeighbour entries are built fresh with FirstSeen set to
// time.Now(). This function restores the original FirstSeen from prevOOS for any
// entry that was already known, keyed on (ReportingDevice, ReportingPort, NeighbourHint).
// Entries not present in prevOOS keep the cycle's time.Now() as their FirstSeen.
func MergeOOSFirstSeen(newOOS, prevOOS []discovery.OutOfScopeNeighbour) []discovery.OutOfScopeNeighbour {
	type oosKey struct {
		ReportingDevice string
		ReportingPort   string
		NeighbourHint   string
	}
	prevFirstSeen := make(map[oosKey]time.Time, len(prevOOS))
	for _, n := range prevOOS {
		k := oosKey{n.ReportingDevice, n.ReportingPort, n.NeighbourHint}
		prevFirstSeen[k] = n.FirstSeen
	}
	out := make([]discovery.OutOfScopeNeighbour, len(newOOS))
	copy(out, newOOS)
	for i := range out {
		k := oosKey{out[i].ReportingDevice, out[i].ReportingPort, out[i].NeighbourHint}
		if t, ok := prevFirstSeen[k]; ok {
			out[i].FirstSeen = t
		}
	}
	return out
}

// DeduplicateDevices removes Device entries with duplicate IDs that can arise
// when the same physical device is polled via multiple target addresses (e.g.
// primary IP and loopback IP both resolving to the same sysName). The first
// occurrence in config order is kept; callers must sort the slice by config
// index before calling this function to ensure deterministic results.
func DeduplicateDevices(devices []discovery.Device) []discovery.Device {
	seen := make(map[string]struct{}, len(devices))
	out := make([]discovery.Device, 0, len(devices))
	for _, d := range devices {
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		out = append(out, d)
	}
	return out
}

// CollectDegradedReasons walks edges and returns the unique set of reasons
// reported via the MetadataKeyDegradedReason key when MetadataKeyDegraded is
// "true". Used to surface module-level degradation in metrics with reason
// labels.
func CollectDegradedReasons(edges []discovery.Edge) []string {
	unique := make(map[string]bool)
	for _, e := range edges {
		if e.Metadata == nil || e.Metadata[discovery.MetadataKeyDegraded] != "true" {
			continue
		}
		reasons := strings.Split(e.Metadata[discovery.MetadataKeyDegradedReason], ",")
		if len(reasons) == 0 {
			reasons = []string{"unknown"}
		}
		for _, reason := range reasons {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "unknown"
			}
			unique[reason] = true
		}
	}
	reasons := make([]string, 0, len(unique))
	for reason := range unique {
		reasons = append(reasons, reason)
	}
	return reasons
}

// SynthesizeEdges resolves protocol-level observations into canonical graph edges.
//
// Phase 1 (done by callers): each protocol module emits observations with raw
// endpoints — MACs from FDB/LLDP, IPs from CDP/BGP — into rawEdges.
//
// Phase 2 (this function): resolve endpoints to sysName device IDs using the
// LLDP chassis-MAC index and ARP table, backfill missing DstPorts from LLDP
// observations that share the same three-endpoint tuple, and drop observations
// whose remote endpoint could not be resolved to a known device.
func SynthesizeEdges(
	logger *slog.Logger,
	rawEdges []discovery.Edge,
	ipToID map[string]string,
	arpMACToIP map[string]string,
	suppressedCounter prometheus.Counter,
) []discovery.Edge {
	// Build MAC→sysName index from LLDP chassis MAC annotations.
	macToID := make(map[string]string)
	for _, e := range rawEdges {
		if e.DiscoveryProto == discovery.DiscoveryProtocolLLDP {
			if mac, ok := e.Metadata[discovery.MetadataKeyPeerChassisMac]; ok && e.DstDevice != "" {
				hw, err := net.ParseMAC(mac)
				if err != nil {
					continue
				}
				dst := e.DstDevice
				// Skip entries where DstDevice is an IP (sysName absent) or a MAC
				// (unresolved); only store proper resolved names.
				if net.ParseIP(dst) != nil {
					continue
				}
				if _, err := net.ParseMAC(dst); err == nil {
					continue
				}
				if existing, exists := macToID[hw.String()]; exists {
					if existing != dst {
						logger.Debug("lldp: MAC chassis ID advertised by multiple devices; keeping first",
							"mac", hw.String(), "kept_device", existing, "discarded_device", dst)
					}
				} else {
					macToID[hw.String()] = dst
				}
			}
		}
	}
	// Second resolution path: ARP table. LLDP takes precedence.
	for mac, ip := range arpMACToIP {
		hw, err := net.ParseMAC(mac)
		if err != nil {
			continue
		}
		canonicalMac := hw.String()
		if _, resolved := macToID[canonicalMac]; resolved {
			continue
		}
		if id, ok := ipToID[ip]; ok {
			macToID[canonicalMac] = id
		}
	}

	edges := ResolveEdgeDstDevices(logger, rawEdges, ipToID, macToID, suppressedCounter)

	// Backfill DstPort on FDB edges from LLDP observations with matching endpoints.
	type epKey struct{ src, srcPort, dst string }
	lldpDstPort := make(map[epKey]string, len(edges))
	for _, e := range edges {
		if e.DiscoveryProto == discovery.DiscoveryProtocolLLDP && e.DstPort != "" {
			lldpDstPort[epKey{e.SrcDevice, e.SrcPort, e.DstDevice}] = e.DstPort
		}
	}
	for i := range edges {
		if edges[i].DiscoveryProto == discovery.DiscoveryProtocolFDB && edges[i].DstPort == "" {
			if p, ok := lldpDstPort[epKey{edges[i].SrcDevice, edges[i].SrcPort, edges[i].DstDevice}]; ok {
				edges[i].DstPort = p
			}
		}
	}
	return edges
}

// ResolveEdgeDstDevices replaces IP-valued and MAC-valued DstDevice fields with
// the canonical device ID (sysName) from the discovered inventory when available.
// BGP/OSPF/IS-IS walks report peer IPs; FDB reports raw MACs; LLDP reports sysNames.
// For IP DstDevices: resolves to sysName using the device walk inventory; unresolved
// IPs are kept (still useful for routing protocol edges).
// For MAC DstDevices: resolves to sysName via the LLDP identity index; unresolved
// MACs are suppressed (likely hosts, not infrastructure).
// suppressedCounter is incremented for each suppressed MAC; pass nil to skip.
func ResolveEdgeDstDevices(logger *slog.Logger, edges []discovery.Edge, ipToID map[string]string, macToID map[string]string, suppressedCounter prometheus.Counter) []discovery.Edge {
	result := make([]discovery.Edge, 0, len(edges))
	for i := range edges {
		e := edges[i]
		dst := e.DstDevice
		if net.ParseIP(dst) != nil {
			if id, ok := ipToID[dst]; ok {
				e.DstDevice = id
			}
			// unresolved IP: keep edge (still useful for routing protocol edges)
		} else if hw, err := net.ParseMAC(dst); err == nil {
			if id, ok := macToID[hw.String()]; ok {
				e.DstDevice = id
			} else if e.DiscoveryProto != discovery.DiscoveryProtocolFDB {
				// Non-FDB protocol (e.g. LLDP) with MAC DstDevice and no sysName:
				// the link is protocol-confirmed; keep the edge with MAC as DstDevice.
			} else {
				// FDB only: unresolved MAC is likely a host, not infrastructure.
				// Suppress rather than publish a mac-<hash> pseudo-device.
				logger.Debug("fdb: suppressing unresolved MAC peer; no LLDP correlation",
					"src_device", e.SrcDevice, "src_port", e.SrcPort, "mac", dst)
				if suppressedCounter != nil {
					suppressedCounter.Inc()
				}
				continue
			}
		}
		result = append(result, e)
	}
	return result
}
