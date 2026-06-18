package ospf

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// walkerCall captures one (walker, outcome) tuple recorded by a
// fakeWalkerMetrics; mirrors the BGP test pattern in
// internal/discovery/bgp/bgp_outcome_test.go.
type walkerCall struct {
	Walker  string
	Outcome string
}

type fakeWalkerMetrics struct {
	mu    sync.Mutex
	calls []walkerCall
}

func (f *fakeWalkerMetrics) RecordWalkerOutcome(walker, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, walkerCall{walker, outcome})
}

func (f *fakeWalkerMetrics) RecordProtocolWalkerOutcome(walker, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, walkerCall{walker, outcome})
}

// RecordDegraded satisfies snmputil.WalkerMetrics (issue #100). This module's
// outcome tests don't assert on degraded signals, so it is a no-op.
func (f *fakeWalkerMetrics) RecordDegraded(_, _ string) {}

// RecordSystemWalkAnomaly satisfies snmputil.WalkerMetrics (issue #101). This
// module's tests don't assert on system-walk anomalies, so it is a no-op.
func (f *fakeWalkerMetrics) RecordSystemWalkAnomaly(_ string) {}

func (f *fakeWalkerMetrics) count(walker, outcome string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, c := range f.calls {
		if c.Walker == walker && c.Outcome == outcome {
			n++
		}
	}
	return n
}

func ospfOutcomeParams(t *testing.T, pdus []gsnmp.SnmpPDU, fake *fakeWalkerMetrics) snmputil.Params {
	t.Helper()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	return snmputil.Params{
		IP:            ip,
		Port:          port,
		Community:     []byte("public"),
		Timeout:       3 * time.Second,
		WalkerMetrics: fake,
	}
}

// TestOutcomeEdges: one full(8) neighbour → outcome=edges.
func TestOutcomeEdges(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := ospfOutcomeParams(t, buildOspfAgentPDUs(), fake)

	edges, _, err := Walk(context.Background(), p, "router-x", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeEdges); got != 1 {
		t.Errorf("ospf edges = %d, want 1", got)
	}
}

// TestOutcomeMIBUnimplemented: device responds but ospfNbrTable BulkWalk
// returns zero PDUs (RFC 4750 MIB not implemented) → outcome=mib_unimplemented.
func TestOutcomeMIBUnimplemented(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("no-ospf-mib")},
	}
	p := ospfOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-x", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeMIBUnimplemented); got != 1 {
		t.Errorf("ospf mib_unimplemented = %d, want 1", got)
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeWalkerDrift); got != 0 {
		t.Errorf("ospf walker_drift = %d, want 0 (zero PDUs is not drift)", got)
	}
}

// TestOutcomeWalkerDrift: ospfNbrTable PDUs arrive but no row carries a usable
// neighbour IP (only the state column is populated) → outcome=walker_drift.
func TestOutcomeWalkerDrift(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"
	pdus := []gsnmp.SnmpPDU{
		// State present, no ospfNbrIpAddr column → row.nbrIP stays nil.
		{Name: base + "6." + idx, Type: gsnmp.Integer, Value: stateFull},
	}
	p := ospfOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-x", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeWalkerDrift); got != 1 {
		t.Errorf("ospf walker_drift = %d, want 1", got)
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeNoNeighbours); got != 0 {
		t.Errorf("ospf no_neighbours = %d, want 0 (no row decoded a usable IP)", got)
	}
}

// TestOutcomeNoNeighbours: a row decodes a usable neighbour IP cleanly but its
// state is down(1) — not an active adjacency → no edge → outcome=no_neighbours.
func TestOutcomeNoNeighbours(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"
	pdus := []gsnmp.SnmpPDU{
		{Name: base + "1." + idx, Type: gsnmp.IPAddress, Value: "192.0.2.1"},
		{Name: base + "6." + idx, Type: gsnmp.Integer, Value: 1}, // down(1)
	}
	p := ospfOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-x", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeNoNeighbours); got != 1 {
		t.Errorf("ospf no_neighbours = %d, want 1", got)
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeWalkerDrift); got != 0 {
		t.Errorf("ospf walker_drift = %d, want 0 (row decoded a usable IP)", got)
	}
}

// TestOutcomeError: unreachable agent (immediate timeout) → outcome=error.
func TestOutcomeError(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := snmputil.Params{
		IP:            net.ParseIP("127.0.0.1"),
		Port:          161,
		Community:     []byte("public"),
		Timeout:       time.Nanosecond,
		WalkerMetrics: fake,
	}

	_, _, _ = Walk(context.Background(), p, "router-x", nil)
	if got := fake.count(walkerOSPF, snmputil.OutcomeError); got != 1 {
		t.Errorf("ospf error = %d, want 1", got)
	}
	if got := fake.count(walkerOSPF, snmputil.OutcomeEdges); got != 0 {
		t.Errorf("ospf edges = %d, want 0 on error path", got)
	}
}
