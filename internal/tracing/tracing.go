// Package tracing implements opt-in OpenTelemetry tracing of the exporter's own
// discovery cycle (issue #68). It is an additive trace signal layered on the
// same OpenTelemetry Go SDK that the OTLP metrics+logs path uses (issue #82):
// the TracerProvider exports spans via OTLP over the protocol configured for
// output.otlp, reusing output.otlp.endpoint and the same transport. Tracing
// does not get its own endpoint.
//
// When tracing is disabled (the default, output.otlp.traces.enabled=false) the
// package installs no global TracerProvider, so otel.Tracer(...) returns the
// SDK's no-op tracer and every instrumentation call in the discovery loop is a
// cheap no-op. Instrumentation sites therefore never branch on an "is tracing
// on" flag — they unconditionally call tracer.Start / span.SetAttributes and
// rely on the no-op tracer to elide the work when disabled.
//
// Sampling is head-based: ParentBased(TraceIDRatioBased(sample_rate)). A child
// span (e.g. a target.poll under a sampled discovery.cycle) inherits the
// parent's sampling decision so a sampled cycle keeps all of its children; an
// unsampled cycle drops them too. This keeps a trace whole rather than
// half-sampling its spans.
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/colinedwardwood/network-topology-exporter/internal/otelx"
)

// ScopeName is the instrumentation scope reported on every span emitted by
// this exporter. It matches the metrics/logs scope (otelx.ScopeName) so a
// single instrumentation library identifies all three signals.
const ScopeName = otelx.ScopeName

// Config holds the settings for the trace exporter. Endpoint, Timeout,
// Protocol, and InstanceID are reused from the output.otlp block — tracing has
// no endpoint of its own.
type Config struct {
	// Endpoint is the base URL of the OTLP receiver (shared with metrics/logs).
	Endpoint string
	// Timeout caps each individual span export. Defaults to 10s when zero.
	Timeout time.Duration
	// Protocol selects the OTLP transport: otelx.ProtocolHTTP (default) or
	// otelx.ProtocolGRPC. Empty is treated as HTTP.
	Protocol otelx.Protocol
	// InstanceID is emitted as service.instance.id; falls back to os.Hostname()
	// when empty, mirroring the otlp metric/log exporter.
	InstanceID string
	// SampleRate is the head-sampling ratio in [0,1] for the root span. Child
	// spans inherit the parent decision (ParentBased).
	SampleRate float64
}

// Provider owns the OTel SDK TracerProvider for the discovery cycle. It is only
// constructed when output.otlp.traces.enabled is true; New installs it as the
// global TracerProvider and sets the W3C TraceContext propagator as the global
// propagator so spoke→hub HTTP pushes carry traceparent.
type Provider struct {
	tp *sdktrace.TracerProvider
}

// New constructs a Provider, wiring an OTLP trace exporter (over the configured
// protocol) into an SDK TracerProvider with head sampling
// ParentBased(TraceIDRatioBased(SampleRate)). It installs the provider as the
// global TracerProvider and sets the global propagator to W3C TraceContext +
// Baggage. A zero Timeout becomes 10s; an empty Protocol defaults to HTTP.
//
// Callers MUST call Shutdown at process exit to flush buffered spans.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Protocol == "" {
		cfg.Protocol = otelx.ProtocolHTTP
	}
	if !cfg.Protocol.Valid() {
		return nil, fmt.Errorf("tracing: unsupported protocol %q (want http or grpc)", cfg.Protocol)
	}
	if cfg.SampleRate < 0 || cfg.SampleRate > 1 {
		return nil, fmt.Errorf("tracing: sample_rate %v out of range [0,1]", cfg.SampleRate)
	}

	res, err := otelx.NewResource(ctx, cfg.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	exp, err := newTraceExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("tracing: build trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))),
	)

	otel.SetTracerProvider(tp)
	// Set the global propagator so the federation spoke can inject a W3C
	// traceparent into its outbound HTTP push and the hub can extract it,
	// linking hub.handlePush to the spoke's spoke.push span across the wire.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Provider{tp: tp}, nil
}

// Shutdown flushes buffered spans and releases the TracerProvider. Safe to call
// once at process shutdown. A nil Provider Shutdown is a no-op so callers need
// not branch on whether tracing was enabled.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tp == nil {
		return nil
	}
	if err := p.tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("tracing: shutdown tracer provider: %w", err)
	}
	return nil
}

// newTraceExporter builds the OTLP trace exporter for the configured protocol.
// Endpoint handling is otelx's — the same parsing the metrics/logs exporters
// use — so tracing and metrics/logs reach the same receiver with identical
// scheme/insecure semantics.
func newTraceExporter(ctx context.Context, cfg Config) (*otlptrace.Exporter, error) {
	switch cfg.Protocol {
	case otelx.ProtocolGRPC:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithTimeout(cfg.Timeout)}
		host, insecure := otelx.EndpointHostInsecure(cfg.Endpoint)
		if host != "" {
			opts = append(opts, otlptracegrpc.WithEndpoint(host))
		}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, opts...)
	default: // otelx.ProtocolHTTP
		opts := []otlptracehttp.Option{otlptracehttp.WithTimeout(cfg.Timeout)}
		if u, insecure, ok := otelx.EndpointURL(cfg.Endpoint); ok {
			opts = append(opts, otlptracehttp.WithEndpointURL(u))
			if insecure {
				opts = append(opts, otlptracehttp.WithInsecure())
			}
		}
		return otlptracehttp.New(ctx, opts...)
	}
}

// Tracer returns the exporter's tracer from the global TracerProvider. When
// tracing is disabled this is the no-op tracer, so every Start call is cheap.
// Instrumentation sites call this rather than holding a tracer handle so they
// observe whichever provider New installed (or the default no-op).
func Tracer() trace.Tracer {
	return otel.Tracer(ScopeName)
}
