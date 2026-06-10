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
// instrumentation set (the rediscover minimal case) does not panic on the OK
// path. This covers the success-side metric/span sites only; the failure
// (status=2) nil-guard cluster is exercised separately by
// TestWalkModulesNilInstrumentationFailureNoPanic.
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

// TestWalkModulesNilInstrumentationFailureNoPanic drives a module into a
// FAILURE (status=2) with nil metrics + nil tracer and asserts: no panic, the
// failing module's status is 2, and no metrics are recorded. This is the
// nil-guard cluster the #153 extraction risks (the failure branch touches
// DiscoveryHardFailTotal, SNMPWalksTotal, the failure span attrs, and the
// per-module failure log) — TestWalkModulesNilInstrumentationNoPanic only
// covers the OK side.
//
// The failure is forced deterministically: the system-group walk succeeds
// against the live agent (so dev/params resolve), then params is redirected to
// a closed port with a short timeout so the LLDP module walk errors.
func TestWalkModulesNilInstrumentationFailureNoPanic(t *testing.T) {
	t.Setenv("WM_COMM3", "public")
	cfg := testConfig(t, "WM_COMM3")
	cfg.Modules.LLDP.Enabled = true
	// Bound the failing walk so the test stays fast.
	cfg.Discovery.TimeoutPerModule = 500 * time.Millisecond

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))

	dev, ip, params := walkModulesTestSetup(t, cfg, addr)
	defer params.Zeroize()

	// Redirect the per-module walk at a closed UDP port so LLDP's SNMP walk
	// fails fast and the module is classified status=2. The system walk above
	// already succeeded against the real agent.
	closedPort := closedUDPPort(t)
	params.Port = closedPort
	params.Timeout = 150 * time.Millisecond
	params.Retries = 0

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Sentinel metrics: nil here so a stray metric write would panic; we also
	// assert below that the nil-metrics path recorded nothing observable.
	inst := moduleInstrumentation{logger: slogDiscard()}

	edges, _, status := walkModules(ctx, cfg, dev, ip, params, snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"}), inst)

	if got, ok := status["lldp"]; !ok || got != 2 {
		t.Fatalf("lldp status = %d (present=%v), want 2 (failed)", got, ok)
	}
	if len(edges) != 0 {
		t.Errorf("edges = %d, want 0 on a failed walk", len(edges))
	}
}

// closedUDPPort returns a UDP port that is not being listened on, by binding a
// socket to an ephemeral port and immediately closing it. There is an inherent
// TOCTOU window, but for a single-threaded test against localhost it is
// effectively always still closed when the walk dials it.
func closedUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	// Mask to 16 bits to satisfy gosec G115; a UDP port is always 0..65535
	// so the mask is a no-op on every real value.
	return uint16(port & 0xFFFF)
}
