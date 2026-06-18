package app_test

// Tests relocated verbatim from cmd/topology-exporter/main_test.go (#171):
// they exercise exported internal/app behaviour, not main-level wiring,
// and belong with the package they test. External test package (app_test)
// so the `app.` call sites move unchanged.

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/grafana/network-topology-exporter/internal/app"
	"github.com/grafana/network-topology-exporter/internal/app/httpx"
	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/credentials"
	"github.com/grafana/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/graph"
	"github.com/grafana/network-topology-exporter/internal/metrics"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
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
			Path: filepath.Join(t.TempDir(), "snapshot.json"),
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

	g, _, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

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
	g, _, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

	if len(g.Devices) != 1 {
		t.Fatalf("expected fallback credential to discover 1 device, got %d", len(g.Devices))
	}
	if profile, ok := resolver.CachedProfile("127.0.0.1"); !ok || profile != "good" {
		t.Fatalf("cached profile = (%q, %v), want (good, true)", profile, ok)
	}
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

		// chassisSubtype=5 (network-address), chassisID=IPv4 127.0.0.1 (IANA family 1 + 4 octets).
		// Using a network-address chassis ID so the 127.0.0.0/8 scope filter passes.
		{Name: remBase + "4." + remSuffix, Type: gsnmp.Integer, Value: int(5)},
		{Name: remBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: []byte{1, 127, 0, 0, 1}},
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
			Path: filepath.Join(t.TempDir(), "snapshot.json"),
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

	g, _, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

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
		return // unreachable, but staticcheck SA5011 needs the explicit terminator
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

// TestMaybeWarnLargeTopologyEmitsOnlyOnUpwardCrossing drives the
// threshold-crossing helper across a sequence of edge counts and asserts
// that the warning fires only on a transition from at-or-below the
// threshold to strictly above (issue #9). Flat-above and downward
// transitions must not re-emit.
func TestMaybeWarnLargeTopologyEmitsOnlyOnUpwardCrossing(t *testing.T) {
	const above = app.LargeTopologyEdgeThreshold + 100
	const below = app.LargeTopologyEdgeThreshold - 100

	// Cycle apart from each other by more than the cooldown so the
	// cooldown does not suppress legitimate re-crossings.
	step := app.LargeTopologyWarnCooldownCycles + 1

	type tc struct {
		name      string
		edges     int
		wantWarn  bool
		prevAbove bool // expected prevAbove going INTO this step (sanity)
	}
	steps := []tc{
		{name: "first cycle below threshold", edges: below, wantWarn: false, prevAbove: false},
		{name: "stays below", edges: below, wantWarn: false, prevAbove: false},
		{name: "upward crossing fires", edges: above, wantWarn: true, prevAbove: false},
		{name: "flat above does not refire", edges: above, wantWarn: false, prevAbove: true},
		{name: "flat above still does not refire", edges: above, wantWarn: false, prevAbove: true},
		{name: "downward crossing does not warn", edges: below, wantWarn: false, prevAbove: true},
		{name: "second upward crossing fires again", edges: above, wantWarn: true, prevAbove: false},
	}

	var buf bytesBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	prevAbove := false
	lastWarnCycle := -app.LargeTopologyWarnCooldownCycles
	cycleNum := 0
	for _, s := range steps {
		cycleNum += step
		if prevAbove != s.prevAbove {
			t.Fatalf("%s: prevAbove mismatch — test set up incorrectly: got %v, want %v", s.name, prevAbove, s.prevAbove)
		}
		before := buf.Len()
		var newLastWarn int
		prevAbove, newLastWarn = app.MaybeWarnLargeTopology(logger, s.edges, s.edges/10, prevAbove, cycleNum, lastWarnCycle)
		emitted := buf.Len() > before
		if emitted != s.wantWarn {
			t.Errorf("%s: warn emitted = %v, want %v (cycle %d, edges %d)", s.name, emitted, s.wantWarn, cycleNum, s.edges)
		}
		if s.wantWarn && newLastWarn != cycleNum {
			t.Errorf("%s: lastWarnCycle = %d, want %d", s.name, newLastWarn, cycleNum)
		}
		if !s.wantWarn && newLastWarn != lastWarnCycle {
			t.Errorf("%s: lastWarnCycle changed to %d without emitting (was %d)", s.name, newLastWarn, lastWarnCycle)
		}
		lastWarnCycle = newLastWarn
	}
}

