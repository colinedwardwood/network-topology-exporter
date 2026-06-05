package app

import (
	"context"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// TestAuthConfigured reflects the flag the constructor was given.
func TestAuthConfigured(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)
	if rd := newTestRediscoverer(t, cfg, m, nil, true); !rd.AuthConfigured() {
		t.Error("AuthConfigured() = false, want true")
	}
	if rd := newTestRediscoverer(t, cfg, m, nil, false); rd.AuthConfigured() {
		t.Error("AuthConfigured() = true, want false")
	}
}

// TestRediscoverResultsBoxesTypedResults verifies the httpx adapter returns one
// boxed result per target, preserving the typed outcome.
func TestRediscoverResultsBoxesTypedResults(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)
	rd := newTestRediscoverer(t, cfg, m, []string{"10.0.0.0/24"}, true)

	out := rd.RediscoverResults(context.Background(), []string{"10.99.0.1", "not-an-ip"})
	if len(out) != 2 {
		t.Fatalf("results = %d, want 2", len(out))
	}
	r0, ok := out[0].(RediscoverResult)
	if !ok {
		t.Fatalf("result[0] type = %T, want RediscoverResult", out[0])
	}
	if r0.Outcome != RediscoverOutOfScope {
		t.Errorf("result[0] outcome = %q, want out_of_scope", r0.Outcome)
	}
	r1 := out[1].(RediscoverResult)
	if r1.Outcome != RediscoverError {
		t.Errorf("result[1] outcome = %q, want error", r1.Outcome)
	}
}

// TestRediscoverContextCancelledIsTimeout marks an in-scope target as timeout
// when the context is already cancelled before its walk begins.
func TestRediscoverContextCancelledIsTimeout(t *testing.T) {
	cfg := testConfig(t, "TEST_COMM")
	m := metrics.New(false)
	rd := newTestRediscoverer(t, cfg, m, []string{"10.0.0.0/24"}, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up front so the per-target select hits ctx.Done()
	results := rd.Rediscover(ctx, []string{"10.0.0.5"})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Outcome != RediscoverTimeout {
		t.Errorf("outcome = %q, want timeout", results[0].Outcome)
	}
}
