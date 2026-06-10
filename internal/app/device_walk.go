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

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/bgp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/cdp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/fdb"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/isis"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/mpls"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/ospf"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// enabledModules returns the canonical 7-element protocol-walker list, gated
// only by cfg.Modules.<X>.Enabled. The per-IP #74 scope intersection is NOT
// applied here (it needs the target IP and lives inside walkModules). This is
// the single source of truth for the module set shared by the regular cycle
// and the /admin/rediscover forced walk, so the slice can no longer drift
// between the two call sites.
func enabledModules(cfg *config.Config) []Module {
	return []Module{
		{"lldp", cfg.Modules.LLDP.Enabled, lldp.Walk},
		{"cdp", cfg.Modules.CDP.Enabled, cdp.Walk},
		{"fdb", cfg.Modules.FDB.Enabled, fdb.Walk},
		{"ospf", cfg.Modules.OSPF.Enabled, ospf.Walk},
		{"bgp", cfg.Modules.BGP.Enabled, bgp.Walk},
		{"isis", cfg.Modules.ISIS.Enabled, isis.Walk},
		{"mpls_te", cfg.Modules.MPLSTE.Enabled, mpls.Walk},
	}
}

// moduleInstrumentation parameterizes the per-module observability sinks of
// walkModules. Each field is independently optional:
//   - metrics nil  ⇒ skip every per-module metric (DiscoveryModuleDuration,
//     SNMPWalksTotal, DiscoveryHardFailTotal, DiscoveryDegradedTotal).
//   - tracer  nil  ⇒ skip every per-module span (<proto>.walk).
//   - logger  is required (the per-module failure log always fires).
//
// The regular discovery cycle supplies all three (full instrumentation, so its
// behaviour is byte-identical to the pre-extraction inline loop). The
// /admin/rediscover forced walk intentionally supplies nil metrics + nil tracer
// (and leaves params.WalkerMetrics unset) so it emits no per-cycle metric
// series or spans — see the comment on Rediscoverer.walkOne. The walker-outcome
// sink rides on params.WalkerMetrics, set by the caller, not here.
type moduleInstrumentation struct {
	metrics *metrics.Metrics
	tracer  trace.Tracer
	logger  *slog.Logger
}

// walkModules runs every enabled-and-in-scope module against one device and
// returns the union of edges, the out-of-scope neighbours (each tagged with the
// reporting protocol), and the per-protocol status map (0 ok | 1 degraded | 2
// failed). It is OUTPUT-ONLY: it neither builds nor zeroizes params, performs
// the ARP walk, nor touches any caller-side aggregation; those stay with the
// caller (C1/C3). The per-device ctx is supplied by the caller so per-module
// spans descend from the per-target span (C4).
func walkModules(
	ctx context.Context,
	cfg *config.Config,
	dev discovery.Device,
	ip net.IP,
	params snmpwalk.Params,
	allowedNets []*net.IPNet,
	inst moduleInstrumentation,
) (edges []discovery.Edge, oos []discovery.OutOfScopeNeighbour, moduleStatus map[string]int) {
	mods := enabledModules(cfg)
	// Issue #74: resolve this target's per-protocol scope once. When an
	// override matches, only the listed modules run (intersected with
	// the global mod.Enabled gate below); when none matches, allowed is
	// nil/overrideMatched=false and ALL enabled modules run — today's
	// unchanged default. The headline case: bgp.Walk only fires where
	// bgp is in the override's module set, so the walker outcome
	// counters partition naturally.
	allowedMods, overrideMatched := cfg.ModulesForIP(ip)
	moduleStatus = map[string]int{}
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
		modCtx := ctx
		var modSpan trace.Span
		if inst.tracer != nil {
			modCtx, modSpan = inst.tracer.Start(ctx, mod.Proto+".walk")
		}
		var modCancel context.CancelFunc = func() {}
		if cfg.Discovery.TimeoutPerModule > 0 {
			modCtx, modCancel = context.WithTimeout(modCtx, cfg.Discovery.TimeoutPerModule)
		}
		mEdges, mOOS, err := mod.Walk(modCtx, params, dev.ID, allowedNets)
		modCancel()
		if inst.metrics != nil {
			inst.metrics.DiscoveryModuleDuration.WithLabelValues(mod.Proto).Observe(time.Since(modStart).Seconds())
		}
		if modSpan != nil {
			modSpan.SetAttributes(attribute.Int("walk.pdu_count", len(mEdges)))
		}
		if err != nil {
			inst.logger.Debug(mod.Proto+" walk failed", "target", ip.String(), "error", err)
			if modSpan != nil {
				modSpan.SetAttributes(attribute.String("walk.outcome", "failed"))
				modSpan.RecordError(err)
				modSpan.SetStatus(codes.Error, "module walk failed")
				modSpan.End()
			}
			reason := "module_walk_error"
			var policyErr *discovery.PolicyError
			if errors.As(err, &policyErr) && policyErr.Reason != "" {
				reason = policyErr.Reason
			}
			if inst.metrics != nil {
				inst.metrics.DiscoveryHardFailTotal.WithLabelValues(mod.Proto, reason).Inc()
				// Issue #20: partition by walk sub-reason. Timeouts
				// keep reason=n/a; module-level non-timeout errors
				// are tagged WalkReasonModuleError. Per-module richer
				// breakdowns (auth_failed at the module layer is
				// already excluded — credentials succeeded for the
				// system walk above) would require module-walker
				// changes and are not in #20's scope.
				if errors.Is(err, context.DeadlineExceeded) {
					inst.metrics.SNMPWalksTotal.WithLabelValues("timeout", metrics.ReasonNA).Inc()
				} else {
					inst.metrics.SNMPWalksTotal.WithLabelValues("error", string(metrics.WalkReasonModuleError)).Inc()
				}
			}
			moduleStatus[mod.Proto] = 2
			continue
		}
		if inst.metrics != nil {
			inst.metrics.SNMPWalksTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
		}
		degradedReasons := CollectDegradedReasons(mEdges)
		if inst.metrics != nil {
			for _, reason := range degradedReasons {
				inst.metrics.DiscoveryDegradedTotal.WithLabelValues(mod.Proto, reason).Inc()
			}
		}
		if len(degradedReasons) > 0 {
			if modSpan != nil {
				modSpan.SetAttributes(attribute.String("walk.outcome", "degraded"))
			}
			if _, ok := moduleStatus[mod.Proto]; !ok {
				moduleStatus[mod.Proto] = 1
			}
		} else {
			if modSpan != nil {
				modSpan.SetAttributes(attribute.String("walk.outcome", "ok"))
			}
			if _, ok := moduleStatus[mod.Proto]; !ok {
				moduleStatus[mod.Proto] = 0
			}
		}
		if modSpan != nil {
			modSpan.End()
		}
		edges = append(edges, mEdges...)
		// Tag each OOS entry with the protocol that reported it so the
		// boundary_observation_info metric and hub OOS matching have a
		// proto label.
		for i := range mOOS {
			mOOS[i].Proto = mod.Proto
		}
		oos = append(oos, mOOS...)
	}
	return edges, oos, moduleStatus
}
