package otlp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
)

// helper: decode the request body as a generic JSON map.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

// drillMetrics walks resourceMetrics[0].scopeMetrics[0].metrics and returns
// a map of metric name → slice of data-point attribute maps.
func drillMetrics(t *testing.T, body map[string]any) map[string][]map[string]any {
	t.Helper()
	rm := body["resourceMetrics"].([]any)[0].(map[string]any)
	sm := rm["scopeMetrics"].([]any)[0].(map[string]any)
	metrics := sm["metrics"].([]any)

	out := make(map[string][]map[string]any)
	for _, raw := range metrics {
		m := raw.(map[string]any)
		name := m["name"].(string)
		gauge := m["gauge"].(map[string]any)
		dps := gauge["dataPoints"].([]any)
		points := make([]map[string]any, 0, len(dps))
		for _, dp := range dps {
			attrSlice := dp.(map[string]any)["attributes"].([]any)
			attrMap := make(map[string]any, len(attrSlice))
			for _, a := range attrSlice {
				kv := a.(map[string]any)
				key := kv["key"].(string)
				val := kv["value"].(map[string]any)["stringValue"]
				attrMap[key] = val
			}
			points = append(points, attrMap)
		}
		out[name] = points
	}
	return out
}

// drillLogs walks resourceLogs[0].scopeLogs[0].logRecords and returns the
// raw logRecord maps.
func drillLogs(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	rl := body["resourceLogs"].([]any)[0].(map[string]any)
	sl := rl["scopeLogs"].([]any)[0].(map[string]any)
	recs := sl["logRecords"].([]any)
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.(map[string]any))
	}
	return out
}

// ── Test 1: PushGraph with 2 edges and 2 devices ──────────────────────────────

func TestPushGraphTwoEdgesTwoDevices(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2", DiscoveryProto: "lldp", LinkKind: "ethernet"},
			{SrcDevice: "sw-c", SrcPort: "Eth1", DstDevice: "sw-d", DstPort: "Eth2", DiscoveryProto: "cdp", LinkKind: "ethernet"},
		},
		Devices: []discovery.Device{
			{ID: "sw-a"},
			{ID: "sw-b"},
		},
	}

	if err := exp.PushGraph(context.Background(), g); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}

	if gotPath != "/v1/metrics" {
		t.Errorf("path = %q, want /v1/metrics", gotPath)
	}

	metrics := drillMetrics(t, gotBody)

	// Verify edge data points.
	edgePoints, ok := metrics["network_topology_edge"]
	if !ok {
		t.Fatal("metric network_topology_edge not found")
	}
	if len(edgePoints) != 2 {
		t.Fatalf("edge data points = %d, want 2", len(edgePoints))
	}

	// Find the sw-a→sw-b edge.
	var foundEdge bool
	for _, pt := range edgePoints {
		if pt["src_device"] == "sw-a" && pt["dst_device"] == "sw-b" {
			foundEdge = true
			if pt["proto"] != "lldp" {
				t.Errorf("proto = %q, want lldp", pt["proto"])
			}
			if pt["link_kind"] != "ethernet" {
				t.Errorf("link_kind = %q, want ethernet", pt["link_kind"])
			}
		}
	}
	if !foundEdge {
		t.Error("sw-a→sw-b edge data point not found")
	}

	// Verify device data points.
	devicePoints, ok := metrics["network_topology_device"]
	if !ok {
		t.Fatal("metric network_topology_device not found")
	}
	if len(devicePoints) != 2 {
		t.Fatalf("device data points = %d, want 2", len(devicePoints))
	}
	deviceIDs := make(map[string]bool)
	for _, pt := range devicePoints {
		deviceIDs[pt["device"].(string)] = true
	}
	for _, want := range []string{"sw-a", "sw-b"} {
		if !deviceIDs[want] {
			t.Errorf("device %q not found in data points", want)
		}
	}
}

// ── Test 2: PushGraph with empty graph ────────────────────────────────────────

func TestPushGraphEmpty(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	if err := exp.PushGraph(context.Background(), discovery.Graph{}); err != nil {
		t.Fatalf("PushGraph empty: %v", err)
	}
	if !called {
		t.Error("server was not called for empty graph")
	}
}

// ── Test 3: PushChanges with Added change → severityNumber=9 ─────────────────

