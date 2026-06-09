package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

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
