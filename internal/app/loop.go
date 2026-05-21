package app

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

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
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
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
	OtlpExp       *otlp.Exporter
	OtlpSem       chan struct{}   // semaphore bounding concurrent OTLP pushes; nil when OTLP disabled
	OtlpWg        *sync.WaitGroup // tracks in-flight OTLP push goroutines for clean shutdown
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

// OtlpPush enqueues an OTLP push function under the configured concurrency cap.
// Drops the push (and increments the dropped counter) if the OtlpSem is full.
func (lc LoopConfig) OtlpPush(ctx context.Context, fn func(context.Context) error, warnMsg string) {
	if lc.OtlpSem != nil {
		select {
		case lc.OtlpSem <- struct{}{}:
		default:
			lc.Logger.Warn("otlp push dropped: concurrent limit reached")
			// status="dropped" never carries a failure reason — use the
			// shared n/a sentinel. Issue #20.
			lc.M.OTLPPushTotal.WithLabelValues("dropped", metrics.ReasonNA).Inc()
			return
		}
	}
	if lc.OtlpWg != nil {
		lc.OtlpWg.Add(1)
	}
	go func() { //nolint:gosec // G118: OTLP push must survive the originating cycle's context — the push is a side-effect of a completed cycle and should reach the collector even if the cycle's deadline already expired
		if lc.OtlpWg != nil {
			defer lc.OtlpWg.Done()
		}
		if lc.OtlpSem != nil {
			defer func() { <-lc.OtlpSem }()
		}
		pushCtx, cancel := context.WithTimeout(context.Background(), OTLPPushTimeout)
		defer cancel()
		if err := fn(pushCtx); err != nil {
			lc.Logger.Warn(warnMsg, "error", err)
			// Issue #20: partition status="error" by the OTLP sub-reason
			// derived from the error (timeout / tls_error / http_4xx /
			// http_5xx / network).
			lc.M.OTLPPushTotal.WithLabelValues("error", string(otlp.ClassifyPushError(err))).Inc()
		} else {
			lc.M.OTLPPushTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
		}
	}()
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
				go func(f snapshot.File, done chan error) { done <- snapshot.Write(lc.Cfg.Snapshot.Path, f) }(f, writeDone)
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

	var cycleNum int
	// State for the large-topology warning (issue #9): track whether the
	// previous cycle was above the threshold so we only emit on upward
	// crossings, and remember the cycle of the last warning so an
	// oscillating topology cannot flood the log.
	prevAboveThreshold := false
	lastWarnCycle := -LargeTopologyWarnCooldownCycles
	cycle := func() {
		cycleNum++
		lc.M.GoRoutines.Set(float64(runtime.NumGoroutine()))
		start := time.Now()
		newGraph, newAges, conflicts, deviceErrors := RunCycle(ctx, lc.Logger, lc.Cfg, lc.M, lc.WalkerMetrics, lc.WarnLimiter, resolver, allowedNets, ages)
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
			if lc.OtlpExp != nil {
				ch := changes
				lc.OtlpPush(ctx, func(ctx context.Context) error {
					return lc.OtlpExp.PushChanges(ctx, ch)
				}, "otlp push changes failed")
			}
		}
		if len(conflicts) > 0 {
			evLogger.EmitConflicts(ctx, conflicts)
			for _, c := range conflicts {
				lc.M.TopologyConflictTotal.WithLabelValues(string(c.Kind)).Inc()
			}
		}
		prevGraph = newGraph
		ages = newAges
		if lc.OtlpExp != nil && (len(changes) > 0 || cycleNum%lc.Cfg.Output.OTLP.HeartbeatCycles == 0) {
			g := newGraph
			lc.OtlpPush(ctx, func(ctx context.Context) error {
				return lc.OtlpExp.PushGraph(ctx, g)
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

		// LD-16/LD-17: spoke mode — push pre-reconciled graph to hub.
		if lc.Spoke != nil {
			payload := federation.SpokePayload{
				SpokeID:    lc.Cfg.Federation.Spoke.SpokeID,
				CycleAt:    time.Now(),
				Devices:    newGraph.Devices,
				Edges:      newGraph.Edges,
				OutOfScope: newGraph.OutOfScope,
				Ages:       ageMap,
			}
			if err := lc.Spoke.Push(ctx, payload); err != nil && ctx.Err() == nil {
				lc.Logger.Warn("spoke push failed", "error", err)
			}
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
			return
		case <-tick.C:
			cycle()
		}
	}
}
