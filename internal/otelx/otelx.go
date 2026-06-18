// Package otelx holds the OpenTelemetry SDK wiring shared by the OTLP
// metrics/logs exporter (internal/output/otlp) and the trace exporter
// (internal/tracing): the transport protocol enum, endpoint normalisation for
// the SDK's HTTP and gRPC exporter options, and the OTel resource identifying
// this process. Both signal paths intentionally describe the same process to
// the same receiver — service.name, service.instance.id, and the
// endpoint/insecure interpretation must never diverge between metrics and
// traces, so the logic lives here exactly once instead of as mirrored copies.
package otelx

import (
	"context"
	"log/slog"
	"net/url"
	"os"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/grafana/network-topology-exporter/internal/version"
)

const (
	// ServiceName is the OTLP resource attribute service.name emitted on
	// every signal (metrics, logs, traces) from this exporter.
	ServiceName = "network-topology-exporter"
	// ScopeName is the instrumentation scope shared by all signals so a
	// single instrumentation library identifies metrics, logs, and spans.
	ScopeName = "github.com/grafana/network-topology-exporter"
)

// Protocol selects the OTLP transport. It is the wire-level counterpart of
// config.OTLPProtocol: output.otlp.protocol configures it once and both the
// metrics/logs exporter and the trace exporter consume the same value.
type Protocol string

// Protocol values. ProtocolHTTP (OTLP/HTTP) is the default.
const (
	ProtocolHTTP Protocol = "http"
	ProtocolGRPC Protocol = "grpc"
)

// Valid reports whether p is a supported OTLP transport.
func (p Protocol) Valid() bool {
	return p == ProtocolHTTP || p == ProtocolGRPC
}

// NewResource builds the OTel resource describing this process. instanceID is
// emitted as service.instance.id; when empty it falls back to os.Hostname() —
// which is the container ID under Docker/Kubernetes and therefore not stable
// across pod restarts, hence the logged recommendation to set it explicitly
// (federation spokes pass their configured spoke_id).
func NewResource(ctx context.Context, instanceID string) (*resource.Resource, error) {
	if instanceID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			slog.Warn("otelx: os.Hostname() failed; service.instance.id will be empty",
				"error", err,
				"recommendation", "set InstanceID explicitly (e.g. federation.spoke.spoke_id)")
		}
		instanceID = hostname
	}
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(version.Version),
			semconv.ServiceInstanceID(instanceID),
		),
	)
}

// EndpointURL normalises an HTTP base endpoint (e.g. "http://alloy:4318")
// into the URL the SDK's WithEndpointURL option expects. WithEndpointURL
// takes the full base (the SDK appends the per-signal path itself only for
// WithEndpoint), so the base passes through verbatim; insecure reports
// whether the scheme is plain http so the caller can add WithInsecure. ok is
// false when endpoint is empty, meaning "let the SDK use its defaults".
func EndpointURL(endpoint string) (rawURL string, insecure bool, ok bool) {
	if endpoint == "" {
		return "", false, false
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint, false, true
	}
	return endpoint, u.Scheme == "http", true
}

// EndpointHostInsecure extracts host:port and the insecure flag for the gRPC
// exporters, which take a bare authority rather than a URL. A scheme-less
// endpoint is treated as a bare host:port and assumed plaintext (no scheme to
// imply TLS).
func EndpointHostInsecure(endpoint string) (host string, insecure bool) {
	if endpoint == "" {
		return "", false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint, true
	}
	return u.Host, u.Scheme == "http"
}
