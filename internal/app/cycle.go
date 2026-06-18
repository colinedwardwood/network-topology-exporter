package app

import (
	"context"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/credentials"
	"github.com/grafana/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/graph"
	"github.com/grafana/network-topology-exporter/internal/metrics"
	"github.com/grafana/network-topology-exporter/internal/tracing"
)

// LargeTopologyEdgeThreshold is the edge count above which the exporter
// emits a warning pointing operators at docs/operator/scale.md. Intentionally
// well below the documented scale ceiling so the warning arrives before
// scrape latency becomes a problem — not after. The warning fires on the
// upward crossing of this threshold (issue #9), not on every cycle while
// above it, and is rate-limited by LargeTopologyWarnCooldownCycles to keep
// an oscillating topology from flooding the log.
const LargeTopologyEdgeThreshold = 5000

// LargeTopologyWarnCooldownCycles caps the large-topology warning at one
// emission per N discovery cycles, even if the topology oscillates around
// the threshold and would otherwise re-cross upward on every cycle.
const LargeTopologyWarnCooldownCycles = 60

// MaybeWarnLargeTopology emits the large-topology warning when the edge
// count crosses LargeTopologyEdgeThreshold upward (was at-or-below on the
// previous cycle, now strictly above), subject to a cooldown of
// LargeTopologyWarnCooldownCycles between warnings. It returns the
// updated (prevAboveThreshold, lastWarnCycle) pair so the caller can
// thread state across cycles. Extracted from the cycle closure so the
// crossing rule can be unit-tested without driving full discovery cycles
// (issue #9).
func MaybeWarnLargeTopology(
	logger *slog.Logger,
	edges, devices int,
	prevAbove bool,
	cycleNum, lastWarnCycle int,
) (nowAbove bool, newLastWarnCycle int) {
	nowAbove = edges > LargeTopologyEdgeThreshold
	if nowAbove && !prevAbove && cycleNum-lastWarnCycle >= LargeTopologyWarnCooldownCycles {
		logger.Warn("topology size is large; review scale guidance",
			"edges", edges,
			"devices", devices,
			"threshold", LargeTopologyEdgeThreshold,
			"guidance", "docs/operator/scale.md")
		return nowAbove, cycleNum
	}
	return nowAbove, lastWarnCycle
}

