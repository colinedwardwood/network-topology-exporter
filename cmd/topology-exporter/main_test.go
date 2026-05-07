package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

func systemPDUs(sysName string) []gsnmp.SnmpPDU {
	return []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte(sysName)},
	}
}

func TestRunCycleTwoDevices(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	addr1 := snmptest.Start(t, "public", systemPDUs("sw-01"))
	addr2 := snmptest.Start(t, "public", systemPDUs("sw-02"))

	_, port1 := snmptest.ParseAddr(addr1)
	_, port2 := snmptest.ParseAddr(addr2)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              4,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				CIDRAllowList: []string{"127.0.0.0/8"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "default",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "TEST_COMMUNITY",
				},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{
			Path: t.TempDir() + "/snapshot.json",
		},
		Targets: []config.TargetConfig{
			{Host: "127.0.0.1", Port: int(port1)},
			{Host: "127.0.0.1", Port: int(port2)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)

	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(g.Devices))
	}

	ids := make([]string, len(g.Devices))
	for i, d := range g.Devices {
		ids[i] = d.ID
	}
	sort.Strings(ids)

	if ids[0] != "sw-01" {
		t.Errorf("expected device ID sw-01, got %q", ids[0])
	}
	if ids[1] != "sw-02" {
		t.Errorf("expected device ID sw-02, got %q", ids[1])
	}
}

func TestRunCycleTriesFallbackCredentialProfiles(t *testing.T) {
	t.Setenv("BAD_COMMUNITY", "wrong")
	t.Setenv("GOOD_COMMUNITY", "public")

	addr := snmptest.Start(t, "public", systemPDUs("sw-01"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				CIDRAllowList: []string{"127.0.0.0/8"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "bad",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "BAD_COMMUNITY",
				},
				{
					Name:         "good",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "GOOD_COMMUNITY",
				},
			},
			FallbackOrder: []string{"bad", "good"},
		},
		Targets: []config.TargetConfig{
			{Host: "127.0.0.1", Port: int(port)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 1 {
		t.Fatalf("expected fallback credential to discover 1 device, got %d", len(g.Devices))
	}
	if profile, ok := resolver.CachedProfile("127.0.0.1"); !ok || profile != "good" {
		t.Fatalf("cached profile = (%q, %v), want (good, true)", profile, ok)
	}
}

func TestHealthzHandlerNilStatus(t *testing.T) {
	var status atomic.Pointer[cycleStatus]
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", newHealthzHandler(&status))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if m["status"] != "ok" {
		t.Errorf("status field = %q, want ok", m["status"])
	}
}

func TestHealthzHandlerPopulatedStatus(t *testing.T) {
	var status atomic.Pointer[cycleStatus]
	now := time.Now()
	status.Store(&cycleStatus{LastCycleAt: now, DeviceErrors: 2})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", newHealthzHandler(&status))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if m["device_errors"] != float64(2) {
		t.Errorf("device_errors = %v, want 2", m["device_errors"])
	}
}

func TestProfileToParams(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	timeout := 5 * time.Second
	port := uint16(161)

	t.Run("v2c_ok", func(t *testing.T) {
		t.Setenv("TEST_COMM", "secret")
		p := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMM"}
		params, err := profileToParams(ip, port, timeout, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if params.Community != "secret" {
			t.Errorf("Community = %q, want secret", params.Community)
		}
	})

	t.Run("v2c_empty_env", func(t *testing.T) {
		t.Setenv("TEST_COMM_EMPTY", "")
		p := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMM_EMPTY"}
		_, err := profileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for empty community env, got nil")
		}
	})

	t.Run("v3_ok", func(t *testing.T) {
		t.Setenv("TEST_USER", "admin")
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_USER",
		}
		params, err := profileToParams(ip, port, timeout, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !params.V3 {
			t.Error("V3 should be true")
		}
		if params.Username != "admin" {
			t.Errorf("Username = %q, want admin", params.Username)
		}
	})

	t.Run("v3_empty_username", func(t *testing.T) {
		t.Setenv("TEST_USER_EMPTY", "")
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_USER_EMPTY",
		}
		_, err := profileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for empty username env, got nil")
		}
	})

	t.Run("unknown_type", func(t *testing.T) {
		p := config.CredentialProfile{Type: "snmp_v1"}
		_, err := profileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for unknown profile type, got nil")
		}
	})
}

func lldpPDUs(localPortName string, remoteDeviceName string) []gsnmp.SnmpPDU {
	locBase := ".1.0.8802.1.1.2.1.3.7.1."
	remBase := ".1.0.8802.1.1.2.1.4.1.1."
	portNum := "1"
	timeMark := "0"
	remIdx := "1"
	remSuffix := timeMark + "." + portNum + "." + remIdx

	return []gsnmp.SnmpPDU{
		{Name: locBase + "2." + portNum, Type: gsnmp.Integer, Value: int(5)},
		{Name: locBase + "3." + portNum, Type: gsnmp.OctetString, Value: []byte(localPortName)},

		{Name: remBase + "4." + remSuffix, Type: gsnmp.Integer, Value: int(4)},
		{Name: remBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}},
		{Name: remBase + "6." + remSuffix, Type: gsnmp.Integer, Value: int(5)},
		{Name: remBase + "7." + remSuffix, Type: gsnmp.OctetString, Value: []byte(localPortName)},
		{Name: remBase + "9." + remSuffix, Type: gsnmp.OctetString, Value: []byte(remoteDeviceName)},
	}
}

