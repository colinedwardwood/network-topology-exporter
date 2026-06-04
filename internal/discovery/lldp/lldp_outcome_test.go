package lldp

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

// fakeWalkerMetrics is a per-test sink that satisfies snmputil.WalkerMetrics.
type fakeWalkerMetrics struct {
	mu    sync.Mutex
	calls []walkerCall
}

// RecordWalkerOutcome implements snmputil.WalkerMetrics (BGP path — unused
// here, present only to satisfy the interface).
func (f *fakeWalkerMetrics) RecordWalkerOutcome(walker, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, walkerCall{walker, outcome})
}

// RecordProtocolWalkerOutcome implements snmputil.WalkerMetrics (the generic
// non-BGP path the LLDP walker uses).
func (f *fakeWalkerMetrics) RecordProtocolWalkerOutcome(walker, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, walkerCall{walker, outcome})
}

// RecordDegraded satisfies snmputil.WalkerMetrics (issue #100). This module's
// outcome tests don't assert on degraded signals, so it is a no-op.
func (f *fakeWalkerMetrics) RecordDegraded(_, _ string) {}

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

func lldpOutcomeParams(t *testing.T, pdus []gsnmp.SnmpPDU, fake *fakeWalkerMetrics) snmputil.Params {
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

// TestOutcomeEdges: a valid lldpRemTable row produces an edge → outcome=edges.
func TestOutcomeEdges(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	p := lldpOutcomeParams(t, buildLLDPAgentPDUs(), fake)

	edges, _, err := Walk(context.Background(), p, "leaf-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := fake.count(walkerLLDP, outcomeEdges); got != 1 {
		t.Errorf("lldp edges = %d, want 1", got)
	}
}

// TestOutcomeMIBUnimplemented: the device responds to SNMP but the
// lldpRemTable BulkWalk returns zero PDUs → outcome=mib_unimplemented.
func TestOutcomeMIBUnimplemented(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	p := lldpOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "leaf-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerLLDP, outcomeMIBUnimplemented); got != 1 {
		t.Errorf("lldp mib_unimplemented = %d, want 1", got)
	}
	if got := fake.count(walkerLLDP, outcomeWalkerDrift); got != 0 {
		t.Errorf("lldp walker_drift = %d, want 0 (zero PDUs is not drift)", got)
	}
}

// TestOutcomeWalkerDrift: lldpRemTable PDUs arrive but every assembled row is
// rejected by the decoder (invalid chassis ID subtype) → outcome=walker_drift.
func TestOutcomeWalkerDrift(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	remBase := ".1.0.8802.1.1.2.1.4.1.1."
	remSuffix := "0.1.1" // timeMark.portNum.remIndex
	pdus := []gsnmp.SnmpPDU{
		// chassisSubtype = 99 (out of the IEEE 1–7 range) → row rejected.
		{Name: remBase + "4." + remSuffix, Type: gsnmp.Integer, Value: 99},
		{Name: remBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: []byte{0, 1, 2, 3, 4, 5}},
		{Name: remBase + "6." + remSuffix, Type: gsnmp.Integer, Value: int(portSubtypeInterfaceName)},
		{Name: remBase + "7." + remSuffix, Type: gsnmp.OctetString, Value: []byte("Ethernet2")},
	}
	p := lldpOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "leaf-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if got := fake.count(walkerLLDP, outcomeWalkerDrift); got != 1 {
		t.Errorf("lldp walker_drift = %d, want 1", got)
	}
	if got := fake.count(walkerLLDP, outcomeNoNeighbours); got != 0 {
		t.Errorf("lldp no_neighbours = %d, want 0 (every row rejected)", got)
	}
}

// TestOutcomeNoNeighbours: a row decodes cleanly but is filtered out of scope
// (its network-address chassis ID is outside allowedNets) → no edge, but the
// row was a clean decode → outcome=no_neighbours.
func TestOutcomeNoNeighbours(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	remBase := ".1.0.8802.1.1.2.1.4.1.1."
	remSuffix := "0.1.1"
	// chassisSubtype 5 (networkAddress), IPv4 family byte 1, addr 10.9.9.9.
	pdus := []gsnmp.SnmpPDU{
		{Name: remBase + "4." + remSuffix, Type: gsnmp.Integer, Value: int(chassisSubtypeNetworkAddress)},
		{Name: remBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: []byte{1, 10, 9, 9, 9}},
		{Name: remBase + "6." + remSuffix, Type: gsnmp.Integer, Value: int(portSubtypeInterfaceName)},
		{Name: remBase + "7." + remSuffix, Type: gsnmp.OctetString, Value: []byte("Ethernet2")},
		{Name: remBase + "9." + remSuffix, Type: gsnmp.OctetString, Value: []byte("peer-out-of-scope")},
	}
	p := lldpOutcomeParams(t, pdus, fake)

	// Allow only 192.168.0.0/16; the 10.9.9.9 neighbour is out of scope.
	_, allowed, _ := net.ParseCIDR("192.168.0.0/16")

	edges, oos, err := Walk(context.Background(), p, "leaf-01", []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope neighbour, got %d", len(oos))
	}
	if got := fake.count(walkerLLDP, outcomeNoNeighbours); got != 1 {
		t.Errorf("lldp no_neighbours = %d, want 1", got)
	}
	if got := fake.count(walkerLLDP, outcomeWalkerDrift); got != 0 {
		t.Errorf("lldp walker_drift = %d, want 0 (row decoded cleanly)", got)
	}
}

// TestOutcomeError: an unreachable agent (immediate timeout) makes the walk
// error → outcome=error.
func TestOutcomeError(t *testing.T) {
	t.Parallel()
	fake := &fakeWalkerMetrics{}
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)
	p := snmputil.Params{
		IP:            ip,
		Port:          port,
		Community:     []byte("public"),
		Timeout:       time.Nanosecond, // immediate timeout
		WalkerMetrics: fake,
	}

	_, _, _ = Walk(context.Background(), p, "leaf-01", nil)
	if got := fake.count(walkerLLDP, outcomeError); got != 1 {
		t.Errorf("lldp error = %d, want 1", got)
	}
	if got := fake.count(walkerLLDP, outcomeEdges); got != 0 {
		t.Errorf("lldp edges = %d, want 0 on error path", got)
	}
}
