package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/loglimit"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

// Run is the topology-exporter top-level entry point: it parses argv, loads
// config, wires up the HTTP server with /metrics, /healthz, /readyz, branches
// on federation.role (hub vs spoke/standalone), starts the discovery loop or
// the hub federation listener, blocks until SIGINT/SIGTERM, and drains
// cleanly. Returns the process exit code; main() calls os.Exit on the
// return value.
func Run(ctx context.Context, args []string) int {
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

	logger := NewLogger(*logLevel)
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

	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool // set to true after the first live cycle or spoke push

	// isReadyFn is the readiness check for /readyz. Default: process-local flag.
	// Hub mode replaces this with hub.IsReady before registering the mux handler.
	isReadyFn := ready.Load

	mux := http.NewServeMux()
	mux.Handle("/metrics", httpx.InstrumentMetricsHandler(
		promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()}),
		m.MetricsRenderDuration,
		m.MetricsPayloadBytes,
	))
	mux.HandleFunc("/healthz", httpx.NewHealthzHandler(&status))
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
			otlpSem = make(chan struct{}, MaxOTLPPushConcurrency)
		}

		workerDone.Add(1)
		go func() {
			defer workerDone.Done()
			RunDiscoveryLoop(ctx, LoopConfig{
				Cancel:        cancel,
				Logger:        logger,
				WarnLimiter:   warnLimiter,
				Cfg:           cfg,
				M:             m,
				WalkerMetrics: walkerMetrics,
				Status:        &status,
				Ready:         &ready,
				Spoke:         spoke,
				OtlpExp:       otlpExp,
				OtlpSem:       otlpSem,
				OtlpWg:        &otlpWg,
			})
		}()
	}

	mux.HandleFunc("/readyz", httpx.NewReadyzHandler(isReadyFn))

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
		case cfg.Listen.TLSCertFile != "": //nolint:staticcheck // intentional: deprecated legacy TLS path supported through v1.5.0 per cfg.EmitDeprecationWarnings
			// Deprecated path — server-side TLS only, no client auth. Operators
			// using this path saw a startup deprecation warning from
			// EmitDeprecationWarnings; the path stays functional until v1.5.0
			// removes the legacy fields.
			logger.Info("metrics TLS server listening (deprecated tls_cert_file/tls_key_file)", "addr", effectiveAddr)
			serveErr = srv.ListenAndServeTLS(cfg.Listen.TLSCertFile, cfg.Listen.TLSKeyFile) //nolint:staticcheck // intentional: deprecated legacy TLS path supported through v1.5.0 per cfg.EmitDeprecationWarnings
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

// NewLogger returns a slog.Logger configured to emit JSON to stderr at the
// requested level. Unknown level strings default to Info.
func NewLogger(level string) *slog.Logger {
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