// TestMaybeWarnLargeTopologyCooldownSuppressesOscillation verifies the
// 60-cycle cooldown: an upward crossing followed by a downward then
// another upward crossing within the cooldown window does NOT re-emit
// the warning (issue #9 rate-limit clause).
func TestMaybeWarnLargeTopologyCooldownSuppressesOscillation(t *testing.T) {
	const above = app.LargeTopologyEdgeThreshold + 100
	const below = app.LargeTopologyEdgeThreshold - 100

	var buf bytesBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	prevAbove := false
	lastWarnCycle := -app.LargeTopologyWarnCooldownCycles

	// Cycle 1: cross upward — should emit.
	prevAbove, lastWarnCycle = app.MaybeWarnLargeTopology(logger, above, 10, prevAbove, 1, lastWarnCycle)
	if buf.Len() == 0 {
		t.Fatal("first upward crossing did not emit")
	}
	if lastWarnCycle != 1 {
		t.Fatalf("lastWarnCycle = %d, want 1", lastWarnCycle)
	}
	first := buf.Len()

	// Cycle 2: drop below.
	prevAbove, lastWarnCycle = app.MaybeWarnLargeTopology(logger, below, 10, prevAbove, 2, lastWarnCycle)
	if buf.Len() != first {
		t.Fatal("downward transition emitted unexpectedly")
	}

	// Cycle 3 (well within cooldown): cross upward again — must be
	// suppressed by cooldown.
	prevAbove, lastWarnCycle = app.MaybeWarnLargeTopology(logger, above, 10, prevAbove, 3, lastWarnCycle)
	if buf.Len() != first {
		t.Errorf("upward crossing inside cooldown re-emitted; expected suppression")
	}
	if lastWarnCycle != 1 {
		t.Errorf("lastWarnCycle = %d, want 1 (cooldown suppressed re-emit)", lastWarnCycle)
	}

	// Cycle 1 + cooldown: drop below first, then cross upward again past the
	// cooldown — must emit.
	prevAbove, lastWarnCycle = app.MaybeWarnLargeTopology(logger, below, 10, prevAbove, 1+app.LargeTopologyWarnCooldownCycles, lastWarnCycle)
	_, lastWarnCycle = app.MaybeWarnLargeTopology(logger, above, 10, prevAbove, 2+app.LargeTopologyWarnCooldownCycles, lastWarnCycle)
	if buf.Len() == first {
		t.Errorf("upward crossing after cooldown did not emit")
	}
	if lastWarnCycle != 2+app.LargeTopologyWarnCooldownCycles {
		t.Errorf("lastWarnCycle = %d, want %d", lastWarnCycle, 2+app.LargeTopologyWarnCooldownCycles)
	}
}

// bytesBuffer is a minimal io.Writer wrapper used by the threshold tests
// to capture slog output without pulling bytes.Buffer (and bytes) into
// scope just for the log capture.
type bytesBuffer struct {
	b []byte
}

func (bb *bytesBuffer) Write(p []byte) (int, error) {
	bb.b = append(bb.b, p...)
	return len(p), nil
}

