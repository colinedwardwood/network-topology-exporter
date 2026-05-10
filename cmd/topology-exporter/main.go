// Command topology-exporter discovers network topology over SNMP, LLDP,
// CDP, BGP, OSPF, and FDB, and emits the result as Prometheus metrics and
// structured log lines.
//
// README.md documents the emitted-signal contract; CONTRIBUTING.md documents
// the LD-09 clean-room development rules.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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
	"github.com/colinedwardwood/network-topology-exporter/internal/events"
	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

// snapshotWriteTimeout caps how long the background snapshot write goroutine
// waits before declaring an NFS stall and continuing the discovery cycle.
const snapshotWriteTimeout = 30 * time.Second

type cycleStatus struct {
	LastCycleAt  time.Time
	DeviceErrors int64
}

// newReadyzHandler returns an HTTP handler for /readyz. It returns 200 once
// isReady() is true (first live cycle or first spoke push received) and 503
// during startup so Kubernetes does not route traffic before the process has
// meaningful topology data to serve.
func newReadyzHandler(isReady func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if isReady() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"starting"}` + "\n"))
	}
}

func newHealthzHandler(status *atomic.Pointer[cycleStatus]) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := status.Load()
		if s == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","last_cycle_at":null,"device_errors":null}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","last_cycle_at":%q,"device_errors":%d}`+"\n",
			s.LastCycleAt.UTC().Format(time.RFC3339), s.DeviceErrors)
	}
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("topology-exporter", flag.ContinueOnError)
	var (
		configPath = fs.String("config.file", "/etc/topology-exporter/config.yaml", "Path to the YAML configuration file.")
		listenAddr = fs.String("web.listen-address", ":9100", "Address on which to expose /metrics and /healthz.")
		logLevel   = fs.String("log.level", "info", "Log level: debug | info | warn | error.")
		showVer    = fs.Bool("version", false, "Print version and exit.")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *showVer {
		fmt.Printf("topology-exporter %s (%s, built %s)\n", version.Version, version.Commit, version.BuildDate)
		return 0
	}

	logger := newLogger(*logLevel)
	logger.Info("starting",
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.BuildDate,
		"config", *configPath,
	)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("loading config failed", "error", err)
		return 1
	}
	logger.Info("config loaded",
		"discovery_interval", cfg.Discovery.Interval,
		"parallelism", cfg.Discovery.Parallelism,
		"target_count", len(cfg.Targets),
	)

	m := metrics.New(cfg.Federation.Role == "uncoordinated")
	m.SnapshotLastWrittenUnix.SetToCurrentTime()

	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool // set to true after the first live cycle or spoke push

	// isReadyFn is the readiness check for /readyz. Default: process-local flag.
	// Hub mode replaces this with hub.IsReady before registering the mux handler.
	isReadyFn := ready.Load

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()}))
	mux.HandleFunc("/healthz", newHealthzHandler(&status))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, "topology-exporter %s\nendpoints: /metrics /healthz /readyz\n", version.Version)
	})

	effectiveAddr := cfg.Listen.Addr
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "web.listen-address" {
			effectiveAddr = *listenAddr
		}
	})

	srv := &http.Server{
		Addr:              effectiveAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var workerDone sync.WaitGroup
	var otlpWg sync.WaitGroup

	switch cfg.Federation.Role {
	case "hub":
		// Hub mode: pure aggregator — no local SNMP discovery. The hub server
		// exposes /spoke/push on a separate mTLS listener (LD-20).
		hub := federation.NewHub(cfg.Federation, m, logger, cfg.Snapshot.Path)

		// LD-13: load snapshot so the hub can serve stale-but-valid metrics
		// (GraphStale=1) until the first live spoke push arrives.
		m.GraphStale.Set(1)
		hubSnap, err := snapshot.Load(cfg.Snapshot.Path)
		if err != nil {
			if errors.Is(err, snapshot.ErrVersionMismatch) {
				logger.Warn("hub snapshot version mismatch, cold start", "path", cfg.Snapshot.Path, "error", err)
			} else {
				logger.Warn("hub snapshot load failed, cold start", "path", cfg.Snapshot.Path, "error", err)
			}
		}
		if hubSnap != nil {
			hub.RestoreGraph(discovery.Graph{
				Devices: hubSnap.Devices,
				Edges:   hubSnap.Edges,
			})
			logger.Info("hub snapshot loaded", "devices", len(hubSnap.Devices), "edges", len(hubSnap.Edges))
			m.SnapshotLoadedDevicesTotal.Set(float64(len(hubSnap.Devices)))
		}

		// In hub mode, readiness is driven by the first live spoke push.
		isReadyFn = hub.IsReady

		workerDone.Add(1)
		go func() {
			defer workerDone.Done()
			if err := hub.Serve(ctx); err != nil && ctx.Err() == nil {
				logger.Error("hub federation server error", "error", err)
				cancel()
			}
		}()
	default: // standalone, uncoordinated, spoke
		// Build the spoke client now so TLS errors surface at startup, not
		// mid-cycle.
		var spoke *federation.Spoke
		if cfg.Federation.Role == "spoke" {
			var err error
			spoke, err = federation.NewSpoke(cfg.Federation, logger, m)
			if err != nil {
				logger.Error("building federation spoke", "error", err)
				return 1
			}
		}

		var otlpExp *otlp.Exporter
		if cfg.Output.OTLP.Enabled {
			otlpExp = otlp.New(otlp.Config{
				Endpoint: cfg.Output.OTLP.Endpoint,
				Timeout:  cfg.Output.OTLP.Timeout,
			})
		}

		var otlpSem chan struct{}
		if cfg.Output.OTLP.Enabled {
			otlpSem = make(chan struct{}, maxOTLPPushConcurrency)
		}

		workerDone.Add(1)
		go func() {
			defer workerDone.Done()
			runDiscoveryLoop(ctx, loopConfig{
				cancel:  cancel,
				logger:  logger,
				cfg:     cfg,
				m:       m,
				status:  &status,
				ready:   &ready,
				spoke:   spoke,
				otlpExp: otlpExp,
				otlpSem: otlpSem,
				otlpWg:  &otlpWg,
			})
		}()
	}

	mux.HandleFunc("/readyz", newReadyzHandler(isReadyFn))

	go func() {
		var serveErr error
		if cfg.Listen.TLSCertFile != "" {
			logger.Info("metrics TLS server listening", "addr", effectiveAddr)
			serveErr = srv.ListenAndServeTLS(cfg.Listen.TLSCertFile, cfg.Listen.TLSKeyFile)
		} else {
			logger.Info("metrics server listening", "addr", effectiveAddr)
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("metrics server error", "error", serveErr)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, draining")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "error", err)
	}
	drainDone := make(chan struct{})
	go func() { workerDone.Wait(); close(drainDone) }()
	select {
	case <-drainDone:
	case <-time.After(cfg.Discovery.TimeoutPerDevice + 5*time.Second):
		logger.Warn("discovery drain timed out, forcing exit")
	}
	otlpWg.Wait()
	logger.Info("otlp push goroutines drained")
	logger.Info("clean shutdown complete")
	return 0
}

const (
	otlpPushTimeout        = 10 * time.Second
	maxOTLPPushConcurrency = 4
)

type loopConfig struct {
	cancel  context.CancelFunc
	logger  *slog.Logger
	cfg     *config.Config
	m       *metrics.Metrics
	status  *atomic.Pointer[cycleStatus]
	ready   *atomic.Bool
	spoke   *federation.Spoke
	otlpExp *otlp.Exporter
	otlpSem chan struct{}   // semaphore bounding concurrent OTLP pushes; nil when OTLP disabled
	otlpWg  *sync.WaitGroup // tracks in-flight OTLP push goroutines for clean shutdown
}

func (lc loopConfig) otlpPush(ctx context.Context, fn func(context.Context) error, warnMsg string) {
	if lc.otlpSem != nil {
		select {
		case lc.otlpSem <- struct{}{}:
		default:
			lc.logger.Warn("otlp push dropped: concurrent limit reached")
			lc.m.OTLPPushTotal.WithLabelValues("dropped").Inc()
			return
		}
	}
	if lc.otlpWg != nil {
		lc.otlpWg.Add(1)
	}
	go func() {
		if lc.otlpWg != nil {
			defer lc.otlpWg.Done()
		}
		if lc.otlpSem != nil {
			defer func() { <-lc.otlpSem }()
		}
		pushCtx, cancel := context.WithTimeout(ctx, otlpPushTimeout)
		defer cancel()
		if err := fn(pushCtx); err != nil {
			lc.logger.Warn(warnMsg, "error", err)
			lc.m.OTLPPushTotal.WithLabelValues("error").Inc()
		} else {
			lc.m.OTLPPushTotal.WithLabelValues("ok").Inc()
		}
	}()
}

// runDiscoveryLoop is the main discovery scheduler. It loads the LD-13
// snapshot on startup, starts the credential resolver, and then runs
// periodic cycles. Each cycle probes all configured targets concurrently
// under the LD-12 rate limiter, reconciles the resulting graph, diffs
// against the previous cycle, emits change events, updates metrics, and
// writes a new snapshot. When spoke is non-nil (federation.role: spoke),
// it also pushes the pre-reconciled graph to the hub after each cycle.
func runDiscoveryLoop(ctx context.Context, lc loopConfig) {
	evLogger := events.New(lc.logger)

	// LD-13: load snapshot, serve stale-but-valid metrics until first live cycle.
	lc.m.GraphStale.Set(1)
	snap, err := snapshot.Load(lc.cfg.Snapshot.Path)
	if err != nil {
		if errors.Is(err, snapshot.ErrVersionMismatch) {
			lc.logger.Warn("snapshot version mismatch, cold start", "path", lc.cfg.Snapshot.Path, "error", err)
		} else {
			lc.logger.Warn("snapshot load failed, cold start", "path", lc.cfg.Snapshot.Path, "error", err)
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
		lc.logger.Info("snapshot loaded",
			"devices", len(snap.Devices),
			"edges", len(snap.Edges),
		)
		lc.m.SnapshotLoadedDevicesTotal.Set(float64(len(snap.Devices)))
		lc.m.Topology.Update(prevGraph)
	}

	// LD-12: credential resolver.
	resolver, err := credentials.New(lc.cfg.Credentials)
	if err != nil {
		lc.logger.Error("building credential resolver", "error", err)
		lc.cancel()
		return
	}
	if snap != nil {
		resolver.LoadCache(snap.CredentialCache)
	}

	// Parse CIDR allow-list once; the config is immutable at runtime.
	allowedNets := snmpwalk.ParseCIDRs(lc.cfg.Discovery.Scope.CIDRAllowList)

	// LD-13: single bounded snapshot writer goroutine. A capacity-1 channel
	// ensures at most one write is queued at a time; a full channel drops the
	// new snapshot rather than accumulating blocked goroutines under NFS stall.
	var snapshotCh chan snapshot.File
	if lc.cfg.Snapshot.Path != "" {
		snapshotCh = make(chan snapshot.File, 1)
		go func() {
			var writeDone chan error // non-nil while a write goroutine is in flight
			for f := range snapshotCh {
				lc.m.SnapshotQueueDepth.Set(float64(len(snapshotCh)))
				// Collect result from any previously timed-out write that has now finished.
				if writeDone != nil {
					select {
					case err := <-writeDone:
						writeDone = nil
						if err != nil {
							lc.logger.Error("snapshot write failed (delayed)", "error", err)
						} else {
							lc.m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
						}
					default:
						// Still blocked — drop this snapshot rather than spawning another goroutine.
						lc.logger.Warn("snapshot write still in flight; dropping snapshot (NFS stall?)")
						continue
					}
				}
				writeDone = make(chan error, 1)
				go func(f snapshot.File, done chan error) { done <- snapshot.Write(lc.cfg.Snapshot.Path, f) }(f, writeDone)
				select {
				case err := <-writeDone:
					writeDone = nil
					if err != nil {
						lc.logger.Error("snapshot write failed", "error", err)
					} else {
						lc.m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
					}
				case <-time.After(snapshotWriteTimeout):
					lc.logger.Warn("snapshot write timed out (NFS stall?); discovery continues", "timeout", snapshotWriteTimeout)
					// writeDone goroutine still running; next iteration will detect this.
				}
			}
		}()
	}

	var cycleNum int
	cycle := func() {
		cycleNum++
		lc.m.GoRoutines.Set(float64(runtime.NumGoroutine()))
		start := time.Now()
		newGraph, newAges, conflicts, deviceErrors := runCycle(ctx, lc.logger, lc.cfg, lc.m, resolver, allowedNets, ages)
		if ctx.Err() != nil {
			return
		}
		lc.status.Store(&cycleStatus{
			LastCycleAt:  time.Now(),
			DeviceErrors: int64(deviceErrors),
		})

		// Admission control: reject local graph updates that exceed the
		// configured size budget (mirrors hub-mode MaxGraph* enforcement).
		maxDevices := lc.cfg.Discovery.MaxGraphDevices
		maxEdges := lc.cfg.Discovery.MaxGraphEdges
		if (maxDevices > 0 && len(newGraph.Devices) > maxDevices) ||
			(maxEdges > 0 && len(newGraph.Edges) > maxEdges) {
			lc.logger.Warn("local graph update rejected: exceeds size budget",
				"devices", len(newGraph.Devices), "max_devices", maxDevices,
				"edges", len(newGraph.Edges), "max_edges", maxEdges)
			lc.m.GraphUpdatesRejectedTotal.Inc()
			// Keep prevGraph as the published graph; skip all downstream updates.
			return
		}

		changes := graph.Diff(prevGraph.Edges, newGraph.Edges)
		if len(changes) > 0 {
			evLogger.Emit(ctx, changes)
			for _, c := range changes {
				proto := ""
				if c.After != nil {
					proto = c.After.DiscoveryProto
				} else if c.Before != nil {
					proto = c.Before.DiscoveryProto
				}
				lc.m.TopologyChangeTotal.WithLabelValues(string(c.Kind), proto).Inc()
			}
			if lc.otlpExp != nil {
				ch := changes
				lc.otlpPush(ctx, func(ctx context.Context) error {
					return lc.otlpExp.PushChanges(ctx, ch)
				}, "otlp push changes failed")
			}
		}
		if len(conflicts) > 0 {
			evLogger.EmitConflicts(ctx, conflicts)
			for _, c := range conflicts {
				lc.m.TopologyConflictTotal.WithLabelValues(string(c.Kind)).Inc()
			}
		}
		prevGraph = newGraph
		ages = newAges
		lc.m.Topology.Update(newGraph)
		if lc.otlpExp != nil && (len(changes) > 0 || cycleNum%lc.cfg.Output.OTLP.HeartbeatCycles == 0) {
			g := newGraph
			lc.otlpPush(ctx, func(ctx context.Context) error {
				return lc.otlpExp.PushGraph(ctx, g)
			}, "otlp push failed")
		}
		lc.m.GraphStale.Set(0)
		if lc.ready != nil {
			lc.ready.CompareAndSwap(false, true)
		}
		lc.m.DiscoveryCycleDuration.Observe(time.Since(start).Seconds())

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
				lc.m.SnapshotQueueDepth.Set(float64(len(snapshotCh)))
			default:
				lc.logger.Warn("snapshot write queue full; dropping (previous write still in flight)")
			}
		}

		// LD-16/LD-17: spoke mode — push pre-reconciled graph to hub.
		if lc.spoke != nil {
			payload := federation.SpokePayload{
				SpokeID:    lc.cfg.Federation.Spoke.SpokeID,
				CycleAt:    time.Now(),
				Devices:    newGraph.Devices,
				Edges:      newGraph.Edges,
				OutOfScope: newGraph.OutOfScope,
				Ages:       ageMap,
			}
			if err := lc.spoke.Push(ctx, payload); err != nil && ctx.Err() == nil {
				lc.logger.Warn("spoke push failed", "error", err)
			}
		}
	}

	cycle()
	tick := time.NewTicker(lc.cfg.Discovery.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if snapshotCh != nil {
				close(snapshotCh)
			}
			return
		case <-tick.C:
			cycle()
		}
	}
}

