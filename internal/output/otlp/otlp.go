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
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
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
	SchemaUrl    string         `json:"schemaUrl,omitempty"`
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type metricsPayload struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

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
	SchemaUrl string      `json:"schemaUrl,omitempty"`
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type logsPayload struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

const (
	serviceName        = "network-topology-exporter"
	scopeName          = "github.com/colinedwardwood/network-topology-exporter"
	severityInfo       = 9
	severityWarn       = 13
	otlpSchemaURL      = "https://opentelemetry.io/schemas/1.21.0"
	metadataAttrPrefix = "network.topology."
)

var serviceRes resource

func init() {
	hostname, _ := os.Hostname()
	serviceRes = resource{
		Attributes: []kv{
			{Key: "service.name", Value: kvValue{StringValue: serviceName}},
			{Key: "service.version", Value: kvValue{StringValue: version.Version}},
			{Key: "service.instance.id", Value: kvValue{StringValue: hostname}},
		},
	}
}

// sanitizeUTF8 replaces sequences of invalid UTF-8 bytes with the Unicode
// replacement character so that JSON serialization never fails on SNMP strings
// sourced from device sysName/ifDescr which may contain arbitrary bytes.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// PushGraph serialises graph.Edges and graph.Devices as OTLP gauge metrics and
// POSTs them to {endpoint}/v1/metrics.
func (e *Exporter) PushGraph(ctx context.Context, g discovery.Graph) error {
	now := strconv.FormatInt(time.Now().UnixNano(), 10)

	edgePoints := make([]dataPoint, 0, len(g.Edges))
	for _, edge := range g.Edges {
		attrs := []kv{
			{Key: "src_device", Value: kvValue{StringValue: sanitizeUTF8(edge.SrcDevice)}},
			{Key: "src_port", Value: kvValue{StringValue: sanitizeUTF8(edge.SrcPort)}},
			{Key: "dst_device", Value: kvValue{StringValue: sanitizeUTF8(edge.DstDevice)}},
			{Key: "dst_port", Value: kvValue{StringValue: sanitizeUTF8(edge.DstPort)}},
			{Key: "proto", Value: kvValue{StringValue: sanitizeUTF8(edge.DiscoveryProto)}},
			{Key: "link_kind", Value: kvValue{StringValue: sanitizeUTF8(edge.LinkKind)}},
		}
		for k, v := range edge.Metadata {
			attrs = append(attrs, kv{Key: metadataAttrPrefix + k, Value: kvValue{StringValue: sanitizeUTF8(v)}})
		}
		edgePoints = append(edgePoints, dataPoint{
			Attributes:   attrs,
			AsDouble:     1.0,
			TimeUnixNano: now,
		})
	}

	devicePoints := make([]dataPoint, 0, len(g.Devices))
	for _, dev := range g.Devices {
		devicePoints = append(devicePoints, dataPoint{
			Attributes: []kv{
				{Key: "device", Value: kvValue{StringValue: sanitizeUTF8(dev.ID)}},
			},
			AsDouble:     1.0,
			TimeUnixNano: now,
		})
	}

	payload := metricsPayload{
		ResourceMetrics: []resourceMetrics{
			{
				SchemaUrl: otlpSchemaURL,
				Resource:  serviceRes,
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
		var srcDevice, srcPort, dstDevice, dstPort, proto, linkKind string
		switch {
		case c.After != nil:
			srcDevice = c.After.SrcDevice
			srcPort = c.After.SrcPort
			dstDevice = c.After.DstDevice
			dstPort = c.After.DstPort
			proto = c.After.DiscoveryProto
			linkKind = c.After.LinkKind
		case c.Before != nil:
			srcDevice = c.Before.SrcDevice
			srcPort = c.Before.SrcPort
			dstDevice = c.Before.DstDevice
			dstPort = c.Before.DstPort
			proto = c.Before.DiscoveryProto
			linkKind = c.Before.LinkKind
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
				{Key: "src_device", Value: kvValue{StringValue: sanitizeUTF8(srcDevice)}},
				{Key: "src_port", Value: kvValue{StringValue: sanitizeUTF8(srcPort)}},
				{Key: "dst_device", Value: kvValue{StringValue: sanitizeUTF8(dstDevice)}},
				{Key: "dst_port", Value: kvValue{StringValue: sanitizeUTF8(dstPort)}},
				{Key: "proto", Value: kvValue{StringValue: sanitizeUTF8(proto)}},
				{Key: "link_kind", Value: kvValue{StringValue: sanitizeUTF8(linkKind)}},
			},
		})
	}

	payload := logsPayload{
		ResourceLogs: []resourceLogs{
			{
				SchemaUrl: otlpSchemaURL,
				Resource:  serviceRes,
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

const (
	postMaxAttempts   = 3
	postBaseDelay     = 100 * time.Millisecond
	postMaxJitter     = 50 * time.Millisecond
	postMaxRetryAfter = 10 * time.Second
)

// cryptoJitter returns a random duration in [0, max) using crypto/rand so the
// retry jitter is not flagged as a weak-random-number-generator lint issue.
func cryptoJitter(max time.Duration) time.Duration {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := int64(binary.LittleEndian.Uint32(b[:]))
	return time.Duration(n % int64(max))
}

// post marshals payload to JSON and POSTs it to e.cfg.Endpoint+path.
// It retries on transient network errors and on HTTP 429, 502, 503, and 504
// with exponential backoff (up to 3 attempts total). On 429 it honours a
// Retry-After header (integer seconds, ≤ 10s).
// Returns an error only after all attempts are exhausted.
func (e *Exporter) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("otlp: marshal payload: %w", err)
	}

	var lastErr error
	delay := postBaseDelay
	for attempt := range postMaxAttempts {
		if attempt > 0 {
			jitter := cryptoJitter(postMaxJitter)
			select {
			case <-time.After(delay + jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay *= 2
			if delay > postMaxRetryAfter {
				delay = postMaxRetryAfter
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.Endpoint+path, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("otlp: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("otlp: post %s: %w", path, err)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode < 400 {
			return nil
		}

		lastErr = fmt.Errorf("otlp: post %s: server returned %d", path, resp.StatusCode)

		// Only retry on transient errors: 429, 502, 503, 504.
		switch resp.StatusCode {
		case http.StatusTooManyRequests,    // 429
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			// retryable — fall through to retry loop
		default:
			return lastErr
		}

		// On 429, honour Retry-After if present and within our cap.
		if resp.StatusCode == http.StatusTooManyRequests {
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.ParseInt(ra, 10, 64); err == nil {
					d := time.Duration(secs) * time.Second
					if d <= postMaxRetryAfter {
						delay = d
					}
				}
			}
		}
	}
	return lastErr
}
