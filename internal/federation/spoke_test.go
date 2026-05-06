package federation

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

func init() {
	// Speed up retry backoff for all spoke tests in this package.
	spokePushBaseBackoff = time.Millisecond
}

func newTestSpokeFor(t *testing.T, hubURL string) *Spoke {
	t.Helper()
	return &Spoke{
		cfg: config.FederationConfig{
			Spoke: config.FederationSpokeConfig{HubURL: hubURL},
		},
		client: &http.Client{Timeout: 5 * time.Second},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		m:      metrics.New(),
	}
}

func TestSpokePushSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/spoke/push" {
			t.Errorf("unexpected path %q, want /spoke/push", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := newTestSpokeFor(t, srv.URL)
	err := s.Push(context.Background(), SpokePayload{
		SpokeID: "dc-test",
		CycleAt: time.Now(),
		Devices: []discovery.Device{{ID: "sw-1"}},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}
}

func TestSpokePushRetryExhaustionIncrementsCounter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := newTestSpokeFor(t, srv.URL)
	err := s.Push(context.Background(), SpokePayload{SpokeID: "dc-test", CycleAt: time.Now()})
	if err == nil {
		t.Fatal("Push returned nil, want error after retry exhaustion")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server received %d requests, want 3 (maxAttempts)", got)
	}
	// The failure counter must be incremented exactly once after all retries fail.
	if got := testutil.ToFloat64(s.m.FederationSpokePushFailuresTotal); got != 1 {
		t.Errorf("FederationSpokePushFailuresTotal = %v, want 1", got)
	}
}

func TestSpokePushContextCancelledBeforeFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be reached when context is already cancelled")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Push is called

	s := newTestSpokeFor(t, srv.URL)
	err := s.Push(ctx, SpokePayload{SpokeID: "dc-test", CycleAt: time.Now()})
	if err == nil {
		t.Fatal("Push returned nil, want context error")
	}
}

// TestSpokePostInvalidURL verifies that post returns an error when the hub URL
// cannot be used to construct a valid HTTP request.
func TestSpokePostInvalidURL(t *testing.T) {
	s := &Spoke{
		cfg: config.FederationConfig{
			Spoke: config.FederationSpokeConfig{HubURL: "://invalid-url"},
		},
		client: &http.Client{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		m:      metrics.New(),
	}
	err := s.post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

// TestNewSpokeErrorOnMissingCAFile verifies that NewSpoke returns an error
// when the CA certificate file does not exist.
func TestNewSpokeErrorOnMissingCAFile(t *testing.T) {
	cfg := config.FederationConfig{
		Spoke: config.FederationSpokeConfig{
			TLSCACert: "/nonexistent/ca.crt",
			TLSCert:   "/nonexistent/client.crt",
			TLSKey:    "/nonexistent/client.key",
		},
	}
	_, err := NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
	if err == nil {
		t.Fatal("NewSpoke: expected error for missing CA file, got nil")
	}
}

// TestNewSpokeErrorOnEmptyCAFile verifies that NewSpoke returns an error when
// the CA certificate file exists but contains no valid PEM blocks.
func TestNewSpokeErrorOnEmptyCAFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a pem certificate"), 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	cfg := config.FederationConfig{
		Spoke: config.FederationSpokeConfig{
			TLSCACert: caPath,
			TLSCert:   filepath.Join(dir, "client.crt"),
			TLSKey:    filepath.Join(dir, "client.key"),
		},
	}
	_, err := NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
	if err == nil {
		t.Fatal("NewSpoke: expected error for non-PEM CA file, got nil")
	}
}

func TestSpokePushContextCancelledDuringRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	s := newTestSpokeFor(t, srv.URL)

	// Cancel the context just after the first failed attempt so the backoff
	// select catches ctx.Done() instead of time.After.
	go func() {
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	err := s.Push(ctx, SpokePayload{SpokeID: "dc-test", CycleAt: time.Now()})
	if err == nil {
		t.Fatal("Push returned nil, want error from context cancellation")
	}
}
