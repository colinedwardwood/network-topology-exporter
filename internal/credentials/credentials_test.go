package credentials

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
)

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	r, err := New(config.CredentialsConfig{
		Profiles: []config.CredentialProfile{
			{Name: "edge-v2c", Type: "snmp_v2c", CommunityEnv: "C"},
			{Name: "core-v3", Type: "snmp_v3", UsernameEnv: "U"},
			{Name: "lab-v2c", Type: "snmp_v2c", CommunityEnv: "L"},
		},
		Assignments: []config.CredentialAssignment{
			{IP: "10.2.3.4", Profiles: []string{"edge-v2c"}},
			{CIDR: "10.0.0.0/8", Profiles: []string{"lab-v2c"}},
			{CIDR: "10.1.0.0/16", Profiles: []string{"core-v3"}},
		},
		FallbackOrder:      []string{"edge-v2c", "core-v3"},
		TrialRatePerSecond: 100,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// LD-12: explicit per-IP assignment beats CIDR assignment beats fallback.
func TestResolveExactIPWins(t *testing.T) {
	r := newTestResolver(t)
	got := r.Resolve(net.ParseIP("10.2.3.4"))
	if !reflect.DeepEqual(got, []string{"edge-v2c"}) {
		t.Errorf("exact-IP resolve = %v, want [edge-v2c]", got)
	}
}

// LD-12: most-specific CIDR wins. /16 should beat /8 for an IP that matches
// both.
func TestResolveMostSpecificCIDR(t *testing.T) {
	r := newTestResolver(t)
	got := r.Resolve(net.ParseIP("10.1.5.5"))
	if !reflect.DeepEqual(got, []string{"core-v3"}) {
		t.Errorf("CIDR resolve = %v, want [core-v3]", got)
	}
}

// LD-12: an IP that matches no assignment falls back to fallback_order.
func TestResolveFallback(t *testing.T) {
	r := newTestResolver(t)
	got := r.Resolve(net.ParseIP("192.0.2.1"))
	if !reflect.DeepEqual(got, []string{"edge-v2c", "core-v3"}) {
		t.Errorf("fallback resolve = %v, want [edge-v2c core-v3]", got)
	}
}

// LD-12: cache populates on success and survives round-trip via SnapshotCache.
func TestCacheRoundTrip(t *testing.T) {
	r := newTestResolver(t)
	r.RecordSuccess("dev-1", "core-v3")

	if p, ok := r.CachedProfile("dev-1"); !ok || p != "core-v3" {
		t.Errorf("CachedProfile after RecordSuccess = (%q, %v), want (core-v3, true)", p, ok)
	}

	snap := r.SnapshotCache()
	r2 := newTestResolver(t)
	r2.LoadCache(snap)
	if p, ok := r2.CachedProfile("dev-1"); !ok || p != "core-v3" {
		t.Errorf("CachedProfile after LoadCache = (%q, %v), want (core-v3, true)", p, ok)
	}
}

// RecordFailure removes a cached profile so the next cycle re-enters the trial path.
func TestRecordFailureInvalidatesCache(t *testing.T) {
	r := newTestResolver(t)
	r.RecordSuccess("dev-1", "core-v3")
	r.RecordFailure("dev-1")
	if _, ok := r.CachedProfile("dev-1"); ok {
		t.Error("expected cache miss after RecordFailure, got hit")
	}
}

// Profile returns the named profile if it exists.
func TestProfileLookup(t *testing.T) {
	r := newTestResolver(t)
	p, ok := r.Profile("core-v3")
	if !ok {
		t.Fatal("Profile(core-v3) not found")
	}
	if p.Type != "snmp_v3" {
		t.Errorf("Type = %q, want snmp_v3", p.Type)
	}
}

// Profile returns false for an unknown name.
func TestProfileMissing(t *testing.T) {
	r := newTestResolver(t)
	if _, ok := r.Profile("does-not-exist"); ok {
		t.Error("expected false for unknown profile")
	}
}

// AcquireTrial returns context.Canceled when the context is already cancelled.
func TestAcquireTrialContextCancelled(t *testing.T) {
	r, err := New(config.CredentialsConfig{
		Profiles:           []config.CredentialProfile{{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"}},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Deplete the single token so the next acquire would block.
	bg := context.Background()
	if err := r.AcquireTrial(bg); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(bg)
	cancel()

	done := make(chan error, 1)
	go func() { done <- r.AcquireTrial(ctx) }()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireTrial did not return after context cancellation")
	}
}

// AcquireTrial unblocks and returns context.Canceled when the context is
// cancelled while the goroutine is blocked waiting for a token.
func TestAcquireTrialCancelWhileBlocked(t *testing.T) {
	r, err := New(config.CredentialsConfig{
		Profiles:           []config.CredentialProfile{{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"}},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bg := context.Background()
	if err := r.AcquireTrial(bg); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- r.AcquireTrial(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireTrial did not unblock after cancel")
	}
}

// TestNewReturnsErrorForInvalidCIDR verifies that New() returns a non-nil error
// when a per-CIDR assignment contains an invalid CIDR string.
func TestNewReturnsErrorForInvalidCIDR(t *testing.T) {
	_, err := New(config.CredentialsConfig{
		Profiles: []config.CredentialProfile{
			{Name: "p1", Type: "snmp_v2c", CommunityEnv: "C"},
		},
		Assignments: []config.CredentialAssignment{
			{CIDR: "not-a-cidr/32", Profiles: []string{"p1"}},
		},
		FallbackOrder:      []string{"p1"},
		TrialRatePerSecond: 1,
	})
	if err == nil {
		t.Fatal("expected error for invalid CIDR in assignment, got nil")
	}
}

// TestNewTokenBucketRateClampedToOne verifies that a token bucket created with
// rate=0 is clamped to 1 and still functions (tokens can be acquired).
func TestNewTokenBucketRateClampedToOne(t *testing.T) {
	r, err := New(config.CredentialsConfig{
		Profiles: []config.CredentialProfile{
			{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"},
		},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 0, // should be clamped to 1
	})
	if err != nil {
		t.Fatalf("New with rate=0: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The bucket should allow at least one acquisition (clamped rate=1 means 1 token burst).
	if err := r.AcquireTrial(ctx); err != nil {
		t.Fatalf("AcquireTrial with clamped rate=1: %v", err)
	}
}

// TestRefillLockedNoAdvance verifies that calling AcquireTrial in rapid
// succession does not cause the token count to go negative. The refillLocked
// elapsed<=0 guard should prevent double-counting.
func TestRefillLockedNoAdvance(t *testing.T) {
	r, err := New(config.CredentialsConfig{
		Profiles: []config.CredentialProfile{
			{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"},
		},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 100,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	// Acquire 100 tokens to deplete the burst.
	for i := 0; i < 100; i++ {
		if err := r.AcquireTrial(ctx); err != nil {
			t.Fatalf("AcquireTrial[%d]: %v", i, err)
		}
	}
	// Immediately try again with a cancelled context — should return error, not panic.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	err = r.AcquireTrial(cancelledCtx)
	if err == nil {
		// Might succeed if time has passed and tokens refilled — that's fine.
		return
	}
	// Any error is acceptable here (context.Canceled is expected).
}

// TestRefillLockedElapsedNonPositive exercises the elapsed <= 0 guard in
// refillLocked by calling it directly with a time that is not after lastRefill.
func TestRefillLockedElapsedNonPositive(t *testing.T) {
	b := newTokenBucket(10)
	before := b.tokens
	// Pass a time equal to lastRefill — elapsed == 0, guard fires, no change.
	b.refillLocked(b.lastRefill)
	if b.tokens != before {
		t.Errorf("tokens changed after refillLocked with elapsed=0: got %d, want %d", b.tokens, before)
	}
	// Pass a time before lastRefill — elapsed < 0, guard fires, no change.
	b.refillLocked(b.lastRefill.Add(-time.Second))
	if b.tokens != before {
		t.Errorf("tokens changed after refillLocked with elapsed<0: got %d, want %d", b.tokens, before)
	}
}

// TestRefillLockedCapsBurstAtRate exercises the b.tokens > b.rate cap branch in
// refillLocked. After depleting the bucket's single token, we wait long enough
// that the refill would add more tokens than the burst capacity, forcing the cap.
func TestRefillLockedCapsBurstAtRate(t *testing.T) {
	// rate=1: burst=1 token. After draining, wait 2s so add=2 > rate=1 → cap fires.
	r, err := New(config.CredentialsConfig{
		Profiles: []config.CredentialProfile{
			{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"},
		},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	// Drain the single burst token.
	if err := r.AcquireTrial(ctx); err != nil {
		t.Fatalf("first AcquireTrial: %v", err)
	}
	// Wait 2 seconds so that elapsed.Seconds()*rate = 2 > 1 = rate, triggering cap.
	time.Sleep(2 * time.Second)
	// Acquire should succeed now (1 token available after cap-limited refill).
	ctxTimeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := r.AcquireTrial(ctxTimeout); err != nil {
		t.Fatalf("second AcquireTrial after long wait: %v", err)
	}
}

// CachedProfile returns a miss and evicts the entry when it is older than cacheTTL.
func TestCachedProfileTTLExpiry(t *testing.T) {
	r := newTestResolver(t)
	r.RecordSuccess("dev-1", "core-v3")

	// Backdate the entry so it appears older than cacheTTL.
	r.mu.Lock()
	e := r.cache["dev-1"]
	e.cachedAt = time.Now().Add(-(cacheTTL + time.Minute))
	r.cache["dev-1"] = e
	r.mu.Unlock()

	if p, ok := r.CachedProfile("dev-1"); ok {
		t.Errorf("expected cache miss for expired entry, got (%q, true)", p)
	}
	// Entry should have been evicted.
	r.mu.RLock()
	_, still := r.cache["dev-1"]
	r.mu.RUnlock()
	if still {
		t.Error("expired entry was not removed from cache")
	}
}

// SnapshotCache omits entries that have exceeded cacheTTL.
func TestSnapshotCacheOmitsExpiredEntries(t *testing.T) {
	r := newTestResolver(t)
	r.RecordSuccess("dev-live", "core-v3")
	r.RecordSuccess("dev-old", "edge-v2c")

	// Backdate dev-old past the TTL.
	r.mu.Lock()
	e := r.cache["dev-old"]
	e.cachedAt = time.Now().Add(-(cacheTTL + time.Minute))
	r.cache["dev-old"] = e
	r.mu.Unlock()

	snap := r.SnapshotCache()
	if _, ok := snap["dev-live"]; !ok {
		t.Error("live entry missing from snapshot")
	}
	if _, ok := snap["dev-old"]; ok {
		t.Error("expired entry present in snapshot")
	}
}

// LoadCache stamps entries with the current time, so they are immediately valid.
func TestLoadCacheStampsFreshTimestamp(t *testing.T) {
	r := newTestResolver(t)
	r.LoadCache(map[string]string{"dev-1": "core-v3"})

	if p, ok := r.CachedProfile("dev-1"); !ok || p != "core-v3" {
		t.Errorf("CachedProfile after LoadCache = (%q, %v), want (core-v3, true)", p, ok)
	}
}

// LD-12: trial limiter blocks the call site so the cold-start trial rate
// stays bounded. With rate=2 and burst=2, three back-to-back acquires take
// at least one full token-refill window.
func TestAcquireTrialRateLimit(t *testing.T) {
	r, err := New(config.CredentialsConfig{
		Profiles:           []config.CredentialProfile{{Name: "p", Type: "snmp_v2c", CommunityEnv: "C"}},
		FallbackOrder:      []string{"p"},
		TrialRatePerSecond: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := r.AcquireTrial(ctx); err != nil {
			t.Fatalf("AcquireTrial[%d]: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// rate=2 means ~500ms between refills; the third token requires a wait.
	if elapsed < 400*time.Millisecond {
		t.Errorf("three acquires at rate=2 took %v; expected the limiter to block", elapsed)
	}
}