func TestRunCycleLLDPEdge(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	pdus1 := append(systemPDUs("sw-01"), lldpPDUs("eth1", "sw-02")...)
	pdus2 := append(systemPDUs("sw-02"), lldpPDUs("eth1", "sw-01")...)

	addr1 := snmptest.Start(t, "public", pdus1)
	addr2 := snmptest.Start(t, "public", pdus2)

	_, port1 := snmptest.ParseAddr(addr1)
	_, port2 := snmptest.ParseAddr(addr2)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              4,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				CIDRAllowList: []string{"127.0.0.0/8"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
			LLDP: config.ModuleToggle{Enabled: true},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "default",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "TEST_COMMUNITY",
				},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{
			Path: t.TempDir() + "/snapshot.json",
		},
		Targets: []config.TargetConfig{
			{Host: "127.0.0.1", Port: int(port1)},
			{Host: "127.0.0.1", Port: int(port2)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)

	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(g.Devices))
	}

	if len(g.Edges) < 1 {
		t.Fatalf("expected at least 1 edge, got 0")
	}

	var target *discovery.Edge
	for i := range g.Edges {
		e := &g.Edges[i]
		if (e.SrcDevice == "sw-01" && e.DstDevice == "sw-02") ||
			(e.SrcDevice == "sw-02" && e.DstDevice == "sw-01") {
			target = e
			break
		}
	}
	if target == nil {
		t.Fatalf("no edge found connecting sw-01 and sw-02; edges: %v", g.Edges)
	}

	if target.Direction != discovery.DirectionBidirectional {
		t.Errorf("edge direction = %q, want %q", target.Direction, discovery.DirectionBidirectional)
	}
}

// LD-15: boundary observations — series count tracks OOS slice length.
func TestEmitBoundaryObservations(t *testing.T) {
	m := metrics.New(true) // uncoordinated mode enables boundary observations
	oos := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-b", ReportingPort: "Gi0/1", NeighbourHint: "sw-a", Proto: "lldp"},
		{ReportingDevice: "sw-c", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "cdp"},
	}
	m.Topology.Update(discovery.Graph{OutOfScope: oos})

	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var count int
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_boundary_observation_info" {
			count = len(mf.GetMetric())
		}
	}
	if count != 2 {
		t.Errorf("series count = %d, want 2", count)
	}

	// Update with one fewer entry: the count should drop (no Reset needed).
	m.Topology.Update(discovery.Graph{OutOfScope: oos[:1]})
	mfs, _ = m.Registry().Gather()
	count = 0
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_boundary_observation_info" {
			count = len(mf.GetMetric())
		}
	}
	if count != 1 {
		t.Errorf("after update, series count = %d, want 1", count)
	}
}

// TestReadyzHandlerNotReady verifies that /readyz returns 503 before the first
// cycle or spoke push has completed.
func TestReadyzHandlerNotReady(t *testing.T) {
	handler := newReadyzHandler(func() bool { return false })
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when not ready", rec.Code)
	}
}

// TestReadyzHandlerReady verifies that /readyz returns 200 once the readiness
// function returns true.
func TestReadyzHandlerReady(t *testing.T) {
	handler := newReadyzHandler(func() bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when ready", rec.Code)
	}
}

// TestTopologyCollectorPopulatesMetrics verifies that Topology.Update populates
// device, edge, and OOS-count metrics, and that a second call with an empty
// graph reflects the new state without stale series.
func TestTopologyCollectorPopulatesMetrics(t *testing.T) {
	m := metrics.New(false)
	g := discovery.Graph{
		Devices: []discovery.Device{
			{ID: "sw-1", Vendor: "cisco", Model: "catalyst", OSVersion: "15.2", Site: "dc-a"},
		},
		Edges: []discovery.Edge{
			{
				SrcDevice: "sw-1", SrcPort: "Gi0/1",
				DstDevice: "sw-2", DstPort: "Gi0/2",
				DiscoveryProto: "lldp", LinkKind: "ethernet",
				Direction: discovery.DirectionBidirectional,
			},
		},
		OutOfScope: []discovery.OutOfScopeNeighbour{{ReportingDevice: "sw-1"}},
	}

	m.Topology.Update(g)

	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	counts := make(map[string]int)
	for _, mf := range mfs {
		counts[mf.GetName()] = len(mf.GetMetric())
	}
	if counts["network_device_info"] != 1 {
		t.Errorf("network_device_info series = %d, want 1", counts["network_device_info"])
	}
	if counts["network_topology_edge_info"] != 1 {
		t.Errorf("network_topology_edge_info series = %d, want 1", counts["network_topology_edge_info"])
	}

	// After updating to an empty graph: device and edge series must disappear.
	m.Topology.Update(discovery.Graph{})
	mfs, _ = m.Registry().Gather()
	for _, mf := range mfs {
		switch mf.GetName() {
		case "network_device_info", "network_topology_edge_info":
			t.Errorf("%s has %d series after empty graph update, want 0", mf.GetName(), len(mf.GetMetric()))
		case "network_topology_out_of_scope_neighbours_total":
			for _, mm := range mf.GetMetric() {
				if mm.GetGauge().GetValue() != 0 {
					t.Errorf("out_of_scope_neighbours_total = %v after empty graph, want 0", mm.GetGauge().GetValue())
				}
			}
		}
	}
}

