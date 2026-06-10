package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// probeResult is the per-target output of a successful probe. RunCycle
// aggregates these (under mu) into the cycle's device/edge/OOS sets. It carries
// no shared state; the caller owns all aggregation (C6/C7).
type probeResult struct {
	targetIdx    int
	device       *discovery.Device
	edges        []discovery.Edge
	outOfScope   []discovery.OutOfScopeNeighbour
	mgmtIP       string
	moduleStatus map[string]int // proto -> 0 ok | 1 degraded | 2 failed
}

// probeDeps bundles the read-only dependencies probeTarget needs. These are the
// values the RunCycle probe closure captured before the #153 extraction; none
// of them is cycle-shared mutable state (no mu/results/okCount/failByReason/
// allARPMACs and no graph/ages handle — the caller owns all of that, C6/C7).
type probeDeps struct {
	cfg           *config.Config
	m             *metrics.Metrics
	walkerMetrics snmpwalk.WalkerMetrics
	warnLimiter   snmpwalk.WarnLimiter
	resolver      *credentials.Resolver
	allowedNets   []*net.IPNet
	pool          *snmpwalk.SessionPool
	logger        *slog.Logger
}

// probeTarget runs the full per-device probe for one configured target and
// returns its result. It does NOT touch the cycle's shared
// mu/results/okCount/failByReason/allARPMACs (the caller aggregates under mu,
// C7/C9) and takes no graph/ages handle (it is read-only w.r.t. the published
// graph, C3).
//
// params is built (by the credential walk) AND zeroized here via
// defer params.Zeroize() because its lifetime spans the module walk AND the ARP
// step (spec C1); walkModules receives the already-mutated params and never
// owns its Zeroize.
//
// On every early-exit fail path probeTarget returns ok=false plus the
// metrics.DiscoveryFailReason the caller records into failByReason — matching
// the pre-extraction inline behaviour for DNS failure, allow-list reject, and
// auth/timeout. Fail paths that fire their own inline metrics (the system-walk
// failure layer's DiscoveryHardFailTotal/CredentialTrialsTotal/SNMPWalksTotal)
// fire them here so they remain identical. The goroutine panic-recover and the
// cycle-budget admission are admission-shell concerns and stay in the caller's
// closure (C7).
//
// cycleCtx is the (optionally budget-bounded) cycle context; the per-target
// span descends from it so span parenting is unchanged (C4).
func probeTarget(cycleCtx context.Context, deps probeDeps, target config.TargetConfig, idx int) (res probeResult, arp map[string]string, ok bool, failReason metrics.DiscoveryFailReason) {
	cfg := deps.cfg
	m := deps.m
	logger := deps.logger

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
			targetSpan.SetStatus(codes.Error, "dns resolution failed")
			return probeResult{}, nil, false, metrics.DiscoveryReasonDNSFailed
		}
		ip = net.ParseIP(addrs[0])
	}

	// LD-11: enforce CIDR allow-list for hostname-based targets whose
	// IP is only known after DNS resolution.
	if len(deps.allowedNets) > 0 && !snmpwalk.IPInNets(ip, deps.allowedNets) {
		logger.Warn("resolved target outside allow-list, skipping",
			"host", target.Host, "ip", ip)
		targetSpan.SetStatus(codes.Error, "outside allow-list")
		return probeResult{}, nil, false, metrics.DiscoveryReasonOutsideAllowList
	}

	dev, params, profileName, err := WalkSystemWithCredentials(targetCtx, cfg, deps.resolver, ip, target, logger)
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
			return probeResult{}, nil, false, metrics.DiscoveryReasonTimeout
		}
		m.SNMPWalksTotal.WithLabelValues("error", string(metrics.WalkReasonAuthFailed)).Inc()
		return probeResult{}, nil, false, metrics.DiscoveryReasonAuthFailed
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

	deps.resolver.RecordSuccess(ip.String(), profileName)
	m.CredentialTrialsTotal.WithLabelValues("ok").Inc()
	m.SNMPWalksTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()

	dev.Site = target.Site
	for k, v := range target.Labels {
		if dev.Labels == nil {
			dev.Labels = make(map[string]string)
		}
		dev.Labels[k] = v
	}

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
	params.WalkerMetrics = deps.walkerMetrics
	params.WarnLimiter = deps.warnLimiter
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
	params.Pool = deps.pool
	params.CredentialProfile = profileName

	// Issue #153: the per-module walk loop (module list → #74 per-IP
	// scope intersection → per-module span/metrics → outcome classify)
	// is shared with /admin/rediscover via walkModules. The cycle
	// supplies FULL instrumentation (metrics + tracer + logger) so its
	// behaviour is byte-identical to the pre-extraction inline loop.
	allEdges, allOOS, modStatus := walkModules(devCtx, cfg, *dev, ip, params, deps.allowedNets,
		moduleInstrumentation{metrics: m, tracer: tracing.Tracer(), logger: logger, host: target.Host})

	// Walk ARP table for MAC→IP enrichment when modules.arp.enabled
	// is true (default). The returned map feeds SynthesizeEdges (via the
	// caller's mu-guarded allARPMACs first-wins merge, C9) as a fallback
	// for FDB-only edges where LLDP did not provide the neighbour
	// identity. Failures are non-fatal: LLDP-based correlation still
	// works without ARP data.
	if cfg.Modules.ARP.Enabled {
		arpClient, arpRelease, arpErr := snmpwalk.Acquire(params)
		if arpErr != nil {
			logger.Debug("ARP table walk failed; MAC→IP resolution unavailable for this device",
				"device", dev.ID, "err", arpErr)
		} else {
			arpMACToIP, arpErr := snmpwalk.WalkARPTable(devCtx, arpClient)
			// Pass the walk error so the pool can evict the session on a
			// connection-level failure (#164).
			arpRelease(arpErr)
			if arpErr != nil {
				logger.Debug("ARP table walk failed; MAC→IP resolution unavailable for this device",
					"device", dev.ID, "err", arpErr)
			} else {
				arp = arpMACToIP
			}
		}
	}

	return probeResult{
		targetIdx:    idx,
		device:       dev,
		edges:        allEdges,
		outOfScope:   allOOS,
		mgmtIP:       ip.String(),
		moduleStatus: modStatus,
	}, arp, true, ""
}
