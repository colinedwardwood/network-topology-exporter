package app

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/events"
	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/loglimit"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// SnapshotWriteTimeout caps how long the background snapshot write goroutine
// waits before declaring an NFS stall and continuing the discovery cycle.
const SnapshotWriteTimeout = 30 * time.Second

// OTLPPushTimeout caps the lifetime of a single OTLP push goroutine; the push
// is cancelled if the upstream collector has not responded by then.
const OTLPPushTimeout = 10 * time.Second

// MaxOTLPPushConcurrency caps the number of in-flight OTLP push goroutines.
// Excess pushes are dropped (counted via OTLPPushTotal{status="dropped"}).
const MaxOTLPPushConcurrency = 4

// LoopConfig bundles the per-process state and dependencies that
// RunDiscoveryLoop reads each cycle. Callers populate the exported fields
// once at startup; the loop reads them across many cycles.
type LoopConfig struct {
	Cancel context.CancelFunc
	Logger *slog.Logger
	// WarnLimiter is the process-singleton rate-limiter for chronic
	// per-cycle Warn emissions (issue #16). Threaded into snmputil.Params
	// for per-walker use and consulted directly by the snapshot-writer
	// goroutine. May be nil — sites that consult it MUST fall back to a
	// direct slog.Warn in that case.
	WarnLimiter *loglimit.Limiter
	Cfg         *config.Config
	M           *metrics.Metrics
	// WalkerMetrics is the snmputil.WalkerMetrics implementation threaded into
	// every Params constructed in RunCycle. Replaces the old bgp package-global
	// counter wiring; see internal/metrics/walker_metrics_adapter.go.
	WalkerMetrics snmpwalk.WalkerMetrics
	Status        *atomic.Pointer[httpx.CycleStatus]
	Ready         *atomic.Bool
	Spoke         *federation.Spoke
	// Otlp owns the OTLP exporter plus the concurrency cap and in-flight
	// tracking for async pushes. Always non-nil; a disabled publisher (OTLP
	// off) makes push a no-op, so the loop never nil-checks.
	Otlp *otlpPublisher
	// CycleMu serialises each regular discovery cycle against forced
	// out-of-cycle walks triggered via POST /admin/rediscover (issue #73).
	// Held only for the duration of RunCycle's SNMP work; the Rediscoverer
	// acquires the same mutex so the two paths never walk devices concurrently.
	// May be nil (e.g. in unit tests that drive the loop without the admin
	// endpoint); a nil mutex means "no serialisation needed".
	CycleMu *sync.Mutex
	// Pool is the opt-in per-target SNMP session pool (issue #83). nil (the
	// default) means each walk opens and closes a fresh session — behaviour
	// byte-identical to pre-#83. Non-nil when discovery.snmp.session_pool.enabled
	// is true; RunCycle threads it into every Params so a target reuses one
	// session across its sequential module walks.
	Pool *snmpwalk.SessionPool
}

// WarnSnapshot rate-limits chronic snapshot Warns via lc.WarnLimiter,
// keyed on site+snapshot-path. Falls back to a direct slog.Warn when no
// limiter is configured.
func (lc LoopConfig) WarnSnapshot(ctx context.Context, site, msg string, attrs ...any) {
	if lc.WarnLimiter != nil {
		key := "snapshot|" + site + "|" + lc.Cfg.Snapshot.Path
		lc.WarnLimiter.Warn(ctx, key, msg, attrs...)
		return
	}
	lc.Logger.WarnContext(ctx, msg, attrs...)
}

