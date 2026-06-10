package otelx

import (
	"context"
	"testing"
)

// TestEndpointURL covers empty, plain-http (insecure), and https endpoints.
// Moved from internal/tracing/endpoint_test.go when the shared implementation
// was extracted (#172) so the single copy is the tested one.
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
			raw, insecure, ok := EndpointURL(tt.endpoint)
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

// TestEndpointHostInsecure covers authority extraction for the gRPC
// exporters, including the empty and bare-host fallbacks.
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
			host, insecure := EndpointHostInsecure(tt.endpoint)
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if insecure != tt.wantInsecure {
				t.Errorf("insecure = %v, want %v", insecure, tt.wantInsecure)
			}
		})
	}
}

func TestProtocolValid(t *testing.T) {
	tests := []struct {
		p    Protocol
		want bool
	}{
		{ProtocolHTTP, true},
		{ProtocolGRPC, true},
		{"", false},
		{"https", false},
	}
	for _, tt := range tests {
		if got := tt.p.Valid(); got != tt.want {
			t.Errorf("Protocol(%q).Valid() = %v, want %v", tt.p, got, tt.want)
		}
	}
}

// TestNewResourceUsesInstanceID confirms an explicit instance ID lands in the
// resource verbatim (no hostname fallback).
func TestNewResourceUsesInstanceID(t *testing.T) {
	res, err := NewResource(context.Background(), "spoke-east-1")
	if err != nil {
		t.Fatalf("NewResource: %v", err)
	}
	var gotService, gotInstance string
	for _, kv := range res.Attributes() {
		switch string(kv.Key) {
		case "service.name":
			gotService = kv.Value.AsString()
		case "service.instance.id":
			gotInstance = kv.Value.AsString()
		}
	}
	if gotService != ServiceName {
		t.Errorf("service.name = %q, want %q", gotService, ServiceName)
	}
	if gotInstance != "spoke-east-1" {
		t.Errorf("service.instance.id = %q, want %q", gotInstance, "spoke-east-1")
	}
}
