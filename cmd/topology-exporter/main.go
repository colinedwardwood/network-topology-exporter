// Command topology-exporter discovers network topology over SNMP, LLDP,
// CDP, BGP, OSPF, and FDB, and emits the result as Prometheus metrics and
// structured log lines.
//
// README.md documents the emitted-signal contract; CONTRIBUTING.md documents
// the clean-room development rules.
package main

import (
	"bufio"
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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"

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
	"github.com/colinedwardwood/network-topology-exporter/internal/loglimit"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

// snapshotWriteTimeout caps how long the background snapshot write goroutine
// waits before declaring an NFS stall and continuing the discovery cycle.
const snapshotWriteTimeout = 30 * time.Second

// largeTopologyEdgeThreshold is the edge count above which the exporter
// emits a warning pointing operators at docs/operator/scale.md. Intentionally
// well below the documented scale ceiling so the warning arrives before
// scrape latency becomes a problem — not after. The warning fires on the
// upward crossing of this threshold (issue #9), not on every cycle while
// above it, and is rate-limited by largeTopologyWarnCooldownCycles to keep
// an oscillating topology from flooding the log.
const largeTopologyEdgeThreshold = 5000

// largeTopologyWarnCooldownCycles caps the large-topology warning at one
// emission per N discovery cycles, even if the topology oscillates around
// the threshold and would otherwise re-cross upward on every cycle.
const largeTopologyWarnCooldownCycles = 60

// maybeWarnLargeTopology emits the large-topology warning when the edge
// count crosses largeTopologyEdgeThreshold upward (was at-or-below on the
// previous cycle, now strictly above), subject to a cooldown of
// largeTopologyWarnCooldownCycles between warnings. It returns the
// updated (prevAboveThreshold, lastWarnCycle) pair so the caller can
// thread state across cycles. Extracted from the cycle closure so the
// crossing rule can be unit-tested without driving full discovery cycles
// (issue #9).
func maybeWarnLargeTopology(
	logger *slog.Logger,
	edges, devices int,
	prevAbove bool,
	cycleNum, lastWarnCycle int,
) (nowAbove bool, newLastWarnCycle int) {
	nowAbove = edges > largeTopologyEdgeThreshold
	if nowAbove && !prevAbove && cycleNum-lastWarnCycle >= largeTopologyWarnCooldownCycles {
		logger.Warn("topology size is large; review scale guidance",
			"edges", edges,
			"devices", devices,
			"threshold", largeTopologyEdgeThreshold,
			"guidance", "docs/operator/scale.md")
		return nowAbove, cycleNum
	}
	return nowAbove, lastWarnCycle
}

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

// instrumentMetricsHandler wraps the Prometheus /metrics handler so that
// each scrape contributes one observation to the render-duration and payload-
// size histograms. Operators alert on the p99 of duration against the
// scraper's scrape_timeout — see docs/operator/scale.md. The wrapper streams
// the response body through to the underlying writer without buffering and
// counts the bytes that flow through Write().
func instrumentMetricsHandler(inner http.Handler, duration, payload prometheus.Histogram) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &countingResponseWriter{ResponseWriter: w}
		inner.ServeHTTP(rec, r)
		duration.Observe(time.Since(start).Seconds())
		payload.Observe(float64(rec.bytesWritten))
	})
}

// countingResponseWriter wraps http.ResponseWriter and counts the bytes
// passed to Write(). It does NOT buffer the body — each Write call streams
// straight to the wrapped writer and the counter is incremented by the
// number of bytes the wrapped writer reports as written. The counter is
// therefore exact for response bodies emitted via Write().
//
// This wrapper deliberately does NOT promote http.Hijacker or http.Pusher.
// Embedding http.ResponseWriter would otherwise silently promote any
// interfaces the underlying writer implements; if a future inner handler
// or middleware invoked Hijack() (RFC 6455 WebSocket upgrade) or Push()
// (HTTP/2 server push), the connection would be detached or pushed without
// passing through Write(), and bytesWritten would diverge from reality
// without any indication. The /metrics path served by this wrapper does
// not use WebSocket upgrade or HTTP/2 server push, so a loud panic on
// those code paths is preferable to a silently wrong byte counter.
type countingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
}

func (c *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.bytesWritten += n
	return n, err
}

