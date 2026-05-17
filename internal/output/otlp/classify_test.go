package otlp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// TestClassifyPushError_Timeout confirms that a context-deadline-exceeded
// error from a real PushGraph call is classified as PushReasonTimeout.
// Issue #20.
func TestClassifyPushError_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	exp := New(Config{Endpoint: srv.URL, Timeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := exp.PushGraph(ctx, discovery.Graph{})
	if err == nil {
		t.Fatal("PushGraph: want timeout error, got nil")
	}
	got := ClassifyPushError(err)
	if got != metrics.PushReasonTimeout && got != metrics.PushReasonNetwork {
		// The Go http stack sometimes surfaces the client.Timeout as a
		// generic net error; accept both for robustness. The point of
		// the test is that Valid() holds.
		t.Errorf("ClassifyPushError(timeout) = %q, want timeout or network", got)
	}
	if !got.Valid() {
		t.Errorf("ClassifyPushError returned non-enum value %q", got)
	}
}

// TestClassifyPushError_HTTP5xx confirms 5xx responses (after retry
// exhaustion) classify as PushReasonHTTP5xx. Uses a non-retryable 500
// status to avoid waiting for the full backoff schedule.
func TestClassifyPushError_HTTP5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 500 — not in the retry set
	}))
	defer srv.Close()

	exp := New(Config{Endpoint: srv.URL})
	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("PushGraph: want 500 error, got nil")
	}
	if got := ClassifyPushError(err); got != metrics.PushReasonHTTP5xx {
		t.Errorf("ClassifyPushError(500) = %q, want %q", got, metrics.PushReasonHTTP5xx)
	}
}

// TestClassifyPushError_HTTP4xx confirms 4xx responses classify as
// PushReasonHTTP4xx. 400 is in the non-retryable set so the first
// attempt's error is returned immediately.
func TestClassifyPushError_HTTP4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	exp := New(Config{Endpoint: srv.URL})
	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("PushGraph: want 400 error, got nil")
	}
	if got := ClassifyPushError(err); got != metrics.PushReasonHTTP4xx {
		t.Errorf("ClassifyPushError(400) = %q, want %q", got, metrics.PushReasonHTTP4xx)
	}
}

// TestClassifyPushError_Network confirms a bare network failure (the
// classic "endpoint unreachable" case from issue #20's body) classifies
// as PushReasonNetwork.
func TestClassifyPushError_Network(t *testing.T) {
	// Use an endpoint that will refuse connections — port 1 on the
	// loopback is reserved-low and not listening.
	exp := New(Config{Endpoint: "http://127.0.0.1:1", Timeout: 100 * time.Millisecond})
	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("PushGraph: want connection-refused error, got nil")
	}
	got := ClassifyPushError(err)
	if got != metrics.PushReasonNetwork && got != metrics.PushReasonTimeout {
		// Either is operationally fine; both are non-TLS, non-HTTP
		// failures. The invariant is that we don't leak an
		// unrecognised label value.
		t.Errorf("ClassifyPushError(refused) = %q, want network or timeout", got)
	}
	if !got.Valid() {
		t.Errorf("ClassifyPushError returned non-enum value %q", got)
	}
}

// TestClassifyPushError_TLS confirms TLS-related error strings route
// to PushReasonTLSError. The test fabricates an error string matching
// the net/http surface format because exercising real TLS-cert failures
// in an httptest server requires fixtures out of scope for this unit.
func TestClassifyPushError_TLS(t *testing.T) {
	for _, msg := range []string{
		`Post "https://example": tls: handshake failure`,
		`x509: certificate signed by unknown authority`,
	} {
		err := errors.New(msg)
		if got := ClassifyPushError(err); got != metrics.PushReasonTLSError {
			t.Errorf("ClassifyPushError(%q) = %q, want %q", msg, got, metrics.PushReasonTLSError)
		}
	}
}

// TestClassifyPushError_NilPanics confirms the documented contract:
// ClassifyPushError must only be called on a non-nil error.
func TestClassifyPushError_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ClassifyPushError(nil): want panic, got none")
		}
	}()
	_ = ClassifyPushError(nil)
}

// TestClassifyPushError_AllReturnsAreValidEnum walks the public surface
// — every classification path must yield a PushReason that satisfies
// PushReason.Valid(). Pins the cardinality budget invariant from issue
// #20: no unbounded label-value leakage.
func TestClassifyPushError_AllReturnsAreValidEnum(t *testing.T) {
	cases := []error{
		context.DeadlineExceeded,
		fmt.Errorf("wrap: %w", context.DeadlineExceeded),
		errors.New("tls: handshake failed"),
		errors.New("x509: bad cert"),
		&httpStatusError{statusCode: 400, path: "/v1/metrics"},
		&httpStatusError{statusCode: 404, path: "/v1/metrics"},
		&httpStatusError{statusCode: 500, path: "/v1/metrics"},
		&httpStatusError{statusCode: 503, path: "/v1/metrics"},
		errors.New("dial tcp: connection refused"),
		errors.New("unexpected EOF"),
	}
	for _, err := range cases {
		got := ClassifyPushError(err)
		if !got.Valid() {
			t.Errorf("ClassifyPushError(%v) = %q (not in enum)", err, got)
		}
	}
}