func (bb *bytesBuffer) Len() int { return len(bb.b) }

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
	if counts["network_topology_device_info"] != 1 {
		t.Errorf("network_topology_device_info series = %d, want 1", counts["network_topology_device_info"])
	}
	if counts["network_topology_edge_info"] != 1 {
		t.Errorf("network_topology_edge_info series = %d, want 1", counts["network_topology_edge_info"])
	}

	// After updating to an empty graph: device and edge series must disappear.
	m.Topology.Update(discovery.Graph{})
	mfs, _ = m.Registry().Gather()
	for _, mf := range mfs {
		switch mf.GetName() {
		case "network_topology_device_info", "network_topology_edge_info":
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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)
	g, _, _, fails := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
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
	g, _, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
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
	g1, ages1, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, m, nil, nil, resolver, allowedNets, initialAges, nil)
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
	g2, _, _, _ := app.RunCycle(context.Background(), slog.Default(), cfg, m, nil, nil, resolver, allowedNets, ages1, nil)

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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
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
	g, _, _, fails := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
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
	g, _, _, fails := app.RunCycle(context.Background(), slog.Default(), cfg, metrics.New(false), nil, nil, resolver, allowedNets, nil, nil)

	if len(g.Devices) != 0 {
		t.Errorf("expected 0 devices (target outside allow-list), got %d", len(g.Devices))
	}
	if fails != 1 {
		t.Errorf("expected 1 failure (outside allow-list), got %d", fails)
	}
}

// ── credentialCandidates tests ────────────────────────────────────────────────

func TestResolveEdgeDstDevices(t *testing.T) {
	ipToID := map[string]string{
		"10.0.0.1": "core-sw-01",
		"10.0.0.2": "core-sw-02",
	}
	macToID := map[string]string{
		"00:1a:2b:3c:4d:5e": "spine-01",
	}

	edges := []discovery.Edge{
		{SrcDevice: "core-sw-01", DstDevice: "10.0.0.2", DiscoveryProto: "bgp"},
		{SrcDevice: "core-sw-02", DstDevice: "10.0.0.1", DiscoveryProto: "ospf"},
		{SrcDevice: "core-sw-01", DstDevice: "core-sw-03", DiscoveryProto: "lldp"},       // already sysName — unchanged
		{SrcDevice: "core-sw-01", DstDevice: "10.0.1.99", DiscoveryProto: "isis"},        // not in inventory — unchanged (routing proto)
		{SrcDevice: "core-sw-01", DstDevice: "00:1a:2b:3c:4d:5e", DiscoveryProto: "fdb"}, // MAC in index → resolved
		{SrcDevice: "core-sw-01", DstDevice: "00:ff:ee:dd:cc:bb", DiscoveryProto: "fdb"}, // MAC not in index → suppressed
	}

	got := app.ResolveEdgeDstDevices(slog.Default(), edges, ipToID, macToID, nil)

	// Unresolved MAC edge is suppressed; expect 5 edges back (not 6).
	want := []string{"core-sw-02", "core-sw-01", "core-sw-03", "10.0.1.99", "spine-01"}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d", len(got), len(want))
	}
	for i, e := range got {
		if e.DstDevice != want[i] {
			t.Errorf("edge[%d] DstDevice = %q, want %q", i, e.DstDevice, want[i])
		}
	}
}