func (c *countingResponseWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack panics: the /metrics handler does not use WebSocket upgrade, and
// allowing Hijack to promote silently through the embedded ResponseWriter
// would let a future middleware detach the connection without passing
// through Write(), invalidating bytesWritten without any signal.
func (c *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	panic("countingResponseWriter: Hijack not supported — wrapper is for the /metrics path only; WebSocket upgrade would bypass the byte counter")
}

// Push panics: the /metrics handler does not use HTTP/2 server push.
// Allowing Push to promote silently would let the inner handler emit
// bytes that never pass through Write(), invalidating bytesWritten.
func (c *countingResponseWriter) Push(target string, opts *http.PushOptions) error {
	panic("countingResponseWriter: Push not supported — wrapper is for the /metrics path only; HTTP/2 server push would bypass the byte counter")
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
	slog.SetDefault(logger)
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
	cfg.EmitDeprecationWarnings(logger)
	logger.Info("config loaded",
		"discovery_interval", cfg.Discovery.Interval,
		"parallelism", cfg.Discovery.Parallelism,
		"target_count", len(cfg.Targets),
	)
	if cfg.Modules.FDB.Enabled && !cfg.Modules.ARP.Enabled {
		logger.Warn("FDB is enabled but ARP enrichment is off; DstPort backfill on FDB-only edges will be unavailable. Set modules.arp.enabled: true to re-enable.")
	}

	m := metrics.New(cfg.Federation.Role == "uncoordinated")
	m.SnapshotLastWrittenUnix.SetToCurrentTime()

	// walkerMetrics adapts m.BGPWalkerOutcomeTotal to the snmputil.WalkerMetrics
	// interface so the bgp package can record walker outcomes without holding a
	// package-global counter handle. Threaded per-cycle into snmputil.Params
	// below; see internal/metrics/walker_metrics_adapter.go for the adapter.
	walkerMetrics := metrics.NewWalkerMetricsAdapter(m)

	// warnLimiter caps chronic same-key Warn emissions (issue #16). Wraps
	// `logger` and is threaded into spoke push, BGP/FDB walks, and the
	// per-cycle NFS-stall sites below. Process-singleton: keys are stable
	// across cycles so a chronic failure does not re-alert on every cycle.
	// DefaultCooldown (1h) keeps operators alerted on first occurrence and
	// re-alerted hourly thereafter without flooding the log.
	warnLimiter := loglimit.New(logger, loglimit.DefaultCooldown)

	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool // set to true after the first live cycle or spoke push

	// isReadyFn is the readiness check for /readyz. Default: process-local flag.
	// Hub mode replaces this with hub.IsReady before registering the mux handler.
	isReadyFn := ready.Load

	mux := http.NewServeMux()
	mux.Handle("/metrics", instrumentMetricsHandler(
		promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()}),
		m.MetricsRenderDuration,
		m.MetricsPayloadBytes,
	))
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
		IdleTimeout:       120 * time.Second,
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
				Devices:    hubSnap.Devices,
				Edges:      hubSnap.Edges,
				OutOfScope: hubSnap.OutOfScope,
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
			spoke, err = federation.NewSpoke(cfg.Federation, logger, warnLimiter, m)
			if err != nil {
				logger.Error("building federation spoke", "error", err)
				return 1
			}
		}

		var otlpExp *otlp.Exporter
		if cfg.Output.OTLP.Enabled {
			otlpExp = otlp.New(otlp.Config{
				Endpoint:   cfg.Output.OTLP.Endpoint,
				Timeout:    cfg.Output.OTLP.Timeout,
				InstanceID: cfg.Federation.Spoke.SpokeID, // empty in non-spoke roles → falls back to hostname
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
				cancel:        cancel,
				logger:        logger,
				warnLimiter:   warnLimiter,
				cfg:           cfg,
				m:             m,
				walkerMetrics: walkerMetrics,
				status:        &status,
				ready:         &ready,
				spoke:         spoke,
				otlpExp:       otlpExp,
				otlpSem:       otlpSem,
				otlpWg:        &otlpWg,
			})
		}()
	}

	mux.HandleFunc("/readyz", newReadyzHandler(isReadyFn))

	go func() {
		var serveErr error
		switch {
		case cfg.Listen.WebConfigFile != "":
			// Prometheus exporter-toolkit web-config: supports basic_auth, server TLS,
			// and mTLS via the same YAML schema as snmp_exporter / node_exporter /
			// blackbox_exporter. The toolkit handles cert reload-on-change, bcrypt,
			// and TLS-cipher hardening — no need to reimplement.
			logger.Info("metrics server listening (web-config)", "addr", effectiveAddr, "web_config_file", cfg.Listen.WebConfigFile)
			webFlags := &web.FlagConfig{
				WebListenAddresses: &[]string{effectiveAddr},
				WebSystemdSocket:   new(bool),
				WebConfigFile:      &cfg.Listen.WebConfigFile,
			}
			serveErr = web.ListenAndServe(srv, webFlags, logger)
		case cfg.Listen.TLSCertFile != "":
			// Deprecated path — server-side TLS only, no client auth. Operators
			// using this path saw a startup deprecation warning from
			// EmitDeprecationWarnings; the path stays functional until v1.5.0
			// removes the legacy fields.
			logger.Info("metrics TLS server listening (deprecated tls_cert_file/tls_key_file)", "addr", effectiveAddr)
			serveErr = srv.ListenAndServeTLS(cfg.Listen.TLSCertFile, cfg.Listen.TLSKeyFile)
		default:
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
	case <-time.After(time.Duration(float64(cfg.Discovery.Interval)*cfg.Discovery.CycleBudgetFraction) + cfg.Discovery.TimeoutPerDevice):
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
	cancel context.CancelFunc
	logger *slog.Logger
	// warnLimiter is the process-singleton rate-limiter for chronic
	// per-cycle Warn emissions (issue #16). Threaded into snmputil.Params
	// for per-walker use and consulted directly by the snapshot-writer
	// goroutine. May be nil — sites that consult it MUST fall back to a
	// direct slog.Warn in that case.
	warnLimiter *loglimit.Limiter
	cfg         *config.Config
	m           *metrics.Metrics
	// walkerMetrics is the snmputil.WalkerMetrics implementation threaded into
	// every Params constructed in runCycle. Replaces the old bgp package-global
	// counter wiring; see internal/metrics/walker_metrics_adapter.go.
	walkerMetrics snmpwalk.WalkerMetrics
	status        *atomic.Pointer[cycleStatus]
	ready         *atomic.Bool
	spoke         *federation.Spoke
	otlpExp       *otlp.Exporter
	otlpSem       chan struct{}   // semaphore bounding concurrent OTLP pushes; nil when OTLP disabled
	otlpWg        *sync.WaitGroup // tracks in-flight OTLP push goroutines for clean shutdown
}

// warnSnapshot emits a chronic-shape snapshot Warn through the per-cycle
// rate limiter. The key combines the named site with the configured
// snapshot path so two operators running on the same host but writing to
// different snapshot files do not share a suppression slot. Falls back to
// a direct slog.Warn when no limiter is configured (e.g. in tests that
// construct loopConfig inline). See issue #16.
func (lc loopConfig) warnSnapshot(ctx context.Context, site, msg string, attrs ...any) {
	if lc.warnLimiter != nil {
		key := "snapshot|" + site + "|" + lc.cfg.Snapshot.Path
		lc.warnLimiter.Warn(ctx, key, msg, attrs...)
		return
	}
	lc.logger.WarnContext(ctx, msg, attrs...)
}

func (lc loopConfig) otlpPush(ctx context.Context, fn func(context.Context) error, warnMsg string) {
	if lc.otlpSem != nil {
		select {
		case lc.otlpSem <- struct{}{}:
		default:
			lc.logger.Warn("otlp push dropped: concurrent limit reached")
			// status="dropped" never carries a failure reason — use the
			// shared n/a sentinel. Issue #20.
			lc.m.OTLPPushTotal.WithLabelValues("dropped", metrics.ReasonNA).Inc()
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
		pushCtx, cancel := context.WithTimeout(context.Background(), otlpPushTimeout)
		defer cancel()
		if err := fn(pushCtx); err != nil {
			lc.logger.Warn(warnMsg, "error", err)
			// Issue #20: partition status="error" by the OTLP sub-reason
			// derived from the error (timeout / tls_error / http_4xx /
			// http_5xx / network).
			lc.m.OTLPPushTotal.WithLabelValues("error", string(otlp.ClassifyPushError(err))).Inc()
		} else {
			lc.m.OTLPPushTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
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
	var snapWg sync.WaitGroup
	if lc.cfg.Snapshot.Path != "" {
		snapshotCh = make(chan snapshot.File, 1)
		snapWg.Add(1)
		go func() {
			defer snapWg.Done()
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
						// Rate-limit per path (issue #16): a chronic NFS stall
						// would emit this Warn every cycle until the stall
						// clears. Limiter keeps the operator alerted at first
						// occurrence and re-alerted hourly, not every minute.
						lc.warnSnapshot(ctx, "snapshot_write_in_flight",
							"snapshot write still in flight; dropping snapshot (NFS stall?)")
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
					// Rate-limit per path (issue #16): same chronic-NFS
					// pattern as the in-flight branch above.
					lc.warnSnapshot(ctx, "snapshot_write_timeout",
						"snapshot write timed out (NFS stall?); discovery continues",
						"timeout", snapshotWriteTimeout)
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
	lastWarnCycle := -largeTopologyWarnCooldownCycles
	cycle := func() {
		cycleNum++
		lc.m.GoRoutines.Set(float64(runtime.NumGoroutine()))
		start := time.Now()
		newGraph, newAges, conflicts, deviceErrors := runCycle(ctx, lc.logger, lc.cfg, lc.m, lc.walkerMetrics, lc.warnLimiter, resolver, allowedNets, ages)
		if ctx.Err() != nil {
			return
		}
		newGraph.OutOfScope = mergeOOSFirstSeen(newGraph.OutOfScope, prevGraph.OutOfScope)
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
			ages = newAges // advance counters so unconfirmed edges can still expire
			lc.m.GraphUpdatesRejectedTotal.WithLabelValues(string(metrics.RejectReasonSizeBudgetExceeded)).Inc()
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
				lc.m.TopologyChangeTotal.WithLabelValues(string(c.Kind), string(proto)).Inc()
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
		if lc.otlpExp != nil && (len(changes) > 0 || cycleNum%lc.cfg.Output.OTLP.HeartbeatCycles == 0) {
			g := newGraph
			lc.otlpPush(ctx, func(ctx context.Context) error {
				return lc.otlpExp.PushGraph(ctx, g)
			}, "otlp push failed")
		}
		lc.m.GraphStale.Set(0)
		lc.m.Topology.Update(newGraph)
		if lc.ready != nil {
			lc.ready.CompareAndSwap(false, true)
		}
		prevAboveThreshold, lastWarnCycle = maybeWarnLargeTopology(
			lc.logger, len(newGraph.Edges), len(newGraph.Devices),
			prevAboveThreshold, cycleNum, lastWarnCycle)
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
				// Rate-limit per path (issue #16): queue-full is the upstream
				// symptom of the same chronic-NFS stall as the two branches
				// in the writer goroutine. Keys are distinct so each path
				// surfaces independently on first occurrence.
				lc.warnSnapshot(ctx, "snapshot_queue_full",
					"snapshot write queue full; dropping (previous write still in flight)")
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
			snapWg.Wait()
			// Issue #5: per-device Zeroize defers in runCycle have already
			// overwritten the in-flight cycle's SNMP credential bytes by the
			// time runCycle's wg.Wait() returns. Logging here gives operators
			// a concrete signal in the shutdown sequence. See
			// docs/operator/security.md for the threat model and limits.
			lc.logger.Info("snmp credentials zeroized; shutting down discovery loop")
			return
		case <-tick.C:
			cycle()
		}
	}
}

// runCycle probes all configured targets concurrently and returns the
// resulting graph, updated unconfirmed-age counters, any reconciliation
// conflicts, and the count of targets that failed discovery.
//
// walkerMetrics is threaded into every snmpwalk.Params constructed below so
// that protocol walkers (currently only bgp) can record outcome counters
// without holding a package-global counter handle. May be nil in tests; the
// walker is expected to treat nil as "drop the increment".
func runCycle(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	m *metrics.Metrics,
	walkerMetrics snmpwalk.WalkerMetrics,
	warnLimiter snmpwalk.WarnLimiter,
	resolver *credentials.Resolver,
	allowedNets []*net.IPNet,
	prevAges map[graph.EdgeKey]int,
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

			ip := net.ParseIP(target.Host)
			if ip == nil {
				addrs, err := net.DefaultResolver.LookupHost(cycleCtx, target.Host)
				if err != nil || len(addrs) == 0 {
					logger.Warn("host resolution failed", "host", target.Host, "error", err)
					recordFail(metrics.DiscoveryReasonDNSFailed)
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
				return
			}

			dev, params, profileName, err := walkSystemWithCredentials(cycleCtx, cfg, resolver, ip, target, logger)
			if err != nil {
				logger.Warn("snmp walk failed", "target", target.Host, "error", err)
				m.DiscoveryHardFailTotal.WithLabelValues("system", "system_group_walk_error").Inc()
				m.CredentialTrialsTotal.WithLabelValues("failed").Inc()
				// Issue #20: partition the walk-failure counter by
				// sub-reason. Timeouts surface via status="timeout"
				// (reason=n/a — the status is the reason). Non-timeout
				// failures from this layer are attributed to auth: the
				// credential-rotation loop in walkSystemWithCredentials
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

			devCtx, cancel := context.WithTimeout(cycleCtx, cfg.Discovery.TimeoutPerDevice)
			defer cancel()
			devCtx = snmpwalk.ContextWithDecodeIssueReporter(devCtx, func(issue snmpwalk.DecodeIssue) {
				m.DiscoveryDecodeIssues.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
				m.DiscoveryQuarantinedRowsTotal.WithLabelValues(issue.Module, string(issue.OID), issue.Reason).Add(float64(issue.Count))
			})

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
					modStatus[mod.proto] = 2
					continue
				}
				m.SNMPWalksTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
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

			// Walk ARP table for MAC→IP enrichment when modules.arp.enabled
			// is true (default). The map feeds synthesizeEdges below as a
			// fallback for FDB-only edges where LLDP did not provide the
			// neighbour identity. Failures are non-fatal: LLDP-based
			// correlation still works without ARP data.
			if cfg.Modules.ARP.Enabled {
				arpClient, arpErr := snmpwalk.Open(params)
				if arpErr != nil {
					logger.Debug("ARP table walk failed; MAC→IP resolution unavailable for this device",
						"device", dev.ID, "err", arpErr)
				} else {
					arpMACToIP, arpErr := snmpwalk.WalkARPTable(devCtx, arpClient)
					_ = arpClient.Conn.Close()
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

	// Sort by config-file order so that deduplicateDevices always picks the
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
	canonicalEdges := synthesizeEdges(logger, rawEdges, ipToID, allARPMACs, m.FDBSuppressedMACs)

	// Phase 2 complete; run reconciliation.
	reconciledEdges, conflicts := graph.Reconcile(canonicalEdges)

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
		Devices:    deduplicateDevices(devices),
		Edges:      reconciledEdges,
		OutOfScope: deduplicateOOS(allOOS),
	}, ages, conflicts, int(failCount)
}

// deduplicateOOS removes duplicate OutOfScopeNeighbour entries that arise when
// multiple discovery protocols (e.g. LLDP and CDP) both observe the same
// out-of-scope neighbour on the same device/port. Uniqueness is keyed on
// (ReportingDevice, ReportingPort, NeighbourHint); the first occurrence is kept
// so the Proto field reflects the first protocol that reported the neighbour.
// The returned slice preserves insertion order.
func deduplicateOOS(oos []discovery.OutOfScopeNeighbour) []discovery.OutOfScopeNeighbour {
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

// mergeOOSFirstSeen preserves FirstSeen timestamps across collection cycles.
// Each cycle, OutOfScopeNeighbour entries are built fresh with FirstSeen set to
// time.Now(). This function restores the original FirstSeen from prevOOS for any
// entry that was already known, keyed on (ReportingDevice, ReportingPort, NeighbourHint).
// Entries not present in prevOOS keep the cycle's time.Now() as their FirstSeen.
func mergeOOSFirstSeen(newOOS, prevOOS []discovery.OutOfScopeNeighbour) []discovery.OutOfScopeNeighbour {
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

// deduplicateDevices removes Device entries with duplicate IDs that can arise
// when the same physical device is polled via multiple target addresses (e.g.
// primary IP and loopback IP both resolving to the same sysName). The first
// occurrence in config order is kept; callers must sort the slice by config
// index before calling this function to ensure deterministic results.
func deduplicateDevices(devices []discovery.Device) []discovery.Device {
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

// synthesizeEdges resolves protocol-level observations into canonical graph edges.
//
// Phase 1 (done by callers): each protocol module emits observations with raw
// endpoints — MACs from FDB/LLDP, IPs from CDP/BGP — into rawEdges.
//
// Phase 2 (this function): resolve endpoints to sysName device IDs using the
// LLDP chassis-MAC index and ARP table, backfill missing DstPorts from LLDP
// observations that share the same three-endpoint tuple, and drop observations
// whose remote endpoint could not be resolved to a known device.
func synthesizeEdges(
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

	edges := resolveEdgeDstDevices(logger, rawEdges, ipToID, macToID, suppressedCounter)

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

// resolveEdgeDstDevices replaces IP-valued and MAC-valued DstDevice fields with
// the canonical device ID (sysName) from the discovered inventory when available.
// BGP/OSPF/IS-IS walks report peer IPs; FDB reports raw MACs; LLDP reports sysNames.
// For IP DstDevices: resolves to sysName using the device walk inventory; unresolved
// IPs are kept (still useful for routing protocol edges).
// For MAC DstDevices: resolves to sysName via the LLDP identity index; unresolved
// MACs are suppressed (likely hosts, not infrastructure).
// suppressedCounter is incremented for each suppressed MAC; pass nil to skip.
func resolveEdgeDstDevices(logger *slog.Logger, edges []discovery.Edge, ipToID map[string]string, macToID map[string]string, suppressedCounter prometheus.Counter) []discovery.Edge {
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

type credentialCandidate struct {
	params      snmpwalk.Params
	profileName string
}

func credentialCandidates(cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig, logger *slog.Logger) []credentialCandidate {
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
				IP:      ip,
				Port:    port,
				Timeout: cfg.Discovery.TimeoutPerDevice,
				// Convert env-string to []byte so the discovery cycle can
				// zeroize the credential at end-of-cycle (issue #5). This
				// []byte is a fresh copy; the env-var storage owned by
				// os.Getenv is out of our reach.
				Community: []byte(community),
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
			logger.Warn("credential profile unusable; skipping",
				"profile", name, "ip", ip, "error", err)
			continue
		}
		candidates = append(candidates, credentialCandidate{params: params, profileName: name})
		seen[name] = true
	}
	return candidates
}

func walkSystemWithCredentials(ctx context.Context, cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig, logger *slog.Logger) (*discovery.Device, snmpwalk.Params, string, error) {
	candidates := credentialCandidates(cfg, resolver, ip, t, logger)
	if len(candidates) == 0 {
		return nil, snmpwalk.Params{}, "", fmt.Errorf("no usable credential profiles for %s", ip)
	}

	// Credential zeroization (issue #5): every candidate carries SNMP secret
	// bytes. On every exit path we overwrite the secrets of every candidate
	// we will NOT return. The winning candidate's secrets stay alive until
	// the caller zeroizes them after module walks complete.
	zeroizeFromIdx := func(start int) {
		for i := start; i < len(candidates); i++ {
			candidates[i].params.Zeroize()
		}
	}

	var lastErr error
	allTimedOut := true // true until we see a non-timeout failure
	for i := range candidates {
		c := &candidates[i]
		if err := resolver.AcquireTrial(ctx); err != nil {
			zeroizeFromIdx(i)
			return nil, snmpwalk.Params{}, "", err
		}
		trialCtx, cancel := context.WithTimeout(ctx, cfg.Discovery.TimeoutPerDevice)
		dev, err := snmpwalk.Walk(trialCtx, c.params)
		cancel()
		if err == nil {
			// Caller owns c.params now and must zeroize it after module walks.
			zeroizeFromIdx(i + 1)
			return dev, c.params, c.profileName, nil
		}
		lastErr = err
		// This candidate is finished — zeroize its credentials.
		c.params.Zeroize()
		if ctx.Err() != nil {
			// Parent context done (SIGTERM or cycle budget expiry) — stop immediately.
			zeroizeFromIdx(i + 1)
			return nil, snmpwalk.Params{}, "", ctx.Err()
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
		// SNMP credentials are held as []byte so the discovery cycle can
		// zeroize them at end-of-cycle (issue #5). Each []byte conversion
		// below is a fresh copy of the env-var bytes.
		community := os.Getenv(p.CommunityEnv)
		if community == "" {
			return params, fmt.Errorf("env %q is empty", p.CommunityEnv)
		}
		params.Community = []byte(community)
	case config.ProfileTypeSNMPv3:
		params.V3 = true
		params.Username = os.Getenv(p.UsernameEnv)
		if params.Username == "" {
			return params, fmt.Errorf("env %q is empty", p.UsernameEnv)
		}
		authKey := os.Getenv(p.AuthKeyEnv)
		if p.AuthKeyEnv != "" && authKey == "" {
			return params, fmt.Errorf("env %q is empty or unset", p.AuthKeyEnv)
		}
		params.AuthKey = []byte(authKey)
		privKey := os.Getenv(p.PrivKeyEnv)
		if p.PrivKeyEnv != "" && privKey == "" {
			return params, fmt.Errorf("env %q is empty or unset", p.PrivKeyEnv)
		}
		params.PrivKey = []byte(privKey)
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
