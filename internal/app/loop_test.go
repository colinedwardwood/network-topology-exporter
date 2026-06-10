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

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/events"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/loglimit"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
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

// enabledPublisher constructs an otlpPublisher in its enabled form (exp + sem +
// wg allocated together as a unit) for tests exercising the cap/drain behavior.
// The exp is a bare non-nil sentinel: push runs the supplied fn directly and
// never calls exp's methods, so a zero-value Exporter is sufficient to pass the
// disabled-guard.
func enabledPublisher(m *metrics.Metrics, semCap int) *otlpPublisher {
	return &otlpPublisher{
		exp:    &otlp.Exporter{},
		sem:    make(chan struct{}, semCap),
		wg:     &sync.WaitGroup{},
		logger: slogDiscard(),
		m:      m,
	}
}

// TestOtlpPushDropsWhenSemFull drops the push (and increments the dropped
// counter) when the concurrency semaphore is already saturated.
func TestOtlpPushDropsWhenSemFull(t *testing.T) {
	m := metrics.New(false)
	p := enabledPublisher(m, 1)
	p.sem <- struct{}{} // saturate
	var ran atomic.Bool
	p.Push(func(context.Context) error {
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
	p := enabledPublisher(m, 1)
	var ran atomic.Bool
	p.Push(func(context.Context) error {
		ran.Store(true)
		return nil
	}, "push ok")
	p.Drain()
	if !ran.Load() {
		t.Error("push fn did not run")
	}
	// Semaphore must have drained so a follow-up push can proceed.
	select {
	case p.sem <- struct{}{}:
		<-p.sem
	default:
		t.Error("semaphore not released after push completed")
	}
}

// TestOtlpPushCountsError runs a failing push and waits for completion.
func TestOtlpPushCountsError(t *testing.T) {
	m := metrics.New(false)
	p := enabledPublisher(m, 1)
	p.Push(func(context.Context) error {
		return errors.New("boom")
	}, "push failed")
	p.Drain()
}

// TestOtlpPushNoOpWhenDisabled asserts a disabled publisher (exp == nil, all
// fields nil) never runs the fn, spawns no goroutine, and drains cleanly — the
// invariant that lets callers drop the nil-checks.
func TestOtlpPushNoOpWhenDisabled(t *testing.T) {
	var p otlpPublisher // zero value: exp/sem/wg all nil
	var ran atomic.Bool
	p.Push(func(context.Context) error {
		ran.Store(true)
		return nil
	}, "should not run")
	if ran.Load() {
		t.Error("push fn ran on a disabled publisher")
	}
	p.Drain() // must not panic on a nil wg
}

// publishTestLoopConfig builds a minimal LoopConfig for exercising publish in
// isolation: real metrics, a disabled (no-op) OTLP publisher, a real resolver,
// and no snapshot channel / spoke pusher. Ready is wired so the happy path can
// exercise the CAS without nil-deref.
func publishTestLoopConfig(t *testing.T, m *metrics.Metrics) (LoopConfig, *credentials.Resolver) {
	t.Helper()
	cfg := testConfig(t, "TEST_COMM")
	cfg.Output.OTLP.HeartbeatCycles = 1
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	var ready atomic.Bool
	return LoopConfig{
		Logger: slogDiscard(),
		Cfg:    cfg,
		M:      m,
		Ready:  &ready,
		Otlp:   &otlpPublisher{}, // disabled: Push is a no-op
	}, resolver
}

func edge(src, dst string) discovery.Edge {
	return discovery.Edge{
		SrcDevice: src, SrcPort: "p1", DstDevice: dst, DstPort: "p2",
		DiscoveryProto: discovery.DiscoveryProtocolLLDP,
	}
}

// TestPublishAdvancesAgesAndDiffsAgainstOldPrev pins condition C8: ages always
// advance to newAges on BOTH the size-budget-reject path and the happy path,
// and the diff is computed against the OLD prevGraph passed in (so a second
// publish against the returned prevGraph sees no further change).
func TestPublishAdvancesAgesAndDiffsAgainstOldPrev(t *testing.T) {
	ctx := context.Background()
	keyA := graph.EdgeKey{}
	const lldp = string(discovery.DiscoveryProtocolLLDP)

	t.Run("size-reject advances ages and publishes nothing", func(t *testing.T) {
		m := metrics.New(false)
		lc, resolver := publishTestLoopConfig(t, m)
		lc.Cfg.Discovery.MaxGraphEdges = 1
		ev := events.New(slogDiscard())

		prev := discovery.Graph{Edges: []discovery.Edge{edge("a", "b")}}
		newG := discovery.Graph{Edges: []discovery.Edge{edge("a", "b"), edge("c", "d")}} // 2 > budget 1
		newAges := map[graph.EdgeKey]int{keyA: 7}

		nextPrev, nextAges, _ := lc.publish(ctx, ctx, ev, resolver, nil, nil,
			prev, newG, newAges, nil, 1, time.Now(), publishState{})

		if nextAges[keyA] != 7 {
			t.Fatalf("ages not advanced on reject path: got %v", nextAges)
		}
		// prevGraph must be unchanged (the rejected newG is not published).
		if len(nextPrev.Edges) != 1 {
			t.Fatalf("prevGraph changed on reject path: got %d edges, want 1", len(nextPrev.Edges))
		}
		if got := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(string(metrics.RejectReasonSizeBudgetExceeded))); got != 1 {
			t.Fatalf("reject metric = %v, want 1", got)
		}
		// Nothing published: no topology-change events recorded.
		if got := testutil.ToFloat64(m.TopologyChangeTotal.WithLabelValues("added", lldp)); got != 0 {
			t.Fatalf("change metric = %v on reject path, want 0", got)
		}
	})

	t.Run("happy path advances ages, diffs old prev, reassigns prev", func(t *testing.T) {
		m := metrics.New(false)
		lc, resolver := publishTestLoopConfig(t, m)
		ev := events.New(slogDiscard())

		prev := discovery.Graph{Edges: []discovery.Edge{edge("a", "b")}}
		newG := discovery.Graph{Edges: []discovery.Edge{edge("a", "b"), edge("c", "d")}}
		newAges := map[graph.EdgeKey]int{keyA: 7}

		nextPrev, nextAges, _ := lc.publish(ctx, ctx, ev, resolver, nil, nil,
			prev, newG, newAges, nil, 1, time.Now(), publishState{})

		if nextAges[keyA] != 7 {
			t.Fatalf("ages not advanced on happy path: got %v", nextAges)
		}
		if len(nextPrev.Edges) != 2 {
			t.Fatalf("prevGraph not reassigned to newGraph: got %d edges, want 2", len(nextPrev.Edges))
		}
		firstChanges := testutil.ToFloat64(m.TopologyChangeTotal.WithLabelValues("added", lldp))
		if firstChanges == 0 {
			t.Fatalf("expected an 'added' change vs old prevGraph, got 0")
		}
		if got := testutil.ToFloat64(m.GraphStale); got != 0 {
			t.Fatalf("GraphStale = %v, want 0 after happy publish", got)
		}

		// Second publish against the returned prevGraph with an identical
		// newGraph must produce NO further 'added' change — proving the diff
		// ran against the OLD prevGraph, and the caller-owned reassignment took.
		_, _, _ = lc.publish(ctx, ctx, ev, resolver, nil, nil,
			nextPrev, newG, newAges, nil, 2, time.Now(), publishState{})
		if got := testutil.ToFloat64(m.TopologyChangeTotal.WithLabelValues("added", lldp)); got != firstChanges {
			t.Fatalf("second publish recorded extra changes: got %v, want %v", got, firstChanges)
		}
	})
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
		// Otlp is always non-nil in production wiring; a disabled (no-op)
		// publisher makes Push a no-op so the loop never nil-derefs.
		Otlp: &otlpPublisher{},
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