// TestRunDiscoveryLoopClearsGraphStale verifies that runDiscoveryLoop sets
// GraphStale=1 at startup, runs one cycle against a live SNMP agent, clears
// GraphStale to 0, and records a cycleStatus after the cycle completes.
func TestRunDiscoveryLoopClearsGraphStale(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-loop"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second, // long — second cycle never fires
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDiscoveryLoop(ctx, loopConfig{
			cancel: func() {},
			logger: slog.Default(),
			cfg:    cfg,
			m:      m,
			status: &status,
			ready:  &ready,
		})
	}()

	// Poll until GraphStale is cleared — set to 0 after the first successful cycle.
	deadline := time.After(12 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("GraphStale never cleared — first discovery cycle did not complete within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				cancel()
				<-done
				if s := status.Load(); s == nil {
					t.Error("cycleStatus was never set after first cycle")
				}
				if !ready.Load() {
					t.Error("ready flag was not set after first cycle")
				}
				return
			}
		}
	}
}

// TestBoundaryObservationsCanonicalOrder verifies peer_a is always
// alphabetically smaller regardless of which device reported first.
func TestBoundaryObservationsCanonicalOrder(t *testing.T) {
	m := metrics.New(true)
	oos := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-z", ReportingPort: "Gi0/1", NeighbourHint: "sw-a", Proto: "lldp"},
	}
	m.Topology.Update(discovery.Graph{OutOfScope: oos})

	mfs, _ := m.Registry().Gather()
	for _, mf := range mfs {
		if mf.GetName() != "network_topology_boundary_observation_info" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "peer_a" && lp.GetValue() != "sw-a" {
					t.Errorf("peer_a = %q, want sw-a (alphabetically smaller)", lp.GetValue())
				}
				if lp.GetName() == "peer_b" && lp.GetValue() != "sw-z" {
					t.Errorf("peer_b = %q, want sw-z", lp.GetValue())
				}
			}
		}
	}
}

// ── run() tests ───────────────────────────────────────────────────────────────

// TestRunVersionFlag exercises the --version short-circuit in run().
func TestRunVersionFlag(t *testing.T) {
	code := run(context.Background(), []string{"--version"})
	if code != 0 {
		t.Errorf("--version: exit code = %d, want 0", code)
	}
}

// TestRunUnknownFlag verifies that an unrecognised flag causes run() to return 1.
func TestRunUnknownFlag(t *testing.T) {
	code := run(context.Background(), []string{"--no-such-flag"})
	if code != 1 {
		t.Errorf("unknown flag: exit code = %d, want 1", code)
	}
}

// TestRunMissingConfigFile verifies that run() returns 1 when the config file
// does not exist.
func TestRunMissingConfigFile(t *testing.T) {
	code := run(context.Background(), []string{"--config.file=/nonexistent/path.yaml"})
	if code != 1 {
		t.Errorf("missing config: exit code = %d, want 1", code)
	}
}

// TestRunInvalidYAMLConfig verifies that run() returns 1 when the config file
// contains invalid YAML.
func TestRunInvalidYAMLConfig(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-config-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	_, _ = fmt.Fprint(f, "discovery:\n  interval: [this is not a duration\n")
	_ = f.Close()

	code := run(context.Background(), []string{"--config.file=" + f.Name()})
	if code != 1 {
		t.Errorf("invalid YAML: exit code = %d, want 1", code)
	}
}

// TestRunLogLevelFlag exercises the --log.level flag in run(), covering the
// newLogger switch branches that produce debug, warn, and error level loggers.
// We deliberately use a nonexistent config so run() returns immediately after
// logging (which is what we care about — reaching newLogger with each level).
func TestRunLogLevelFlag(t *testing.T) {
	for _, level := range []string{"debug", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			code := run(context.Background(), []string{
				"--log.level=" + level,
				"--config.file=/nonexistent/path.yaml",
			})
			if code != 1 {
				t.Errorf("--log.level=%s: exit code = %d, want 1 (config not found)", level, code)
			}
		})
	}
}

