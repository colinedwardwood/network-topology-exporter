package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/credentials"
	snmpwalk "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/metrics"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// OID bases for building a minimal system-group + LLDP fixture.
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysObjectID = "1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = "1.3.6.1.2.1.1.3.0"
	oidSysName     = "1.3.6.1.2.1.1.5.0"

	lldpLocBase = ".1.0.8802.1.1.2.1.3.7.1."
	lldpRemBase = ".1.0.8802.1.1.2.1.4.1.1."
)

// systemAndLLDPPDUs returns a fixture that answers the system-group GET and
// advertises one LLDP neighbour via a network-address (IPv4) chassis ID so the
// edge survives active CIDR scope filtering (a MAC-chassis neighbour is dropped
// at the module layer when scope filtering is on, and only re-resolved later by
// the synthesis layer, which the single-device forced walk does not run).
func systemAndLLDPPDUs(sysName string, remoteIP net.IP) []gsnmp.SnmpPDU {
	const (
		chassisSubtypeNetworkAddress = 5
		portSubtypeIfName            = 5
	)
	remSuffix := "0.1.1" // timeMark=0, portNum=1, remIdx=1
	v4 := remoteIP.To4()
	chassisID := append([]byte{1}, v4...) // IANA family 1 (IPv4) + 4 octets
	return []gsnmp.SnmpPDU{
		{Name: "." + oidSysDescr, Type: gsnmp.OctetString, Value: []byte("Test OS 1.2.3")},
		{Name: "." + oidSysObjectID, Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: "." + oidSysUpTime, Type: gsnmp.TimeTicks, Value: uint32(123456)},
		{Name: "." + oidSysName, Type: gsnmp.OctetString, Value: []byte(sysName)},

		// lldpLocPortTable: port 1
		{Name: lldpLocBase + "2.1", Type: gsnmp.Integer, Value: portSubtypeIfName},
		{Name: lldpLocBase + "3.1", Type: gsnmp.OctetString, Value: []byte("Gi0/1")},
		// lldpRemTable: one remote neighbour
		{Name: lldpRemBase + "4." + remSuffix, Type: gsnmp.Integer, Value: chassisSubtypeNetworkAddress},
		{Name: lldpRemBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: chassisID},
		{Name: lldpRemBase + "6." + remSuffix, Type: gsnmp.Integer, Value: portSubtypeIfName},
		{Name: lldpRemBase + "7." + remSuffix, Type: gsnmp.OctetString, Value: []byte("Gi0/2")},
		{Name: lldpRemBase + "9." + remSuffix, Type: gsnmp.OctetString, Value: []byte("sw-b")},
	}
}

func testConfig(t *testing.T, communityEnv string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Discovery.TimeoutPerDevice = 3 * time.Second
	cfg.Discovery.Parallelism = 4
	cfg.Modules.SNMP.CommunityEnv = communityEnv
	cfg.Modules.LLDP.Enabled = true
	cfg.Credentials.TrialRatePerSecond = 100
	cfg.Modules.FDB.MaxVlans = 100
	return cfg
}

func newTestRediscoverer(t *testing.T, cfg *config.Config, m *metrics.Metrics, allow []string, auth bool) *Rediscoverer {
	t.Helper()
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	var mu sync.Mutex
	return NewRediscoverer(cfg, m, slogDiscard(), resolver, snmpwalk.ParseCIDRs(allow), &mu, auth)
}

// TestRediscoverOutOfScopeRejected verifies an in-band target outside the
// CIDR allow-list is rejected with out_of_scope and never walked — the admin
// call cannot expand scope (issue #73).
func TestRediscoverOutOfScopeRejected(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)
	rd := newTestRediscoverer(t, cfg, m, []string{"10.0.0.0/24"}, true)

	results := rd.Rediscover(context.Background(), []string{"10.99.0.1"})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Outcome != RediscoverOutOfScope {
		t.Fatalf("outcome = %q, want out_of_scope", results[0].Outcome)
	}
	if got := testutil.ToFloat64(m.AdminRediscoveryTotal.WithLabelValues("out_of_scope")); got != 1 {
		t.Errorf("out_of_scope metric = %v, want 1", got)
	}
}

