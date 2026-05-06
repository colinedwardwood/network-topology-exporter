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
	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
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
	mux.HandleFunc("/healthz", newHealthzHandler(&status))
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

	var workerDone sync.WaitGroup

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
				cancel()
				os.Exit(1) //nolint:gocritic
			}
		}
		workerDone.Add(1)
		go func() {
			defer workerDone.Done()
			runDiscoveryLoop(ctx, logger, cfg, m, &status, spoke)
		}()
	}

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
	go func() { workerDone.Wait(); close(drainDone) }()
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
// writes a new snapshot. When spoke is non-nil (federation.role: spoke),
// it also pushes the pre-reconciled graph to the hub after each cycle.
func runDiscoveryLoop(ctx context.Context, logger *slog.Logger, cfg *config.Config, m *metrics.Metrics, status *atomic.Pointer[cycleStatus], spoke *federation.Spoke) {
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

	cycle := func() {
		start := time.Now()
		newGraph, newAges, conflicts, deviceErrors := runCycle(ctx, logger, cfg, m, resolver, allowedNets, ages)
		if ctx.Err() != nil {
			return
		}
		status.Store(&cycleStatus{
			LastCycleAt:  time.Now(),
			DeviceErrors: int64(deviceErrors),
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

		// LD-13: write snapshot in a goroutine with a timeout so an NFS stall
		// cannot block the discovery cycle. f is passed by value so the next
		// cycle cannot mutate the data while the write is in progress.
		credCache := resolver.SnapshotCache()
		f := snapshot.File{
			Devices:         newGraph.Devices,
			Edges:           newGraph.Edges,
			OutOfScope:      newGraph.OutOfScope,
			CredentialCache: credCache,
			UnconfirmedAges: graph.EdgeKeysToAges(ages),
		}
		go func(f snapshot.File) {
			done := make(chan error, 1)
			go func() { done <- snapshot.Write(cfg.Snapshot.Path, f) }()
			select {
			case err := <-done:
				if err != nil {
					logger.Error("snapshot write failed", "error", err)
				} else {
					m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
				}
			case <-time.After(snapshotWriteTimeout):
				logger.Warn("snapshot write timed out (NFS stall?); discovery continues", "timeout", snapshotWriteTimeout)
			}
		}(f)

		// LD-15: uncoordinated mode — emit canonical-pair boundary observations
		// so a Mimir recording rule can stitch cross-boundary edges.
		if cfg.Federation.Role == "uncoordinated" {
			emitBoundaryObservations(newGraph.OutOfScope, m)
		}

		// LD-16/LD-17: spoke mode — push pre-reconciled graph to hub.
		if spoke != nil {
			payload := federation.SpokePayload{
				SpokeID:    cfg.Federation.Spoke.SpokeID,
				CycleAt:    time.Now(),
				Devices:    newGraph.Devices,
				Edges:      newGraph.Edges,
				OutOfScope: newGraph.OutOfScope,
				Ages:       graph.EdgeKeysToAges(ages),
			}
			if err := spoke.Push(ctx, payload); err != nil && ctx.Err() == nil {
				logger.Warn("spoke push failed", "error", err)
			}
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

			dev, params, profileName, err := walkSystemWithCredentials(ctx, cfg, resolver, ip, target)
			if err != nil {
				logger.Debug("snmp walk failed", "target", target.Host, "error", err)
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

			devCtx, cancel := context.WithTimeout(ctx, cfg.Discovery.TimeoutPerDevice)
			defer cancel()

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
				modStart := time.Now()
				edges, oos, err := mod.walk(devCtx, params, dev.ID, allowedNets)
				m.DiscoveryModuleDuration.WithLabelValues(mod.proto).Observe(time.Since(modStart).Seconds())
				if err != nil {
					logger.Debug(mod.proto+" walk failed", "target", target.Host, "error", err)
					if errors.Is(err, context.DeadlineExceeded) {
						m.SNMPWalksTotal.WithLabelValues("timeout").Inc()
					} else {
						m.SNMPWalksTotal.WithLabelValues("error").Inc()
					}
					continue
				}
				m.SNMPWalksTotal.WithLabelValues("ok").Inc()
				allEdges = append(allEdges, edges...)
				// Tag each OOS entry with the protocol that reported it so the
				// boundary_observation_info metric and hub OOS matching have a
				// proto label.
				for i := range oos {
					oos[i].Proto = mod.proto
				}
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
	}, ages, conflicts, int(failCount)
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

	// No profiles configured — fall back to legacy single-community from
	// modules.snmp.community_env for dev-time convenience.
	if len(cfg.Credentials.Profiles) == 0 {
		community := os.Getenv(cfg.Modules.SNMP.CommunityEnv)
		if community == "" {
			community = "public"
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

// emitBoundaryObservations resets and repopulates BoundaryObservationInfo for
// the current cycle. Each out-of-scope neighbour becomes one series; peer_a is
// always the alphabetically-smaller endpoint so a simple count == 2 recording
// rule detects confirmed cross-boundary edges (LD-15).
func emitBoundaryObservations(oos []discovery.OutOfScopeNeighbour, m *metrics.Metrics) {
	m.BoundaryObservationInfo.Reset()
	for _, n := range oos {
		peerA, peerB := canonicalPair(n.ReportingDevice, n.NeighbourHint)
		m.BoundaryObservationInfo.WithLabelValues(
			peerA, peerB, n.ReportingDevice, n.ReportingPort, n.Proto,
		).Set(1)
	}
}

func canonicalPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
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