// runCycle probes all configured targets concurrently and returns the
// resulting graph, updated unconfirmed-age counters, any reconciliation
// conflicts, and the count of targets that failed discovery.
func runCycle(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	m *metrics.Metrics,
	resolver *credentials.Resolver,
	allowedNets []*net.IPNet,
	prevAges map[graph.EdgeKey]int,
) (discovery.Graph, map[graph.EdgeKey]int, []graph.Conflict, int) {
	type probeResult struct {
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
	var okCount, failCount int64

	for _, t := range cfg.Targets {
		target := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-cycleCtx.Done():
				m.CycleBudgetSkipsTotal.Inc()
				return
			}
			defer func() { <-sem }()

			ip := net.ParseIP(target.Host)
			if ip == nil {
				addrs, err := net.DefaultResolver.LookupHost(cycleCtx, target.Host)
				if err != nil || len(addrs) == 0 {
					logger.Warn("host resolution failed", "host", target.Host, "error", err)
					mu.Lock()
					failCount++
					mu.Unlock()
					return
				}
				ip = net.ParseIP(addrs[0])
			}

			// LD-11: enforce CIDR allow-list for hostname-based targets whose
			// IP is only known after DNS resolution.
			if len(allowedNets) > 0 && !snmpwalk.IPInNets(ip, allowedNets) {
				logger.Warn("resolved target outside allow-list, skipping",
					"host", target.Host, "ip", ip)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			dev, params, profileName, err := walkSystemWithCredentials(cycleCtx, cfg, resolver, ip, target)
			if err != nil {
				logger.Warn("snmp walk failed", "target", target.Host, "error", err)
				m.DiscoveryHardFailTotal.WithLabelValues("system", "system_group_walk_error").Inc()
				m.CredentialTrialsTotal.WithLabelValues("failed").Inc()
				if errors.Is(err, context.DeadlineExceeded) {
					m.SNMPWalksTotal.WithLabelValues("timeout").Inc()
				} else {
					m.SNMPWalksTotal.WithLabelValues("error").Inc()
				}
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			devCtx, cancel := context.WithTimeout(cycleCtx, cfg.Discovery.TimeoutPerDevice)
			defer cancel()
			devCtx = snmpwalk.ContextWithDecodeIssueReporter(devCtx, func(issue snmpwalk.DecodeIssue) {
				m.DiscoveryDecodeIssues.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
				m.DiscoveryQuarantinedRowsTotal.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
			})

			resolver.RecordSuccess(ip.String(), profileName)
			m.CredentialTrialsTotal.WithLabelValues("ok").Inc()
			m.SNMPWalksTotal.WithLabelValues("ok").Inc()

			dev.Site = target.Site
			for k, v := range target.Labels {
				if dev.Labels == nil {
					dev.Labels = make(map[string]string)
				}
				dev.Labels[k] = v
			}

			var allEdges []discovery.Edge
			var allOOS []discovery.OutOfScopeNeighbour

			// Propagate FDB-specific tuning into params. MaxVlans is only
			// consumed by fdb.Walk; other modules ignore it.
			params.MaxVlans = cfg.Modules.FDB.MaxVlans

			mods := []module{
				{"lldp", cfg.Modules.LLDP.Enabled, lldp.Walk},
				{"cdp", cfg.Modules.CDP.Enabled, cdp.Walk},
				{"fdb", cfg.Modules.FDB.Enabled, fdb.Walk},
				{"ospf", cfg.Modules.OSPF.Enabled, ospf.Walk},
				{"bgp", cfg.Modules.BGP.Enabled, bgp.Walk},
				{"isis", cfg.Modules.ISIS.Enabled, isis.Walk},
				{"mpls_te", cfg.Modules.MPLSTE.Enabled, mpls.Walk},
			}
			modStatus := map[string]int{}
			for _, mod := range mods {
				if !mod.enabled {
					continue
				}
				modStart := time.Now()
				modCtx := devCtx
				var modCancel context.CancelFunc = func() {}
				if cfg.Discovery.TimeoutPerModule > 0 {
					modCtx, modCancel = context.WithTimeout(devCtx, cfg.Discovery.TimeoutPerModule)
				}
				edges, oos, err := mod.walk(modCtx, params, dev.ID, allowedNets)
				modCancel()
				m.DiscoveryModuleDuration.WithLabelValues(mod.proto).Observe(time.Since(modStart).Seconds())
				if err != nil {
					logger.Debug(mod.proto+" walk failed", "target", target.Host, "error", err)
					reason := "module_walk_error"
					var policyErr *discovery.PolicyError
					if errors.As(err, &policyErr) && policyErr.Reason != "" {
						reason = policyErr.Reason
					}
					m.DiscoveryHardFailTotal.WithLabelValues(mod.proto, reason).Inc()
					if errors.Is(err, context.DeadlineExceeded) {
						m.SNMPWalksTotal.WithLabelValues("timeout").Inc()
					} else {
						m.SNMPWalksTotal.WithLabelValues("error").Inc()
					}
					modStatus[mod.proto] = 2
					continue
				}
				m.SNMPWalksTotal.WithLabelValues("ok").Inc()
				degradedReasons := collectDegradedReasons(edges)
				for _, reason := range degradedReasons {
					m.DiscoveryDegradedTotal.WithLabelValues(mod.proto, reason).Inc()
				}
				if len(degradedReasons) > 0 {
					if _, ok := modStatus[mod.proto]; !ok {
						modStatus[mod.proto] = 1
					}
				} else {
					if _, ok := modStatus[mod.proto]; !ok {
						modStatus[mod.proto] = 0
					}
				}
				allEdges = append(allEdges, edges...)
				// Tag each OOS entry with the protocol that reported it so the
				// boundary_observation_info metric and hub OOS matching have a
				// proto label.
				for i := range oos {
					oos[i].Proto = mod.proto
				}
				allOOS = append(allOOS, oos...)
			}

			// Walk ARP table for MAC→IP resolution. Failures are non-fatal:
			// LLDP-based correlation still works without ARP data.
			arpClient, arpErr := snmpwalk.Open(params)
			if arpErr != nil {
				slog.Debug("fdb: ARP table walk failed; MAC→IP resolution unavailable for this device",
					"device", dev.ID, "err", arpErr)
			} else {
				arpMACToIP, arpErr := snmpwalk.WalkARPTable(devCtx, arpClient)
				_ = arpClient.Conn.Close()
				if arpErr != nil {
					slog.Debug("fdb: ARP table walk failed; MAC→IP resolution unavailable for this device",
						"device", dev.ID, "err", arpErr)
				} else {
					mu.Lock()
					for mac, ip := range arpMACToIP {
						if _, exists := allARPMACs[mac]; !exists {
							allARPMACs[mac] = ip
						}
					}
					mu.Unlock()
				}
			}

			mu.Lock()
			results = append(results, probeResult{device: dev, edges: allEdges, outOfScope: allOOS, mgmtIP: ip.String(), moduleStatus: modStatus})
			okCount++
			mu.Unlock()
		}()
	}
	wg.Wait()

	m.DiscoveryDevicesTotal.WithLabelValues("success").Set(float64(okCount))
	m.DiscoveryDevicesTotal.WithLabelValues("failed").Set(float64(failCount))

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
				ipToID[r.mgmtIP] = r.device.ID
			}
		}
		rawEdges = append(rawEdges, r.edges...)
		allOOS = append(allOOS, r.outOfScope...)
	}
	// Build MAC→sysName identity index from LLDP observations.
	// LLDP sets MetadataKeyPeerChassisMac when it knows both the MAC and sysName of a peer.
	macToID := make(map[string]string)
	for _, e := range rawEdges {
		if e.DiscoveryProto == "lldp" {
			if mac, ok := e.Metadata[discovery.MetadataKeyPeerChassisMac]; ok && e.DstDevice != "" {
				macToID[mac] = e.DstDevice
			}
		}
	}
	// Second resolution path: ARP table entries.
	// If a MAC is not already in macToID (no LLDP correlation) but it appears
	// in the ARP table of a polled device and that IP is a known device, resolve it.
	for mac, ip := range allARPMACs {
		if _, resolved := macToID[mac]; resolved {
			continue // LLDP takes precedence
		}
		if id, ok := ipToID[ip]; ok {
			macToID[mac] = id
		}
	}
	rawEdges = resolveEdgeDstDevices(rawEdges, ipToID, macToID, m.FDBSuppressedMACs)

	// Backfill DstPort on FDB edges from LLDP observations with matching endpoints.
	// After MAC→sysName resolution, LLDP and FDB can agree on all four endpoint
	// fields, letting graph.Reconcile merge them into one edge (LLDP wins on rank).
	type epKey struct{ src, srcPort, dst string }
	lldpDstPort := make(map[epKey]string, len(rawEdges))
	for _, e := range rawEdges {
		if e.DiscoveryProto == "lldp" && e.DstPort != "" {
			lldpDstPort[epKey{e.SrcDevice, e.SrcPort, e.DstDevice}] = e.DstPort
		}
	}
	for i := range rawEdges {
		if rawEdges[i].DiscoveryProto == "fdb" && rawEdges[i].DstPort == "" {
			if p, ok := lldpDstPort[epKey{rawEdges[i].SrcDevice, rawEdges[i].SrcPort, rawEdges[i].DstDevice}]; ok {
				rawEdges[i].DstPort = p
			}
		}
	}

	reconciledEdges, conflicts := graph.Reconcile(rawEdges)

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
		Devices:    devices,
		Edges:      reconciledEdges,
		OutOfScope: allOOS,
	}, ages, conflicts, int(failCount)
}