// TestRediscoverHappyPath walks a live in-process SNMP agent and asserts a
// success outcome with the expected edge count and audit metric.
func TestRediscoverHappyPath(t *testing.T) {
	t.Setenv("TEST_COMM", "public")
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)

	// The agent listens on 127.0.0.1 on a random port; the advertised neighbour
	// is 127.0.0.2. Allow 127.0.0.0/8 so both are in scope, and override the
	// SNMP port so walkOne reaches the agent rather than the default 161.
	rd := newTestRediscoverer(t, cfg, m, []string{"127.0.0.0/8"}, true)
	rd.targetPortOverride = map[string]uint16{ip.String(): port}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	results := rd.Rediscover(ctx, []string{ip.String()})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Outcome != RediscoverSuccess {
		t.Fatalf("outcome = %q (err=%q), want success", results[0].Outcome, results[0].Error)
	}
	if results[0].Edges != 1 {
		t.Errorf("edges = %d, want 1", results[0].Edges)
	}
	if got := testutil.ToFloat64(m.AdminRediscoveryTotal.WithLabelValues("success")); got != 1 {
		t.Errorf("success metric = %v, want 1", got)
	}
}

// TestRediscoverEmitsNoPerCycleMetrics pins the intentional instrumentation
// gap (issue #153, spec C2): the /admin/rediscover forced walk must return the
// same (outcome, edgeCount) as the inline loop did AND emit ZERO per-cycle
// walker/decode/module-duration metric series. Feeding those series from the
// out-of-cycle path would corrupt the cycle's rate(...) dashboards, so routing
// walkOne through the shared walkModules helper must keep passing nil
// metrics + nil tracer and leave params.WalkerMetrics unset. This test locks
// that invariant; it passed against the pre-refactor inline loop too.
func TestRediscoverEmitsNoPerCycleMetrics(t *testing.T) {
	t.Setenv("TEST_COMM", "public")
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)

	rd := newTestRediscoverer(t, cfg, m, []string{"127.0.0.0/8"}, true)
	rd.targetPortOverride = map[string]uint16{ip.String(): port}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	results := rd.Rediscover(ctx, []string{ip.String()})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Outcome != RediscoverSuccess {
		t.Fatalf("outcome = %q (err=%q), want success", results[0].Outcome, results[0].Error)
	}
	if results[0].Edges != 1 {
		t.Errorf("edges = %d, want 1", results[0].Edges)
	}

	// No per-cycle walker-outcome series (driven by params.WalkerMetrics, left
	// unset by rediscover).
	if got := testutil.CollectAndCount(m.WalkerOutcomeTotal); got != 0 {
		t.Errorf("walker_outcome_total series = %d, want 0 (rediscover emits none)", got)
	}
	if got := testutil.CollectAndCount(m.BGPWalkerOutcomeTotal); got != 0 {
		t.Errorf("bgp_walker_outcome_total series = %d, want 0 (rediscover emits none)", got)
	}
	// No per-module SNMP-walk counter (driven by inst.metrics, passed nil).
	if got := testutil.CollectAndCount(m.SNMPWalksTotal); got != 0 {
		t.Errorf("snmp_walks_total series = %d, want 0 (rediscover emits none)", got)
	}
	// No per-module duration histogram observations.
	if got := testutil.CollectAndCount(m.DiscoveryModuleDuration); got != 0 {
		t.Errorf("discovery_module_duration_seconds series = %d, want 0 (rediscover emits none)", got)
	}
}

// TestRediscoverInvalidIPRejected verifies a non-IP target string is rejected
// with the error outcome and never walked.
func TestRediscoverInvalidIPRejected(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)
	rd := newTestRediscoverer(t, cfg, m, []string{"10.0.0.0/24"}, true)

	results := rd.Rediscover(context.Background(), []string{"not-an-ip"})
	if len(results) != 1 || results[0].Outcome != RediscoverError {
		t.Fatalf("results = %+v, want one error outcome", results)
	}
}