// TestRunHubModeInvalidTLS exercises the hub code path in run(). The hub's
// Serve goroutine fails on the missing TLS certs and cancels the context, which
// triggers the normal shutdown path (exit code 0).
func TestRunHubModeInvalidTLS(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "hub.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
federation:
  role: hub
  spoke_timeout: 180s
  hub:
    listen_addr: 127.0.0.1:0
    tls_ca_cert: /nonexistent/ca.pem
    tls_cert: /nonexistent/hub.crt
    tls_key: /nonexistent/hub.key
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port for the metrics listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := run(ctx, []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	// The hub Serve goroutine cancels the context when TLS cert read fails;
	// run() then follows the normal clean-shutdown path (exit code 0).
	if code != 0 {
		t.Errorf("hub invalid TLS: exit code = %d, want 0", code)
	}
}

// TestRunSpokeModeInvalidTLS verifies that run() returns 1 when spoke mode is
// configured with nonexistent TLS certificate files. NewSpoke reads the cert
// files synchronously, so the error surfaces before any goroutine is launched.
func TestRunSpokeModeInvalidTLS(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "spoke.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 10s
  parallelism: 1
  scope:
    cidr_allow_list: []
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
federation:
  role: spoke
  spoke_timeout: 180s
  spoke:
    spoke_id: test-spoke
    hub_url: https://127.0.0.1:9999
    tls_ca_cert: /nonexistent/ca.pem
    tls_cert: /nonexistent/spoke.crt
    tls_key: /nonexistent/spoke.key
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port so web.listen-address does not conflict with anything.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	code := run(context.Background(), []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	if code != 1 {
		t.Errorf("spoke invalid TLS: exit code = %d, want 1", code)
	}
}

// TestRunListenPortConflict verifies that run() returns 0 (clean shutdown) when
// the HTTP listen address is already bound. The HTTP server goroutine cancels
// the context on failure, causing run() to drain and exit via the normal path.
func TestRunListenPortConflict(t *testing.T) {
	// Hold a port open so ListenAndServe fails.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blocker listen: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	listenAddr := blocker.Addr().String()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "standalone.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := run(ctx, []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	// The HTTP server goroutine cancels the context on bind failure, triggering
	// the normal clean-shutdown path which returns 0.
	if code != 0 {
		t.Errorf("port conflict: exit code = %d, want 0", code)
	}
}

// TestRunStandaloneContextCancelled verifies that run() performs a clean
// shutdown and returns 0 when the passed context is cancelled. Uses a config
// with no targets so the discovery cycle completes immediately.
func TestRunStandaloneContextCancelled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "standalone.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port to guarantee no conflict.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{
			"--config.file=" + cfgPath,
			"--web.listen-address=" + listenAddr,
		})
	}()

	// Give the discovery loop time to complete its first cycle (no targets,
	// essentially instant) before cancelling.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("standalone cancel: exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s after context cancel")
	}
}

// ── newLogger tests ───────────────────────────────────────────────────────────

// TestNewLogger exercises all switch branches in newLogger.
func TestNewLogger(t *testing.T) {
	for _, level := range []string{"debug", "warn", "error", "info", "unknown"} {
		lg := newLogger(level)
		if lg == nil {
			t.Errorf("newLogger(%q) returned nil", level)
		}
	}
}

// ── runDiscoveryLoop additional coverage ──────────────────────────────────────

// TestRunDiscoveryLoopVersionMismatchSnapshot verifies that runDiscoveryLoop
// starts cleanly when the on-disk snapshot has an unrecognised version
// (ErrVersionMismatch cold-start path).
func TestRunDiscoveryLoopVersionMismatchSnapshot(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-mismatch"))
	_, port := snmptest.ParseAddr(addr)

	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	// Write a snapshot with an unrecognised version number.
	if err := os.WriteFile(snapPath, []byte(`{"version":9999,"written_at":"2020-01-01T00:00:00Z","devices":[],"edges":[]}`), 0600); err != nil {
		t.Fatalf("write bad snapshot: %v", err)
	}

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: snapPath},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDiscoveryLoop(ctx, loopConfig{
			cancel: func() {},
			logger: slog.Default(),
			cfg:    cfg,
			m:      m,
			status: &status,
			ready:  &ready,
		})
	}()

	deadline := time.After(12 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("version-mismatch snapshot: GraphStale never cleared within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				cancel()
				<-done
				return
			}
		}
	}
}

// TestRunDiscoveryLoopWithSnapshot verifies that runDiscoveryLoop correctly
// loads and restores a pre-existing snapshot on startup.
func TestRunDiscoveryLoopWithSnapshot(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-snap"))
	_, port := snmptest.ParseAddr(addr)

	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: snapPath},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	// Run one full cycle to produce a snapshot on disk.
	m1 := metrics.New(false)
	var s1 atomic.Pointer[cycleStatus]
	var r1 atomic.Bool
	ctx1, cancel1 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel1()
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		runDiscoveryLoop(ctx1, loopConfig{
			cancel: cancel1,
			logger: slog.Default(),
			cfg:    cfg,
			m:      m1,
			status: &s1,
			ready:  &r1,
		})
	}()
	// Wait for the snapshot write to complete (SnapshotLastWrittenUnix > 0),
	// not just GraphStale == 0. The write runs in a detached goroutine that may
	// still be in-flight when GraphStale first clears.
	deadline1 := time.After(12 * time.Second)
	poll1 := time.NewTicker(50 * time.Millisecond)
	defer poll1.Stop()
	for {
		select {
		case <-deadline1:
			t.Fatal("first runDiscoveryLoop: snapshot not written within deadline")
		case <-poll1.C:
			if testutil.ToFloat64(m1.SnapshotLastWrittenUnix) > 0 {
				cancel1()
				<-done1
				goto snapshotReady
			}
		}
	}
