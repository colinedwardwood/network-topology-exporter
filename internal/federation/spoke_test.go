package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	// Construct pushURL directly without going through buildSpokeURL so that
	// plain-HTTP httptest servers (which are valid in tests) are not rejected by
	// the HTTPS enforcement added in Fix 3. Tests that exercise buildSpokeURL
	// itself (TestBuildSpokeURL, TestBuildSpokeURLInvalidURL) call it directly.
	pushURL := strings.TrimRight(hubURL, "/") + "/spoke/push"
	return &Spoke{
		cfg: config.FederationConfig{
			Spoke: config.FederationSpokeConfig{HubURL: hubURL},
		},
		pushURL: pushURL,
		client:  &http.Client{Timeout: 5 * time.Second},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		m:       metrics.New(false),
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

// TestBuildSpokeURLInvalidURL verifies that buildSpokeURL rejects a malformed base URL.
func TestBuildSpokeURLInvalidURL(t *testing.T) {
	_, err := buildSpokeURL("://invalid-url")
	if err == nil {
		t.Fatal("buildSpokeURL: expected error for invalid URL, got nil")
	}
}

// TestBuildSpokeURLRequiresHTTPS verifies that buildSpokeURL rejects non-HTTPS URLs.
func TestBuildSpokeURLRequiresHTTPS(t *testing.T) {
	_, err := buildSpokeURL("http://hub:9101")
	if err == nil {
		t.Fatal("buildSpokeURL: expected error for http:// URL, got nil")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error = %q, want to contain 'HTTPS'", err.Error())
	}
}

func TestBuildSpokeURL(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{"https://hub:9101", "https://hub:9101/spoke/push"},
		{"https://hub:9101/", "https://hub:9101/spoke/push"},
		{"https://hub:9101/base", "https://hub:9101/base/spoke/push"},
	}
	for _, tc := range tests {
		got, err := buildSpokeURL(tc.base)
		if err != nil {
			t.Errorf("buildSpokeURL(%q): %v", tc.base, err)
			continue
		}
		if got != tc.want {
			t.Errorf("buildSpokeURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
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
	_, err := NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New(false))
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
	_, err := NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New(false))
	if err == nil {
		t.Fatal("NewSpoke: expected error for non-PEM CA file, got nil")
	}
}

// TestNewSpokeErrorOnBadKeyFile verifies that NewSpoke returns an error when the
// CA cert file is valid PEM but the cert/key pair cannot be loaded because the
// key file contains garbage (LoadX509KeyPair failure path).
func TestNewSpokeErrorOnBadKeyFile(t *testing.T) {
	dir := t.TempDir()

	// Write a minimal self-signed CA PEM so AppendCertsFromPEM succeeds.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write CA cert: %v", err)
	}

	// Write a valid cert file and a garbage key file.
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	caCertParsed, _ := x509.ParseCertificate(caDER)
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "spoke"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCertParsed, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	certPath := filepath.Join(dir, "client.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(keyPath, []byte("this is not a valid key"), 0o600); err != nil {
		t.Fatalf("write garbage key: %v", err)
	}

	cfg := config.FederationConfig{
		Spoke: config.FederationSpokeConfig{
			TLSCACert: caPath,
			TLSCert:   certPath,
			TLSKey:    keyPath,
		},
	}
	_, err = NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New(false))
	if err == nil {
		t.Fatal("NewSpoke: expected error for garbage key file, got nil")
	}
	if !strings.Contains(err.Error(), "load client cert/key") {
		t.Errorf("error = %q, want to contain 'load client cert/key'", err.Error())
	}
}

// TestSpokePushFatalOn4xx verifies that Push stops immediately (no retries) when
// the hub returns a 4xx status, and that 429 is treated as retryable.
func TestSpokePushFatalOn4xx(t *testing.T) {
	fatalCodes := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
	}
	for _, code := range fatalCodes {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			s := newTestSpokeFor(t, srv.URL)
			err := s.Push(context.Background(), SpokePayload{SpokeID: "dc-test", CycleAt: time.Now()})
			if err == nil {
				t.Fatalf("Push returned nil for HTTP %d, want error", code)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("HTTP %d: server received %d requests, want 1 (no retry)", code, got)
			}
			// Failure counter must be incremented on fatal errors too.
			if got := testutil.ToFloat64(s.m.FederationSpokePushFailuresTotal); got != 1 {
				t.Errorf("HTTP %d: FederationSpokePushFailuresTotal = %v, want 1", code, got)
			}
		})
	}

	// 429 Too Many Requests must be retried like a 5xx.
	t.Run("429 is retryable", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		s := newTestSpokeFor(t, srv.URL)
		_ = s.Push(context.Background(), SpokePayload{SpokeID: "dc-test", CycleAt: time.Now()})
		if got := calls.Load(); got != 3 {
			t.Errorf("429: server received %d requests, want 3 (maxAttempts)", got)
		}
	})
}

// TestSpokePostNetworkError verifies that post() returns an error when the HTTP
// client cannot connect (connection refused), covering the client.Do error branch.
func TestSpokePostNetworkError(t *testing.T) {
	// Use a closed server so the connection is immediately refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Close() // close immediately so any connection attempt fails

	s := newTestSpokeFor(t, srv.URL)
	err := s.post(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("post: expected network error for closed server, got nil")
	}
}

// TestNewSpokeSuccess verifies that NewSpoke returns a non-nil Spoke (and no
// error) when the CA cert file, client cert file, and key file are all valid.
func TestNewSpokeSuccess(t *testing.T) {
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("write CA cert: %v", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	caCertParsed, _ := x509.ParseCertificate(caDER)
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "spoke"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCertParsed, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}), 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER}), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	cfg := config.FederationConfig{
		Spoke: config.FederationSpokeConfig{
			TLSCACert: caPath,
			TLSCert:   certPath,
			TLSKey:    keyPath,
			HubURL:    "https://hub:9101",
		},
	}
	spoke, err := NewSpoke(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New(false))
	if err != nil {
		t.Fatalf("NewSpoke: unexpected error: %v", err)
	}
	if spoke == nil {
		t.Fatal("NewSpoke returned nil Spoke, want non-nil")
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
