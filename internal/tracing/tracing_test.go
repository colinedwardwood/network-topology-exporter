package tracing

import (
	"context"
	"testing"
)

// TestNewRejectsBadProtocol verifies New rejects an unsupported protocol.
func TestNewRejectsBadProtocol(t *testing.T) {
	_, err := New(context.Background(), Config{Protocol: "carrier-pigeon", SampleRate: 0.1})
	if err == nil {
		t.Fatal("expected error for unsupported protocol, got nil")
	}
}

// TestNewRejectsOutOfRangeSampleRate verifies the [0,1] range is enforced.
func TestNewRejectsOutOfRangeSampleRate(t *testing.T) {
	for _, sr := range []float64{-0.1, 1.5} {
		if _, err := New(context.Background(), Config{SampleRate: sr}); err == nil {
			t.Errorf("sample_rate %v: expected range error, got nil", sr)
		}
	}
}

// TestNewInstallsProviderAndShutdown verifies New builds a provider over HTTP
// (no live receiver needed — the exporter connects lazily) and that Shutdown is
// safe, including on a nil Provider.
func TestNewInstallsProviderAndShutdown(t *testing.T) {
	p, err := New(context.Background(), Config{
		Endpoint:   "http://127.0.0.1:4318",
		Protocol:   ProtocolHTTP,
		SampleRate: 0.5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("New returned nil provider without error")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}

	var nilP *Provider
	if err := nilP.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Provider Shutdown should be a no-op, got %v", err)
	}
}