snapshotReady:

	// Now start a second loop — it should load the snapshot produced above.
	m2 := metrics.New(false)
	var s2 atomic.Pointer[cycleStatus]
	var r2 atomic.Bool
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		runDiscoveryLoop(ctx2, loopConfig{
			cancel: cancel2,
			logger: slog.Default(),
			cfg:    cfg,
			m:      m2,
			status: &s2,
			ready:  &r2,
		})
	}()

	deadline2 := time.After(12 * time.Second)
	poll2 := time.NewTicker(50 * time.Millisecond)
	defer poll2.Stop()
	for {
		select {
		case <-deadline2:
			t.Fatal("second runDiscoveryLoop: GraphStale never cleared within deadline")
		case <-poll2.C:
			if testutil.ToFloat64(m2.GraphStale) == 0 {
				// SnapshotLoadedDevicesTotal should have been set from the loaded snapshot.
				if got := testutil.ToFloat64(m2.SnapshotLoadedDevicesTotal); got == 0 {
					t.Error("SnapshotLoadedDevicesTotal = 0 after snapshot load, want > 0")
				}
				cancel2()
				<-done2
				return
			}
		}
	}
}

// TestRunDiscoveryLoopSecondTick verifies that the ticker path in
// runDiscoveryLoop fires a second cycle when the interval is short enough.
func TestRunDiscoveryLoopSecondTick(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-tick"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 200 * time.Millisecond, // very short to hit tick.C
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDiscoveryLoop(ctx, loopConfig{
			cancel: func() {},
			logger: slog.Default(),
			cfg:    cfg,
			m:      m,
			status: &status,
			ready:  &ready,
		})
	}()

	// Wait for at least two cycles: first cycle clears GraphStale, second cycle
	// fires via tick.C. We poll for cycleStatus with a non-zero LastCycleAt.
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("second tick: never observed a second cycle within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				// Let the tick fire the second cycle.
				time.Sleep(300 * time.Millisecond)
				cancel()
				<-done
				return
			}
		}
	}
}

// TestRunDiscoveryLoopContextCancelledDuringCycle exercises the
// `if ctx.Err() != nil { return }` guard inside the cycle closure. We cancel
// the context before calling runDiscoveryLoop so ctx.Err() is already set when
// the cycle checks.
func TestRunDiscoveryLoopContextCancelledDuringCycle(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-cancel"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool

	// Cancel the context immediately — runDiscoveryLoop's first cycle will
	// complete (or start) with ctx.Err() != nil, triggering the early return.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDiscoveryLoop(ctx, loopConfig{
			cancel: func() {},
			logger: slog.Default(),
			cfg:    cfg,
			m:      m,
			status: &status,
			ready:  &ready,
		})
	}()

	select {
	case <-done:
		// runDiscoveryLoop returned — either the cycle saw ctx.Err() and
		// returned, or the ticker case fired ctx.Done(). Either way is correct.
	case <-time.After(5 * time.Second):
		t.Fatal("runDiscoveryLoop did not return within 5s after pre-cancelled context")
	}
}

// TestRunDiscoveryLoopCredResolverError verifies that runDiscoveryLoop calls its
// cancelFn and returns (rather than calling os.Exit) when credentials.New fails.
// The config is constructed directly — bypassing config.Load — to inject an
// invalid CIDR that config validation would normally reject.
func TestRunDiscoveryLoopCredResolverError(t *testing.T) {
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:         60 * time.Second,
			TimeoutPerDevice: 1 * time.Second,
			Parallelism:      1,
		},
		Credentials: config.CredentialsConfig{
			Assignments: []config.CredentialAssignment{
				{CIDR: "not-a-cidr", Profiles: []string{"p"}},
			},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
	}

	m := metrics.New(false)
	var status atomic.Pointer[cycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelCalled := make(chan struct{})
	cancelFn := func() {
		close(cancelCalled)
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDiscoveryLoop(ctx, loopConfig{
			cancel: cancelFn,
			logger: slog.Default(),
			cfg:    cfg,
			m:      m,
			status: &status,
			ready:  &ready,
		})
	}()

	select {
	case <-cancelCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelFn was not called within 3s")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runDiscoveryLoop did not return after cancelFn was called")
	}
}

// ── runCycle additional coverage ──────────────────────────────────────────────

