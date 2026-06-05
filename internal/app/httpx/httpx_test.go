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
	mux.HandleFunc("/healthz", NewHealthzHandler(&status, 0, nil))

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
	mux.HandleFunc("/healthz", NewHealthzHandler(&status, 0, nil))

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

// TestHealthzHandlerLivenessGate drives the liveness staleness gate across its
// boundary cases with an injected clock: a fresh cycle stays 200, a cycle older
// than maxStale returns 503 with a stale body, a disabled gate (maxStale == 0)
// is always 200 even for an ancient cycle, and a nil status (no cycle yet)
// stays 200 regardless of the gate. Every case asserts the body parses as JSON.
func TestHealthzHandlerLivenessGate(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	const maxStale = 3 * time.Minute

	cases := []struct {
		name     string
		setLast  bool          // store a CycleStatus with LastCycleAt = base - age
		age      time.Duration // how stale the stored cycle is, relative to base
		maxStale time.Duration
		wantCode int
		wantStat string
	}{
		{name: "fresh cycle within window", setLast: true, age: 1 * time.Minute, maxStale: maxStale, wantCode: http.StatusOK, wantStat: "ok"},
		{name: "cycle exactly at threshold is not stale", setLast: true, age: maxStale, maxStale: maxStale, wantCode: http.StatusOK, wantStat: "ok"},
		{name: "cycle older than maxStale is stale", setLast: true, age: maxStale + time.Second, maxStale: maxStale, wantCode: http.StatusServiceUnavailable, wantStat: "stale"},
		{name: "disabled gate always ok even when ancient", setLast: true, age: 24 * time.Hour, maxStale: 0, wantCode: http.StatusOK, wantStat: "ok"},
		{name: "no cycle yet stays ok with gate enabled", setLast: false, maxStale: maxStale, wantCode: http.StatusOK, wantStat: "ok"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var status atomic.Pointer[CycleStatus]
			if tc.setLast {
				status.Store(&CycleStatus{LastCycleAt: base.Add(-tc.age), DeviceErrors: 1})
			}
			now := func() time.Time { return base }
			h := NewHealthzHandler(&status, tc.maxStale, now)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body.String())
			}
			var m map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				t.Fatalf("response is not valid JSON: %v", err)
			}
			if m["status"] != tc.wantStat {
				t.Errorf("status field = %q, want %q", m["status"], tc.wantStat)
			}
			if tc.wantCode == http.StatusServiceUnavailable {
				if m["stale_threshold_seconds"] != float64(int64(tc.maxStale.Seconds())) {
					t.Errorf("stale_threshold_seconds = %v, want %v", m["stale_threshold_seconds"], tc.maxStale.Seconds())
				}
			}
		})
	}
}

// TestIsStale covers the shared staleness predicate directly, including the
// disabled (non-positive threshold) short-circuit and the strict ">" boundary.
func TestIsStale(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		last      time.Time
		threshold time.Duration
		want      bool
	}{
		{"disabled threshold zero", now.Add(-time.Hour), 0, false},
		{"disabled threshold negative", now.Add(-time.Hour), -time.Minute, false},
		{"within window", now.Add(-time.Minute), 2 * time.Minute, false},
		{"exactly at threshold", now.Add(-2 * time.Minute), 2 * time.Minute, false},
		{"past threshold", now.Add(-3 * time.Minute), 2 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStale(now, tc.last, tc.threshold); got != tc.want {
				t.Errorf("IsStale = %v, want %v", got, tc.want)
			}
		})
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
