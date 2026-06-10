package app_test

// Tests relocated verbatim from cmd/topology-exporter/main_test.go (#171):
// they exercise exported internal/app behaviour, not main-level wiring,
// and belong with the package they test. External test package (app_test)
// so the `app.` call sites move unchanged.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/app"
	"github.com/colinedwardwood/network-topology-exporter/internal/app/httpx"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// TestNewLoggerLevels (renamed on move: package app already has a TestNewLogger) exercises all switch branches in newLogger.
func TestNewLoggerLevels(t *testing.T) {
	for _, level := range []string{"debug", "warn", "error", "info", "unknown"} {
		lg := app.NewLogger(level)
		if lg == nil {
			t.Errorf("app.NewLogger(%q) returned nil", level)
		}
	}
}

// ── runDiscoveryLoop additional coverage ──────────────────────────────────────

// TestRunDiscoveryLoopClearsGraphStale verifies that runDiscoveryLoop sets
// GraphStale=1 at startup, runs one cycle against a live SNMP agent, clears
// GraphStale to 0, and records a cycleStatus after the cycle completes.
func TestRunDiscoveryLoopClearsGraphStale(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-loop"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second, // long — second cycle never fires
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: func() {},
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	// Poll until GraphStale is cleared — set to 0 after the first successful cycle.
	deadline := time.After(12 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("GraphStale never cleared — first discovery cycle did not complete within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				cancel()
				<-done
				if s := status.Load(); s == nil {
					t.Error("cycleStatus was never set after first cycle")
				}
				if !ready.Load() {
					t.Error("ready flag was not set after first cycle")
				}
				return
			}
		}
	}
}

// TestRunDiscoveryLoopVersionMismatchSnapshot verifies that runDiscoveryLoop
// starts cleanly when the on-disk snapshot has an unrecognised version
// (ErrVersionMismatch cold-start path).
func TestRunDiscoveryLoopVersionMismatchSnapshot(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-mismatch"))
	_, port := snmptest.ParseAddr(addr)

	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	// Write a snapshot with an unrecognised version number.
	if err := os.WriteFile(snapPath, []byte(`{"version":9999,"written_at":"2020-01-01T00:00:00Z","devices":[],"edges":[]}`), 0600); err != nil {
		t.Fatalf("write bad snapshot: %v", err)
	}

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: snapPath},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: func() {},
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	deadline := time.After(12 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("version-mismatch snapshot: GraphStale never cleared within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				cancel()
				<-done
				return
			}
		}
	}
}