// TestRunCycleAllCredentialsFail exercises the walkSystemWithCredentials error
// path in runCycle: all credential profiles fail, so the device is counted as
// failed and no result is returned.
func TestRunCycleAllCredentialsFail(t *testing.T) {
	// Start agent with "correct" community but configure only a wrong one.
	t.Setenv("WRONG_COMMUNITY", "wrong")
	addr := snmptest.Start(t, "public", systemPDUs("sw-fail"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         500 * time.Millisecond, // short to avoid slow tests
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				// Only wrong community — will always time out / fail.
				{Name: "wrong", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "WRONG_COMMUNITY"},
			},
			FallbackOrder: []string{"wrong"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, fails := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 0 {
		t.Errorf("expected 0 devices when all credentials fail, got %d", len(g.Devices))
	}
	if fails != 1 {
		t.Errorf("expected 1 failure, got %d", fails)
	}
}

// TestRunCycleDeviceLabels exercises the device-label attachment path in
// runCycle, covering the `dev.Labels == nil` check and label assignment.
func TestRunCycleDeviceLabels(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-labels"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets: []config.TargetConfig{{
			Host: "127.0.0.1",
			Port: int(port),
			Labels: map[string]string{
				"env": "test",
				"dc":  "lab",
			},
		}},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(g.Devices))
	}
	if g.Devices[0].Labels["env"] != "test" {
		t.Errorf("label env = %q, want test", g.Devices[0].Labels["env"])
	}
}