// TestCollectDegradedReasonsTable (renamed on move: package app already has a TestCollectDegradedReasons) covers all branches of collectDegradedReasons:
// empty input, nil Metadata, non-degraded edges, comma-separated reasons,
// deduplication across edges, and empty reason → "unknown" substitution.
func TestCollectDegradedReasonsTable(t *testing.T) {
	cases := []struct {
		name  string
		edges []discovery.Edge
		// want is a set of expected reasons (order is not guaranteed).
		want map[string]bool
	}{
		{
			name:  "empty_edge_slice",
			edges: []discovery.Edge{},
			want:  map[string]bool{},
		},
		{
			name: "nil_metadata_skipped",
			edges: []discovery.Edge{
				{Metadata: nil},
			},
			want: map[string]bool{},
		},
		{
			name: "non_degraded_skipped",
			edges: []discovery.Edge{
				{Metadata: map[string]string{
					discovery.MetadataKeyDegraded: "false",
				}},
			},
			want: map[string]bool{},
		},
		{
			name: "comma_separated_reasons",
			edges: []discovery.Edge{
				{Metadata: map[string]string{
					discovery.MetadataKeyDegraded:       "true",
					discovery.MetadataKeyDegradedReason: "reason_a,reason_b",
				}},
			},
			want: map[string]bool{"reason_a": true, "reason_b": true},
		},
		{
			name: "overlapping_reasons_deduplicated",
			edges: []discovery.Edge{
				{Metadata: map[string]string{
					discovery.MetadataKeyDegraded:       "true",
					discovery.MetadataKeyDegradedReason: "reason_a",
				}},
				{Metadata: map[string]string{
					discovery.MetadataKeyDegraded:       "true",
					discovery.MetadataKeyDegradedReason: "reason_a,reason_b",
				}},
			},
			want: map[string]bool{"reason_a": true, "reason_b": true},
		},
		{
			name: "empty_reason_string_becomes_unknown",
			edges: []discovery.Edge{
				{Metadata: map[string]string{
					discovery.MetadataKeyDegraded:       "true",
					discovery.MetadataKeyDegradedReason: "",
				}},
			},
			want: map[string]bool{"unknown": true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := app.CollectDegradedReasons(tc.edges)
			gotSet := make(map[string]bool, len(got))
			for _, r := range got {
				gotSet[r] = true
			}
			if len(gotSet) != len(tc.want) {
				t.Fatalf("len(reasons) = %d, want %d; got %v", len(gotSet), len(tc.want), gotSet)
			}
			for r := range tc.want {
				if !gotSet[r] {
					t.Errorf("expected reason %q in result, not found; got %v", r, gotSet)
				}
			}
		})
	}
}

// TestSynthesizeEdgesARPResolution verifies that resolveEdgeDstDevices resolves
// a MAC-addressed FDB edge to a sysName when the MAC appears in the ARP table
// and the ARP-mapped IP belongs to a known device. This exercises the
// ARP-based resolution path that runs after LLDP correlation in runCycle.
func TestSynthesizeEdgesARPResolution(t *testing.T) {
	// Arrange: one FDB edge whose DstDevice is a raw MAC address.
	rawEdges := []discovery.Edge{
		{
			SrcDevice:      "router-a",
			SrcPort:        "eth0",
			DstDevice:      "aa:bb:cc:dd:ee:ff",
			DiscoveryProto: "fdb",
			Direction:      discovery.DirectionUnidirectional,
		},
	}
	// ipToID maps the management IP of router-b to its sysName.
	ipToID := map[string]string{
		"10.0.0.2": "router-b",
	}
	// arpMACToIP maps router-b's MAC to its IP.
	arpMACToIP := map[string]string{
		"aa:bb:cc:dd:ee:ff": "10.0.0.2",
	}
	// Build macToID the same way runCycle does: first LLDP (none here), then ARP.
	macToID := make(map[string]string)
	for mac, ip := range arpMACToIP {
		if _, resolved := macToID[mac]; resolved {
			continue
		}
		if id, ok := ipToID[ip]; ok {
			macToID[mac] = id
		}
	}

	got := app.ResolveEdgeDstDevices(slog.Default(), rawEdges, ipToID, macToID, nil)

	if len(got) != 1 {
		t.Fatalf("expected 1 resolved edge, got %d", len(got))
	}
	if got[0].DstDevice != "router-b" {
		t.Errorf("DstDevice = %q, want router-b", got[0].DstDevice)
	}
}

