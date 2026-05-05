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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/bgp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/cdp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/fdb"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/ospf"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/events"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

type cycleStatus struct {
	LastCycleAt  time.Time
	DeviceErrors int64
}

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

	var status atomic.Pointer[cycleStatus]

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
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

	var discoveryDone sync.WaitGroup
	discoveryDone.Add(1)
	go func() {
		defer discoveryDone.Done()
		runDiscoveryLoop(ctx, logger, cfg, m, &status)
	}()

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
	drainDone := make(chan struct{})
	go func() { discoveryDone.Wait(); close(drainDone) }()
	select {
	case <-drainDone:
	case <-time.After(cfg.Discovery.TimeoutPerDevice + 5*time.Second):
		logger.Warn("discovery drain timed out, forcing exit")
	}
	logger.Info("clean shutdown complete")
}

// runDiscoveryLoop is the main discovery scheduler. It loads the LD-13
// snapshot on startup, starts the credential resolver, and then runs
// periodic cycles. Each cycle probes all configured targets concurrently
// under the LD-12 rate limiter, reconciles the resulting graph, diffs
// against the previous cycle, emits change events, updates metrics, and
// writes a new snapshot.
func runDiscoveryLoop(ctx context.Context, logger *slog.Logger, cfg *config.Config, m *metrics.Metrics, status *atomic.Pointer[cycleStatus]) {
	evLogger := events.New(logger)

	// LD-13: load snapshot, serve stale-but-valid metrics until first live cycle.
	m.GraphStale.Set(1)
	snap, err := snapshot.Load(cfg.Snapshot.Path)
	if err != nil {
		if errors.Is(err, snapshot.ErrVersionMismatch) {
			logger.Warn("snapshot version mismatch, cold start", "path", cfg.Snapshot.Path, "error", err)
		} else {
			logger.Warn("snapshot load failed, cold start", "path", cfg.Snapshot.Path, "error", err)
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
		logger.Info("snapshot loaded",
			"devices", len(snap.Devices),
			"edges", len(snap.Edges),
		)
		m.SnapshotLoadedDevicesTotal.Set(float64(len(snap.Devices)))
		publishInventoryMetrics(prevGraph, m)
	}

	// LD-12: credential resolver.
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		logger.Error("building credential resolver", "error", err)
		os.Exit(1)
	}
	if snap != nil {
		resolver.LoadCache(snap.CredentialCache)
	}

	// Parse CIDR allow-list once; the config is immutable at runtime.
	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)

	// Run one cycle immediately, then on the configured interval.
	cycle := func() {
		start := time.Now()
		newGraph, newAges, conflicts := runCycle(ctx, logger, cfg, m, resolver, allowedNets, ages)
		if ctx.Err() != nil {
			return
		}
		status.Store(&cycleStatus{
			LastCycleAt:  time.Now(),
			DeviceErrors: int64(len(cfg.Targets) - len(newGraph.Devices)),
		})
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
				m.TopologyChangeTotal.WithLabelValues(string(c.Kind), proto).Inc()
			}
		}
		if len(conflicts) > 0 {
			evLogger.EmitConflicts(ctx, conflicts)
			for _, c := range conflicts {
				m.TopologyConflictTotal.WithLabelValues(string(c.Kind)).Inc()
			}
		}
		prevGraph = newGraph
		ages = newAges
		publishInventoryMetrics(newGraph, m)
		m.GraphStale.Set(0)
		m.DiscoveryCycleDuration.Observe(time.Since(start).Seconds())

		// LD-13: write snapshot.
		credCache := resolver.SnapshotCache()
		f := snapshot.File{
			Devices:         newGraph.Devices,
			Edges:           newGraph.Edges,
			OutOfScope:      newGraph.OutOfScope,
			CredentialCache: credCache,
			UnconfirmedAges: graph.EdgeKeysToAges(ages),
		}
		if err := snapshot.Write(cfg.Snapshot.Path, f); err != nil {
			logger.Error("snapshot write failed", "error", err)
		} else {
			m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
		}
	}

	cycle()
	tick := time.NewTicker(cfg.Discovery.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cycle()
		}
	}
}

