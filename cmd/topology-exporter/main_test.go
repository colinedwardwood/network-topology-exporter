package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
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

	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(), resolver, allowedNets, nil)

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
	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(), resolver, allowedNets, nil)

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

	g, _, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(), resolver, allowedNets, nil)

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

// LD-15: canonical pair ordering.
func TestCanonicalPair(t *testing.T) {
	tests := []struct {
		a, b       string
		wantA, wantB string
	}{
		{"sw-a", "sw-b", "sw-a", "sw-b"},
		{"sw-b", "sw-a", "sw-a", "sw-b"},
		{"z", "a", "a", "z"},
		{"same", "same", "same", "same"},
	}
	for _, tc := range tests {
		gotA, gotB := canonicalPair(tc.a, tc.b)
		if gotA != tc.wantA || gotB != tc.wantB {
			t.Errorf("canonicalPair(%q, %q) = (%q, %q), want (%q, %q)",
				tc.a, tc.b, gotA, gotB, tc.wantA, tc.wantB)
		}
	}
}

// LD-15: emitBoundaryObservations resets and repopulates on each call.
func TestEmitBoundaryObservations(t *testing.T) {
	m := metrics.New()
	oos := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-b", ReportingPort: "Gi0/1", NeighbourHint: "sw-a", Proto: "lldp"},
		{ReportingDevice: "sw-c", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "cdp"},
	}
	emitBoundaryObservations(oos, m)

	// Gather and count BoundaryObservationInfo series.
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

	// After a second call with one fewer entry, the count should drop.
	emitBoundaryObservations(oos[:1], m)
	mfs, _ = m.Registry().Gather()
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_boundary_observation_info" {
			count = len(mf.GetMetric())
		}
	}
	if count != 1 {
		t.Errorf("after reset, series count = %d, want 1", count)
	}
}

// TestEmitBoundaryObservationsCanonicalOrder verifies peer_a is always
// alphabetically smaller regardless of which device reported first.
func TestEmitBoundaryObservationsCanonicalOrder(t *testing.T) {
	m := metrics.New()
	oos := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-z", ReportingPort: "Gi0/1", NeighbourHint: "sw-a", Proto: "lldp"},
	}
	emitBoundaryObservations(oos, m)

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
