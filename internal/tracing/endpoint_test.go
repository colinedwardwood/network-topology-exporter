package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/colinedwardwood/network-topology-exporter/internal/otelx"
)

// Endpoint parsing tests (TestEndpointURL / TestEndpointHostInsecure) live in
// internal/otelx with the shared implementation (#172); this file keeps only
// the trace-exporter-specific coverage.

// TestNewTraceExporterGRPC builds the gRPC exporter (it connects lazily, so no
// live receiver is needed) for an insecure endpoint.
func TestNewTraceExporterGRPC(t *testing.T) {
	exp, err := newTraceExporter(context.Background(), Config{
		Protocol: otelx.ProtocolGRPC,
		Endpoint: "http://127.0.0.1:4317",
	})
	if err != nil {
		t.Fatalf("newTraceExporter grpc: %v", err)
	}
	if exp == nil {
		t.Fatal("nil exporter without error")
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Errorf("exporter shutdown: %v", err)
	}
}

// TestNewGRPCProviderAndShutdown exercises the gRPC branch of New end to end and
// confirms Shutdown is clean.
func TestNewGRPCProviderAndShutdown(t *testing.T) {
	p, err := New(context.Background(), Config{
		Endpoint:   "http://127.0.0.1:4317",
		Protocol:   otelx.ProtocolGRPC,
		SampleRate: 1.0,
		InstanceID: "test-instance",
	})
	if err != nil {
		t.Fatalf("New grpc: %v", err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestTracerReturnsScopedTracer confirms Tracer() returns a usable tracer from
// the global provider (the no-op when none is installed).
func TestTracerReturnsScopedTracer(t *testing.T) {
	tr := Tracer()
	if tr == nil {
		t.Fatal("Tracer() returned nil")
	}
	// A span Start/End on the global (no-op) tracer must not panic.
	_, span := tr.Start(context.Background(), "unit.smoke")
	span.End()
	// Sanity: the package scope tracer matches otel.Tracer(ScopeName).
	if otel.Tracer(ScopeName) == nil {
		t.Error("otel.Tracer(ScopeName) returned nil")
	}
}
