package app

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/bgp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/cdp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/fdb"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/isis"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/mpls"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/ospf"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
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
	type probeResult struct {
		targetIdx    int
		device       *discovery.Device
		edges        []discovery.Edge
		outOfScope   []discovery.OutOfScopeNeighbour
		mgmtIP       string
		moduleStatus map[string]int // proto -> 0 ok | 1 degraded | 2 failed
	}

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

	for i, t := range cfg.Targets {
		target := t
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("per-device probe panicked", "target", target.Host, "panic", r)
					recordFail(metrics.DiscoveryReasonPanic)
				}
			}()
			select {
			case sem <- struct{}{}:
			case <-cycleCtx.Done():
				m.CycleBudgetSkipsTotal.Inc()
				recordFail(metrics.DiscoveryReasonBudgetExpired)
				return
			}
			defer func() { <-sem }()

			// Issue #68: per-target span, child of discovery.cycle. targetCtx
			// flows into the credential resolve and every module walk so they
			// nest under target.poll. No-op when tracing is disabled.
			targetStart := time.Now()
			targetCtx, targetSpan := tracing.Tracer().Start(cycleCtx, "target.poll",
				trace.WithAttributes(attribute.String("target.ip", target.Host)))
			defer func() {
				targetSpan.SetAttributes(
					attribute.Float64("target.latency_seconds", time.Since(targetStart).Seconds()))
				targetSpan.End()
			}()

			ip := net.ParseIP(target.Host)
			if ip == nil {
				addrs, err := net.DefaultResolver.LookupHost(targetCtx, target.Host)
				if err != nil || len(addrs) == 0 {
					logger.Warn("host resolution failed", "host", target.Host, "error", err)
					recordFail(metrics.DiscoveryReasonDNSFailed)
					targetSpan.SetStatus(codes.Error, "dns resolution failed")
					return
				}
				ip = net.ParseIP(addrs[0])
			}

			// LD-11: enforce CIDR allow-list for hostname-based targets whose
			// IP is only known after DNS resolution.
			if len(allowedNets) > 0 && !snmpwalk.IPInNets(ip, allowedNets) {
				logger.Warn("resolved target outside allow-list, skipping",
					"host", target.Host, "ip", ip)
				recordFail(metrics.DiscoveryReasonOutsideAllowList)
				targetSpan.SetStatus(codes.Error, "outside allow-list")
				return
			}

			dev, params, profileName, err := WalkSystemWithCredentials(targetCtx, cfg, resolver, ip, target, logger)
			targetSpan.SetAttributes(attribute.String("credential.profile", profileName))
			if err != nil {
				logger.Warn("snmp walk failed", "target", target.Host, "error", err)
				targetSpan.RecordError(err)
				targetSpan.SetStatus(codes.Error, "credential resolve / system walk failed")
				m.DiscoveryHardFailTotal.WithLabelValues("system", "system_group_walk_error").Inc()
				m.CredentialTrialsTotal.WithLabelValues("failed").Inc()
				// Issue #20: partition the walk-failure counter by
				// sub-reason. Timeouts surface via status="timeout"
				// (reason=n/a — the status is the reason). Non-timeout
				// failures from this layer are attributed to auth: the
				// credential-rotation loop in WalkSystemWithCredentials
				// only returns a non-timeout error when at least one
				// candidate was rejected non-silently (DeadlineExceeded
				// is the silent-drop / unreachable case).
				if errors.Is(err, context.DeadlineExceeded) {
					m.SNMPWalksTotal.WithLabelValues("timeout", metrics.ReasonNA).Inc()
					recordFail(metrics.DiscoveryReasonTimeout)
				} else {
					m.SNMPWalksTotal.WithLabelValues("error", string(metrics.WalkReasonAuthFailed)).Inc()
					recordFail(metrics.DiscoveryReasonAuthFailed)
				}
				return
			}
			// Zeroize the winning credential bytes as soon as this device's
			// modules are finished, before the goroutine exits and params
			// becomes unreachable to a sensible cleanup. Issue #5.
			defer params.Zeroize()

			devCtx, cancel := context.WithTimeout(targetCtx, cfg.Discovery.TimeoutPerDevice)
			defer cancel()
			devCtx = snmpwalk.ContextWithDecodeIssueReporter(devCtx, func(issue snmpwalk.DecodeIssue) {
				m.DiscoveryDecodeIssues.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
				m.DiscoveryQuarantinedRowsTotal.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
			})
			// Nil-tolerant panic-reporter seam for the raw SNMP transport
			// goroutines (snmpwalk.BulkWalk / Walk spawn their own goroutine
			// stacks for BulkWalkAll/WalkAll/Get; this per-device recover does
			// not cover them). On a recovered transport panic the walk returns a
			// normal error and bumps network_topology_panics_total{site="snmp_walk"}
			// without the discovery package importing the prometheus client.
			devCtx = snmpwalk.ContextWithPanicReporter(devCtx, func(site string) {
				m.PanicsRecoveredTotal.WithLabelValues(site).Inc()
			})

			// Issue #72: per-target SNMP PDU rate limiter. When configured, cap
			// the steady-state request rate against this single device so a high
			// parallelism + all-modules run cannot self-DoS its SNMP daemon. One
			// limiter per device per cycle (per-target isolation — never shared
			// across devices). Burst is set equal to the rate so a freshly-built
			// limiter starts with a full 1-second bucket (no artificial stall on
			// the first walk) while still pacing sustained throughput to the
			// configured ceiling. Unset/0 injects nothing — zero overhead,
			// unchanged behaviour.
			if rps := cfg.Discovery.PerTargetPDURatePerSecond; rps > 0 {
				lim := rate.NewLimiter(rate.Limit(rps), rps)
				devCtx = snmpwalk.ContextWithRateLimiter(devCtx, lim)
				devCtx = snmpwalk.ContextWithRateLimitWaitObserver(devCtx, func(d time.Duration) {
					m.SNMPRateLimitWaitSeconds.Observe(d.Seconds())
				})
			}

			resolver.RecordSuccess(ip.String(), profileName)
			m.CredentialTrialsTotal.WithLabelValues("ok").Inc()
			m.SNMPWalksTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()

			dev.Site = target.Site
			for k, v := range target.Labels {
				if dev.Labels == nil {
					dev.Labels = make(map[string]string)
				}
				dev.Labels[k] = v
			}

			var allEdges []discovery.Edge
			var allOOS []discovery.OutOfScopeNeighbour

			// Propagate module-specific tuning into params. MaxVlans is only
			// consumed by fdb.Walk; Vendor and UseBGPV2MIB are only consumed
			// by bgp.Walk; other modules ignore them. UseBGPV2MIB is the
			// inverse of the operator-facing DisableV2MIB knob (default false
			// = v2 enabled). WalkerMetrics is read by bgp.Walk via the
			// recordWalkerOutcome helper; nil is tolerated (drops the
			// increment) so unit tests that build Params inline don't need
			// to wire a fake sink unless they care about the counter.
			params.MaxVlans = cfg.Modules.FDB.MaxVlans
			params.Vendor = dev.Vendor
			params.UseBGPV2MIB = !cfg.Modules.BGP.DisableV2MIB
			params.WalkerMetrics = walkerMetrics
			params.WarnLimiter = warnLimiter
			// Nil-tolerant panic-reporter seam (ops hardening): the FDB module
			// spawns one goroutine per VLAN and recovers a panic locally so one
			// bad VLAN can't crash discovery; this closure lets it bump
			// network_topology_panics_total{site} without the discovery package
			// importing the prometheus client (mirrors the WalkerMetrics seam).
			params.PanicReporter = func(site string) {
				m.PanicsRecoveredTotal.WithLabelValues(site).Inc()
			}
			// Issue #83: opt-in per-target SNMP session pool. When pool is nil
			// (the default), params.Pool stays nil and snmpwalk.Acquire uses the
			// fresh open+close path — byte-identical to pre-#83 behaviour. When a
			// pool is wired, the (IP, CredentialProfile) key lets a target reuse
			// one session across its sequential module walks. CredentialProfile
			// is the winning profile from WalkSystemWithCredentials above; it
			// must be part of the key so a credential change yields a new entry
			// and InvalidateProfile can evict a rotated profile's sessions.
			params.Pool = pool
			params.CredentialProfile = profileName

			mods := []Module{
				{"lldp", cfg.Modules.LLDP.Enabled, lldp.Walk},
				{"cdp", cfg.Modules.CDP.Enabled, cdp.Walk},
				{"fdb", cfg.Modules.FDB.Enabled, fdb.Walk},
				{"ospf", cfg.Modules.OSPF.Enabled, ospf.Walk},
				{"bgp", cfg.Modules.BGP.Enabled, bgp.Walk},
				{"isis", cfg.Modules.ISIS.Enabled, isis.Walk},
				{"mpls_te", cfg.Modules.MPLSTE.Enabled, mpls.Walk},
			}
			// Issue #74: resolve this target's per-protocol scope once. When an
			// override matches, only the listed modules run (intersected with
			// the global mod.Enabled gate below); when none matches, allowed is
			// nil/overrideMatched=false and ALL enabled modules run — today's
			// unchanged default. The headline case: bgp.Walk only fires where
			// bgp is in the override's module set, so the walker outcome
			// counters partition naturally.
			allowedMods, overrideMatched := cfg.ModulesForIP(ip)
			modStatus := map[string]int{}
			for _, mod := range mods {
				if !mod.Enabled {
					continue
				}
				// Per-target protocol scoping: an override never widens beyond
				// mod.Enabled (intersection), it only narrows.
				if overrideMatched && !allowedMods[mod.Proto] {
					continue
				}
				modStart := time.Now()
				// Issue #68: per-module span, child of target.poll. Span name
				// is "<proto>.walk" (e.g. lldp.walk, bgp.walk; the MPLS module
				// uses its mpls_te proto identifier → mpls_te.walk). No-op when
				// tracing is disabled.
				modCtx, modSpan := tracing.Tracer().Start(devCtx, mod.Proto+".walk")
				var modCancel context.CancelFunc = func() {}
				if cfg.Discovery.TimeoutPerModule > 0 {
					modCtx, modCancel = context.WithTimeout(modCtx, cfg.Discovery.TimeoutPerModule)
				}
				edges, oos, err := mod.Walk(modCtx, params, dev.ID, allowedNets)
				modCancel()
				m.DiscoveryModuleDuration.WithLabelValues(mod.Proto).Observe(time.Since(modStart).Seconds())
				modSpan.SetAttributes(attribute.Int("walk.pdu_count", len(edges)))
				if err != nil {
					logger.Debug(mod.Proto+" walk failed", "target", target.Host, "error", err)
					modSpan.SetAttributes(attribute.String("walk.outcome", "failed"))
					modSpan.RecordError(err)
					modSpan.SetStatus(codes.Error, "module walk failed")
					modSpan.End()
					reason := "module_walk_error"
					var policyErr *discovery.PolicyError
					if errors.As(err, &policyErr) && policyErr.Reason != "" {
						reason = policyErr.Reason
					}
					m.DiscoveryHardFailTotal.WithLabelValues(mod.Proto, reason).Inc()
					// Issue #20: partition by walk sub-reason. Timeouts
					// keep reason=n/a; module-level non-timeout errors
					// are tagged WalkReasonModuleError. Per-module richer
					// breakdowns (auth_failed at the module layer is
					// already excluded — credentials succeeded for the
					// system walk above) would require module-walker
					// changes and are not in #20's scope.
					if errors.Is(err, context.DeadlineExceeded) {
						m.SNMPWalksTotal.WithLabelValues("timeout", metrics.ReasonNA).Inc()
					} else {
						m.SNMPWalksTotal.WithLabelValues("error", string(metrics.WalkReasonModuleError)).Inc()
					}
					modStatus[mod.Proto] = 2
					continue
				}
				m.SNMPWalksTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
				degradedReasons := CollectDegradedReasons(edges)
				for _, reason := range degradedReasons {
					m.DiscoveryDegradedTotal.WithLabelValues(mod.Proto, reason).Inc()
				}
				if len(degradedReasons) > 0 {
					modSpan.SetAttributes(attribute.String("walk.outcome", "degraded"))
					if _, ok := modStatus[mod.Proto]; !ok {
						modStatus[mod.Proto] = 1
					}
				} else {
					modSpan.SetAttributes(attribute.String("walk.outcome", "ok"))
					if _, ok := modStatus[mod.Proto]; !ok {
						modStatus[mod.Proto] = 0
					}
				}
				modSpan.End()
				allEdges = append(allEdges, edges...)
				// Tag each OOS entry with the protocol that reported it so the
				// boundary_observation_info metric and hub OOS matching have a
				// proto label.
				for i := range oos {
					oos[i].Proto = mod.Proto
				}
				allOOS = append(allOOS, oos...)
			}

			// Walk ARP table for MAC→IP enrichment when modules.arp.enabled
			// is true (default). The map feeds SynthesizeEdges below as a
			// fallback for FDB-only edges where LLDP did not provide the
			// neighbour identity. Failures are non-fatal: LLDP-based
			// correlation still works without ARP data.
			if cfg.Modules.ARP.Enabled {
				arpClient, arpRelease, arpErr := snmpwalk.Acquire(params)
				if arpErr != nil {
					logger.Debug("ARP table walk failed; MAC→IP resolution unavailable for this device",
						"device", dev.ID, "err", arpErr)
				} else {
					arpMACToIP, arpErr := snmpwalk.WalkARPTable(devCtx, arpClient)
					arpRelease()
					if arpErr != nil {
						logger.Debug("ARP table walk failed; MAC→IP resolution unavailable for this device",
							"device", dev.ID, "err", arpErr)
					} else {
						mu.Lock()
						for mac, ip := range arpMACToIP {
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
					}
				}
			}

			mu.Lock()
			results = append(results, probeResult{targetIdx: idx, device: dev, edges: allEdges, outOfScope: allOOS, mgmtIP: ip.String(), moduleStatus: modStatus})
			okCount++
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
