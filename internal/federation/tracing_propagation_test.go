package federation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/discovery"
	"github.com/grafana/network-topology-exporter/internal/metrics"
)

// installTracing installs an always-sample SDK TracerProvider backed by a
// SpanRecorder plus the W3C TraceContext propagator, restoring the previous
// globals at test end. Mirrors what tracing.New does in production but without
// an OTLP exporter so the test stays in-memory.
func installTracing(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return sr
}

// TestSpokeToHubTraceparentPropagation proves the spoke→hub HTTP push carries
// the W3C traceparent end to end: the spoke injects its spoke.push span's trace
// context into the outbound request headers, and the hub's handlePush extracts
// it so hub.handlePush shares the spoke.push trace ID. This is the propagation
// acceptance test for issue #68.
func TestSpokeToHubTraceparentPropagation(t *testing.T) {
	sr := installTracing(t)

	h := NewHub(config.FederationConfig{SpokeTimeout: time.Minute}, metrics.New(false), nil, "")

	// httptest server with no TLS: handlePush handles r.TLS == nil (skips the
	// cert-CN binding) so the push is accepted and hub.handlePush runs.
	srv := httptest.NewServer(http.HandlerFunc(h.handlePush))
	defer srv.Close()

	s := newTestSpokeFor(t, srv.URL)
	err := s.Push(context.Background(), SpokePayload{
		SpokeID: "dc-test",
		CycleAt: time.Now(),
		Devices: []discovery.Device{{ID: "sw-1"}},
		Edges: []discovery.Edge{{
			SrcDevice:      "sw-1",
			SrcPort:        "Gi0/1",
			DstDevice:      "sw-2",
			DiscoveryProto: discovery.DiscoveryProtocolLLDP,
			Direction:      discovery.DirectionUnidirectional,
		}},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	var spokeSpan, hubSpan sdktrace.ReadOnlySpan
	for _, sp := range sr.Ended() {
		switch sp.Name() {
		case "spoke.push":
			spokeSpan = sp
		case "hub.handlePush":
			hubSpan = sp
		}
	}
	if spokeSpan == nil {
		t.Fatal("no spoke.push span recorded")
	}
	if hubSpan == nil {
		t.Fatal("no hub.handlePush span recorded")
	}

	spokeTID := spokeSpan.SpanContext().TraceID()
	hubTID := hubSpan.SpanContext().TraceID()
	if spokeTID != hubTID {
		t.Errorf("trace ID mismatch: spoke.push %s != hub.handlePush %s", spokeTID, hubTID)
	}
	// The hub span's parent must be the spoke.push span (continued across the
	// wire via traceparent).
	if hubSpan.Parent().SpanID() != spokeSpan.SpanContext().SpanID() {
		t.Errorf("hub.handlePush parent = %s, want spoke.push %s",
			hubSpan.Parent().SpanID(), spokeSpan.SpanContext().SpanID())
	}
}