func TestPushChangesAdded(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	after := &discovery.Edge{
		SrcDevice: "sw-a", SrcPort: "Gi0/1",
		DstDevice: "sw-b", DstPort: "Gi0/2",
		DiscoveryProto: "lldp",
		ObservedAt:     time.Now(),
	}
	changes := []graph.EdgeChange{
		{Kind: graph.ChangeAdded, After: after},
	}

	if err := exp.PushChanges(context.Background(), changes); err != nil {
		t.Fatalf("PushChanges: %v", err)
	}

	records := drillLogs(t, gotBody)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1", len(records))
	}

	rec := records[0]
	if sev := rec["severityNumber"].(float64); sev != 9 {
		t.Errorf("severityNumber = %v, want 9", sev)
	}
	body := rec["body"].(map[string]any)["stringValue"].(string)
	if !strings.Contains(body, "topology change: added") {
		t.Errorf("body = %q, want to contain %q", body, "topology change: added")
	}
}

// ── Test 4: PushChanges with Removed change → severityNumber=13 ──────────────

func TestPushChangesRemoved(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	before := &discovery.Edge{
		SrcDevice: "sw-x", SrcPort: "eth0",
		DstDevice: "sw-y", DstPort: "eth1",
		DiscoveryProto: "cdp",
		ObservedAt:     time.Now(),
	}
	changes := []graph.EdgeChange{
		{Kind: graph.ChangeRemoved, Before: before},
	}

	if err := exp.PushChanges(context.Background(), changes); err != nil {
		t.Fatalf("PushChanges: %v", err)
	}

	records := drillLogs(t, gotBody)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1", len(records))
	}

	rec := records[0]
	if sev := rec["severityNumber"].(float64); sev != 13 {
		t.Errorf("severityNumber = %v, want 13 (WARN for removed)", sev)
	}
	if text := rec["severityText"].(string); text != "WARN" {
		t.Errorf("severityText = %q, want WARN", text)
	}
}

// ── Test 5: Server returns 500 → PushGraph returns error ─────────────────────

func TestPushGraphServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

// ── Test 6: Context cancelled → post returns error before sending ─────────────

func TestPushGraphContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := exp.PushGraph(ctx, discovery.Graph{})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// Test 7: TCP connection reuse — two sequential post calls succeed and the
// server receives both. This works only if the response body is fully drained
// before Close, allowing the HTTP client to reuse the connection.
func TestPostReusesConnection(t *testing.T) {
	var hitCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the request body so the server side is clean.
		_, _ = io.ReadAll(r.Body)
		hitCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2", DiscoveryProto: "lldp", LinkKind: "ethernet"},
		},
	}

	if err := exp.PushGraph(context.Background(), g); err != nil {
		t.Fatalf("first PushGraph: %v", err)
	}
	if err := exp.PushGraph(context.Background(), g); err != nil {
		t.Fatalf("second PushGraph: %v", err)
	}

	if hitCount != 2 {
		t.Errorf("server hit count = %d, want 2", hitCount)
	}
}

// Test 8: post returns error for 4xx status codes.
func TestPostClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
}

// TestPostRetries503 verifies that post retries on 503 and succeeds on the
// third attempt (initial + 2 retries = 3 total requests).
func TestPostRetries503(t *testing.T) {
	var hitCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount++
		if hitCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err != nil {
		t.Fatalf("expected no error after retry, got: %v", err)
	}
	if hitCount != 3 {
		t.Errorf("server hit count = %d, want 3 (initial + 2 retries)", hitCount)
	}
}

// TestPostRetryAfterHeader verifies that post honours a Retry-After: 0 header
// on a 429 response and succeeds on the second attempt.
func TestPostRetryAfterHeader(t *testing.T) {
	var hitCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount++
		if hitCount == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err != nil {
		t.Fatalf("expected no error after retry, got: %v", err)
	}
}

// TestServiceResourceAttributes verifies that the serialised OTLP payload
// includes service.version and service.instance.id resource attributes.
func TestServiceResourceAttributes(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	if err := exp.PushGraph(context.Background(), discovery.Graph{}); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}

	rm := gotBody["resourceMetrics"].([]any)[0].(map[string]any)
	resource := rm["resource"].(map[string]any)
	attrs := resource["attributes"].([]any)

	attrMap := make(map[string]string, len(attrs))
	for _, a := range attrs {
		kv := a.(map[string]any)
		key := kv["key"].(string)
		val := kv["value"].(map[string]any)["stringValue"].(string)
		attrMap[key] = val
	}

	if _, ok := attrMap["service.version"]; !ok {
		t.Error("service.version attribute missing from resource")
	}
	if _, ok := attrMap["service.instance.id"]; !ok {
		t.Error("service.instance.id attribute missing from resource")
	}
}

