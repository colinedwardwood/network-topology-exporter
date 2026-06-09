package app

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/colinedwardwood/network-topology-exporter/internal/federation"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// SpokePushDrainTimeout bounds the single final push attempt at shutdown so a
// wedged hub cannot hold up process termination. Well under the typical 30s
// Kubernetes terminationGracePeriodSeconds.
const SpokePushDrainTimeout = 5 * time.Second

// spokePushFunc performs one push of a payload. Production wires
// (*federation.Spoke).Push; tests inject a controllable fake.
type spokePushFunc func(context.Context, federation.SpokePayload) error

// queuedPush is the mailbox cell: the payload plus the trace context captured
// from the discovery cycle, so the asynchronous push still propagates the
// cycle's W3C traceparent to the hub (issue #68) even though the cycle span has
// already closed by the time the push runs.
type queuedPush struct {
	payload federation.SpokePayload
	span    trace.SpanContext
}

// spokePusher decouples the spoke→hub push from the discovery cycle (#6). It
// keeps a latest-only mailbox (capacity-1, overwrite-on-full): a newer payload
// supersedes an un-consumed older one, so the cycle never blocks and a stale
// intermediate payload is never sent.
type spokePusher struct {
	push    spokePushFunc
	m       *metrics.Metrics
	logger  *slog.Logger
	ch      chan queuedPush
	drain   time.Duration
	stopped chan struct{}
}

func newSpokePusher(push spokePushFunc, m *metrics.Metrics, logger *slog.Logger) *spokePusher {
	return &spokePusher{
		push:    push,
		m:       m,
		logger:  logger,
		ch:      make(chan queuedPush, 1),
		drain:   SpokePushDrainTimeout,
		stopped: make(chan struct{}),
	}
}

// Enqueue places payload in the mailbox without blocking. If the slot already
// holds an un-consumed payload, that older one is dropped (superseded) and the
// newer one takes its place. The cycle's trace context is captured for #68.
func (p *spokePusher) Enqueue(ctx context.Context, pl federation.SpokePayload) {
	q := queuedPush{payload: pl, span: trace.SpanContextFromContext(ctx)}
	select {
	case p.ch <- q:
	default:
		select {
		case <-p.ch:
			p.m.FederationSpokePushDropsTotal.WithLabelValues("superseded").Inc()
		default:
		}
		select {
		case p.ch <- q:
		default:
			// Pusher consumed the slot between the drain and the re-insert; the
			// in-flight push will be followed by the next cycle's payload. Benign.
		}
	}
	p.m.FederationSpokePushQueueDepth.Set(float64(len(p.ch)))
}