func collectDegradedReasons(edges []discovery.Edge) []string {
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

// resolveEdgeDstDevices replaces IP-valued and MAC-valued DstDevice fields with
// the canonical device ID (sysName) from the discovered inventory when available.
// BGP/OSPF/IS-IS walks report peer IPs; FDB reports raw MACs; LLDP reports sysNames.
// For IP DstDevices: resolves to sysName using the device walk inventory; unresolved
// IPs are kept (still useful for routing protocol edges).
// For MAC DstDevices: resolves to sysName via the LLDP identity index; unresolved
// MACs are suppressed (likely hosts, not infrastructure).
// suppressedCounter is incremented for each suppressed MAC; pass nil to skip.
func resolveEdgeDstDevices(edges []discovery.Edge, ipToID map[string]string, macToID map[string]string, suppressedCounter prometheus.Counter) []discovery.Edge {
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
			if id, ok := macToID[dst]; ok {
				e.DstDevice = id
			} else {
				// Unresolved MAC — likely a host, not infrastructure.
				// Suppress rather than publish a mac-<hash> pseudo-device.
				slog.Debug("fdb: suppressing unresolved MAC peer; no LLDP correlation",
					"src_device", e.SrcDevice, "src_port", e.SrcPort, "mac", dst)
				_ = hw
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

type credentialCandidate struct {
	params      snmpwalk.Params
	profileName string
}

func credentialCandidates(cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig) []credentialCandidate {
	port := uint16(t.Port) //nolint:gosec
	if port == 0 {
		port = 161
	}

	var candidates []credentialCandidate
	seen := make(map[string]bool)
	if profileName, ok := resolver.CachedProfile(ip.String()); ok {
		if p, found := resolver.Profile(profileName); found {
			if params, err := profileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p); err == nil {
				candidates = append(candidates, credentialCandidate{params: params, profileName: profileName})
				seen[profileName] = true
			}
		}
	}

	// No profiles configured — use single-community from modules.snmp.community_env.
	// If the env var is unset, return no candidates so the caller fails closed.
	if len(cfg.Credentials.Profiles) == 0 {
		community := os.Getenv(cfg.Modules.SNMP.CommunityEnv)
		if community == "" {
			return candidates
		}
		return append(candidates, credentialCandidate{
			params: snmpwalk.Params{
				IP:        ip,
				Port:      port,
				Timeout:   cfg.Discovery.TimeoutPerDevice,
				Community: community,
			},
		})
	}

	for _, name := range resolver.Resolve(ip) {
		if seen[name] {
			continue
		}
		p, ok := resolver.Profile(name)
		if !ok {
			continue
		}
		params, err := profileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p)
		if err != nil {
			slog.Warn("credential profile unusable; skipping",
				"profile", name, "ip", ip, "error", err)
			continue
		}
		candidates = append(candidates, credentialCandidate{params: params, profileName: name})
		seen[name] = true
	}
	return candidates
}

func walkSystemWithCredentials(ctx context.Context, cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig) (*discovery.Device, snmpwalk.Params, string, error) {
	candidates := credentialCandidates(cfg, resolver, ip, t)
	if len(candidates) == 0 {
		return nil, snmpwalk.Params{}, "", fmt.Errorf("no usable credential profiles for %s", ip)
	}

	var lastErr error
	allTimedOut := true // true until we see a non-timeout failure
	for _, c := range candidates {
		if err := resolver.AcquireTrial(ctx); err != nil {
			return nil, snmpwalk.Params{}, "", err
		}
		trialCtx, cancel := context.WithTimeout(ctx, cfg.Discovery.TimeoutPerDevice)
		dev, err := snmpwalk.Walk(trialCtx, c.params)
		cancel()
		if err == nil {
			return dev, c.params, c.profileName, nil
		}
		lastErr = err
		if errors.Is(err, context.Canceled) {
			// Parent context cancelled (SIGTERM) — stop immediately.
			return nil, snmpwalk.Params{}, "", err
		}
		// SNMP v2c agents silently drop packets with a wrong community string —
		// the client gets DeadlineExceeded just as if the device were unreachable.
		// Always try the next candidate. Only call RecordFailure when at least one
		// failure was clearly not a timeout (i.e. the device is reachable but the
		// credential was wrong). Timing out on all candidates preserves the cache.
		if !errors.Is(err, context.DeadlineExceeded) {
			allTimedOut = false
		}
	}
	// Don't invalidate the cache when all failures were timeouts: a timeout
	// means the device was unreachable this cycle (not an auth failure), so
	// the cached profile is still likely correct.
	if !allTimedOut {
		resolver.RecordFailure(ip.String())
	}
	return nil, snmpwalk.Params{}, "", lastErr
}

func profileToParams(ip net.IP, port uint16, timeout time.Duration, p config.CredentialProfile) (snmpwalk.Params, error) {
	params := snmpwalk.Params{
		IP:          ip,
		Port:        port,
		Timeout:     timeout,
		Retries:     p.Retries,
		ContextName: p.ContextName,
	}
	switch p.Type {
	case config.ProfileTypeSNMPv2c:
		community := os.Getenv(p.CommunityEnv)
		if community == "" {
			return params, fmt.Errorf("env %q is empty", p.CommunityEnv)
		}
		params.Community = community
	case config.ProfileTypeSNMPv3:
		params.V3 = true
		params.Username = os.Getenv(p.UsernameEnv)
		if params.Username == "" {
			return params, fmt.Errorf("env %q is empty", p.UsernameEnv)
		}
		params.AuthKey = os.Getenv(p.AuthKeyEnv)
		params.PrivKey = os.Getenv(p.PrivKeyEnv)
		// Auth/priv protocol names are config-level (not secret); passed as
		// strings so main.go doesn't need to import gosnmp directly.
		params.AuthProto = p.AuthProtocol
		params.PrivProto = p.PrivProtocol
	default:
		return params, fmt.Errorf("unknown profile type %q", p.Type)
	}
	return params, nil
}

type moduleWalkFn func(context.Context, snmpwalk.Params, string, []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error)

type module struct {
	proto   string
	enabled bool
	walk    moduleWalkFn
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
