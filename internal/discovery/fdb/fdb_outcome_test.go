package fdb

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// walkerCall captures one (walker, outcome) tuple recorded by a
// fakeWalkerMetrics; mirrors the BGP test pattern in
// internal/discovery/bgp/bgp_outcome_test.go.
type walkerCall struct {
	Walker  string
	Outcome string
}

// degradedCall captures one (module, reason) tuple recorded via
// RecordDegraded (issue #100).
type degradedCall struct {
	Module string
	Reason string
}

type fakeWalkerMetrics struct {
	mu        sync.Mutex
	calls     []walkerCall
	degradeds []degradedCall
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

func (f *fakeWalkerMetrics) RecordDegraded(module, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.degradeds = append(f.degradeds, degradedCall{module, reason})
}

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

func (f *fakeWalkerMetrics) countDegraded(module, reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, d := range f.degradeds {
		if d.Module == module && d.Reason == reason {
			n++
		}
	}
	return n
}

func fdbOutcomeParams(t *testing.T, pdus []gsnmp.SnmpPDU, fake *fakeWalkerMetrics) snmputil.Params {
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

// TestOutcomeEdges: one learned MAC on a forwarding port → one peer →
// outcome=edges.
func TestOutcomeEdges(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := fdbOutcomeParams(t, buildFdbAgentPDUs(), fake)

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := fake.count(walkerFDB, outcomeEdges); got != 1 {
		t.Errorf("fdb edges = %d, want 1", got)
	}
}

// TestOutcomeMIBUnimplemented: device responds but no FDB-family table has any
// PDUs (not a learning bridge) → outcome=mib_unimplemented.
func TestOutcomeMIBUnimplemented(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("not-a-bridge")},
	}
	p := fdbOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerFDB, outcomeMIBUnimplemented); got != 1 {
		t.Errorf("fdb mib_unimplemented = %d, want 1", got)
	}
	if got := fake.count(walkerFDB, outcomeWalkerDrift); got != 0 {
		t.Errorf("fdb walker_drift = %d, want 0 (zero PDUs is not drift)", got)
	}
}

// TestOutcomeWalkerDrift: dot1dTpFdbTable PDUs arrive but no entry carries a
// valid MAC (only the status column is populated, never the address) →
// outcome=walker_drift.
func TestOutcomeWalkerDrift(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	fdbBase := ".1.3.6.1.2.1.17.4.3.1."
	macSuffix := "0.10.187.204.221.238"
	pdus := []gsnmp.SnmpPDU{
		// Status + port present, but the address column (col 1) is never set,
		// so e.mac stays nil → no entry decodes cleanly.
		{Name: fdbBase + "2." + macSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: fdbBase + "3." + macSuffix, Type: gsnmp.Integer, Value: fdbStatusLearned},
	}
	p := fdbOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerFDB, outcomeWalkerDrift); got != 1 {
		t.Errorf("fdb walker_drift = %d, want 1", got)
	}
	if got := fake.count(walkerFDB, outcomeNoNeighbours); got != 0 {
		t.Errorf("fdb no_neighbours = %d, want 0 (no entry decoded cleanly)", got)
	}
}

// TestOutcomeNoNeighbours: an FDB entry decodes cleanly (valid MAC + port) but
// its status is self(4), not learned(3) — it is filtered before edge
// construction → no peer → outcome=no_neighbours.
func TestOutcomeNoNeighbours(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	fdbBase := ".1.3.6.1.2.1.17.4.3.1."
	basePortBase := ".1.3.6.1.2.1.17.1.4.1."
	macSuffix := "0.10.187.204.221.238"
	const fdbStatusSelf = 4
	pdus := []gsnmp.SnmpPDU{
		{Name: fdbBase + "1." + macSuffix, Type: gsnmp.OctetString, Value: []byte{0, 10, 187, 204, 221, 238}},
		{Name: fdbBase + "2." + macSuffix, Type: gsnmp.Integer, Value: 1},
		{Name: fdbBase + "3." + macSuffix, Type: gsnmp.Integer, Value: fdbStatusSelf},
		// Provide the bridge-port → ifIndex mapping so the only thing that
		// suppresses the edge is the self(4) status, isolating no_neighbours.
		{Name: basePortBase + "2.1", Type: gsnmp.Integer, Value: 2},
	}
	p := fdbOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerFDB, outcomeNoNeighbours); got != 1 {
		t.Errorf("fdb no_neighbours = %d, want 1", got)
	}
	if got := fake.count(walkerFDB, outcomeWalkerDrift); got != 0 {
		t.Errorf("fdb walker_drift = %d, want 0 (entry decoded cleanly)", got)
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

	_, _, _ = Walk(context.Background(), p, "sw-01", nil)
	if got := fake.count(walkerFDB, outcomeError); got != 1 {
		t.Errorf("fdb error = %d, want 1", got)
	}
	if got := fake.count(walkerFDB, outcomeEdges); got != 0 {
		t.Errorf("fdb edges = %d, want 0 on error path", got)
	}
}