// TestSynthesizeEdgesIdempotent verifies that calling resolveEdgeDstDevices
// twice on already-resolved edges produces identical results — no spurious
// modifications occur on edges whose DstDevice is already a sysName.
func TestSynthesizeEdgesIdempotent(t *testing.T) {
	// Edges where DstDevice is already a fully-resolved sysName (not MAC or IP).
	resolved := []discovery.Edge{
		{
			SrcDevice:      "sw-a",
			SrcPort:        "Gi0/1",
			DstDevice:      "sw-b",
			DstPort:        "Gi0/2",
			DiscoveryProto: "lldp",
			Direction:      discovery.DirectionBidirectional,
		},
		{
			SrcDevice:      "sw-b",
			SrcPort:        "Gi0/3",
			DstDevice:      "sw-c",
			DstPort:        "Gi0/4",
			DiscoveryProto: "lldp",
			Direction:      discovery.DirectionUnidirectional,
		},
	}

	ipToID := map[string]string{"10.0.0.1": "sw-a"}
	macToID := map[string]string{"00:11:22:33:44:55": "sw-a"}

	first := app.ResolveEdgeDstDevices(slog.Default(), resolved, ipToID, macToID, nil)
	second := app.ResolveEdgeDstDevices(slog.Default(), first, ipToID, macToID, nil)

	if len(first) != len(second) {
		t.Fatalf("first call returned %d edges, second returned %d", len(first), len(second))
	}
	for i := range first {
		a, b := first[i], second[i]
		if a.SrcDevice != b.SrcDevice || a.SrcPort != b.SrcPort ||
			a.DstDevice != b.DstDevice || a.DstPort != b.DstPort ||
			a.DiscoveryProto != b.DiscoveryProto || a.Direction != b.Direction {
			t.Errorf("edge[%d] differs between first and second call: %+v vs %+v", i, a, b)
		}
	}
}

// TestGraphSizeAdmissionControl verifies that when MaxGraphDevices is set and
// the discovered graph exceeds it, the cycle increments GraphUpdatesRejectedTotal
// and does not update the published topology.
func TestGraphSizeAdmissionControl(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	// Start 5 SNMP agents, each with a distinct sysName.
	var ports []int
	for i := 0; i < 5; i++ {
		addr := snmptest.Start(t, "public", systemPDUs(fmt.Sprintf("sw-%02d", i+1)))
		_, port := snmptest.ParseAddr(addr)
		ports = append(ports, int(port))
	}

	targets := make([]config.TargetConfig, len(ports))
	for i, p := range ports {
		targets[i] = config.TargetConfig{Host: "127.0.0.1", Port: p}
	}

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              5,
			UnconfirmedLinkTTLCycles: 3,
			MaxGraphDevices:          3, // fewer than the 5 agents above
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
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
		Targets:  targets,
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: func() {},
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	// Wait for the first cycle to complete: status must be set (cycle ran)
	// and the rejection counter must be > 0.
	deadline := time.After(12 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("admission control: cycle did not complete within deadline")
		case <-poll.C:
			if status.Load() == nil {
				continue
			}
			rejected := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(string(metrics.RejectReasonSizeBudgetExceeded)))
			if rejected > 0 {
				cancel()
				<-done
				// GraphStale must still be 1: the update was rejected so topology
				// should never have been published from the live cycle.
				if testutil.ToFloat64(m.GraphStale) != 1 {
					t.Errorf("GraphStale = %v after rejected update, want 1", testutil.ToFloat64(m.GraphStale))
				}
				return
			}
		}
	}
}

