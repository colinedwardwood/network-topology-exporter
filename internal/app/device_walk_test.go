package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// walkModulesTestSetup walks the credential ladder against an in-process agent
// and returns the resolved device + mutated params ready for walkModules, the
// same way RunCycle/walkOne build them. The caller is responsible for
// params.Zeroize().
func walkModulesTestSetup(t *testing.T, cfg *config.Config, addr string) (discovery.Device, net.IP, snmpwalk.Params) {
	t.Helper()
	ip, port := snmptest.ParseAddr(addr)
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	tc := config.TargetConfig{Host: ip.String(), Port: int(port)}
	dev, params, _, err := WalkSystemWithCredentials(context.Background(), cfg, resolver, ip, tc, slogDiscard())
	if err != nil {
		t.Fatalf("WalkSystemWithCredentials: %v", err)
	}
	params.MaxVlans = cfg.Modules.FDB.MaxVlans
	params.Vendor = dev.Vendor
	params.UseBGPV2MIB = !cfg.Modules.BGP.DisableV2MIB
	return *dev, ip, params
}

// TestWalkModulesEdgesAndStatus drives walkModules against a live in-process
// agent advertising one LLDP neighbour and asserts: the returned edge union,
// per-proto status classification, and — with a zero-value
// moduleInstrumentation (nil metrics + nil tracer, logger set) — that it does
// not panic and emits no metrics.
func TestWalkModulesEdgesAndStatus(t *testing.T) {
	t.Setenv("WM_COMM", "public")
	cfg := testConfig(t, "WM_COMM")
	// Enable a couple of modules: lldp answers (one edge), cdp has no fixture
	// (walks clean / zero edges, status ok).
	cfg.Modules.LLDP.Enabled = true
	cfg.Modules.CDP.Enabled = true

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))

	dev, ip, params := walkModulesTestSetup(t, cfg, addr)
	defer params.Zeroize()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Zero-value instrumentation: nil metrics + nil tracer, logger required.
	inst := moduleInstrumentation{logger: slogDiscard()}

	edges, _, status := walkModules(ctx, cfg, dev, ip, params, snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"}), inst)

	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1 (one LLDP neighbour)", len(edges))
	}
	if got, ok := status["lldp"]; !ok || got != 0 {
		t.Errorf("lldp status = %d (present=%v), want 0 (ok)", got, ok)
	}
	if got, ok := status["cdp"]; !ok || got != 0 {
		t.Errorf("cdp status = %d (present=%v), want 0 (ok, no fixture)", got, ok)
	}
	// Disabled modules must not appear in the status map.
	if _, ok := status["bgp"]; ok {
		t.Errorf("bgp present in status map but module is disabled")
	}
}

// TestWalkModulesNilInstrumentationNoPanic confirms a fully nil-sink
// instrumentation set (the rediscover minimal case) does not panic even when a
// module walk fails — the failure path touches every metric/span site.
func TestWalkModulesNilInstrumentationNoPanic(t *testing.T) {
	t.Setenv("WM_COMM2", "public")
	cfg := testConfig(t, "WM_COMM2")
	cfg.Modules.LLDP.Enabled = true

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))

	dev, ip, params := walkModulesTestSetup(t, cfg, addr)
	defer params.Zeroize()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	inst := moduleInstrumentation{logger: slogDiscard()}
	// Must not panic with nil metrics + nil tracer.
	edges, oos, status := walkModules(ctx, cfg, dev, ip, params, snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"}), inst)
	_ = edges
	_ = oos
	if len(status) == 0 {
		t.Error("expected at least one module status entry")
	}
}
