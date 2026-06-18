package app_test

// Tests relocated verbatim from cmd/topology-exporter/main_test.go (#171):
// they exercise exported internal/app behaviour, not main-level wiring,
// and belong with the package they test. External test package (app_test)
// so the `app.` call sites move unchanged.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/grafana/network-topology-exporter/internal/app"
	"github.com/grafana/network-topology-exporter/internal/metrics"
	"github.com/grafana/network-topology-exporter/internal/output/otlp"
)

// TestOtlpPushDropsWhenSemaphoreFull verifies that Push increments the
// dropped counter and returns immediately when the semaphore is full. The
// publisher is built in its enabled form (a bare non-nil exporter sentinel
// plus a saturated cap-1 semaphore) so the drop path is exercised.
func TestOtlpPushDropsWhenSemaphoreFull(t *testing.T) {
	m := metrics.New(false)
	pub := app.NewOTLPPublisher(&otlp.Exporter{}, 1, slog.Default(), m)

	// Saturate the cap by enqueuing one push that blocks until released so the
	// single semaphore slot stays occupied for the duration of the drop test.
	unblock := make(chan struct{})
	pub.Push(func(_ context.Context) error {
		<-unblock
		return nil
	}, "occupier")

	// The next push must be dropped immediately (semaphore full).
	pub.Push(func(_ context.Context) error {
		t.Error("dropped push fn ran")
		return nil
	}, "should not be called")

	// Issue #20 widened the label set to {status, reason}.
	if got := testutil.ToFloat64(m.OTLPPushTotal.WithLabelValues("dropped", metrics.ReasonNA)); got != 1 {
		t.Errorf("OTLPPushTotal{dropped,n/a} = %v, want 1", got)
	}

	close(unblock)
	pub.Drain()
}

// TestOtlpPushDrainsOnShutdown verifies that Drain blocks until all in-flight
// goroutines finish: the goroutine spawned by Push must complete before Drain
// returns.
func TestOtlpPushDrainsOnShutdown(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})

	pub := app.NewOTLPPublisher(&otlp.Exporter{}, 1, slog.Default(), metrics.New(false))

	pub.Push(func(_ context.Context) error {
		close(started) // signal that the goroutine has begun
		<-unblock      // block until the test says go
		return nil
	}, "drain test")

	// Wait until the goroutine has started before draining.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not start within 5s")
	}

	// Release the goroutine and drain.
	close(unblock)

	done := make(chan struct{})
	go func() { pub.Drain(); close(done) }()

	select {
	case <-done:
		// Drain returned cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("Drain() did not return within 5s after goroutine finished")
	}
}
