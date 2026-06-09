package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"

	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func payload(id string) federation.SpokePayload {
	return federation.SpokePayload{SpokeID: id, CycleAt: time.Now()}
}

func testutilCounterVecValue(t *testing.T, cv *prometheus.CounterVec, label string) float64 {
	t.Helper()
	return testutil.ToFloat64(cv.WithLabelValues(label))
}

// Enqueue must never block, and a second enqueue into an occupied slot must
// drop the older payload (superseded) and keep the newer one.
func TestSpokePusherEnqueueLatestOnly(t *testing.T) {
	m := metrics.New(false)
	p := newSpokePusher(func(context.Context, federation.SpokePayload) error { return nil }, m, testLogger())

	p.Enqueue(context.Background(), payload("A")) // fills slot
	p.Enqueue(context.Background(), payload("B")) // supersedes A

	if got := testutilCounterVecValue(t, m.FederationSpokePushDropsTotal, "superseded"); got != 1 {
		t.Errorf("superseded drops = %v, want 1", got)
	}
	q := <-p.ch
	if q.payload.SpokeID != "B" {
		t.Errorf("mailbox holds %q, want B (most-recent-wins)", q.payload.SpokeID)
	}
}

// With a push blocked in flight, Enqueue must still return immediately.
func TestSpokePusherEnqueueNeverBlocks(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	push := func(context.Context, federation.SpokePayload) error {
		started <- struct{}{}
		<-release
		return nil
	}
	m := metrics.New(false)
	p := newSpokePusher(push, m, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go p.run(ctx)

	p.Enqueue(context.Background(), payload("A")) // consumed; push blocks
	<-started

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			p.Enqueue(context.Background(), payload("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked while a push was in flight")
	}

	close(release)
	cancel()
	<-p.stopped
}

// drainOnce must return within the bounded deadline when the final push hangs,
// and count a shutdown drop. Driven directly (payload pre-loaded in the mailbox)
// so the assertion is deterministic.
func TestSpokePusherDrainOnceBounded(t *testing.T) {
	push := func(ctx context.Context, _ federation.SpokePayload) error {
		<-ctx.Done() // hang until the bounded drain context expires
		return ctx.Err()
	}
	m := metrics.New(false)
	p := newSpokePusher(push, m, testLogger())
	p.drain = 50 * time.Millisecond

	p.Enqueue(context.Background(), payload("A")) // sits in the mailbox, no consumer

	start := time.Now()
	p.drainOnce()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("drainOnce took %v, want ~%v (bounded)", elapsed, p.drain)
	}
	if got := testutilCounterVecValue(t, m.FederationSpokePushDropsTotal, "shutdown"); got < 1 {
		t.Errorf("shutdown drops = %v, want >= 1", got)
	}
}

// run must exit cleanly on ctx cancel and leak no goroutine.
func TestSpokePusherRunNoLeak(t *testing.T) {
	defer goleak.VerifyNone(t)
	m := metrics.New(false)
	p := newSpokePusher(func(context.Context, federation.SpokePayload) error { return nil }, m, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go p.run(ctx)
	p.Enqueue(context.Background(), payload("A"))
	cancel()
	p.Shutdown()
}

// On a successful push the freshness gauge advances.
func TestSpokePusherFreshnessGauge(t *testing.T) {
	m := metrics.New(false)
	p := newSpokePusher(func(context.Context, federation.SpokePayload) error { return nil }, m, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go p.run(ctx)

	p.Enqueue(context.Background(), payload("A"))

	deadline := time.After(2 * time.Second)
	for testutil.ToFloat64(m.FederationSpokePushLastSuccessUnix) == 0 {
		select {
		case <-deadline:
			t.Fatal("freshness gauge never advanced after a successful push")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-p.stopped
}
