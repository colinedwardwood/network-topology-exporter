package isis

import (
	"context"
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/grafana/network-topology-exporter/internal/discovery"
	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// degradedCall captures one (module, reason) tuple recorded via RecordDegraded
// (issue #100). IS-IS uses this for skipped IPv6 adjacencies (issue #102).
type degradedCall struct {
	Module string
	Reason string
}

// fakeWalkerMetrics satisfies snmputil.WalkerMetrics. Only RecordDegraded is
// asserted on here; the other methods are no-ops because IS-IS does not emit
// walker-outcome or system-walk-anomaly signals.
type fakeWalkerMetrics struct {
	mu        sync.Mutex
	degradeds []degradedCall
}

func (f *fakeWalkerMetrics) RecordWalkerOutcome(_, _ string)         {}
func (f *fakeWalkerMetrics) RecordProtocolWalkerOutcome(_, _ string) {}
func (f *fakeWalkerMetrics) RecordSystemWalkAnomaly(_ string)        {}

func (f *fakeWalkerMetrics) RecordDegraded(module, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.degradeds = append(f.degradeds, degradedCall{module, reason})
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

// isisOutcomeParams starts a fake agent and returns Params wired to the fake
// metrics sink.
func isisOutcomeParams(t *testing.T, pdus []gsnmp.SnmpPDU, fake *fakeWalkerMetrics) snmputil.Params {
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

// Walk: a device advertising IPv6 IS-IS adjacencies increments
// discovery_degraded_total{module="isis", reason="unsupported_ip_version"}
// exactly once, regardless of how many IPv6 rows are skipped, while the IPv4
// edge is still produced.
func TestWalkIPv6AdjacencyRecordsDegradedOnce(t *testing.T) {
	const adjKeyIPv4 = "0.1.1"
	const adjKeyIPv6a = "0.1.2"
	const adjKeyIPv6b = "0.1.3"
	ipv6SuffixA := adjKeyIPv6a + ".2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1"
	ipv6SuffixB := adjKeyIPv6b + ".2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2"
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKeyIPv4, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjStateBase + adjKeyIPv6a, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjStateBase + adjKeyIPv6b, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjIPBase + adjKeyIPv4 + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
		{Name: adjIPBase + ipv6SuffixA, Type: gsnmp.OctetString, Value: []byte{32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
		{Name: adjIPBase + ipv6SuffixB, Type: gsnmp.OctetString, Value: []byte{32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}},
	}
	fake := &fakeWalkerMetrics{}
	p := isisOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 IPv4 edge, got %d", len(edges))
	}
	if got := fake.countDegraded("isis", discovery.DegradedReasonUnsupportedIPVersion); got != 1 {
		t.Errorf("RecordDegraded(isis, %s) called %d times, want exactly 1", discovery.DegradedReasonUnsupportedIPVersion, got)
	}
}

// Walk: an IPv6-only device (zero IPv4 edges) still increments the degraded
// metric exactly once — the worst case the direct sink exists to cover.
func TestWalkIPv6OnlyRecordsDegraded(t *testing.T) {
	const adjKeyIPv6 = "0.1.1"
	ipv6Suffix := adjKeyIPv6 + ".2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1"
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKeyIPv6, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjIPBase + ipv6Suffix, Type: gsnmp.OctetString, Value: []byte{32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
	}
	fake := &fakeWalkerMetrics{}
	p := isisOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for IPv6-only device, got %d", len(edges))
	}
	if got := fake.countDegraded("isis", discovery.DegradedReasonUnsupportedIPVersion); got != 1 {
		t.Errorf("RecordDegraded(isis, %s) called %d times, want exactly 1", discovery.DegradedReasonUnsupportedIPVersion, got)
	}
}

// Walk: a pure-IPv4 device does NOT increment the IPv6 reason.
func TestWalkPureIPv4DoesNotRecordUnsupportedIPVersion(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: adjStateBase + adjKey, Type: gsnmp.Integer, Value: isisAdjStateUp},
		{Name: adjIPBase + adjKey + ".1.4.192.0.2.1", Type: gsnmp.OctetString, Value: []byte{192, 0, 2, 1}},
	}
	fake := &fakeWalkerMetrics{}
	p := isisOutcomeParams(t, pdus, fake)

	edges, _, err := Walk(context.Background(), p, "router-a", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if got := fake.countDegraded("isis", discovery.DegradedReasonUnsupportedIPVersion); got != 0 {
		t.Errorf("RecordDegraded(isis, %s) called %d times, want 0 for pure-IPv4 device", discovery.DegradedReasonUnsupportedIPVersion, got)
	}
}
