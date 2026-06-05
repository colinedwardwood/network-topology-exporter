package app

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/loglimit"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// TestNewLogger maps level strings to slog levels and never returns nil.
func TestNewLogger(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "bogus", ""} {
		if NewLogger(lvl) == nil {
			t.Errorf("NewLogger(%q) returned nil", lvl)
		}
	}
}

// TestWarnSnapshotFallback uses the direct slog path when no limiter is set, and
// the rate-limited path when a limiter is configured. Neither must panic.
func TestWarnSnapshotFallback(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	cfg.Snapshot.Path = "/tmp/x.json"

	// No limiter: direct slog.Warn path.
	lc := LoopConfig{Logger: slogDiscard(), Cfg: cfg}
	lc.WarnSnapshot(context.Background(), "site-a", "no limiter path")

	// With limiter: rate-limited path keyed on site+path.
	lc.WarnLimiter = loglimit.New(slogDiscard(), time.Hour)
	lc.WarnSnapshot(context.Background(), "site-a", "limiter path")
}

// TestOtlpPushDropsWhenSemFull drops the push (and increments the dropped
// counter) when the concurrency semaphore is already saturated.
func TestOtlpPushDropsWhenSemFull(t *testing.T) {
	m := metrics.New(false)
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // saturate
	var ran atomic.Bool
	lc := LoopConfig{Logger: slogDiscard(), M: m, OtlpSem: sem}
	lc.OtlpPush(context.Background(), func(context.Context) error {
		ran.Store(true)
		return nil
	}, "should not run")
	if ran.Load() {
		t.Error("push fn ran despite saturated semaphore")
	}
}

// TestOtlpPushRunsAndCountsOK enqueues a push that succeeds and waits for it via
// the WaitGroup, asserting the goroutine completed and the semaphore drained.
func TestOtlpPushRunsAndCountsOK(t *testing.T) {
	m := metrics.New(false)
	sem := make(chan struct{}, 1)
	var wg sync.WaitGroup
	var ran atomic.Bool
	lc := LoopConfig{Logger: slogDiscard(), M: m, OtlpSem: sem, OtlpWg: &wg}
	lc.OtlpPush(context.Background(), func(context.Context) error {
		ran.Store(true)
		return nil
	}, "push ok")
	wg.Wait()
	if !ran.Load() {
		t.Error("push fn did not run")
	}
	// Semaphore must have drained so a follow-up push can proceed.
	select {
	case sem <- struct{}{}:
		<-sem
	default:
		t.Error("semaphore not released after push completed")
	}
}

// TestOtlpPushCountsError runs a failing push and waits for completion.
func TestOtlpPushCountsError(t *testing.T) {
	m := metrics.New(false)
	var wg sync.WaitGroup
	lc := LoopConfig{Logger: slogDiscard(), M: m, OtlpWg: &wg}
	lc.OtlpPush(context.Background(), func(context.Context) error {
		return errors.New("boom")
	}, "push failed")
	wg.Wait()
}

// TestRunDiscoveryLoopShutsDownCleanly drives the full loop against an
// in-process SNMP agent for one cycle, writing a snapshot, then cancels the
// context and asserts the loop returns (goleak in TestMain guards that the
// snapshot-writer goroutine and any in-flight work shut down with no leak).
func TestRunDiscoveryLoopShutsDownCleanly(t *testing.T) {
	t.Setenv("TEST_COMM", "public")
	cfg := testConfig(t, "TEST_COMM")
	cfg.Discovery.Interval = time.Hour // one immediate cycle, then block on ticker
	cfg.Discovery.CycleBudgetFraction = 1
	cfg.Discovery.UnconfirmedLinkTTLCycles = 3
	cfg.Discovery.Scope.CIDRAllowList = []string{"127.0.0.0/8"}
	cfg.Snapshot.Path = filepath.Join(t.TempDir(), "snap.json")

	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", net.ParseIP("127.0.0.2")))
	ip, port := snmptest.ParseAddr(addr)
	cfg.Targets = []config.TargetConfig{{Host: ip.String(), Port: int(port)}}

	m := metrics.New(false)
	ctx, cancel := context.WithCancel(context.Background())

	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool
	lc := LoopConfig{
		Cancel: cancel,
		Logger: slogDiscard(),
		Cfg:    cfg,
		M:      m,
		Status: &status,
		Ready:  &ready,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunDiscoveryLoop(ctx, lc)
	}()

	// Wait until the first cycle has marked the loop ready, then cancel.
	deadline := time.After(8 * time.Second)
	for !ready.Load() {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("loop never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("RunDiscoveryLoop did not return after context cancel")
	}

	if status.Load() == nil {
		t.Error("cycle status was never stored")
	}
}