// runCycle probes all configured targets concurrently and returns the
// resulting graph and updated unconfirmed-age counters.
func runCycle(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	m *metrics.Metrics,
	resolver *credentials.Resolver,
	allowedNets []*net.IPNet,
	prevAges map[graph.EdgeKey]int,
) (discovery.Graph, map[graph.EdgeKey]int, []graph.Conflict) {
	type probeResult struct {
		device     *discovery.Device
		edges      []discovery.Edge
		outOfScope []discovery.OutOfScopeNeighbour
	}

	results := make([]probeResult, 0, len(cfg.Targets))
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
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			ip := net.ParseIP(target.Host)
			if ip == nil {
				addrs, err := net.DefaultResolver.LookupHost(ctx, target.Host)
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

			if err := resolver.AcquireTrial(ctx); err != nil {
				return
			}

			params, profileName, ok := resolveParams(cfg, resolver, ip, target)
			if !ok {
				logger.Warn("no credential profile found", "target", target.Host)
				m.CredentialTrialsTotal.WithLabelValues("failed").Inc()
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			devCtx, cancel := context.WithTimeout(ctx, cfg.Discovery.TimeoutPerDevice)
			defer cancel()

			dev, err := snmpwalk.Walk(devCtx, params)
			if err != nil {
				logger.Debug("snmp walk failed", "target", target.Host, "error", err)
				// Don't invalidate the credential cache on context cancellation
				// (SIGTERM, timeout): those are not auth failures.
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					resolver.RecordFailure(ip.String())
				}
				m.CredentialTrialsTotal.WithLabelValues("failed").Inc()
				m.SNMPWalksTotal.WithLabelValues("error").Inc()
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

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

			mods := []module{
				{"lldp", cfg.Modules.LLDP.Enabled, lldp.Walk},
				{"cdp", cfg.Modules.CDP.Enabled, cdp.Walk},
				{"fdb", cfg.Modules.FDB.Enabled, fdb.Walk},
				{"ospf", cfg.Modules.OSPF.Enabled, ospf.Walk},
				{"bgp", cfg.Modules.BGP.Enabled, bgp.Walk},
			}
			for _, mod := range mods {
				if !mod.enabled {
					continue
				}
				edges, oos := runModuleWalk(devCtx, logger, m, mod.proto, target.Host, mod.walk, params, dev.ID, allowedNets)
				allEdges = append(allEdges, edges...)
				allOOS = append(allOOS, oos...)
			}

			mu.Lock()
			results = append(results, probeResult{device: dev, edges: allEdges, outOfScope: allOOS})
			okCount++
			mu.Unlock()
		}()
	}
	wg.Wait()

	m.DiscoveryDevicesTotal.WithLabelValues("success").Set(float64(okCount))
	m.DiscoveryDevicesTotal.WithLabelValues("failed").Set(float64(failCount))

	var devices []discovery.Device
	var rawEdges []discovery.Edge
	var allOOS []discovery.OutOfScopeNeighbour
	for _, r := range results {
		if r.device != nil {
			devices = append(devices, *r.device)
		}
		rawEdges = append(rawEdges, r.edges...)
		allOOS = append(allOOS, r.outOfScope...)
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
	}, ages, conflicts
}

// resolveParams builds an snmpwalk.Params for the given target IP.
// Returns (params, profileName, true) on success, ("", "", false) when no
// usable credential is found.
func resolveParams(cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig) (snmpwalk.Params, string, bool) {
	port := uint16(t.Port)
	if port == 0 {
		port = 161
	}

	// Fast path: cached winning profile from a previous cycle.
	if profileName, ok := resolver.CachedProfile(ip.String()); ok {
		if p, found := resolver.Profile(profileName); found {
			params, err := profileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p)
			if err == nil {
				return params, profileName, true
			}
		}
	}

	// No profiles configured — fall back to legacy single-community from
	// modules.snmp.community_env for dev-time convenience.
	if len(cfg.Credentials.Profiles) == 0 {
		community := os.Getenv(cfg.Modules.SNMP.CommunityEnv)
		if community == "" {
			community = "public"
		}
		return snmpwalk.Params{
			IP:        ip,
			Port:      port,
			Timeout:   cfg.Discovery.TimeoutPerDevice,
			Community: community,
		}, "", true
	}

	// Trial path: try each profile in resolve order.
	names := resolver.Resolve(ip)
	for _, name := range names {
		p, ok := resolver.Profile(name)
		if !ok {
			continue
		}
		params, err := profileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p)
		if err == nil {
			return params, name, true
		}
	}
	return snmpwalk.Params{}, "", false
}

func profileToParams(ip net.IP, port uint16, timeout time.Duration, p config.CredentialProfile) (snmpwalk.Params, error) {
	params := snmpwalk.Params{
		IP:      ip,
		Port:    port,
		Timeout: timeout,
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

// publishInventoryMetrics replaces the Prometheus gauge sets for the current
// graph. Reset() followed by re-population removes stale device/edge series.
// There is a brief window between Reset and re-population where a concurrent
// scrape sees empty gauges; this is a known trade-off of the Reset pattern.
func publishInventoryMetrics(g discovery.Graph, m *metrics.Metrics) {
	m.DeviceInfo.Reset()
	m.DeviceUptimeSeconds.Reset()
	for _, d := range g.Devices {
		m.DeviceInfo.WithLabelValues(d.ID, d.Vendor, d.Model, d.OSVersion, d.Site).Set(1)
		m.DeviceUptimeSeconds.WithLabelValues(d.ID).Set(d.Uptime.Seconds())
	}

	m.TopologyEdgeInfo.Reset()
	for _, e := range g.Edges {
		m.TopologyEdgeInfo.WithLabelValues(
			e.SrcDevice, e.SrcPort,
			e.DstDevice, e.DstPort,
			e.DiscoveryProto,
			e.LinkKind,
			string(e.Direction),
		).Set(1)
	}

	m.OutOfScopeNeighboursTotal.Set(float64(len(g.OutOfScope)))
}

type moduleWalkFn func(context.Context, snmpwalk.Params, string, []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error)

type module struct {
	proto   string
	enabled bool
	walk    moduleWalkFn
}

func runModuleWalk(ctx context.Context, logger *slog.Logger, m *metrics.Metrics, proto, target string, fn moduleWalkFn, params snmpwalk.Params, devID string, allowedNets []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour) {
	edges, oos, err := fn(ctx, params, devID, allowedNets)
	if err != nil {
		logger.Debug(proto+" walk failed", "target", target, "error", err)
		m.SNMPWalksTotal.WithLabelValues("error").Inc()
		return nil, nil
	}
	m.SNMPWalksTotal.WithLabelValues("ok").Inc()
	return edges, oos
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
