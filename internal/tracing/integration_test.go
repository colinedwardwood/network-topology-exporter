package tracing_test

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// TestTracingIntegration exercises a real span export against a live OTLP trace
// receiver (e.g. Grafana Tempo, a Grafana Alloy otelcol.receiver.otlp, or an
// OpenTelemetry Collector feeding Tempo/Jaeger). It is skipped unless
// TRACING_INTEGRATION_ENDPOINT is set, because CI and the sandbox have no
// receiver. This is the manual follow-up the maintainer must run to satisfy the
// "spans show on a live Tempo/Jaeger receiver" acceptance item in issue #68.
//
// Run with, for example:
//
//	TRACING_INTEGRATION_ENDPOINT=http://localhost:4318 \
//	TRACING_INTEGRATION_PROTOCOL=http \
//	go test ./internal/tracing/ -run TestTracingIntegration -v
//
// then confirm a trace named "integration.smoke" with a child "integration.child"
// arrives at the receiver. Repeat with TRACING_INTEGRATION_PROTOCOL=grpc against :4317.
func TestTracingIntegration(t *testing.T) {
	endpoint := os.Getenv("TRACING_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TRACING_INTEGRATION_ENDPOINT to run the live-receiver integration test")
	}
	protocol := tracing.Protocol(os.Getenv("TRACING_INTEGRATION_PROTOCOL"))

	ctx := context.Background()
	p, err := tracing.New(ctx, tracing.Config{
		Endpoint:   endpoint,
		Protocol:   protocol,
		Timeout:    5 * time.Second,
		SampleRate: 1.0,
	})
	if err != nil {
		t.Fatalf("tracing.New: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(shutdownCtx)
	})

	tr := otel.Tracer(tracing.ScopeName)
	ctx, root := tr.Start(ctx, "integration.smoke")
	_, child := tr.Start(ctx, "integration.child")
	child.End()
	root.End()
}