// TestPushGraphEdgeMetadata verifies that per-edge metadata is serialised as
// attributes with the network.topology. prefix, covering the metadata loop.
func TestPushGraphEdgeMetadata(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{
				SrcDevice:      "sw-a",
				SrcPort:        "Gi0/1",
				DstDevice:      "sw-b",
				DstPort:        "Gi0/2",
				DiscoveryProto: "lldp",
				LinkKind:       "ethernet",
				Metadata:       map[string]string{"vlan": "100", "speed": "1G"},
			},
		},
	}

	if err := exp.PushGraph(context.Background(), g); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}

	metrics := drillMetrics(t, gotBody)
	edgePoints, ok := metrics["network_topology_edge"]
	if !ok || len(edgePoints) != 1 {
		t.Fatalf("expected 1 edge data point, got %v", edgePoints)
	}

	pt := edgePoints[0]
	if pt["network.topology.vlan"] != "100" {
		t.Errorf("metadata attr network.topology.vlan = %v, want 100", pt["network.topology.vlan"])
	}
	if pt["network.topology.speed"] != "1G" {
		t.Errorf("metadata attr network.topology.speed = %v, want 1G", pt["network.topology.speed"])
	}
}

// TestPushChangesNoBeforeNoAfterTimestamp verifies the fallback to time.Now()
// when neither Before nor After has a valid ObservedAt timestamp.
func TestPushChangesNoBeforeNoAfterTimestamp(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	// Both Before and After are nil — timestamp must default to time.Now().
	changes := []graph.EdgeChange{
		{Kind: graph.ChangeAdded},
	}

	if err := exp.PushChanges(context.Background(), changes); err != nil {
		t.Fatalf("PushChanges: %v", err)
	}

	records := drillLogs(t, gotBody)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1", len(records))
	}
	// TimeUnixNano must be a non-empty string (the time.Now() path was hit).
	ts, ok := records[0]["timeUnixNano"].(string)
	if !ok || ts == "" {
		t.Errorf("timeUnixNano = %v, want non-empty string", records[0]["timeUnixNano"])
	}
}

// TestPostAllRetriesExhausted503 verifies that post returns an error after
// exhausting all retries on persistent 503 responses.
func TestPostAllRetriesExhausted503(t *testing.T) {
	var hitCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(context.Background(), discovery.Graph{})
	if err == nil {
		t.Fatal("expected error after exhausting all 503 retries, got nil")
	}
	// postMaxAttempts = 3: all three must have been tried.
	if hitCount != 3 {
		t.Errorf("server hit count = %d, want 3", hitCount)
	}
}

// TestPostContextCancelledDuringRetryDelay verifies that post returns ctx.Err()
// when the context is cancelled while waiting for a retry delay (503 path).
func TestPostContextCancelledDuringRetryDelay(t *testing.T) {
	// Cancel the context after the first request returns so that the retry
	// delay select hits the ctx.Done() branch instead of time.After.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The server cancels ctx immediately after responding 503, before the
	// retry delay (postBaseDelay = 100ms + jitter) can fire.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel()
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	err := exp.PushGraph(ctx, discovery.Graph{})
	if err == nil {
		t.Fatal("expected error when context cancelled during retry, got nil")
	}
}

// TestSchemaURLPresent verifies that the serialised OTLP metrics payload
// includes a schemaUrl field set to the OpenTelemetry 1.21.0 schema URL.
func TestSchemaURLPresent(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	if err := exp.PushGraph(context.Background(), discovery.Graph{}); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}

	rm := gotBody["resourceMetrics"].([]any)[0].(map[string]any)
	schemaURL, ok := rm["schemaUrl"].(string)
	if !ok || schemaURL == "" {
		t.Fatalf("schemaUrl missing or empty in resourceMetrics[0]; got %v", rm["schemaUrl"])
	}
	const wantURL = "https://opentelemetry.io/schemas/1.21.0"
	if schemaURL != wantURL {
		t.Errorf("schemaUrl = %q, want %q", schemaURL, wantURL)
	}
}

