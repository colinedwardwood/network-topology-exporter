package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
)

// otlpPublisher owns the OTLP exporter plus the concurrency cap and in-flight
// tracking for async pushes. A publisher whose exp is nil is a no-op (OTLP
// disabled), so callers never nil-check. The enabled publisher allocates exp,
// sem, and wg together as a unit; the disabled publisher leaves all three nil
// and push early-returns before touching sem/wg.
type otlpPublisher struct {
	exp    *otlp.Exporter  // nil ⇒ disabled, push() is a no-op
	sem    chan struct{}   // semaphore bounding concurrent OTLP pushes
	wg     *sync.WaitGroup // tracks in-flight push goroutines for clean shutdown
	logger *slog.Logger
	m      *metrics.Metrics
}

// NoopOTLPPublisher returns a disabled (no-op) publisher: Push is a no-op and
// Drain returns immediately. This is the publisher used when OTLP output is
// off, so callers always hold a non-nil LoopConfig.Otlp and never nil-check.
func NoopOTLPPublisher() *otlpPublisher { //nolint:revive // unexported-return: intentional, the type is package-internal and callers store it in LoopConfig.Otlp
	return &otlpPublisher{}
}

// NewOTLPPublisher builds an enabled publisher: exp, sem, and wg are allocated
// together as a unit so push never has to nil-check sem/wg after the exp guard.
// cap bounds the concurrent in-flight pushes. The returned publisher is suitable
// for LoopConfig.Otlp; a disabled (no-op) publisher is the zero value
// &otlpPublisher{}.
func NewOTLPPublisher(exp *otlp.Exporter, cap int, logger *slog.Logger, m *metrics.Metrics) *otlpPublisher { //nolint:revive // unexported-return: intentional, the type is package-internal and callers store it in LoopConfig.Otlp
	return &otlpPublisher{
		exp:    exp,
		sem:    make(chan struct{}, cap),
		wg:     &sync.WaitGroup{},
		logger: logger,
		m:      m,
	}
}

// Enabled reports whether OTLP output is on (exp != nil). Callers use this only
// to skip work that is pointless when disabled — e.g. evaluating the heartbeat
// gate — and never to nil-check before Push (which is itself a no-op when off).
func (p *otlpPublisher) Enabled() bool { return p.exp != nil }

// Push enqueues fn under the configured concurrency cap and runs it
// asynchronously, tracked for shutdown drain, with panic recovery and #20 error
// classification. It drops the push (and increments the dropped counter) if the
// semaphore is full. No-op when the publisher is disabled (exp == nil).
func (p *otlpPublisher) Push(fn func(context.Context) error, warnMsg string) {
	if p.exp == nil {
		return
	}
	select {
	case p.sem <- struct{}{}:
	default:
		p.logger.Warn("otlp push dropped: concurrent limit reached")
		// status="dropped" never carries a failure reason — use the
		// shared n/a sentinel. Issue #20.
		p.m.OTLPPushTotal.WithLabelValues("dropped", metrics.ReasonNA).Inc()
		return
	}
	p.wg.Add(1)
	go func() { //nolint:gosec // G118: OTLP push must survive the originating cycle's context — the push is a side-effect of a completed cycle and should reach the collector even if the cycle's deadline already expired
		// Recover a panic in the push body so a bug in the OTLP exporter
		// cannot crash the process. Registered first so it runs LAST in the
		// defer chain — after the semaphore release and wg.Done below —
		// keeping the concurrency cap and shutdown drain correct on panic.
		// One-shot: the push goroutine exits on recovery.
		defer recoverGoroutine("otlp_push", p.logger, p.m)
		defer p.wg.Done()
		defer func() { <-p.sem }()
		pushCtx, cancel := context.WithTimeout(context.Background(), OTLPPushTimeout)
		defer cancel()
		if err := fn(pushCtx); err != nil {
			p.logger.Warn(warnMsg, "error", err)
			// Issue #20: partition status="error" by the OTLP sub-reason
			// derived from the error (timeout / tls_error / http_4xx /
			// http_5xx / network).
			p.m.OTLPPushTotal.WithLabelValues("error", string(otlp.ClassifyPushError(err))).Inc()
		} else {
			p.m.OTLPPushTotal.WithLabelValues("ok", metrics.ReasonNA).Inc()
		}
	}()
}

// Drain blocks until in-flight pushes finish (shutdown). No-op when disabled.
func (p *otlpPublisher) Drain() {
	if p.wg != nil {
		p.wg.Wait()
	}
}
