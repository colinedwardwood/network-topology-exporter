// Package loglimit provides a per-key, cooldown-based suppressor for chronic
// Warn-level log emissions. It wraps (does not replace) *slog.Logger.
//
// # Motivation
//
// Several discovery sites emit a Warn on every cycle while a failure condition
// persists — vendor BGP walker drift, NFS-stalled snapshot writes, hub
// unreachable on spoke push, FDB community-string anomalies. With a 60s
// discovery interval that is up to 1440 identical lines per day per affected
// device. Operators page on the first occurrence; the next 1439 are noise that
// inflates log-storage cost and dilutes other signal. See issue #16.
//
// # Design
//
// Limiter is a thin facade over *slog.Logger with one operation, Warn. The
// caller supplies a stable per-emission-site key (typically derived from the
// failure dimensions: device + walker, device + path, etc.). For a given key,
// the first Warn within a cooldown window passes through to the inner logger;
// subsequent emits with that key are dropped until the cooldown elapses. Keys
// are independent — a new failure dimension always emits.
//
// Bookkeeping is bounded: maxKeys caps the map size. On overflow we evict the
// single least-recently-seen entry (O(n) over a small bounded n). This avoids
// pathological growth when callers derive keys from high-cardinality dimensions
// (e.g. error strings that include nonces) — the cooldown contract still
// holds for the in-use working set.
//
// # Invariants
//
//  1. First occurrence of a (key) is never suppressed.
//  2. Within cooldown, repeats of the same key are suppressed.
//  3. After cooldown expires, the next emit for that key passes through.
//  4. Concurrent emits for the same key resolve to exactly one passthrough
//     within the cooldown window (critical section covers both decision and
//     timestamp update).
//  5. Distinct keys are independent — emitting key A never affects key B.
//
// # Non-goals
//
//   - Sampling Error-level logs. Error is reserved for operator-actionable
//     conditions; rate-limiting Error would risk hiding genuine incidents.
//   - Dropping distinct-signal Warns. The caller chooses the key; any
//     information that should always emit must be encoded into the key.
//   - Async flushing. Emission is synchronous through the wrapped slog.Logger.
//     There is no goroutine, no channel, and no shutdown drain.
package loglimit

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultCooldown is the suppression window applied when callers do not
// specify a per-call override. One hour balances "operator gets the first
// alert per problem per hour" against "we still re-surface chronic problems
// daily so they cannot be forgotten".
const DefaultCooldown = time.Hour

// DefaultMaxKeys bounds the number of distinct keys tracked. When this
// limit is reached, the least-recently-seen entry is evicted on the next
// emit. Sized for tens of thousands of devices times a handful of distinct
// failure dimensions per device.
const DefaultMaxKeys = 4096

// entry tracks the last-emit time per key.
type entry struct {
	lastEmit time.Time
}

// Limiter wraps an *slog.Logger and suppresses repeated Warns sharing a key.
// The zero value is not usable; construct via New or NewWithClock.
//
// Limiter is safe for concurrent use. The hot-path critical section is a
// single map read + one timestamp compare + one map write; emission to the
// wrapped logger happens outside the lock so a slow slog handler cannot
// serialise discovery cycles.
type Limiter struct {
	inner    *slog.Logger
	cooldown time.Duration
	maxKeys  int
	now      func() time.Time

	mu   sync.Mutex
	seen map[string]entry
}

// New returns a Limiter that wraps inner and suppresses repeats of the same
// key within cooldown. If cooldown <= 0, DefaultCooldown is used. The
// returned Limiter uses time.Now as its clock; tests should use NewWithClock.
func New(inner *slog.Logger, cooldown time.Duration) *Limiter {
	return NewWithClock(inner, cooldown, time.Now)
}

// NewWithClock is New with an injectable clock for deterministic tests.
// now must not be nil.
func NewWithClock(inner *slog.Logger, cooldown time.Duration, now func() time.Time) *Limiter {
	if inner == nil {
		inner = slog.Default()
	}
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		inner:    inner,
		cooldown: cooldown,
		maxKeys:  DefaultMaxKeys,
		now:      now,
		seen:     make(map[string]entry),
	}
}

// Inner returns the wrapped logger for non-Warn emissions.
func (l *Limiter) Inner() *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l.inner
}

// Warn emits msg with attrs at Warn level on the wrapped logger if and only
// if no Warn for the given key has been emitted within the current cooldown
// window. Returns true if the Warn was emitted, false if suppressed.
//
// key is the suppression identity. Callers should construct it from the
// failure dimensions that distinguish one chronic problem from another —
// typically site-identifier + device + error-category. Two distinct error
// strings under the same key share one suppression slot; if both should
// surface independently, include the discriminator in the key.
//
// A nil Limiter behaves as a passthrough: Warn always emits via slog.Default.
// This keeps call sites uncluttered with nil checks during incremental
// migration.
func (l *Limiter) Warn(ctx context.Context, key, msg string, attrs ...any) bool {
	if l == nil {
		slog.Default().WarnContext(ctx, msg, attrs...)
		return true
	}
	return l.warnEvery(ctx, key, l.cooldown, msg, attrs...)
}

// WarnEvery is Warn with a per-call cooldown override. Useful for call sites
// where the chronic-cadence justifies a different window than the Limiter's
// default (e.g. faster re-surface for high-severity NFS stalls).
func (l *Limiter) WarnEvery(ctx context.Context, key string, cooldown time.Duration, msg string, attrs ...any) bool {
	if l == nil {
		slog.Default().WarnContext(ctx, msg, attrs...)
		return true
	}
	if cooldown <= 0 {
		cooldown = l.cooldown
	}
	return l.warnEvery(ctx, key, cooldown, msg, attrs...)
}

// warnEvery is the shared implementation behind Warn and WarnEvery. The
// emission decision and timestamp update happen inside the lock so two
// goroutines racing on the same key resolve to exactly one passthrough.
func (l *Limiter) warnEvery(ctx context.Context, key string, cooldown time.Duration, msg string, attrs ...any) bool {
	now := l.now()
	allowed := false

	l.mu.Lock()
	e, ok := l.seen[key]
	if !ok || now.Sub(e.lastEmit) >= cooldown {
		// Bound the map. Evict the single least-recently-seen entry on
		// overflow; the cooldown contract still holds for in-use keys
		// because eviction targets cold keys first.
		if !ok && len(l.seen) >= l.maxKeys {
			l.evictOldestLocked()
		}
		l.seen[key] = entry{lastEmit: now}
		allowed = true
	}
	l.mu.Unlock()

	if allowed {
		// Emit outside the lock so a slow handler does not block other
		// cycles emitting against different keys.
		l.inner.WarnContext(ctx, msg, attrs...)
	}
	return allowed
}

// evictOldestLocked removes the entry with the smallest lastEmit. Caller
// must hold l.mu. O(n) over l.seen, but only runs when the map is full,
// which is bounded by DefaultMaxKeys and (in practice) never reached in
// normal operation.
func (l *Limiter) evictOldestLocked() {
	var (
		oldestKey  string
		oldestTime time.Time
		first      = true
	)
	for k, v := range l.seen {
		if first || v.lastEmit.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.lastEmit
			first = false
		}
	}
	if !first {
		delete(l.seen, oldestKey)
	}
}

// Len reports the number of keys currently tracked. Exposed for tests and
// operator instrumentation; not part of the suppression decision path.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}
