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
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/version"
)

// Config holds the settings for the OTLP exporter.
type Config struct {
	// Endpoint is the base URL of the OTLP receiver, e.g. "http://alloy:4318".
	// Must have no trailing slash.
	Endpoint string

	// Timeout caps each individual POST. Defaults to 10s when zero.
	Timeout time.Duration

	// InstanceID is the value emitted as the OTLP resource attribute
	// service.instance.id. When empty, falls back to os.Hostname() — which
	// is the container ID under Docker/Kubernetes and is therefore not stable
	// across pod restarts. Federation spoke deployments should pass the
	// configured spoke_id here so the instance identity is stable.
	InstanceID string
}

// Exporter pushes topology data to an OTLP/HTTP endpoint.
type Exporter struct {
	cfg        Config
	client     *http.Client
	resourceID resource
}

// New returns an Exporter configured with cfg. A zero Timeout in cfg is
// replaced with 10 seconds.
func New(cfg Config) *Exporter {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	instanceID := cfg.InstanceID
	if instanceID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			slog.Warn("otlp: os.Hostname() failed; service.instance.id will be empty",
				"error", err,
				"recommendation", "set InstanceID explicitly (e.g. federation.spoke.spoke_id)")
		}
		instanceID = hostname
	}
	return &Exporter{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		resourceID: resource{
			Attributes: []kv{
				{Key: "service.name", Value: kvValue{StringValue: serviceName}},
				{Key: "service.version", Value: kvValue{StringValue: version.Version}},
				{Key: "service.instance.id", Value: kvValue{StringValue: instanceID}},
			},
		},
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
			{Key: "proto", Value: kvValue{StringValue: sanitizeUTF8(string(edge.DiscoveryProto))}},
			{Key: "link_kind", Value: kvValue{StringValue: sanitizeUTF8(string(edge.LinkKind))}},
			{Key: "direction", Value: kvValue{StringValue: sanitizeUTF8(string(edge.Direction))}},
			{Key: "confidence", Value: kvValue{StringValue: sanitizeUTF8(string(edge.Confidence))}},
			{Key: "adjacency", Value: kvValue{StringValue: sanitizeUTF8(string(edge.Adjacency))}},
			{Key: "precedence_rank", Value: kvValue{StringValue: strconv.Itoa(edge.PrecedenceRank)}},
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
		attrs := []kv{
			{Key: "device", Value: kvValue{StringValue: sanitizeUTF8(dev.ID)}},
		}
		if dev.Vendor != "" {
			attrs = append(attrs, kv{Key: "vendor", Value: kvValue{StringValue: sanitizeUTF8(dev.Vendor)}})
		}
		if dev.Model != "" {
			attrs = append(attrs, kv{Key: "model", Value: kvValue{StringValue: sanitizeUTF8(dev.Model)}})
		}
		if dev.OSVersion != "" {
			attrs = append(attrs, kv{Key: "os_version", Value: kvValue{StringValue: sanitizeUTF8(dev.OSVersion)}})
		}
		if dev.Site != "" {
			attrs = append(attrs, kv{Key: "site", Value: kvValue{StringValue: sanitizeUTF8(dev.Site)}})
		}
		devicePoints = append(devicePoints, dataPoint{
			Attributes:   attrs,
			AsDouble:     1.0,
			TimeUnixNano: now,
		})
	}

	payload := metricsPayload{
		ResourceMetrics: []resourceMetrics{
			{
				SchemaUrl: otlpSchemaURL,
				Resource:  e.resourceID,
				ScopeMetrics: []scopeMetrics{
					{
						Scope: scope{Name: scopeName},
						Metrics: []metric{
							{
								Name:  "network_topology_edge_info",
								Gauge: gauge{DataPoints: edgePoints},
							},
							{
								Name:  "network_topology_device_info",
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
			proto = string(c.After.DiscoveryProto)
			linkKind = string(c.After.LinkKind)
		case c.Before != nil:
			srcDevice = c.Before.SrcDevice
			srcPort = c.Before.SrcPort
			dstDevice = c.Before.DstDevice
			dstPort = c.Before.DstPort
			proto = string(c.Before.DiscoveryProto)
			linkKind = string(c.Before.LinkKind)
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
				Resource:  e.resourceID,
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

// httpStatusError carries the receiver's HTTP status code so the caller's
// metric classifier can partition 4xx vs 5xx without parsing error text.
// Issue #20: network_topology_otlp_push_total{reason=...}.
type httpStatusError struct {
	statusCode int
	path       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("otlp: post %s: server returned %d", e.path, e.statusCode)
}

// StatusCode returns the HTTP status code that produced this error. Used
// by ClassifyPushError; exported for callers that build their own
// classifier.
func (e *httpStatusError) StatusCode() int { return e.statusCode }

// ClassifyPushError maps a non-nil error returned by PushGraph or
// PushChanges to a metrics.PushReason for use as the `reason` label on
// network_topology_otlp_push_total{status="error"}. err must be non-nil;
// passing nil panics — call sites should use metrics.ReasonNA for the
// status="ok" path. The returned value is guaranteed to satisfy
// metrics.PushReason.Valid().
//
// Categorisation:
//   - context.DeadlineExceeded                     → PushReasonTimeout
//   - *tls.CertificateVerificationError or wrapped → PushReasonTLSError
//     "tls:" prefix from net/http
//   - *httpStatusError, 4xx                        → PushReasonHTTP4xx
//   - *httpStatusError, 5xx                        → PushReasonHTTP5xx
//   - everything else                              → PushReasonNetwork
//
// The catch-all PushReasonNetwork covers DNS failures, connection
// refused, EOF, and other transport-layer faults. Operators alerting on
// "collector is down" should match on PushReasonNetwork.
func ClassifyPushError(err error) metrics.PushReason {
	if err == nil {
		panic("otlp: ClassifyPushError called with nil error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return metrics.PushReasonTimeout
	}
	// TLS verification errors surface as *tls.CertificateVerificationError
	// from crypto/tls; older Go versions and some wrap paths surface them
	// as a plain error containing "tls:". Match both shapes.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return metrics.PushReasonTLSError
	}
	if msg := err.Error(); strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") {
		return metrics.PushReasonTLSError
	}
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.statusCode >= 400 && httpErr.statusCode < 500:
			return metrics.PushReasonHTTP4xx
		case httpErr.statusCode >= 500 && httpErr.statusCode < 600:
			return metrics.PushReasonHTTP5xx
		}
	}
	return metrics.PushReasonNetwork
}

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

		// Wrap status-code failures in *httpStatusError so the metric
		// classifier (ClassifyPushError) can partition 4xx vs 5xx without
		// parsing the error text. Issue #20.
		lastErr = &httpStatusError{statusCode: resp.StatusCode, path: path}

		// Only retry on transient errors: 429, 502, 503, 504.
		switch resp.StatusCode {
		case http.StatusTooManyRequests, // 429
			http.StatusBadGateway,         // 502
			http.StatusServiceUnavailable, // 503
			http.StatusGatewayTimeout:     // 504
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
