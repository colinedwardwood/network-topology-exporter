// Package otlp implements an OTLP/HTTP push output for the network topology
// exporter. It serialises topology graphs as OTLP metrics and change events as
// OTLP log records, then POSTs them to a configurable endpoint (e.g. a Grafana
// Alloy otelcol.receiver.otlp receiver).
//
// All serialisation is done with encoding/json against hand-written structs
// that match the OTLP proto3 JSON mapping. No otel SDK is required.
package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
)

// Config holds the settings for the OTLP exporter.
type Config struct {
	// Endpoint is the base URL of the OTLP receiver, e.g. "http://alloy:4318".
	// Must have no trailing slash.
	Endpoint string

	// Timeout caps each individual POST. Defaults to 10s when zero.
	Timeout time.Duration
}

// Exporter pushes topology data to an OTLP/HTTP endpoint.
type Exporter struct {
	cfg    Config
	client *http.Client
}

// New returns an Exporter configured with cfg. A zero Timeout in cfg is
// replaced with 10 seconds.
func New(cfg Config) *Exporter {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Exporter{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// ── OTLP JSON types ───────────────────────────────────────────────────────────

// The structs below are a minimal faithful encoding of the OTLP proto3 JSON
// mapping for metrics and logs. uint64 timestamps are encoded as strings per
// the proto3 spec (avoids JSON number precision loss).

type kvValue struct {
	StringValue string `json:"stringValue"`
}

type kv struct {
	Key   string  `json:"key"`
	Value kvValue `json:"value"`
}

type resource struct {
	Attributes []kv `json:"attributes"`
}

type scope struct {
	Name string `json:"name"`
}

// ── Metrics types ─────────────────────────────────────────────────────────────

type dataPoint struct {
	Attributes   []kv    `json:"attributes"`
	AsDouble     float64 `json:"asDouble"`
	TimeUnixNano string  `json:"timeUnixNano"`
}

type gauge struct {
	DataPoints []dataPoint `json:"dataPoints"`
}

type metric struct {
	Name  string `json:"name"`
	Gauge gauge  `json:"gauge"`
}

type scopeMetrics struct {
	Scope   scope    `json:"scope"`
	Metrics []metric `json:"metrics"`
}

type resourceMetrics struct {
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type metricsPayload struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

// ── Logs types ────────────────────────────────────────────────────────────────

type logBody struct {
	StringValue string `json:"stringValue"`
}

type logRecord struct {
	TimeUnixNano   string  `json:"timeUnixNano"`
	SeverityNumber int     `json:"severityNumber"`
	SeverityText   string  `json:"severityText"`
	Body           logBody `json:"body"`
	Attributes     []kv    `json:"attributes"`
}

type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type logsPayload struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

// ── constants ─────────────────────────────────────────────────────────────────

const (
	serviceName  = "network-topology-exporter"
	scopeName    = "github.com/colinedwardwood/network-topology-exporter"
	severityInfo = 9
	severityWarn = 13
)

func serviceResource() resource {
	return resource{
		Attributes: []kv{
			{Key: "service.name", Value: kvValue{StringValue: serviceName}},
		},
	}
}

// ── PushGraph ─────────────────────────────────────────────────────────────────

// PushGraph serialises graph.Edges and graph.Devices as OTLP gauge metrics and
// POSTs them to {endpoint}/v1/metrics.
func (e *Exporter) PushGraph(ctx context.Context, g discovery.Graph) error {
	now := strconv.FormatInt(time.Now().UnixNano(), 10)

	// Build edge data points.
	edgePoints := make([]dataPoint, 0, len(g.Edges))
	for _, edge := range g.Edges {
		edgePoints = append(edgePoints, dataPoint{
			Attributes: []kv{
				{Key: "src_device", Value: kvValue{StringValue: edge.SrcDevice}},
				{Key: "src_port", Value: kvValue{StringValue: edge.SrcPort}},
				{Key: "dst_device", Value: kvValue{StringValue: edge.DstDevice}},
				{Key: "dst_port", Value: kvValue{StringValue: edge.DstPort}},
				{Key: "proto", Value: kvValue{StringValue: edge.DiscoveryProto}},
				{Key: "link_kind", Value: kvValue{StringValue: edge.LinkKind}},
			},
			AsDouble:     1.0,
			TimeUnixNano: now,
		})
	}

	// Build device data points.
	devicePoints := make([]dataPoint, 0, len(g.Devices))
	for _, dev := range g.Devices {
		devicePoints = append(devicePoints, dataPoint{
			Attributes: []kv{
				{Key: "device", Value: kvValue{StringValue: dev.ID}},
			},
			AsDouble:     1.0,
			TimeUnixNano: now,
		})
	}

	payload := metricsPayload{
		ResourceMetrics: []resourceMetrics{
			{
				Resource: serviceResource(),
				ScopeMetrics: []scopeMetrics{
					{
						Scope: scope{Name: scopeName},
						Metrics: []metric{
							{
								Name:  "network_topology_edge",
								Gauge: gauge{DataPoints: edgePoints},
							},
							{
								Name:  "network_topology_device",
								Gauge: gauge{DataPoints: devicePoints},
							},
						},
					},
				},
			},
		},
	}

	return e.post(ctx, "/v1/metrics", payload)
}

// ── PushChanges ───────────────────────────────────────────────────────────────

// PushChanges serialises changes as OTLP log records and POSTs them to
// {endpoint}/v1/logs. Each EdgeChange becomes one log record.
func (e *Exporter) PushChanges(ctx context.Context, changes []graph.EdgeChange) error {
	records := make([]logRecord, 0, len(changes))
	for _, c := range changes {
		sevNum := severityInfo
		sevText := "INFO"
		if c.Kind == graph.ChangeRemoved {
			sevNum = severityWarn
			sevText = "WARN"
		}

		// Prefer "after" edge for attribute values; fall back to "before".
		var srcDevice, srcPort, dstDevice, dstPort, proto string
		switch {
		case c.After != nil:
			srcDevice = c.After.SrcDevice
			srcPort = c.After.SrcPort
			dstDevice = c.After.DstDevice
			dstPort = c.After.DstPort
			proto = c.After.DiscoveryProto
		case c.Before != nil:
			srcDevice = c.Before.SrcDevice
			srcPort = c.Before.SrcPort
			dstDevice = c.Before.DstDevice
			dstPort = c.Before.DstPort
			proto = c.Before.DiscoveryProto
		}

		var ts time.Time
		switch {
		case c.After != nil && !c.After.ObservedAt.IsZero():
			ts = c.After.ObservedAt
		case c.Before != nil && !c.Before.ObservedAt.IsZero():
			ts = c.Before.ObservedAt
		default:
			ts = time.Now()
		}

		records = append(records, logRecord{
			TimeUnixNano:   strconv.FormatInt(ts.UnixNano(), 10),
			SeverityNumber: sevNum,
			SeverityText:   sevText,
			Body:           logBody{StringValue: "topology change: " + string(c.Kind)},
			Attributes: []kv{
				{Key: "change_kind", Value: kvValue{StringValue: string(c.Kind)}},
				{Key: "src_device", Value: kvValue{StringValue: srcDevice}},
				{Key: "src_port", Value: kvValue{StringValue: srcPort}},
				{Key: "dst_device", Value: kvValue{StringValue: dstDevice}},
				{Key: "dst_port", Value: kvValue{StringValue: dstPort}},
				{Key: "proto", Value: kvValue{StringValue: proto}},
			},
		})
	}

	payload := logsPayload{
		ResourceLogs: []resourceLogs{
			{
				Resource: serviceResource(),
				ScopeLogs: []scopeLogs{
					{
						Scope:      scope{Name: scopeName},
						LogRecords: records,
					},
				},
			},
		},
	}

	return e.post(ctx, "/v1/logs", payload)
}

// ── post ──────────────────────────────────────────────────────────────────────

// post marshals payload to JSON and POSTs it to e.cfg.Endpoint+path.
// Returns an error for any HTTP error or status >= 400.
func (e *Exporter) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("otlp: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("otlp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("otlp: post %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		return fmt.Errorf("otlp: post %s: server returned %d", path, resp.StatusCode)
	}
	return nil
}