// RunStaleWatchdog re-asserts the network_topology_graph_stale gauge when a
// running discovery loop wedges. The wedged loop cannot set its own gauge (it
// is stuck inside a cycle), so this independent goroutine ticks roughly every
// `interval` and, once at least one cycle has completed (status != nil), sets
// the gauge to 1 when the last cycle is older than maxStale. It deliberately
// does nothing before the first cycle so it never fights the cold-start
// GraphStale=1 / first-success GraphStale=0 logic in RunDiscoveryLoop.
//
// Write-ownership split (single clearer): the watchdog ONLY escalates — it
// sets the gauge to 1 when stale and never clears it. RunDiscoveryLoop
// exclusively owns clearing, setting GraphStale=0 on each successful cycle.
// Two independent writers (the watchdog also clearing on its own ticker)
// caused gauge flicker / alert-noise on slow-but-healthy cycles. On a
// stall→recovery the loop's next successful cycle clears the gauge via its
// existing success-path Set(0); the watchdog need not (and must not) clear it.
//
// The watchdog is only started when a local discovery loop runs (hub mode is
// excluded by the caller) and when the gate is enabled (maxStale > 0). It stops
// the ticker and returns on context cancellation so graceful shutdown joins it
// cleanly and goleak stays green. `now` defaults to time.Now when nil.
func RunStaleWatchdog(ctx context.Context, status *atomic.Pointer[httpx.CycleStatus], m *metrics.Metrics, interval, maxStale time.Duration, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s := status.Load()
			if s == nil {
				// No cycle has completed yet; leave the cold-start gauge state
				// owned by RunDiscoveryLoop untouched.
				continue
			}
			// Escalate-only: set stale when wedged, never clear. Clearing is
			// owned exclusively by RunDiscoveryLoop's success path (Set(0)).
			if httpx.IsStale(now(), s.LastCycleAt, maxStale) {
				m.GraphStale.Set(1)
			}
		}
	}
}