// TestRunDiscoveryLoopWithSnapshot verifies that runDiscoveryLoop correctly
// loads and restores a pre-existing snapshot on startup.
func TestRunDiscoveryLoopWithSnapshot(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-snap"))
	_, port := snmptest.ParseAddr(addr)

	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: snapPath},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	// Run one full cycle to produce a snapshot on disk.
	m1 := metrics.New(false)
	var s1 atomic.Pointer[httpx.CycleStatus]
	var r1 atomic.Bool
	ctx1, cancel1 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel1()
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		app.RunDiscoveryLoop(ctx1, app.LoopConfig{
			Cancel: cancel1,
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m1,
			Status: &s1,
			Ready:  &r1,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()
	// Wait for the snapshot write to complete (SnapshotLastWrittenUnix > 0),
	// not just GraphStale == 0. The write runs in a detached goroutine that may
	// still be in-flight when GraphStale first clears.
	deadline1 := time.After(12 * time.Second)
	poll1 := time.NewTicker(50 * time.Millisecond)
	defer poll1.Stop()
outer:
	for {
		select {
		case <-deadline1:
			t.Fatal("first runDiscoveryLoop: snapshot not written within deadline")
		case <-poll1.C:
			if testutil.ToFloat64(m1.SnapshotLastWrittenUnix) > 0 {
				cancel1()
				<-done1
				break outer
			}
		}
	}

	// Now start a second loop — it should load the snapshot produced above.
	m2 := metrics.New(false)
	var s2 atomic.Pointer[httpx.CycleStatus]
	var r2 atomic.Bool
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		app.RunDiscoveryLoop(ctx2, app.LoopConfig{
			Cancel: cancel2,
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m2,
			Status: &s2,
			Ready:  &r2,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	deadline2 := time.After(12 * time.Second)
	poll2 := time.NewTicker(50 * time.Millisecond)
	defer poll2.Stop()
	for {
		select {
		case <-deadline2:
			t.Fatal("second runDiscoveryLoop: GraphStale never cleared within deadline")
		case <-poll2.C:
			if testutil.ToFloat64(m2.GraphStale) == 0 {
				// SnapshotLoadedDevicesTotal should have been set from the loaded snapshot.
				if got := testutil.ToFloat64(m2.SnapshotLoadedDevicesTotal); got == 0 {
					t.Error("SnapshotLoadedDevicesTotal = 0 after snapshot load, want > 0")
				}
				cancel2()
				<-done2
				return
			}
		}
	}
}

// TestRunDiscoveryLoopSecondTick verifies that the ticker path in
// runDiscoveryLoop fires a second cycle when the interval is short enough.
func TestRunDiscoveryLoopSecondTick(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-tick"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 200 * time.Millisecond, // very short to hit tick.C
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: func() {},
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	// Wait for at least two cycles: first cycle clears GraphStale, second cycle
	// fires via tick.C. We poll for cycleStatus with a non-zero LastCycleAt.
	deadline := time.After(10 * time.Second)
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("second tick: never observed a second cycle within deadline")
		case <-poll.C:
			if testutil.ToFloat64(m.GraphStale) == 0 {
				// Let the tick fire the second cycle.
				time.Sleep(300 * time.Millisecond)
				cancel()
				<-done
				return
			}
		}
	}
}

// TestRunDiscoveryLoopContextCancelledDuringCycle exercises the
// `if ctx.Err() != nil { return }` guard inside the cycle closure. We cancel
// the context before calling runDiscoveryLoop so ctx.Err() is already set when
// the cycle checks.
func TestRunDiscoveryLoopContextCancelledDuringCycle(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")
	addr := snmptest.Start(t, "public", systemPDUs("sw-cancel"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              1,
			UnconfirmedLinkTTLCycles: 3,
			Scope:                    config.ScopeConfig{CIDRAllowList: []string{"127.0.0.0/8"}},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "default", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMMUNITY"},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
		Targets:  []config.TargetConfig{{Host: "127.0.0.1", Port: int(port)}},
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	// Cancel the context immediately — runDiscoveryLoop's first cycle will
	// complete (or start) with ctx.Err() != nil, triggering the early return.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: func() {},
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	select {
	case <-done:
		// runDiscoveryLoop returned — either the cycle saw ctx.Err() and
		// returned, or the ticker case fired ctx.Done(). Either way is correct.
	case <-time.After(5 * time.Second):
		t.Fatal("runDiscoveryLoop did not return within 5s after pre-cancelled context")
	}
}

// TestRunDiscoveryLoopCredResolverError verifies that runDiscoveryLoop calls its
// cancelFn and returns (rather than calling os.Exit) when credentials.New fails.
// The config is constructed directly — bypassing config.Load — to inject an
// invalid CIDR that config validation would normally reject.
func TestRunDiscoveryLoopCredResolverError(t *testing.T) {
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:         60 * time.Second,
			TimeoutPerDevice: 1 * time.Second,
			Parallelism:      1,
		},
		Credentials: config.CredentialsConfig{
			Assignments: []config.CredentialAssignment{
				{CIDR: "not-a-cidr", Profiles: []string{"p"}},
			},
		},
		Snapshot: config.SnapshotConfig{Path: filepath.Join(t.TempDir(), "snap.json")},
	}

	m := metrics.New(false)
	var status atomic.Pointer[httpx.CycleStatus]
	var ready atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelCalled := make(chan struct{})
	cancelFn := func() {
		close(cancelCalled)
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.RunDiscoveryLoop(ctx, app.LoopConfig{
			Cancel: cancelFn,
			Logger: slog.Default(),
			Cfg:    cfg,
			M:      m,
			Status: &status,
			Ready:  &ready,
			Otlp:   app.NoopOTLPPublisher(),
		})
	}()

	select {
	case <-cancelCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelFn was not called within 3s")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runDiscoveryLoop did not return after cancelFn was called")
	}
}

// ── runCycle additional coverage ──────────────────────────────────────────────
