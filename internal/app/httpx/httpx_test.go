package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestHealthzHandlerNilStatus(t *testing.T) {
	var status atomic.Pointer[CycleStatus]
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", NewHealthzHandler(&status))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if m["status"] != "ok" {
		t.Errorf("status field = %q, want ok", m["status"])
	}
}

func TestHealthzHandlerPopulatedStatus(t *testing.T) {
	var status atomic.Pointer[CycleStatus]
	now := time.Now()
	status.Store(&CycleStatus{LastCycleAt: now, DeviceErrors: 2})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", NewHealthzHandler(&status))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if m["device_errors"] != float64(2) {
		t.Errorf("device_errors = %v, want 2", m["device_errors"])
	}
}

// TestReadyzHandlerNotReady verifies that /readyz returns 503 before the
// first cycle or spoke push has completed.
func TestReadyzHandlerNotReady(t *testing.T) {
	handler := NewReadyzHandler(func() bool { return false })
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when not ready", rec.Code)
	}
}

// TestReadyzHandlerReady verifies that /readyz returns 200 once the readiness
// function returns true.
func TestReadyzHandlerReady(t *testing.T) {
	handler := NewReadyzHandler(func() bool { return true })
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when ready", rec.Code)
	}
}

// TestInstrumentMetricsHandlerRecordsScrape verifies the wrapper observes
// both render duration and payload size into the supplied histograms.
func TestInstrumentMetricsHandlerRecordsScrape(t *testing.T) {
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "dur"})
	payload := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "bytes"})

	innerBody := "# TYPE foo gauge\nfoo 42\n"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(innerBody))
	})

	wrapped := InstrumentMetricsHandler(inner, duration, payload)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Body.String() != innerBody {
		t.Fatalf("body = %q, want %q (wrapper must not buffer or modify the response)", rec.Body.String(), innerBody)
	}

	var dm dto.Metric
	if err := payload.Write(&dm); err != nil {
		t.Fatalf("payload.Write: %v", err)
	}
	if got, want := dm.GetHistogram().GetSampleCount(), uint64(1); got != want {
		t.Errorf("payload sample count = %d, want %d", got, want)
	}
	if got, want := dm.GetHistogram().GetSampleSum(), float64(len(innerBody)); got != want {
		t.Errorf("payload sum = %v, want %v (bytes of body)", got, want)
	}

	dm.Reset()
	if err := duration.Write(&dm); err != nil {
		t.Fatalf("duration.Write: %v", err)
	}
	if got, want := dm.GetHistogram().GetSampleCount(), uint64(1); got != want {
		t.Errorf("duration sample count = %d, want %d", got, want)
	}
}

// TestCountingResponseWriterStreamsLargePayload verifies that
// countingResponseWriter does NOT buffer the body — it streams every
// Write through to the underlying writer — and that bytesWritten equals
// the actual response body length for a non-trivial (>1MB) payload.
// Regression guard for issue #7: the wrapper's doc comment used to
// incorrectly claim it buffered the body.
func TestCountingResponseWriterStreamsLargePayload(t *testing.T) {
	// 1 MiB + a tail so the total clears 1 MB and exercises a payload
	// large enough that any silent buffering would be noticeable.
	const bodySize = (1 << 20) + 4096
	body := make([]byte, bodySize)
	for i := range body {
		body[i] = byte(i % 251) // non-trivial pattern, not all-zero
	}

	duration := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "dur_large"})
	payload := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "bytes_large"})

	// Inner handler writes the payload in multiple chunks to exercise
	// repeated Write() calls through the wrapper.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		const chunk = 64 * 1024
		for off := 0; off < len(body); off += chunk {
			end := off + chunk
			if end > len(body) {
				end = len(body)
			}
			if _, err := w.Write(body[off:end]); err != nil {
				t.Errorf("inner Write: %v", err)
				return
			}
		}
	})

	wrapped := InstrumentMetricsHandler(inner, duration, payload)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if got := rec.Body.Len(); got != bodySize {
		t.Fatalf("response body length = %d, want %d (wrapper must stream, not buffer/alter)", got, bodySize)
	}
	if !bytesEqual(rec.Body.Bytes(), body) {
		t.Fatalf("response body bytes diverge from inner handler output")
	}

	var dm dto.Metric
	if err := payload.Write(&dm); err != nil {
		t.Fatalf("payload.Write: %v", err)
	}
	if got, want := dm.GetHistogram().GetSampleSum(), float64(bodySize); got != want {
		t.Errorf("payload sum = %v, want %v (bytesWritten must match actual response body length)", got, want)
	}
}

// TestCountingResponseWriterHijackPanics verifies that Hijack panics
// loudly rather than silently bypassing the byte counter via interface
// promotion through the embedded http.ResponseWriter (issue #7).
func TestCountingResponseWriterHijackPanics(t *testing.T) {
	c := &countingResponseWriter{ResponseWriter: httptest.NewRecorder()}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Hijack did not panic; expected loud failure to prevent silent counter divergence")
		}
	}()
	_, _, _ = c.Hijack()
}

// TestCountingResponseWriterPushPanics verifies that Push panics
// loudly rather than silently bypassing the byte counter via interface
// promotion through the embedded http.ResponseWriter (issue #7).
func TestCountingResponseWriterPushPanics(t *testing.T) {
	c := &countingResponseWriter{ResponseWriter: httptest.NewRecorder()}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Push did not panic; expected loud failure to prevent silent counter divergence")
		}
	}()
	_ = c.Push("/anything", nil)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
