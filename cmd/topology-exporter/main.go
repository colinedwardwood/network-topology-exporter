// Command topology-exporter discovers network topology over SNMP / LLDP / CDP
// / BGP / OSPF / ARP / FDB and emits Prometheus metrics, Loki push events,
// and OpenTelemetry traces.
//
// See README.md for the emitted-signal reference and CONTRIBUTING.md for the
// LD-09 clean-room development commitments.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/owner-tbd/network-topology-exporter/internal/config"
	"github.com/owner-tbd/network-topology-exporter/internal/metrics"
	"github.com/owner-tbd/network-topology-exporter/internal/version"
)

func main() {
	var (
		configPath = flag.String("config.file", "/etc/topology-exporter/config.yaml", "Path to the YAML configuration file.")
		listenAddr = flag.String("web.listen-address", ":9100", "Address on which to expose /metrics and /healthz.")
		logLevel   = flag.String("log.level", "info", "Log level: debug | info | warn | error.")
		showVer    = flag.Bool("version", false, "Print version and exit.")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("topology-exporter %s (%s, built %s)\n", version.Version, version.Commit, version.BuildDate)
		return
	}

	logger := newLogger(*logLevel)
	logger.Info("starting",
		"version", version.Version,
		"commit", version.Commit,
		"build_date", version.BuildDate,
		"config", *configPath,
		"listen", *listenAddr,
	)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("loading config failed", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded",
		"discovery_interval", cfg.Discovery.Interval,
		"parallelism", cfg.Discovery.Parallelism,
		"target_count", len(cfg.Targets),
	)

	m := metrics.New()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, "topology-exporter %s\nendpoints: /metrics /healthz\n", version.Version)
	})

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go runDiscoveryLoop(ctx, logger, cfg, m)

	go func() {
		logger.Info("http server listening", "addr", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "error", err)
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
	logger.Info("clean shutdown complete")
}

// runDiscoveryLoop is the v0.1 placeholder discovery loop. It seeds one
// network_device_info series per configured target so the /metrics surface
// is non-empty end to end. Real walking arrives Day 2 per the v1 plan.
func runDiscoveryLoop(ctx context.Context, logger *slog.Logger, cfg *config.Config, m *metrics.Metrics) {
	tick := time.NewTicker(cfg.Discovery.Interval)
	defer tick.Stop()

	cycle := func() {
		start := time.Now()
		var ok int
		for _, t := range cfg.Targets {
			deviceID := t.Host
			m.DeviceInfo.WithLabelValues(deviceID, "unknown", "unknown", "unknown", t.Site, "").Set(1)
			ok++
		}
		m.DiscoveryDevicesTotal.WithLabelValues("success").Set(float64(ok))
		m.DiscoveryCycleDuration.Observe(time.Since(start).Seconds())
		logger.Info("discovery cycle complete",
			"devices_ok", ok,
			"duration_seconds", time.Since(start).Seconds(),
		)
	}

	cycle()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cycle()
		}
	}
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