// RunDiscoveryLoop is the main discovery scheduler. It loads the LD-13
// snapshot on startup, starts the credential resolver, and then runs
// periodic cycles. Each cycle probes all configured targets concurrently
// under the LD-12 rate limiter, reconciles the resulting graph, diffs
// against the previous cycle, emits change events, updates metrics, and
// writes a new snapshot. When Spoke is non-nil (federation.role: spoke),
// it also pushes the pre-reconciled graph to the hub after each cycle.
func RunDiscoveryLoop(ctx context.Context, lc LoopConfig) {
	evLogger := events.New(lc.Logger)

	// LD-13: load snapshot, serve stale-but-valid metrics until first live cycle.
	lc.M.GraphStale.Set(1)
	snap, err := snapshot.Load(lc.Cfg.Snapshot.Path)
	if err != nil {
		if errors.Is(err, snapshot.ErrVersionMismatch) {
			lc.Logger.Warn("snapshot version mismatch, cold start", "path", lc.Cfg.Snapshot.Path, "error", err)
		} else {
			lc.Logger.Warn("snapshot load failed, cold start", "path", lc.Cfg.Snapshot.Path, "error", err)
		}
	}

	var prevGraph discovery.Graph
	ages := make(map[graph.EdgeKey]int)

	if snap != nil {
		prevGraph = discovery.Graph{
			Devices:    snap.Devices,
			Edges:      snap.Edges,
			OutOfScope: snap.OutOfScope,
		}
		ages = graph.AgesToEdgeKeys(snap.UnconfirmedAges)
		lc.Logger.Info("snapshot loaded",
			"devices", len(snap.Devices),
			"edges", len(snap.Edges),
		)
		lc.M.SnapshotLoadedDevicesTotal.Set(float64(len(snap.Devices)))
		lc.M.Topology.Update(prevGraph)
	}

	// LD-12: credential resolver.
	resolver, err := credentials.New(lc.Cfg.Credentials)
	if err != nil {
		lc.Logger.Error("building credential resolver", "error", err)
		lc.Cancel()
		return
	}
	if snap != nil {
		resolver.LoadCache(snap.CredentialCache)
	}

	// Parse CIDR allow-list once; the config is immutable at runtime.
	allowedNets := snmpwalk.ParseCIDRs(lc.Cfg.Discovery.Scope.CIDRAllowList)

	// LD-13: single bounded snapshot writer goroutine. A capacity-1 channel
	// ensures at most one write is queued at a time; a full channel drops the
	// new snapshot rather than accumulating blocked goroutines under NFS stall.
	var snapshotCh chan snapshot.File
	var snapWg sync.WaitGroup
	if lc.Cfg.Snapshot.Path != "" {
		snapshotCh = make(chan snapshot.File, 1)
		snapWg.Add(1)
		go func() {
			defer snapWg.Done()
			// Recover a panic in the snapshot writer so a bug in the write
			// path cannot crash the process (and, in spoke mode, take the
			// live discovery loop with it). One-shot: on recovery the writer
			// exits; the loop's enqueue path then trips queue_full and counts
			// the dropped snapshots, so the failure stays observable.
			defer recoverGoroutine("snapshot_writer", lc.Logger, lc.M)
			var writeDone chan error // non-nil while a write goroutine is in flight
			for f := range snapshotCh {
				lc.M.SnapshotQueueDepth.Set(float64(len(snapshotCh)))
				// Collect result from any previously timed-out write that has now finished.
				// writeDone is always reassigned a few lines down before the next
				// iteration's check, so the prior in-progress channel reference is
				// always replaced before being re-read.
				if writeDone != nil {
					select {
					case err := <-writeDone:
						if err != nil {
							lc.Logger.Error("snapshot write failed (delayed)", "error", err)
						} else {
							lc.M.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
						}
					default:
						// Still blocked — drop this snapshot rather than spawning another goroutine.
						// Rate-limit per path (issue #16): a chronic NFS stall
						// would emit this Warn every cycle until the stall
						// clears. Limiter keeps the operator alerted at first
						// occurrence and re-alerted hourly, not every minute.
						lc.M.SnapshotDropsTotal.WithLabelValues(string(metrics.SnapshotDropReasonWriteInFlight)).Inc()
						lc.WarnSnapshot(ctx, "snapshot_write_in_flight",
							"snapshot write still in flight; dropping snapshot (NFS stall?)")
						continue
					}
				}
				writeDone = make(chan error, 1)
				go func(f snapshot.File, done chan error) {
					// Recover a panic in snapshot.Write so it cannot crash the
					// process; the buffered done channel still receives nothing
					// on panic, so the parent's timeout branch handles it as a
					// stalled write. The deferred recover runs before the
					// goroutine unwinds, so the counter is bumped here too.
					defer recoverGoroutine("snapshot_writer", lc.Logger, lc.M)
					done <- snapshot.Write(lc.Cfg.Snapshot.Path, f)
				}(f, writeDone)
				select {
				case err := <-writeDone:
					writeDone = nil
					if err != nil {
						lc.Logger.Error("snapshot write failed", "error", err)
					} else {
						lc.M.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
					}
				case <-time.After(SnapshotWriteTimeout):
					// Rate-limit per path (issue #16): same chronic-NFS
					// pattern as the in-flight branch above.
					lc.WarnSnapshot(ctx, "snapshot_write_timeout",
						"snapshot write timed out (NFS stall?); discovery continues",
						"timeout", SnapshotWriteTimeout)
					// writeDone goroutine still running; next iteration will detect this.
				}
			}
		}()
	}

	// #6: decouple the spoke→hub push from the discovery cycle. The pusher
	// keeps a latest-only mailbox and pushes on its own goroutine so a slow hub
	// can never stall a cycle or evict the spoke.
	var pusher *spokePusher
	if lc.Spoke != nil {
		pusher = newSpokePusher(lc.Spoke.Push, lc.M, lc.Logger)
		go pusher.run(ctx)
	}

	var cycleNum int
	// State for the large-topology warning (issue #9): track whether the
	// previous cycle was above the threshold so we only emit on upward
	// crossings, and remember the cycle of the last warning so an
	// oscillating topology cannot flood the log.
	prevAboveThreshold := false
	lastWarnCycle := -LargeTopologyWarnCooldownCycles
	cycle := func() {
		// Per-cycle panic recovery (ops hardening): the discovery loop is
		// long-lived, so a panic in one cycle's reconcile/diff/publish path
		// must NOT kill the scheduler — recover here, count it under
		// discovery_loop, and let the next tick run a fresh cycle. The
		// per-device probe recover in cycle.go covers panics inside a single
		// target's walk; this covers everything else in a cycle.
		defer recoverGoroutine("discovery_loop", lc.Logger, lc.M)
		cycleNum++
		lc.M.GoRoutines.Set(float64(runtime.NumGoroutine()))
		start := time.Now()

		// Issue #68: root span for the cycle. cycleCtx carries the span so all
		// per-target / per-module / reconcile spans nest under it. When tracing
		// is disabled the tracer is the OTel no-op and this is free.
		cycleCtx, cycleSpan := tracing.Tracer().Start(ctx, "discovery.cycle",
			trace.WithAttributes(
				attribute.Int("cycle.number", cycleNum),
				attribute.String("cycle.start_time", start.Format(time.RFC3339Nano)),
			))
		defer cycleSpan.End()

		// Serialise the cycle's SNMP work against forced out-of-cycle walks
		// (issue #73): the Rediscoverer holds the same mutex, so a regular
		// cycle and an admin rediscover never walk devices concurrently.
		if lc.CycleMu != nil {
			lc.CycleMu.Lock()
		}
		newGraph, newAges, conflicts, deviceErrors := RunCycle(cycleCtx, lc.Logger, lc.Cfg, lc.M, lc.WalkerMetrics, lc.WarnLimiter, resolver, allowedNets, ages, lc.Pool)
		if lc.CycleMu != nil {
			lc.CycleMu.Unlock()
		}
		cycleSpan.SetAttributes(
			attribute.Int("cycle.device_count", len(newGraph.Devices)),
			attribute.Int("cycle.edge_count", len(newGraph.Edges)),
			attribute.Int("cycle.device_errors", deviceErrors),
		)
		if ctx.Err() != nil {
			return
		}
		newGraph.OutOfScope = MergeOOSFirstSeen(newGraph.OutOfScope, prevGraph.OutOfScope)
		lc.Status.Store(&httpx.CycleStatus{
			LastCycleAt:  time.Now(),
			DeviceErrors: int64(deviceErrors),
		})

		// Admission control: reject local graph updates that exceed the
		// configured size budget (mirrors hub-mode MaxGraph* enforcement).
		maxDevices := lc.Cfg.Discovery.MaxGraphDevices
		maxEdges := lc.Cfg.Discovery.MaxGraphEdges
		if (maxDevices > 0 && len(newGraph.Devices) > maxDevices) ||
			(maxEdges > 0 && len(newGraph.Edges) > maxEdges) {
			lc.Logger.Warn("local graph update rejected: exceeds size budget",
				"devices", len(newGraph.Devices), "max_devices", maxDevices,
				"edges", len(newGraph.Edges), "max_edges", maxEdges)
			ages = newAges // advance counters so unconfirmed edges can still expire
			lc.M.GraphUpdatesRejectedTotal.WithLabelValues(string(metrics.RejectReasonSizeBudgetExceeded)).Inc()
			// Keep prevGraph as the published graph; skip all downstream updates.
			return
		}

		changes := graph.Diff(prevGraph.Edges, newGraph.Edges)
		if len(changes) > 0 {
			evLogger.Emit(ctx, changes)
			for _, c := range changes {
				var proto discovery.DiscoveryProtocol
				if c.After != nil {
					proto = c.After.DiscoveryProto
				} else if c.Before != nil {
					proto = c.Before.DiscoveryProto
				}
				lc.M.TopologyChangeTotal.WithLabelValues(string(c.Kind), string(proto)).Inc()
			}
			ch := changes
			lc.Otlp.Push(func(ctx context.Context) error {
				return lc.Otlp.exp.PushChanges(ctx, ch)
			}, "otlp push changes failed")
		}
		if len(conflicts) > 0 {
			evLogger.EmitConflicts(ctx, conflicts)
			for _, c := range conflicts {
				lc.M.TopologyConflictTotal.WithLabelValues(string(c.Kind)).Inc()
			}
		}
		prevGraph = newGraph
		ages = newAges
		if lc.Otlp.Enabled() && (len(changes) > 0 || cycleNum%lc.Cfg.Output.OTLP.HeartbeatCycles == 0) {
			g := newGraph
			lc.Otlp.Push(func(ctx context.Context) error {
				return lc.Otlp.exp.PushGraph(ctx, g)
			}, "otlp push failed")
		}
		lc.M.GraphStale.Set(0)
		lc.M.Topology.Update(newGraph)
		if lc.Ready != nil {
			lc.Ready.CompareAndSwap(false, true)
		}
		prevAboveThreshold, lastWarnCycle = MaybeWarnLargeTopology(
			lc.Logger, len(newGraph.Edges), len(newGraph.Devices),
			prevAboveThreshold, cycleNum, lastWarnCycle)
		lc.M.DiscoveryCycleDuration.Observe(time.Since(start).Seconds())

		// LD-13: write snapshot via the bounded writer channel so an NFS stall
		// cannot accumulate goroutines across cycles. f is passed by value so the
		// next cycle cannot mutate the data while the write is in progress.
		ageMap := graph.EdgeKeysToAges(ages)
		credCache := resolver.SnapshotCache()
		f := snapshot.File{
			Devices:         newGraph.Devices,
			Edges:           newGraph.Edges,
			OutOfScope:      newGraph.OutOfScope,
			CredentialCache: credCache,
			UnconfirmedAges: ageMap,
		}
		if snapshotCh != nil {
			select {
			case snapshotCh <- f:
				lc.M.SnapshotQueueDepth.Set(float64(len(snapshotCh)))
			default:
				// Rate-limit per path (issue #16): queue-full is the upstream
				// symptom of the same chronic-NFS stall as the two branches
				// in the writer goroutine. Keys are distinct so each path
				// surfaces independently on first occurrence.
				lc.M.SnapshotDropsTotal.WithLabelValues(string(metrics.SnapshotDropReasonQueueFull)).Inc()
				lc.WarnSnapshot(ctx, "snapshot_queue_full",
					"snapshot write queue full; dropping (previous write still in flight)")
			}
		}

		// LD-16/LD-17: spoke mode — hand the pre-reconciled graph to the async
		// pusher (#6). Enqueue never blocks; cycleCtx is captured so the eventual
		// push still propagates the cycle's W3C traceparent to the hub (#68).
		if pusher != nil {
			pusher.Enqueue(cycleCtx, federation.SpokePayload{
				SpokeID:    lc.Cfg.Federation.Spoke.SpokeID,
				CycleAt:    time.Now(),
				Devices:    newGraph.Devices,
				Edges:      newGraph.Edges,
				OutOfScope: newGraph.OutOfScope,
				Ages:       ageMap,
			})
		}
	}

	cycle()
	tick := time.NewTicker(lc.Cfg.Discovery.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if snapshotCh != nil {
				close(snapshotCh)
			}
			snapWg.Wait()
			// Issue #5: per-device Zeroize defers in RunCycle have already
			// overwritten the in-flight cycle's SNMP credential bytes by the
			// time RunCycle's wg.Wait() returns. Logging here gives operators
			// a concrete signal in the shutdown sequence. See
			// docs/operator/security.md for the threat model and limits.
			lc.Logger.Info("snmp credentials zeroized; shutting down discovery loop")
			if pusher != nil {
				pusher.Shutdown()
			}
			return
		case <-tick.C:
			cycle()
		}
	}
}
