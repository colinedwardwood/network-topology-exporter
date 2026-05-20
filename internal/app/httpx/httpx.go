// Package httpx provides HTTP handler factories and response-writer
// instrumentation used by cmd/topology-exporter.
//
// Handlers live here so that the entry point keeps argv parsing,
// signal handling, and lifecycle wiring while the per-endpoint
// concerns (readiness, health, metrics-scrape instrumentation) can be
// unit-tested in isolation against a small surface.
package httpx

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// CycleStatus is the most recent discovery cycle's outcome surfaced by
// /healthz. It is written by the discovery loop after each cycle and
// read by NewHealthzHandler under an atomic.Pointer so the read path
// never blocks.
type CycleStatus struct {
	LastCycleAt  time.Time
	DeviceErrors int64
}

// NewReadyzHandler returns an HTTP handler for /readyz. It returns 200
// once isReady() is true (first live cycle or first spoke push received)
// and 503 during startup so Kubernetes does not route traffic before the
// process has meaningful topology data to serve.
func NewReadyzHandler(isReady func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if isReady() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"starting"}` + "\n"))
	}
}

// NewHealthzHandler returns an HTTP handler for /healthz. It reports the
// timestamp of the most recent discovery cycle and that cycle's device
// error count. Until the first cycle completes (status pointer is nil)
// it reports `{"status":"ok","last_cycle_at":null,"device_errors":null}`
// so a fresh process is healthy from the orchestrator's perspective
// while readiness gating still keeps traffic away (see NewReadyzHandler).
func NewHealthzHandler(status *atomic.Pointer[CycleStatus]) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := status.Load()
		if s == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","last_cycle_at":null,"device_errors":null}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","last_cycle_at":%q,"device_errors":%d}`+"\n",
			s.LastCycleAt.UTC().Format(time.RFC3339), s.DeviceErrors)
	}
}

// InstrumentMetricsHandler wraps the Prometheus /metrics handler so that
// each scrape contributes one observation to the render-duration and
// payload-size histograms. Operators alert on the p99 of duration against
// the scraper's scrape_timeout — see docs/operator/scale.md. The wrapper
// streams the response body through to the underlying writer without
// buffering and counts the bytes that flow through Write().
func InstrumentMetricsHandler(inner http.Handler, duration, payload prometheus.Histogram) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &countingResponseWriter{ResponseWriter: w}
		inner.ServeHTTP(rec, r)
		duration.Observe(time.Since(start).Seconds())
		payload.Observe(float64(rec.bytesWritten))
	})
}

// countingResponseWriter wraps http.ResponseWriter and counts the bytes
// passed to Write(). It does NOT buffer the body — each Write call streams
// straight to the wrapped writer and the counter is incremented by the
// number of bytes the wrapped writer reports as written. The counter is
// therefore exact for response bodies emitted via Write().
//
// This wrapper deliberately does NOT promote http.Hijacker or http.Pusher.
// Embedding http.ResponseWriter would otherwise silently promote any
// interfaces the underlying writer implements; if a future inner handler
// or middleware invoked Hijack() (RFC 6455 WebSocket upgrade) or Push()
// (HTTP/2 server push), the connection would be detached or pushed without
// passing through Write(), and bytesWritten would diverge from reality
// without any indication. The /metrics path served by this wrapper does
// not use WebSocket upgrade or HTTP/2 server push, so a loud panic on
// those code paths is preferable to a silently wrong byte counter.
type countingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
}

func (c *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.bytesWritten += n
	return n, err
}

func (c *countingResponseWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack panics: the /metrics handler does not use WebSocket upgrade, and
// allowing Hijack to promote silently through the embedded ResponseWriter
// would let a future middleware detach the connection without passing
// through Write(), invalidating bytesWritten without any signal.
func (c *countingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	panic("countingResponseWriter: Hijack not supported — wrapper is for the /metrics path only; WebSocket upgrade would bypass the byte counter")
}

// Push panics: the /metrics handler does not use HTTP/2 server push.
// Allowing Push to promote silently would let the inner handler emit
// bytes that never pass through Write(), invalidating bytesWritten.
func (c *countingResponseWriter) Push(target string, opts *http.PushOptions) error {
	panic("countingResponseWriter: Push not supported — wrapper is for the /metrics path only; HTTP/2 server push would bypass the byte counter")
}
