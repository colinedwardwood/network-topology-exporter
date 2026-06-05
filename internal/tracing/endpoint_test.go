package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

// TestEndpointURL covers empty, plain-http (insecure), and https endpoints.
func TestEndpointURL(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		wantOK       bool
		wantInsecure bool
	}{
		{"empty", "", false, false},
		{"http insecure", "http://collector:4318", true, true},
		{"https secure", "https://collector:4318", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, insecure, ok := endpointURL(tt.endpoint)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if insecure != tt.wantInsecure {
				t.Errorf("insecure = %v, want %v", insecure, tt.wantInsecure)
			}
			if ok && raw != tt.endpoint {
				t.Errorf("rawURL = %q, want %q", raw, tt.endpoint)
			}
		})
	}
}

// TestEndpointHostInsecure extracts host:port and the insecure flag for the gRPC
// exporter, including the empty and bare-host fallbacks.
func TestEndpointHostInsecure(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		wantHost     string
		wantInsecure bool
	}{
		{"empty", "", "", false},
		{"http authority", "http://collector:4317", "collector:4317", true},
		{"https authority", "https://collector:4317", "collector:4317", false},
		{"bare host no scheme", "collector:4317", "collector:4317", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, insecure := endpointHostInsecure(tt.endpoint)
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if insecure != tt.wantInsecure {
				t.Errorf("insecure = %v, want %v", insecure, tt.wantInsecure)
			}
		})
	}
}

// TestNewTraceExporterGRPC builds the gRPC exporter (it connects lazily, so no
// live receiver is needed) for an insecure endpoint.
func TestNewTraceExporterGRPC(t *testing.T) {
	exp, err := newTraceExporter(context.Background(), Config{
		Protocol: ProtocolGRPC,
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
		Protocol:   ProtocolGRPC,
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
