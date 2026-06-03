package otlp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// TestClassifyPushError_HTTP4xx confirms an SDK-style 4xx transport error
// classifies as PushReasonHTTP4xx. The OpenTelemetry HTTP exporter formats
// non-retryable failures as "failed to send ... <status> ...", embedding the
// HTTP status text (e.g. "400 Bad Request").
func TestClassifyPushError_HTTP4xx(t *testing.T) {
	err := fmt.Errorf("failed to send metrics to http://collector:4318/v1/metrics: 400 Bad Request (body: bad)")
	if got := ClassifyPushError(err); got != metrics.PushReasonHTTP4xx {
		t.Errorf("ClassifyPushError(4xx) = %q, want %q", got, metrics.PushReasonHTTP4xx)
	}
}

// TestClassifyPushError_HTTP5xx confirms an SDK-style 5xx transport error
// classifies as PushReasonHTTP5xx.
func TestClassifyPushError_HTTP5xx(t *testing.T) {
	err := fmt.Errorf("failed to send metrics to http://collector:4318/v1/metrics: 500 Internal Server Error (body: boom)")
	if got := ClassifyPushError(err); got != metrics.PushReasonHTTP5xx {
		t.Errorf("ClassifyPushError(5xx) = %q, want %q", got, metrics.PushReasonHTTP5xx)
	}
}

// TestClassifyPushError_Timeout confirms that the two timeout shapes the SDK
// surfaces — a wrapped context.DeadlineExceeded and a transport "i/o timeout"
// string — both classify as PushReasonTimeout.
func TestClassifyPushError_Timeout(t *testing.T) {
	cases := []error{
		fmt.Errorf("otlp: flush metrics: %w", context.DeadlineExceeded),
		errors.New(`Post "http://collector:4318/v1/metrics": context deadline exceeded`),
		errors.New(`dial tcp 10.0.0.1:4318: i/o timeout`),
	}
	for _, err := range cases {
		if got := ClassifyPushError(err); got != metrics.PushReasonTimeout {
			t.Errorf("ClassifyPushError(%q) = %q, want %q", err, got, metrics.PushReasonTimeout)
		}
	}
}

// TestClassifyPushError_Network confirms a bare connection-refused failure from
// a real PushGraph classifies to a valid non-TLS, non-HTTP reason.
func TestClassifyPushError_Network(t *testing.T) {
	exp, err := New(context.Background(), Config{
		Endpoint: "http://127.0.0.1:1", // reserved-low port, not listening
		Timeout:  500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = exp.Shutdown(context.Background()) }()

	pushErr := exp.PushGraph(context.Background(), discovery.Graph{Devices: []discovery.Device{{ID: "d"}}})
	if pushErr == nil {
		t.Fatal("PushGraph: want connection-refused error, got nil")
	}
	got := ClassifyPushError(pushErr)
	if got != metrics.PushReasonNetwork && got != metrics.PushReasonTimeout {
		t.Errorf("ClassifyPushError(refused) = %q, want network or timeout", got)
	}
	if !got.Valid() {
		t.Errorf("ClassifyPushError returned non-enum value %q", got)
	}
}

// TestClassifyPushError_TLS confirms TLS-related error strings route to
// PushReasonTLSError.
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

// TestClassifyPushError_NilPanics confirms the documented contract.
func TestClassifyPushError_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ClassifyPushError(nil): want panic, got none")
		}
	}()
	_ = ClassifyPushError(nil)
}

// TestClassifyPushError_AllReturnsAreValidEnum walks the public surface — every
// classification path must yield a PushReason that satisfies Valid(), pinning
// the cardinality-budget invariant from issue #20.
func TestClassifyPushError_AllReturnsAreValidEnum(t *testing.T) {
	cases := []error{
		context.DeadlineExceeded,
		fmt.Errorf("wrap: %w", context.DeadlineExceeded),
		errors.New("tls: handshake failed"),
		errors.New("x509: bad cert"),
		errors.New("failed to send metrics to http://c/v1/metrics: 400 Bad Request"),
		errors.New("failed to send logs to http://c/v1/logs: 404 Not Found"),
		errors.New("failed to send metrics to http://c/v1/metrics: 500 Internal Server Error"),
		errors.New("retry-able request failure: 503 Service Unavailable"),
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
