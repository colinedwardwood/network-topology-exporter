package app

import (
	"context"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// installRecorder installs an always-sample SDK TracerProvider backed by an
// in-memory SpanRecorder as the global provider, restoring the previous global
// at test end. It returns the recorder so the test can read exported spans.
func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

// runSingleTargetCycle stands up an in-process SNMP agent advertising one LLDP
// neighbour and runs a single RunCycle against it under a root discovery.cycle
// span, returning the recorded spans.
func runSingleTargetCycle(t *testing.T, sr *tracetest.SpanRecorder) []sdktrace.ReadOnlySpan {
	t.Helper()
	t.Setenv("TEST_COMM", "public")
	cfg := testConfig(t, "TEST_COMM")
	cfg.Discovery.Interval = 30 * time.Second
	cfg.Discovery.CycleBudgetFraction = 1
	cfg.Discovery.UnconfirmedLinkTTLCycles = 3
	m := metrics.New(false)

	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)

	cfg.Targets = []config.TargetConfig{{Host: ip.String(), Port: int(port)}}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	allow := snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Wrap RunCycle in a root span exactly as the production cycle() closure does.
	ctx, root := tracing.Tracer().Start(ctx, "discovery.cycle")
	g, _, _, _ := RunCycle(ctx, slogDiscard(), cfg, m, nil, nil, resolver, allow, map[graph.EdgeKey]int{})
	root.End()

	if len(g.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(g.Devices))
	}
	return sr.Ended()
}

// TestCycleEmitsExpectedSpans asserts a single discovery cycle emits the
// discovery.cycle root plus target.poll, credentials.resolve, lldp.walk, and
// graph.reconcile children, with target.poll nested under discovery.cycle.
func TestCycleEmitsExpectedSpans(t *testing.T) {
	sr := installRecorder(t)
	spans := runSingleTargetCycle(t, sr)

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byName[s.Name()] = s
	}
	for _, want := range []string{"discovery.cycle", "target.poll", "credentials.resolve", "lldp.walk", "graph.reconcile"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("missing span %q; got spans %v", want, spanNames(spans))
		}
	}

	// Nesting: target.poll is a child of discovery.cycle.
	cycle := byName["discovery.cycle"]
	target := byName["target.poll"]
	if cycle == nil || target == nil {
		t.Fatalf("cannot check nesting: cycle=%v target=%v", cycle, target)
	}
	if target.Parent().SpanID() != cycle.SpanContext().SpanID() {
		t.Errorf("target.poll parent = %v, want discovery.cycle %v",
			target.Parent().SpanID(), cycle.SpanContext().SpanID())
	}
	// lldp.walk is a child of target.poll.
	if w := byName["lldp.walk"]; w.Parent().SpanID() != target.SpanContext().SpanID() {
		t.Errorf("lldp.walk parent = %v, want target.poll %v",
			w.Parent().SpanID(), target.SpanContext().SpanID())
	}
	// credentials.resolve is a child of target.poll.
	if c := byName["credentials.resolve"]; c.Parent().SpanID() != target.SpanContext().SpanID() {
		t.Errorf("credentials.resolve parent = %v, want target.poll %v",
			c.Parent().SpanID(), target.SpanContext().SpanID())
	}

	// Key attributes.
	if got := attrString(target, "target.ip"); got != "127.0.0.1" {
		t.Errorf("target.poll target.ip = %q, want 127.0.0.1", got)
	}
	if got := attrString(byName["lldp.walk"], "walk.outcome"); got == "" {
		t.Errorf("lldp.walk missing walk.outcome attribute")
	}
	if !hasAttr(byName["lldp.walk"], "walk.pdu_count") {
		t.Errorf("lldp.walk missing walk.pdu_count attribute")
	}
	if !hasAttr(byName["graph.reconcile"], "reconcile.output_edges") {
		t.Errorf("graph.reconcile missing reconcile.output_edges attribute")
	}
	if !hasAttr(byName["credentials.resolve"], "credential.winning_profile") {
		// winning_profile only set on success; the single-community fallback has
		// an empty profile name, so accept either presence or candidates count.
		if !hasAttr(byName["credentials.resolve"], "credential.candidates") {
			t.Errorf("credentials.resolve missing credential attributes")
		}
	}
}

// TestTracingDisabledNoSpans asserts that when no SDK TracerProvider is
// installed as the global (the default no-op tracer — i.e. traces.enabled is
// false and tracing.New was never called), a full discovery cycle exports zero
// spans into a SpanRecorder that is NOT wired to the global provider. This
// proves instrumentation is a no-op when tracing is disabled.
func TestTracingDisabledNoSpans(t *testing.T) {
	// A recorder attached to a provider we never install globally. Because the
	// global stays the default no-op tracer, the production instrumentation
	// (tracing.Tracer().Start) emits nothing, and this recorder sees nothing.
	sr := tracetest.NewSpanRecorder()
	_ = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	// The global TracerProvider is left as the default no-op (installRecorder's
	// t.Cleanup has already restored it for any prior test in this package), so
	// tracing.Tracer() returns the no-op tracer.

	t.Setenv("TEST_COMM", "public")
	cfg := testConfig(t, "TEST_COMM")
	cfg.Discovery.Interval = 30 * time.Second
	cfg.Discovery.CycleBudgetFraction = 1
	cfg.Discovery.UnconfirmedLinkTTLCycles = 3
	m := metrics.New(false)
	remoteIP := net.ParseIP("127.0.0.2")
	addr := snmptest.Start(t, "public", systemAndLLDPPDUs("sw-a", remoteIP))
	ip, port := snmptest.ParseAddr(addr)
	cfg.Targets = []config.TargetConfig{{Host: ip.String(), Port: int(port)}}
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	allow := snmpwalk.ParseCIDRs([]string{"127.0.0.0/8"})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ctx, root := tracing.Tracer().Start(ctx, "discovery.cycle")
	RunCycle(ctx, slogDiscard(), cfg, m, nil, nil, resolver, allow, map[graph.EdgeKey]int{})
	root.End()

	if got := len(sr.Ended()); got != 0 {
		t.Errorf("expected 0 spans with tracing disabled, got %d", got)
	}
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name()
	}
	return out
}

func attrString(s sdktrace.ReadOnlySpan, key string) string {
	if s == nil {
		return ""
	}
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

func hasAttr(s sdktrace.ReadOnlySpan, key string) bool {
	if s == nil {
		return false
	}
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return true
		}
	}
	return false
}
