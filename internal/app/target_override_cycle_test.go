package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// TestCycleHonoursTargetOverride proves the issue #74 per-target protocol
// scope: with both lldp and cdp enabled globally but an override scoping the
// target to lldp only, the cycle dispatches lldp.walk and skips cdp.walk. The
// per-module spans are the observable proof a walker fired (or did not).
func TestCycleHonoursTargetOverride(t *testing.T) {
	t.Setenv("TEST_COMM", "public")
	sr := installRecorder(t)

	cfg := testConfig(t, "TEST_COMM")
	cfg.Discovery.Interval = 30 * time.Second
	cfg.Discovery.CycleBudgetFraction = 1
	cfg.Discovery.UnconfirmedLinkTTLCycles = 3
	// Both modules enabled globally; the override must narrow to lldp only.
	cfg.Modules.LLDP.Enabled = true
	cfg.Modules.CDP.Enabled = true

	m := metrics.New(false)

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)
	cfg.Targets = []config.TargetConfig{{Host: ip.String(), Port: int(port)}}

	// Scope this target (matched by /32) to lldp only.
	cfg.Discovery.Scope.CIDRAllowList = []string{"127.0.0.0/8"}
	cfg.Discovery.TargetOverrides = []config.TargetOverride{
		{CIDR: ip.String() + "/32", Modules: []string{"lldp"}},
	}
	if err := cfg.BuildTargetOverrideResolver(); err != nil {
		t.Fatalf("BuildTargetOverrideResolver: %v", err)
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	allow := snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ctx, root := tracing.Tracer().Start(ctx, "discovery.cycle")
	g, _, _, _ := RunCycle(ctx, slogDiscard(), cfg, m, nil, nil, resolver, allow, map[graph.EdgeKey]int{}, nil)
	root.End()

	if len(g.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(g.Devices))
	}

	names := map[string]bool{}
	for _, s := range sr.Ended() {
		names[s.Name()] = true
	}
	if !names["lldp.walk"] {
		t.Errorf("expected lldp.walk span (in-override module must run); got %v", spanNames(sr.Ended()))
	}
	if names["cdp.walk"] {
		t.Errorf("cdp.walk span present but cdp is not in the override; out-of-override module must be skipped")
	}
}

// TestCycleNoOverrideRunsAllEnabled is the control: with no target_overrides,
// every enabled module dispatches — confirming the default path is unchanged.
func TestCycleNoOverrideRunsAllEnabled(t *testing.T) {
	t.Setenv("TEST_COMM", "public")
	sr := installRecorder(t)

	cfg := testConfig(t, "TEST_COMM")
	cfg.Discovery.Interval = 30 * time.Second
	cfg.Discovery.CycleBudgetFraction = 1
	cfg.Discovery.UnconfirmedLinkTTLCycles = 3
	cfg.Modules.LLDP.Enabled = true
	cfg.Modules.CDP.Enabled = true

	m := metrics.New(false)

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)
	cfg.Targets = []config.TargetConfig{{Host: ip.String(), Port: int(port)}}
	cfg.Discovery.Scope.CIDRAllowList = []string{"127.0.0.0/8"}
	if err := cfg.BuildTargetOverrideResolver(); err != nil {
		t.Fatalf("BuildTargetOverrideResolver: %v", err)
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	allow := snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ctx, root := tracing.Tracer().Start(ctx, "discovery.cycle")
	RunCycle(ctx, slogDiscard(), cfg, m, nil, nil, resolver, allow, map[graph.EdgeKey]int{}, nil)
	root.End()

	names := map[string]bool{}
	for _, s := range sr.Ended() {
		names[s.Name()] = true
	}
	if !names["lldp.walk"] || !names["cdp.walk"] {
		t.Errorf("expected both lldp.walk and cdp.walk with no override; got %v", spanNames(sr.Ended()))
	}
}