func TestDeduplicateOOSTable(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	tests := []struct {
		name string
		in   []discovery.OutOfScopeNeighbour
		want []discovery.OutOfScopeNeighbour
	}{
		{
			name: "empty slice returns empty slice",
			in:   nil,
			want: []discovery.OutOfScopeNeighbour{},
		},
		{
			name: "no duplicates passes through unchanged",
			in: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.2", Proto: "cdp"},
			},
			want: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.2", Proto: "cdp"},
			},
		},
		{
			name: "duplicate from second protocol is dropped, first proto is kept",
			in: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp", FirstSeen: t0, LastSeen: t0},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "cdp", FirstSeen: t1, LastSeen: t1},
			},
			want: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp", FirstSeen: t0, LastSeen: t0},
			},
		},
		{
			name: "same neighbour on different ports are kept separately",
			in: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.99", Proto: "lldp"},
			},
			want: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.99", Proto: "lldp"},
			},
		},
		{
			name: "same neighbour on same port reported by different devices are kept separately",
			in: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
				{ReportingDevice: "sw-02", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
			},
			want: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
				{ReportingDevice: "sw-02", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", Proto: "lldp"},
			},
		},
		{
			name: "insertion order is preserved after dedup",
			in: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.2", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", Proto: "cdp"}, // dup
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/3", NeighbourHint: "10.0.0.3", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.2", Proto: "cdp"}, // dup
			},
			want: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.2", Proto: "lldp"},
				{ReportingDevice: "sw-01", ReportingPort: "Gi0/3", NeighbourHint: "10.0.0.3", Proto: "lldp"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := app.DeduplicateOOS(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("app.DeduplicateOOS() len = %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				g, w := got[i], tc.want[i]
				if g.ReportingDevice != w.ReportingDevice ||
					g.ReportingPort != w.ReportingPort ||
					g.NeighbourHint != w.NeighbourHint ||
					g.Proto != w.Proto ||
					!g.FirstSeen.Equal(w.FirstSeen) ||
					!g.LastSeen.Equal(w.LastSeen) {
					t.Errorf("app.DeduplicateOOS()[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

// deduplicateDevices: two devices with the same ID → only the first (config order) is returned.
// runCycle sorts probe results by targetIdx before calling deduplicateDevices, so
// the device from the earliest config entry always wins over later duplicates.
func TestDeduplicateDevicesDuplicateID(t *testing.T) {
	devices := []discovery.Device{
		{ID: "sw-01", Site: "site-a"},
		{ID: "sw-01", Site: "site-b"},
	}
	got := app.DeduplicateDevices(devices)
	if len(got) != 1 {
		t.Fatalf("expected 1 device after dedup, got %d", len(got))
	}
	if got[0].Site != "site-a" {
		t.Errorf("Site = %q, want site-a (first occurrence kept)", got[0].Site)
	}
}

// deduplicateDevices: two devices with different IDs → both are returned.
func TestDeduplicateDevicesDifferentIDs(t *testing.T) {
	devices := []discovery.Device{
		{ID: "sw-01"},
		{ID: "sw-02"},
	}
	got := app.DeduplicateDevices(devices)
	if len(got) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(got))
	}
}

// mergeOOSFirstSeen: entry present in prevOOS → FirstSeen is restored from prev.
func TestMergeOOSFirstSeenPreservesExisting(t *testing.T) {
	original := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Now()

	prev := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", FirstSeen: original},
	}
	newOOS := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.99", FirstSeen: now},
	}

	got := app.MergeOOSFirstSeen(newOOS, prev)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if !got[0].FirstSeen.Equal(original) {
		t.Errorf("FirstSeen = %v, want %v (original from prevOOS)", got[0].FirstSeen, original)
	}
}

// mergeOOSFirstSeen: entry not in prevOOS → FirstSeen is unchanged (cycle's time).
func TestMergeOOSFirstSeenKeepsNewEntry(t *testing.T) {
	now := time.Now()

	prev := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-01", ReportingPort: "Gi0/1", NeighbourHint: "10.0.0.1", FirstSeen: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	newOOS := []discovery.OutOfScopeNeighbour{
		{ReportingDevice: "sw-01", ReportingPort: "Gi0/2", NeighbourHint: "10.0.0.99", FirstSeen: now},
	}

	got := app.MergeOOSFirstSeen(newOOS, prev)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if !got[0].FirstSeen.Equal(now) {
		t.Errorf("FirstSeen = %v, want %v (cycle time kept for new entry)", got[0].FirstSeen, now)
	}
}