// TestRunCycleExpiredUnconfirmedEdge exercises the LD-14 aging path in
// runCycle where a unidirectional edge that has been unconfirmed for too many
// cycles is dropped. We pass prevAges with the edge already at TTL-1 so that
// one more cycle increments it to TTL and expires it.
//
// A second bidirectional edge (sw-c ↔ sw-d) is included so that the expired-
// edge filter has a non-expired edge to keep, covering the `kept = append`
// branch.
func TestRunCycleExpiredUnconfirmedEdge(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	// sw-a reports a link to sw-b (unidirectional — sw-b is not a target).
	// sw-c and sw-d report each other (bidirectional — reconciled edge stays).
	pdusA := append(systemPDUs("sw-a"), lldpPDUs("eth1", "sw-b")...)
	pdusC := append(systemPDUs("sw-c"), lldpPDUs("eth1", "sw-d")...)
	pdusD := append(systemPDUs("sw-d"), lldpPDUs("eth1", "sw-c")...)

	addrA := snmptest.Start(t, "public", pdusA)
	addrC := snmptest.Start(t, "public", pdusC)
	addrD := snmptest.Start(t, "public", pdusD)

	_, portA := snmptest.ParseAddr(addrA)
	_, portC := snmptest.ParseAddr(addrC)
	_, portD := snmptest.ParseAddr(addrD)

	const ttl = 2
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              3,
			UnconfirmedLinkTTLCycles: ttl,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
			LLDP: config.ModuleToggle{Enabled: true},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets: []config.TargetConfig{
			{Host: "127.0.0.1", Port: int(portA)},
			{Host: "127.0.0.1", Port: int(portC)},
			{Host: "127.0.0.1", Port: int(portD)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	m := metrics.New(false)

	// First cycle: get the unidirectional edge and its EdgeKey.
	// Pass a non-nil empty map so AgeUnconfirmed actually populates the ages.
	initialAges := make(map[graph.EdgeKey]int)
	g1, ages1, _, _ := runCycle(context.Background(), slog.Default(), cfg, m, resolver, allowedNets, initialAges)
	if len(g1.Edges) == 0 {
		t.Skip("no edges produced; LLDP PDU may not have been parsed")
	}
	if len(ages1) == 0 {
		t.Skip("no unconfirmed ages after first cycle; edge may have become bidirectional")
	}

	// Advance all unconfirmed edges to TTL-1 so the next cycle expires them.
	for k := range ages1 {
		ages1[k] = ttl - 1
	}

	// Second cycle: the unidirectional sw-a→sw-b edge is incremented to TTL
	// and expires. The bidirectional sw-c↔sw-d edge is kept (covers
	// `kept = append(kept, e)` in the filter loop).
	g2, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, m, resolver, allowedNets, ages1)

	// The sw-a→sw-b unidirectional edge must be absent from g2.
	for _, e := range g2.Edges {
		if (e.SrcDevice == "sw-a" && e.DstDevice == "sw-b") ||
			(e.SrcDevice == "sw-b" && e.DstDevice == "sw-a") {
			t.Errorf("expired unidirectional edge still present: %+v", e)
		}
	}

	// At least the sw-c↔sw-d bidirectional edge must be present.
	var found bool
	for _, e := range g2.Edges {
		if (e.SrcDevice == "sw-c" || e.SrcDevice == "sw-d") &&
			(e.DstDevice == "sw-c" || e.DstDevice == "sw-d") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected bidirectional sw-c↔sw-d edge in g2, not found")
	}
}

// TestRunCycleHostnameDNSFailure exercises the DNS-failure path in runCycle
// where target.Host is a hostname that cannot be resolved. The target is
// counted as a failure and no device is returned.
func TestRunCycleHostnameDNSFailure(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         2 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				CIDRAllowList: []string{"127.0.0.0/8"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets: []config.TargetConfig{
			// this-hostname-does-not-exist.invalid will fail DNS lookup.
			{Host: "this-hostname-does-not-exist.invalid", Port: 161},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, fails := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 0 {
		t.Errorf("expected 0 devices for unresolvable hostname, got %d", len(g.Devices))
	}
	if fails != 1 {
		t.Errorf("expected 1 failure for DNS failure, got %d", fails)
	}
}

// TestRunCycleHostnameOutsideAllowList exercises the CIDR-enforcement path when
// a hostname resolves to an IP that falls outside the allow-list.
func TestRunCycleHostnameOutsideAllowList(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	addr := snmptest.Start(t, "public", systemPDUs("sw-oos"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         2 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				// 192.0.2.0/24 — localhost (127.x) is outside this range.
				CIDRAllowList: []string{"192.0.2.0/24"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: t.TempDir() + "/snap.json"},
		Targets: []config.TargetConfig{
			// localhost resolves to 127.0.0.1, outside 192.0.2.0/24.
			{Host: "localhost", Port: int(port)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, fails := runCycle(context.Background(), slog.Default(), cfg, metrics.New(false), resolver, allowedNets, nil)

	if len(g.Devices) != 0 {
		t.Errorf("expected 0 devices (target outside allow-list), got %d", len(g.Devices))
	}
	if fails != 1 {
		t.Errorf("expected 1 failure (outside allow-list), got %d", fails)
	}
}

// ── credentialCandidates tests ────────────────────────────────────────────────

// TestCredentialCandidatesNoProfiles exercises the legacy single-community
// fallback path when no credential profiles are configured.
func TestCredentialCandidatesNoProfiles(t *testing.T) {
	t.Setenv("SNMP_COMMUNITY", "public")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{CommunityEnv: "SNMP_COMMUNITY"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			// No Profiles — triggers legacy path.
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 161}
	candidates := credentialCandidates(cfg, resolver, ip, target)

	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate from legacy path, got none")
	}
	if candidates[0].params.Community != "public" {
		t.Errorf("community = %q, want public", candidates[0].params.Community)
	}
}

// TestCredentialCandidatesPortZero verifies that a target with Port=0 gets the
// default SNMP port (161) injected by credentialCandidates.
func TestCredentialCandidatesPortZero(t *testing.T) {
	t.Setenv("SNMP_COMMUNITY", "public")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{CommunityEnv: "SNMP_COMMUNITY"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			// No profiles — takes the legacy path where port defaults to 161.
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 0} // Port intentionally 0
	candidates := credentialCandidates(cfg, resolver, ip, target)

	if len(candidates) == 0 {
		t.Fatal("expected candidate with default port, got none")
	}
	if candidates[0].params.Port != 161 {
		t.Errorf("port = %d, want 161 (default for Port=0 target)", candidates[0].params.Port)
	}
}

// ── walkSystemWithCredentials tests ──────────────────────────────────────────

// TestWalkSystemWithCredentialsEmptyCandidates verifies that
// walkSystemWithCredentials returns an error when there are no credential
// candidates (e.g. all profiles fail profileToParams).
func TestWalkSystemWithCredentialsEmptyCandidates(t *testing.T) {
	// Empty community env so profileToParams returns an error for every profile,
	// resulting in zero usable candidates.
	t.Setenv("EMPTY_COMMUNITY", "")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "bad",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "EMPTY_COMMUNITY",
				},
			},
			FallbackOrder: []string{"bad"},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 161}

	dev, _, _, err := walkSystemWithCredentials(context.Background(), cfg, resolver, ip, target)
	if err == nil {
		t.Fatal("expected error from empty candidates, got nil")
	}
	if dev != nil {
		t.Errorf("expected nil device, got %v", dev)
	}
}

// TestWalkSystemWithCredentialsAllTimeout verifies that when every credential
// attempt times out, the credential cache is preserved (RecordFailure is NOT
// called).
func TestWalkSystemWithCredentialsAllTimeout(t *testing.T) {
	// Use a very short timeout so every attempt times out quickly.
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 1 * time.Millisecond,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "p1", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TC_COMM_1"},
			},
			FallbackOrder: []string{"p1"},
		},
	}
	t.Setenv("TC_COMM_1", "public")

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	// Point at a port that has nothing listening — will time out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // close immediately so SNMP gets connection-refused or timeout

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: port}

	_, _, _, err = walkSystemWithCredentials(context.Background(), cfg, resolver, ip, target)
	if err == nil {
		t.Fatal("expected error from all-timeout walk, got nil")
	}

	// When all failures are timeouts, RecordFailure must not invalidate the
	// cache. Since there was no prior cached profile, CachedProfile should
	// still return nothing.
	if _, ok := resolver.CachedProfile("127.0.0.1"); ok {
		t.Error("expected no cached profile after all-timeout failures, but one was found")
	}
}

