package loglimit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for deterministic cooldown tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newCapturingLogger returns a JSON-handler slog.Logger that writes to a
// bytes.Buffer the caller can inspect. The buffer is goroutine-safe enough
// for our tests because slog's JSONHandler serialises writes through a
// sync.Mutex internally.
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	return slog.New(h), &buf
}

// countLines returns the number of newline-terminated lines in buf.
func countLines(buf *bytes.Buffer) int {
	s := buf.String()
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n")
}

// Invariant 1: first occurrence of a key always passes through.
func TestWarn_FirstEmissionPassesThrough(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	ok := l.Warn(context.Background(), "device-A|vendor_walk_error", "bgp vendor walk failed", "device", "A")
	if !ok {
		t.Fatalf("first Warn returned false; expected true")
	}
	if got := countLines(buf); got != 1 {
		t.Fatalf("expected 1 log line, got %d (buf=%q)", got, buf.String())
	}
}

// Invariant 2: within the cooldown window, repeats of the same key are
// suppressed.
func TestWarn_DuplicateWithinCooldownSuppressed(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	ctx := context.Background()
	_ = l.Warn(ctx, "k1", "msg")
	// Advance less than cooldown.
	clk.advance(30 * time.Minute)
	if ok := l.Warn(ctx, "k1", "msg again"); ok {
		t.Fatalf("expected suppression within cooldown, got passthrough")
	}
	if got := countLines(buf); got != 1 {
		t.Fatalf("expected 1 log line after suppressed repeat, got %d", got)
	}
}

// Invariant 3: after cooldown elapses, the next emit for that key passes
// through.
func TestWarn_AfterCooldownEmitsAgain(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	ctx := context.Background()
	_ = l.Warn(ctx, "k1", "first")
	clk.advance(time.Hour + time.Second) // cross the boundary
	if ok := l.Warn(ctx, "k1", "second"); !ok {
		t.Fatalf("expected passthrough after cooldown elapsed, got suppression")
	}
	if got := countLines(buf); got != 2 {
		t.Fatalf("expected 2 log lines, got %d (buf=%q)", got, buf.String())
	}
}

// Invariant 5: distinct keys are independent.
func TestWarn_DifferentKeysIndependent(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	ctx := context.Background()
	_ = l.Warn(ctx, "kA", "msg")
	_ = l.Warn(ctx, "kB", "msg")
	_ = l.Warn(ctx, "kC", "msg")
	if got := countLines(buf); got != 3 {
		t.Fatalf("expected 3 log lines for 3 distinct keys, got %d", got)
	}
}

// Invariant 4: concurrent same-key emits resolve to exactly one passthrough
// within the cooldown window.
func TestWarn_ConcurrentSameKeyEmitsOnce(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	const goroutines = 64
	var wg sync.WaitGroup
	var passthroughs atomic.Int32
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // align goroutines so they race the lock
			if l.Warn(context.Background(), "racekey", "msg") {
				passthroughs.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := passthroughs.Load(); got != 1 {
		t.Fatalf("expected exactly 1 passthrough under concurrent race, got %d", got)
	}
	if got := countLines(buf); got != 1 {
		t.Fatalf("expected exactly 1 log line emitted, got %d", got)
	}
}

// Map-growth bound: keys above maxKeys are bounded by eviction.
func TestWarn_BoundedByMaxKeys(t *testing.T) {
	inner, _ := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)
	l.maxKeys = 4 // shrink for test

	ctx := context.Background()
	for i := 0; i < 100; i++ {
		// Each iteration uses a unique key.
		_ = l.Warn(ctx, "k"+string(rune('A'+i%26))+string(rune('a'+i/26)), "msg")
		clk.advance(time.Second) // distinct lastEmit so eviction is deterministic
	}
	if got := l.Len(); got > l.maxKeys {
		t.Fatalf("Len()=%d exceeded maxKeys=%d", got, l.maxKeys)
	}
}

// WarnEvery: per-call cooldown override works.
func TestWarnEvery_PerCallCooldownOverride(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now) // default cooldown 1h

	ctx := context.Background()
	_ = l.WarnEvery(ctx, "k1", time.Minute, "first")
	clk.advance(90 * time.Second) // > 1m override, < 1h default
	if ok := l.WarnEvery(ctx, "k1", time.Minute, "second"); !ok {
		t.Fatalf("expected passthrough with 1m override after 90s, got suppression")
	}
	if got := countLines(buf); got != 2 {
		t.Fatalf("expected 2 log lines with per-call override, got %d", got)
	}
}

// Nil-Limiter passthrough: avoids nil-check clutter at call sites. The Warn
// goes through slog.Default(); we just verify it does not panic and reports
// true.
func TestWarn_NilLimiterPassesThrough(t *testing.T) {
	var l *Limiter // nil
	if !l.Warn(context.Background(), "k1", "msg") {
		t.Fatalf("nil Limiter Warn returned false; expected true (passthrough)")
	}
	if !l.WarnEvery(context.Background(), "k1", time.Minute, "msg") {
		t.Fatalf("nil Limiter WarnEvery returned false; expected true (passthrough)")
	}
	if got := l.Len(); got != 0 {
		t.Fatalf("nil Limiter Len()=%d; expected 0", got)
	}
	if l.Inner() == nil {
		t.Fatalf("nil Limiter Inner() returned nil; expected slog.Default()")
	}
}

// Default cooldown clamping: cooldown <= 0 falls back to DefaultCooldown.
func TestNew_DefaultCooldownClampsNonPositive(t *testing.T) {
	inner, _ := newCapturingLogger()
	l := New(inner, 0)
	if l.cooldown != DefaultCooldown {
		t.Fatalf("New(inner, 0).cooldown=%v; expected DefaultCooldown=%v", l.cooldown, DefaultCooldown)
	}
	l = New(inner, -time.Hour)
	if l.cooldown != DefaultCooldown {
		t.Fatalf("New(inner, -1h).cooldown=%v; expected DefaultCooldown=%v", l.cooldown, DefaultCooldown)
	}
}

// Boundary case: emission exactly at cooldown boundary passes through (>=
// not >).
func TestWarn_BoundaryEmitsAtExactCooldown(t *testing.T) {
	inner, buf := newCapturingLogger()
	clk := newFakeClock(time.Unix(0, 0))
	l := NewWithClock(inner, time.Hour, clk.now)

	ctx := context.Background()
	_ = l.Warn(ctx, "k1", "first")
	clk.advance(time.Hour) // exactly at boundary
	if ok := l.Warn(ctx, "k1", "second"); !ok {
		t.Fatalf("expected passthrough at exact cooldown boundary, got suppression")
	}
	if got := countLines(buf); got != 2 {
		t.Fatalf("expected 2 log lines, got %d", got)
	}
}