// TestPushGraphInvalidUTF8 verifies that edge and device fields containing
// invalid UTF-8 bytes are sanitized before JSON serialization. An SNMP device
// may return arbitrary bytes in sysName or ifDescr; without sanitization
// json.Marshal would produce invalid JSON or fail entirely.
func TestPushGraphInvalidUTF8(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	// Embed invalid UTF-8 bytes directly in the string fields.
	// 0xff and 0xfe are never valid in UTF-8; they should be replaced with "".
	badSrc := "sw-\xffa"        // invalid byte mid-string
	badDst := "\xfe\xffsw-b"    // invalid bytes at start
	badPort := "Gi0/\xff"        // invalid byte at end
	badDevice := "core-\xff\xfe" // invalid bytes in device ID

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{
				SrcDevice:      badSrc,
				SrcPort:        badPort,
				DstDevice:      badDst,
				DstPort:        "Gi0/2",
				DiscoveryProto: "lldp",
				LinkKind:       "ethernet",
			},
		},
		Devices: []discovery.Device{
			{ID: badDevice},
		},
	}

	if err := exp.PushGraph(context.Background(), g); err != nil {
		t.Fatalf("PushGraph with invalid UTF-8: %v", err)
	}

	metrics := drillMetrics(t, gotBody)

	edgePoints, ok := metrics["network_topology_edge"]
	if !ok || len(edgePoints) != 1 {
		t.Fatalf("expected 1 edge data point, got %v", edgePoints)
	}

	pt := edgePoints[0]

	// Each sanitized value must be valid UTF-8 and must not equal the raw input.
	for key, raw := range map[string]string{
		"src_device": badSrc,
		"src_port":   badPort,
		"dst_device": badDst,
	} {
		got, ok := pt[key].(string)
		if !ok {
			t.Errorf("attribute %q missing or not a string", key)
			continue
		}
		if got == raw {
			t.Errorf("attribute %q was not sanitized: got %q (same as input)", key, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("attribute %q is still invalid UTF-8 after sanitization: %q", key, got)
		}
		// The replacement character must be present.
		if !strings.Contains(got, "�") {
			t.Errorf("attribute %q expected replacement char '\\ufffd', got %q", key, got)
		}
	}

	devicePoints, ok := metrics["network_topology_device"]
	if !ok || len(devicePoints) != 1 {
		t.Fatalf("expected 1 device data point, got %v", devicePoints)
	}
	devID, ok := devicePoints[0]["device"].(string)
	if !ok {
		t.Fatal("device attribute missing or not a string")
	}
	if devID == badDevice {
		t.Errorf("device ID was not sanitized: got %q", devID)
	}
	if !utf8.ValidString(devID) {
		t.Errorf("device ID is still invalid UTF-8: %q", devID)
	}
}

// TestPushChangesInvalidUTF8 verifies that edge change fields with invalid
// UTF-8 bytes are sanitized before JSON serialization in PushChanges.
func TestPushChangesInvalidUTF8(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := otlp.New(otlp.Config{Endpoint: srv.URL})

	badSrc := "sw-\xff"
	badDst := "\xfesw-b"

	after := &discovery.Edge{
		SrcDevice:      badSrc,
		SrcPort:        "Gi0/1",
		DstDevice:      badDst,
		DstPort:        "Gi0/2",
		DiscoveryProto: "lldp",
		ObservedAt:     time.Now(),
	}
	changes := []graph.EdgeChange{
		{Kind: graph.ChangeAdded, After: after},
	}

	if err := exp.PushChanges(context.Background(), changes); err != nil {
		t.Fatalf("PushChanges with invalid UTF-8: %v", err)
	}

	records := drillLogs(t, gotBody)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1", len(records))
	}

	// Extract attributes from the log record.
	attrSlice := records[0]["attributes"].([]any)
	attrMap := make(map[string]string, len(attrSlice))
	for _, a := range attrSlice {
		kv := a.(map[string]any)
		key := kv["key"].(string)
		val := kv["value"].(map[string]any)["stringValue"].(string)
		attrMap[key] = val
	}

	for key, raw := range map[string]string{
		"src_device": badSrc,
		"dst_device": badDst,
	} {
		got, ok := attrMap[key]
		if !ok {
			t.Errorf("log attribute %q missing", key)
			continue
		}
		if got == raw {
			t.Errorf("log attribute %q was not sanitized: got %q", key, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("log attribute %q is still invalid UTF-8: %q", key, got)
		}
	}
}