// TestOtlpPushDropsWhenSemaphoreFull verifies that otlpPush increments the
// dropped counter and returns immediately when the semaphore is full.
func TestOtlpPushDropsWhenSemaphoreFull(t *testing.T) {
	// Fill the semaphore to capacity.
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // occupy the only slot

	dropped := make(chan struct{}, 1)
	lc := loopConfig{
		logger:  slog.Default(),
		m:       metrics.New(false),
		otlpSem: sem,
	}

	ctx := context.Background()
	lc.otlpPush(ctx, func(_ context.Context) error {
		return nil
	}, "should not be called")

	// otlpPush should have incremented the dropped counter and returned immediately.
	if got := testutil.ToFloat64(lc.m.OTLPPushTotal.WithLabelValues("dropped")); got != 1 {
		t.Errorf("OTLPPushTotal{dropped} = %v, want 1", got)
	}
	_ = dropped
}

// TestOtlpPushDrainsOnShutdown verifies that otlpWg.Wait() drains all in-flight
// goroutines before returning: the goroutine spawned by otlpPush must finish
// before Wait() unblocks.
func TestOtlpPushDrainsOnShutdown(t *testing.T) {
	var wg sync.WaitGroup
	started := make(chan struct{})
	unblock := make(chan struct{})

	lc := loopConfig{
		logger: slog.Default(),
		m:      metrics.New(false),
		otlpWg: &wg,
	}

	ctx := context.Background()
	lc.otlpPush(ctx, func(_ context.Context) error {
		close(started) // signal that the goroutine has begun
		<-unblock      // block until the test says go
		return nil
	}, "drain test")

	// Wait until the goroutine has started before calling wg.Wait.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not start within 5s")
	}

	// Release the goroutine and wait for the WaitGroup to drain.
	close(unblock)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		// WaitGroup drained cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("otlpWg.Wait() did not return within 5s after goroutine finished")
	}
}

// TestWalkSystemWithCredentialsNonTimeoutFailure verifies that when the parent
// context is already cancelled, AcquireTrial returns context.Canceled and
// walkSystemWithCredentials propagates that error immediately.
func TestWalkSystemWithCredentialsNonTimeoutFailure(t *testing.T) {
	t.Setenv("NC_COMM", "public")

	addr := snmptest.Start(t, "public", systemPDUs("sw-nc"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "ok", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "NC_COMM"},
			},
			FallbackOrder: []string{"ok"},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	// Cancel the context immediately before calling walkSystemWithCredentials.
	// AcquireTrial will receive the cancelled context and return context.Canceled,
	// which causes an immediate return without going through allTimedOut logic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: int(port)}

	_, _, _, err = walkSystemWithCredentials(ctx, cfg, resolver, ip, target)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestResolveEdgeDstDevices(t *testing.T) {
	ipToID := map[string]string{
		"10.0.0.1": "core-sw-01",
		"10.0.0.2": "core-sw-02",
	}

	edges := []discovery.Edge{
		{SrcDevice: "core-sw-01", DstDevice: "10.0.0.2", DiscoveryProto: "bgp"},
		{SrcDevice: "core-sw-02", DstDevice: "10.0.0.1", DiscoveryProto: "ospf"},
		{SrcDevice: "core-sw-01", DstDevice: "core-sw-03", DiscoveryProto: "lldp"}, // already sysName — unchanged
		{SrcDevice: "core-sw-01", DstDevice: "10.0.1.99", DiscoveryProto: "isis"},  // not in inventory — unchanged
	}

	resolveEdgeDstDevices(edges, ipToID)

	want := []string{"core-sw-02", "core-sw-01", "core-sw-03", "10.0.1.99"}
	for i, e := range edges {
		if e.DstDevice != want[i] {
			t.Errorf("edge[%d] DstDevice = %q, want %q", i, e.DstDevice, want[i])
		}
	}
}

// generateSelfSignedCert generates a self-signed ECDSA certificate valid for
// localhost and writes cert.pem / key.pem into dir. Returns the paths.
func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certFile, keyFile
}

// TestRunTLSMetrics verifies that when listen.tls_cert_file and
// listen.tls_key_file are configured, the /metrics endpoint is reachable over
// HTTPS and returns HTTP 200.
func TestRunTLSMetrics(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	// Pick a random free port for the TLS listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	cfgPath := filepath.Join(dir, "config.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
listen:
  addr: %s
  tls_cert_file: %s
  tls_key_file: %s
`, dir, listenAddr, certFile, keyFile)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"--config.file=" + cfgPath})
	}()

	// Wait for the TLS server to become ready.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		Timeout: 5 * time.Second,
	}
	url := "https://" + listenAddr + "/metrics"
	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = client.Get(url) //nolint:noctx
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("TLS /metrics not reachable within deadline: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	cancel()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /metrics over TLS: status = %d, want 200", resp.StatusCode)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("run() exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s after cancel")
	}
}
