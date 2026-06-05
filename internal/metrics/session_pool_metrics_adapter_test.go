package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Issue #83: the adapter bridges the snmp.SessionPoolMetrics calls onto the
// registered Prometheus collectors.
func TestSessionPoolMetricsAdapter(t *testing.T) {
	m := New(false)
	a := NewSessionPoolMetricsAdapter(m)

	a.RecordHit()
	a.RecordHit()
	a.RecordMiss()
	a.SetSize(3)
	a.RecordEviction("idle")
	a.RecordEviction("idle")
	a.RecordEviction("credential_rotation")

	if got := testutil.ToFloat64(m.SNMPSessionPoolHits); got != 2 {
		t.Errorf("hits = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.SNMPSessionPoolMisses); got != 1 {
		t.Errorf("misses = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.SNMPSessionPoolSize); got != 3 {
		t.Errorf("size = %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.SNMPSessionPoolEvictions.WithLabelValues("idle")); got != 2 {
		t.Errorf("idle evictions = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.SNMPSessionPoolEvictions.WithLabelValues("credential_rotation")); got != 1 {
		t.Errorf("credential_rotation evictions = %v, want 1", got)
	}
}
