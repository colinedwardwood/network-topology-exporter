package cdp

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

func cdpOutcomeParams(t *testing.T, pdus []gsnmp.SnmpPDU, fake *fakeWalkerMetrics) snmputil.Params {
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

// validCDPPDUs builds an ifName entry plus a cdpCacheTable row with a usable
// neighbour device ID and port and an IPv4 cache address.
func validCDPPDUs(neighIP []byte) []gsnmp.SnmpPDU {
	cacheBase := "." + oidCDPCacheTable + ".1."
	idx := "3.1" // ifIndex.neighIndex
	return []gsnmp.SnmpPDU{
		// ifXTable.ifName for ifIndex 3.
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.3", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/3")},
		// cdpCacheTable row.
		{Name: cacheBase + "3." + idx, Type: gsnmp.Integer, Value: 1}, // addrType IPv4
		{Name: cacheBase + "4." + idx, Type: gsnmp.OctetString, Value: neighIP},
		{Name: cacheBase + "6." + idx, Type: gsnmp.OctetString, Value: []byte("peer-sw")},
		{Name: cacheBase + "7." + idx, Type: gsnmp.OctetString, Value: []byte("Gi0/1")},
	}
}

// TestOutcomeEdges: a valid cache row produces an edge → outcome=edges.
func TestOutcomeEdges(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := cdpOutcomeParams(t, validCDPPDUs([]byte{10, 1, 2, 3}), fake)

	edges, _, err := Walk(context.Background(), p, "local-sw", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := fake.count(walkerCDP, outcomeEdges); got != 1 {
		t.Errorf("cdp edges = %d, want 1", got)
	}
}

// TestOutcomeMIBUnimplemented: device responds but cdpCacheTable BulkWalk
// returns zero PDUs (e.g. non-Cisco) → outcome=mib_unimplemented.
func TestOutcomeMIBUnimplemented(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("non-cisco")},
	}
	p := cdpOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "local-sw", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerCDP, outcomeMIBUnimplemented); got != 1 {
		t.Errorf("cdp mib_unimplemented = %d, want 1", got)
	}
	if got := fake.count(walkerCDP, outcomeWalkerDrift); got != 0 {
		t.Errorf("cdp walker_drift = %d, want 0 (zero PDUs is not drift)", got)
	}
}

// TestOutcomeWalkerDrift: cache-table PDUs arrive but no row yields a usable
// neighbour identity (only the addrType column is populated, never deviceID
// or devPort) → outcome=walker_drift.
func TestOutcomeWalkerDrift(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	cacheBase := "." + oidCDPCacheTable + ".1."
	idx := "3.1"
	pdus := []gsnmp.SnmpPDU{
		// Only addrType populated; deviceID/devPort never set → entry unusable.
		{Name: cacheBase + "3." + idx, Type: gsnmp.Integer, Value: 1},
		{Name: cacheBase + "4." + idx, Type: gsnmp.OctetString, Value: []byte{10, 1, 2, 3}},
	}
	p := cdpOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "local-sw", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerCDP, outcomeWalkerDrift); got != 1 {
		t.Errorf("cdp walker_drift = %d, want 1", got)
	}
	if got := fake.count(walkerCDP, outcomeNoNeighbours); got != 0 {
		t.Errorf("cdp no_neighbours = %d, want 0 (no row decoded cleanly)", got)
	}
}

// TestOutcomeNoNeighbours: a row decodes cleanly (deviceID + devPort present)
// but its IPv4 neighbour falls outside allowedNets → no edge, clean decode →
// outcome=no_neighbours.
func TestOutcomeNoNeighbours(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := cdpOutcomeParams(t, validCDPPDUs([]byte{10, 9, 9, 9}), fake)

	_, allowed, _ := net.ParseCIDR("192.168.0.0/16")

	edges, oos, err := Walk(context.Background(), p, "local-sw", []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope neighbour, got %d", len(oos))
	}
	if got := fake.count(walkerCDP, outcomeNoNeighbours); got != 1 {
		t.Errorf("cdp no_neighbours = %d, want 1", got)
	}
	if got := fake.count(walkerCDP, outcomeWalkerDrift); got != 0 {
		t.Errorf("cdp walker_drift = %d, want 0 (row decoded cleanly)", got)
	}
}

// TestOutcomeError: unreachable agent (immediate timeout) → outcome=error.
func TestOutcomeError(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)
	p := snmputil.Params{
		IP:            ip,
		Port:          port,
		Community:     []byte("public"),
		Timeout:       time.Nanosecond,
		WalkerMetrics: fake,
	}

	_, _, _ = Walk(context.Background(), p, "local-sw", nil)
	if got := fake.count(walkerCDP, outcomeError); got != 1 {
		t.Errorf("cdp error = %d, want 1", got)
	}
	if got := fake.count(walkerCDP, outcomeEdges); got != 0 {
		t.Errorf("cdp edges = %d, want 0 on error path", got)
	}
}
