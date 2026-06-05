package app

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// runRecovered calls fn inside a function whose only deferred call is
// recoverGoroutine, so the test can observe that a panic in fn is recovered
// (the helper returns normally) rather than propagating up the stack.
func runRecovered(site string, m *metrics.Metrics, fn func()) {
	defer recoverGoroutine(site, nil, m)
	fn()
}

// TestRecoverGoroutine_RecoversAndCounts verifies the core contract: a wrapped
// function that panics does NOT propagate the panic, and the
// network_topology_panics_total{site} counter is incremented by 1.
func TestRecoverGoroutine_RecoversAndCounts(t *testing.T) {
	m := metrics.New(false)

	// If recoverGoroutine re-panicked, the test goroutine would crash; reaching
	// the assertion below proves the panic was swallowed.
	runRecovered("snapshot_writer", m, func() {
		panic("boom")
	})

	got := testutil.ToFloat64(m.PanicsRecoveredTotal.WithLabelValues("snapshot_writer"))
	if got != 1 {
		t.Fatalf("PanicsRecoveredTotal{site=snapshot_writer} = %v, want 1", got)
	}
	// A different site must not have been touched.
	if other := testutil.ToFloat64(m.PanicsRecoveredTotal.WithLabelValues("otlp_push")); other != 0 {
		t.Fatalf("PanicsRecoveredTotal{site=otlp_push} = %v, want 0", other)
	}
}

// TestRecoverGoroutine_NoPanic verifies the happy path is a no-op: a function
// that does not panic leaves the counter at zero.
func TestRecoverGoroutine_NoPanic(t *testing.T) {
	m := metrics.New(false)
	ran := false
	runRecovered("otlp_push", m, func() { ran = true })
	if !ran {
		t.Fatal("wrapped function did not run")
	}
	if got := testutil.ToFloat64(m.PanicsRecoveredTotal.WithLabelValues("otlp_push")); got != 0 {
		t.Fatalf("PanicsRecoveredTotal{site=otlp_push} = %v, want 0 on no-panic path", got)
	}
}

// TestRecoverGoroutine_NilMetrics verifies the helper tolerates a nil *Metrics
// (used by goroutines started before metrics are wired, and by tests): it must
// recover the panic without itself panicking on the nil counter.
func TestRecoverGoroutine_NilMetrics(t *testing.T) {
	// Must not panic even though m is nil.
	runRecovered("hub_serve", nil, func() { panic("boom") })
}
