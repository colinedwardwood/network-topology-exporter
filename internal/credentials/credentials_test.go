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
