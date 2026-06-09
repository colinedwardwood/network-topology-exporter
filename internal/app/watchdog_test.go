package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// TestLivenessMaxStale asserts the hub-exclusion and disable rules that gate
// both the /healthz liveness check and the watchdog goroutine: a standalone
// loop gets interval × cycles; a pure hub gets 0 (no local loop to wedge); an
// explicit 0 cycle count disables the gate regardless of role.
func TestLivenessMaxStale(t *testing.T) {
	cycles := func(n int) *int { return &n }

	cases := []struct {
		name     string
		role     config.Role
		interval time.Duration
		cycles   *int
		want     time.Duration
	}{
		{"standalone default", "standalone", 60 * time.Second, cycles(3), 3 * time.Minute},
		{"spoke custom", "spoke", 30 * time.Second, cycles(5), 150 * time.Second},
		{"hub excluded even with cycles set", "hub", 60 * time.Second, cycles(3), 0},
		{"explicit zero disables", "standalone", 60 * time.Second, cycles(0), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Federation.Role = tc.role
			cfg.Discovery.Interval = tc.interval
			cfg.Discovery.LivenessMaxStaleCycles = tc.cycles
			if got := livenessMaxStale(cfg); got != tc.want {
				t.Errorf("livenessMaxStale = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunStaleWatchdogEscalatesOnly drives the watchdog with a fast tick and a
// controllable clock, asserting the escalate-only ownership split: GraphStale
// flips to 1 when the last cycle ages past maxStale, and the watchdog does NOT
// clear it back to 0 once a fresh cycle lands (clearing is owned exclusively by
// RunDiscoveryLoop's success path). It also confirms the goroutine exits on
// context cancellation (the package goleak TestMain catches a leak if not).
func TestRunStaleWatchdogEscalatesOnly(t *testing.T) {
	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]

	// clock is the watchdog's notion of "now"; advance it to age the last cycle.
	var clockMu sync.Mutex
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}

	const maxStale = 3 * time.Minute

	// First cycle has just completed (fresh).
	status.Store(&httpx.CycleStatus{LastCycleAt: now()})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Tick fast so the test does not wait on real interval-length sleeps.
		RunStaleWatchdog(ctx, &status, m, 5*time.Millisecond, maxStale, now)
	}()

	waitFor := func(want float64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if testutil.ToFloat64(m.GraphStale) == want {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("GraphStale = %v, want %v", testutil.ToFloat64(m.GraphStale), want)
	}

	// Age the clock past the window: the watchdog must re-assert stale.
	advance(maxStale + time.Minute)
	waitFor(1)

	// A fresh cycle lands: the watchdog must NOT clear stale — clearing is
	// owned exclusively by RunDiscoveryLoop's success path. The gauge stays 1
	// until the loop itself sets it to 0. Give the watchdog several ticks to
	// (incorrectly) clear it and assert it does not.
	status.Store(&httpx.CycleStatus{LastCycleAt: now()})
	time.Sleep(40 * time.Millisecond)
	if got := testutil.ToFloat64(m.GraphStale); got != 1 {
		t.Errorf("GraphStale = %v, want 1 (watchdog must not clear; the loop owns Set(0))", got)
	}

	// The loop's success path (simulated) is the only writer that clears.
	m.GraphStale.Set(0)
	waitFor(0)

	// Cancellation must stop the goroutine cleanly.
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not exit on context cancellation")
	}
}

// TestRunStaleWatchdogIgnoresNilStatus verifies the watchdog leaves the gauge
// untouched before the first cycle (status == nil), so it never fights the
// cold-start GraphStale=1 owned by RunDiscoveryLoop.
func TestRunStaleWatchdogIgnoresNilStatus(t *testing.T) {
	m := metrics.New(false)
	m.GraphStale.Set(1) // cold-start state owned by the loop
	var status atomic.Pointer[httpx.CycleStatus]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		RunStaleWatchdog(ctx, &status, m, 2*time.Millisecond, time.Minute, time.Now)
	}()

	// Give the watchdog several ticks with a nil status.
	time.Sleep(30 * time.Millisecond)
	if got := testutil.ToFloat64(m.GraphStale); got != 1 {
		t.Errorf("GraphStale = %v, want 1 (watchdog must not touch the gauge before first cycle)", got)
	}
	cancel()
	wg.Wait()
}