// RunCycle probes all configured targets concurrently and returns the
// resulting graph, updated unconfirmed-age counters, any reconciliation
// conflicts, and the count of targets that failed discovery.
//
// walkerMetrics is threaded into every snmpwalk.Params constructed below so
// that protocol walkers (currently only bgp) can record outcome counters
// without holding a package-global counter handle. May be nil in tests; the
// walker is expected to treat nil as "drop the increment".
func RunCycle(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	m *metrics.Metrics,
	walkerMetrics snmpwalk.WalkerMetrics,
	warnLimiter snmpwalk.WarnLimiter,
	resolver *credentials.Resolver,
	allowedNets []*net.IPNet,
	prevAges map[graph.EdgeKey]int,
	pool *snmpwalk.SessionPool,
) (discovery.Graph, map[graph.EdgeKey]int, []graph.Conflict, int) {
	cycleCtx := ctx
	cycleCancel := func() {}
	if cfg.Discovery.CycleBudgetFraction > 0 {
		cycleDeadline := time.Now().Add(time.Duration(float64(cfg.Discovery.Interval) * cfg.Discovery.CycleBudgetFraction))
		cycleCtx, cycleCancel = context.WithDeadline(ctx, cycleDeadline)
	}
	defer cycleCancel()

	results := make([]probeResult, 0, len(cfg.Targets))
	allARPMACs := make(map[string]string) // mac → ip, merged from all polled devices
	var mu sync.Mutex
	sem := make(chan struct{}, cfg.Discovery.Parallelism)
	var wg sync.WaitGroup
	var okCount int64
	// Issue #20: track per-reason device-failure counts so the
	// network_topology_discovery_devices_total gauge can be emitted
	// partitioned by {status, reason}. Keys are the closed
	// metrics.DiscoveryFailReason enum. The pre-#20 unpartitioned
	// failCount is recovered as sum(failByReason) at emission time.
	failByReason := make(map[metrics.DiscoveryFailReason]int64)
	recordFail := func(reason metrics.DiscoveryFailReason) {
		mu.Lock()
		failByReason[reason]++
		mu.Unlock()
	}

	// Read-only deps shared by every probe goroutine. probeTarget captures
	// none of the cycle's mutable aggregation state (mu/results/okCount/
	// failByReason/allARPMACs) and no graph/ages handle (C6/C7).
	deps := probeDeps{
		cfg:           cfg,
		m:             m,
		walkerMetrics: walkerMetrics,
		warnLimiter:   warnLimiter,
		resolver:      resolver,
		allowedNets:   allowedNets,
		pool:          pool,
		logger:        logger,
	}

	for i, t := range cfg.Targets {
		target := t
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A recover() only catches its own goroutine's panics, so the
			// per-device panic-recover MUST wrap the probeTarget call here
			// rather than living inside probeTarget (C7).
			defer func() {
				if r := recover(); r != nil {
					logger.Error("per-device probe panicked", "target", target.Host, "panic", r)
					recordFail(metrics.DiscoveryReasonPanic)
				}
			}()
			// Cycle-budget admission. The budget early-exit is an
			// admission-shell concern and stays in the closure (C7).
			select {
			case sem <- struct{}{}:
			case <-cycleCtx.Done():
				m.CycleBudgetSkipsTotal.Inc()
				recordFail(metrics.DiscoveryReasonBudgetExpired)
				return
			}
			defer func() { <-sem }()

			res, arp, ok, failReason := probeTarget(cycleCtx, deps, target, idx)

			// mu-guarded aggregation (C7/C9). The allARPMACs merge is
			// performed here, at collection time, first-wins by goroutine
			// scheduling — NOT in the post-fan-out sorted pass — so which IP
			// wins a MAC conflict is unchanged.
			mu.Lock()
			if ok {
				results = append(results, res)
				okCount++
			} else {
				failByReason[failReason]++
			}
			for mac, ip := range arp {
				if existing, exists := allARPMACs[mac]; exists {
					if existing != ip {
						logger.Debug("arp: MAC seen with conflicting IPs across devices; keeping first",
							"mac", mac, "kept_ip", existing, "discarded_ip", ip)
					}
					continue
				}
				allARPMACs[mac] = ip
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Sort by config-file order so that DeduplicateDevices always picks the
	// first-configured target when two targets resolve to the same device ID.
	slices.SortStableFunc(results, func(a, b probeResult) int {
		return a.targetIdx - b.targetIdx
	})

	// Issue #20: emit the discovery-device gauge partitioned by
	// {status, reason}. status="success" carries reason=n/a; status="failed"
	// emits one series per reason in metrics.DiscoveryFailReason that was
	// observed this cycle. Reasons with zero hits are not emitted (the
	// gauge would be stale from the previous cycle — same as the
	// pre-#20 behaviour, but the partitioning means dashboards must use
	// `sum by (status)` to reproduce the old totals).
	m.DiscoveryDevicesTotal.Reset()
	m.DiscoveryDevicesTotal.WithLabelValues("success", metrics.ReasonNA).Set(float64(okCount))
	var failCount int64
	for reason, count := range failByReason {
		m.DiscoveryDevicesTotal.WithLabelValues("failed", string(reason)).Set(float64(count))
		failCount += count
	}

	// Aggregate per-module worst status across all devices and publish.
	worstStatus := map[string]int{}
	for _, r := range results {
		for proto, status := range r.moduleStatus {
			if status > worstStatus[proto] {
				worstStatus[proto] = status
			}
		}
	}
	for proto, status := range worstStatus {
		m.ModuleLastStatus.WithLabelValues(proto).Set(float64(status))
	}

	var devices []discovery.Device
	var rawEdges []discovery.Edge
	var allOOS []discovery.OutOfScopeNeighbour
	ipToID := make(map[string]string, len(results))
	for _, r := range results {
		if r.device != nil {
			devices = append(devices, *r.device)
			if r.mgmtIP != "" {
				if existing, ok := ipToID[r.mgmtIP]; ok {
					if existing != r.device.ID {
						logger.Debug("arp: management IP shared by multiple devices; keeping first",
							"ip", r.mgmtIP, "kept_device", existing, "discarded_device", r.device.ID)
					}
				} else {
					ipToID[r.mgmtIP] = r.device.ID
				}
			}
		}
		rawEdges = append(rawEdges, r.edges...)
		allOOS = append(allOOS, r.outOfScope...)
	}
	canonicalEdges := SynthesizeEdges(logger, rawEdges, ipToID, allARPMACs, m.FDBSuppressedMACs)

	// Phase 2 complete; run reconciliation. Issue #68: graph.reconcile span,
	// child of discovery.cycle, with per-source (discovery protocol) input edge
	// counts as attributes. No-op when tracing is disabled.
	_, reconcileSpan := tracing.Tracer().Start(ctx, "graph.reconcile")
	bySource := make(map[discovery.DiscoveryProtocol]int)
	for _, e := range canonicalEdges {
		bySource[e.DiscoveryProto]++
	}
	reconcileAttrs := make([]attribute.KeyValue, 0, len(bySource)+2)
	reconcileAttrs = append(reconcileAttrs, attribute.Int("reconcile.input_edges", len(canonicalEdges)))
	for proto, n := range bySource {
		reconcileAttrs = append(reconcileAttrs, attribute.Int("reconcile.edges."+string(proto), n))
	}
	reconciledEdges, conflicts := graph.Reconcile(canonicalEdges)
	reconcileAttrs = append(reconcileAttrs, attribute.Int("reconcile.output_edges", len(reconciledEdges)))
	reconcileSpan.SetAttributes(reconcileAttrs...)
	reconcileSpan.End()

	// LD-14: advance unconfirmed-link age counters and drop expired edges.
	ages := maps.Clone(prevAges)
	expired := graph.AgeUnconfirmed(reconciledEdges, ages, cfg.Discovery.UnconfirmedLinkTTLCycles)
	if len(expired) > 0 {
		expiredSet := make(map[graph.EdgeKey]bool, len(expired))
		for _, k := range expired {
			expiredSet[k] = true
		}
		kept := reconciledEdges[:0]
		for _, e := range reconciledEdges {
			if !expiredSet[graph.Key(e)] {
				kept = append(kept, e)
			}
		}
		reconciledEdges = kept
	}

	return discovery.Graph{
		Devices:    DeduplicateDevices(devices),
		Edges:      reconciledEdges,
		OutOfScope: DeduplicateOOS(allOOS),
	}, ages, conflicts, int(failCount)
}
