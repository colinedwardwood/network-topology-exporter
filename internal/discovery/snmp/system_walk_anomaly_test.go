package snmp

import (
	"context"
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// fakeAnomalySink is a per-test WalkerMetrics implementation that captures the
// reasons passed to RecordSystemWalkAnomaly so the system-walk tests can assert
// which low-cardinality anomaly fired (issue #101). The other interface methods
// are no-ops — the system walk only ever calls RecordSystemWalkAnomaly. The
// mutex guards against the goroutine Walk uses internally for the GET, although
// in practice the increments run on the caller's goroutine after the GET
// completes.
type fakeAnomalySink struct {
	mu      sync.Mutex
	reasons []string
}

func (f *fakeAnomalySink) RecordWalkerOutcome(_, _ string)         {}
func (f *fakeAnomalySink) RecordProtocolWalkerOutcome(_, _ string) {}
func (f *fakeAnomalySink) RecordDegraded(_, _ string)              {}

func (f *fakeAnomalySink) RecordSystemWalkAnomaly(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons = append(f.reasons, reason)
}

// count returns how many times the given reason was recorded.
func (f *fakeAnomalySink) count(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, r := range f.reasons {
		if r == reason {
			n++
		}
	}
	return n
}

// walkAnomalyParams starts a fake agent serving pdus and returns Params wired
// to the supplied sink.
func walkAnomalyParams(t *testing.T, pdus []gsnmp.SnmpPDU, sink *fakeAnomalySink) Params {
	t.Helper()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	return Params{
		IP:            ip,
		Port:          port,
		Community:     []byte("public"),
		Timeout:       3 * time.Second,
		WalkerMetrics: sink,
	}
}

// Walk: an empty sysName falls back to the management IP and increments
// {reason="empty_sysname"} exactly once. The vendor is known here (cisco), so
// the unknown_vendor reason must NOT fire.
func TestSystemWalkAnomalyEmptySysName(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS 15.2")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		// Garbage sysName: whitespace-only → NormaliseName yields "".
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("   ")},
	}
	sink := &fakeAnomalySink{}
	p := walkAnomalyParams(t, pdus, sink)

	dev, err := Walk(context.Background(), p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev.ID != p.IP.String() {
		t.Errorf("ID = %q, want IP fallback %s", dev.ID, p.IP.String())
	}
	if got := sink.count(systemWalkAnomalyEmptySysName); got != 1 {
		t.Errorf("empty_sysname count = %d, want 1", got)
	}
	if got := sink.count(systemWalkAnomalyUnknownVendor); got != 0 {
		t.Errorf("unknown_vendor count = %d, want 0 (vendor is cisco)", got)
	}
}

// Walk: an unrecognised enterprise sysObjectID leaves Vendor="unknown" and
// increments {reason="unknown_vendor"} exactly once. sysName is good here, so
// empty_sysname must NOT fire.
func TestSystemWalkAnomalyUnknownVendor(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Acme OS 1.0")},
		// Enterprise 9999 is not in enterprisePrefixes → "unknown".
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9999.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("edge-sw-07")},
	}
	sink := &fakeAnomalySink{}
	p := walkAnomalyParams(t, pdus, sink)

	dev, err := Walk(context.Background(), p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev.Vendor != "unknown" {
		t.Errorf("Vendor = %q, want unknown", dev.Vendor)
	}
	if got := sink.count(systemWalkAnomalyUnknownVendor); got != 1 {
		t.Errorf("unknown_vendor count = %d, want 1", got)
	}
	if got := sink.count(systemWalkAnomalyEmptySysName); got != 0 {
		t.Errorf("empty_sysname count = %d, want 0 (sysName is good)", got)
	}
}

// Walk: a normal device (good sysName, known vendor) records NEITHER anomaly.
func TestSystemWalkAnomalyNormalDevice(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS 15.2")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("core-sw-01")},
	}
	sink := &fakeAnomalySink{}
	p := walkAnomalyParams(t, pdus, sink)

	dev, err := Walk(context.Background(), p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev.ID != "core-sw-01" || dev.Vendor != "cisco" {
		t.Fatalf("unexpected device: ID=%q Vendor=%q", dev.ID, dev.Vendor)
	}
	if got := sink.count(systemWalkAnomalyEmptySysName); got != 0 {
		t.Errorf("empty_sysname count = %d, want 0", got)
	}
	if got := sink.count(systemWalkAnomalyUnknownVendor); got != 0 {
		t.Errorf("unknown_vendor count = %d, want 0", got)
	}
}

// Walk: a nil WalkerMetrics must not panic on either anomaly path. The
// empty-sysName + unknown-vendor device hits both increment sites with a nil
// sink, exercising the nil-tolerant recordSystemWalkAnomaly helper.
func TestSystemWalkAnomalyNilSink(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Acme OS 1.0")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9999.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(0)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	// WalkerMetrics left nil.
	p := Params{IP: ip, Port: port, Community: []byte("public"), Timeout: 3 * time.Second}

	if _, err := Walk(context.Background(), p); err != nil {
		t.Fatalf("Walk with nil sink: %v", err)
	}
}
