package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/limits"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// Per-push payload caps. These bound the size of a single spoke push and are
// federation-specific (not per-field). The per-field byte caps live in
// internal/limits because they are shared with the snapshot loader; raising
// them in one place without the other would silently desynchronise wire-format
// acceptance and on-disk validation. See limits.MaxDeviceIDBytes etc.
const (
	maxDevicesPerPush = 10_000
	maxEdgesPerPush   = 50_000
)

type spokeEntry struct {
	payload  SpokePayload
	lastSeen time.Time
}

// acceptedPush carries the spoke state to commit atomically with a winning
// publication. entry.lastSeen is the accept time used for the liveness gauge.
// A nil *acceptedPush (e.g. from eviction) publishes the graph without
// registering any spoke or touching liveness metrics.
type acceptedPush struct {
	id    string
	entry spokeEntry
}

// Hub aggregates SpokePayload pushes from spoke instances, reconciles the
// combined edge set across all spoke domains, and updates the shared
// Prometheus metrics with the unified topology. Per LD-16, spokes push;
// the hub never polls spokes.
type Hub struct {
	cfg                  config.FederationConfig
	mu                   sync.Mutex
	spokes               map[string]spokeEntry
	m                    *metrics.Metrics
	logger               *slog.Logger
	snapshotPath         string
	snapshotCh           chan discovery.Graph
	firstLive            atomic.Bool // set to true on the first live publishMetrics call
	snapshotWriteFn      func(string, snapshot.File) error
	snapshotWriteTimeout time.Duration
	publishGen           atomic.Uint64
	// lastPublishedGen is the generation of the last graph actually published.
	// Guarded by mu (read+written only inside publishIfWinner). Plain uint64,
	// not atomic: the CAS loop is gone now that gen comparison happens under mu.
	lastPublishedGen uint64
}

// NewHub constructs a Hub ready to accept spoke pushes. snapshotPath enables
// LD-13 persistence; pass "" to disable snapshot writes (e.g., in tests).
func NewHub(cfg config.FederationConfig, m *metrics.Metrics, logger *slog.Logger, snapshotPath string) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hub{
		cfg:                  cfg,
		spokes:               make(map[string]spokeEntry),
		m:                    m,
		logger:               logger,
		snapshotPath:         snapshotPath,
		snapshotWriteFn:      snapshot.Write,
		snapshotWriteTimeout: 30 * time.Second,
	}
	if snapshotPath != "" {
		h.snapshotCh = make(chan discovery.Graph, 1)
	}
	return h
}

// recoverGoroutine is the hub's panic-recovery body for its long-lived
// background goroutines (eviction loop, snapshot writer). Without it, a panic
// in any of them crashes the whole aggregator and destroys the combined graph
// for every spoke. It mirrors the per-device recover block in
// internal/app/cycle.go: it logs the panic value plus stack trace at Error
// level, increments network_topology_panics_total{site} so the bug is never
// hidden silently, and returns cleanly (does NOT re-panic) so the goroutine
// dies gracefully. The hub already holds its own *metrics.Metrics handle
// (h.m), so no injection seam is needed here — unlike the discovery modules,
// the federation package legitimately depends on internal/metrics. h.m is
// tolerated as nil (skips the counter) so tests can construct a bare Hub.
//
// Used as the first deferred call at the top of a goroutine body, e.g.
// `defer h.recoverGoroutine("hub_rebuild")`. site must be one of the closed,
// low-cardinality strings documented on metrics.PanicsRecoveredTotal.
func (h *Hub) recoverGoroutine(site string) {
	r := recover()
	if r == nil {
		return
	}
	h.logger.Error("hub background goroutine panicked; recovered",
		"site", site,
		"panic", r,
		"stack", string(debug.Stack()),
	)
	if h.m != nil {
		h.m.PanicsRecoveredTotal.WithLabelValues(site).Inc()
	}
}

// RestoreGraph populates hub metrics from a snapshot loaded at startup so the
// hub can serve stale-but-valid metrics (GraphStale=1) until the first live
// spoke push arrives (LD-13). The caller must set m.GraphStale=1 before
// invoking this; the hub clears it after the first successful push.
func (h *Hub) RestoreGraph(g discovery.Graph) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishMetrics(g, false)
}

// Serve starts the mTLS federation server and the spoke-eviction goroutine,
// blocking until ctx is cancelled. Per LD-20, connections without a valid
// client certificate signed by the configured CA are rejected at the TLS
// handshake before any payload is read.
func (h *Hub) Serve(ctx context.Context) error {
	caCert, err := os.ReadFile(h.cfg.Hub.TLSCACert)
	if err != nil {
		return fmt.Errorf("hub: read CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return fmt.Errorf("hub: no CA certs parsed from %q", h.cfg.Hub.TLSCACert)
	}
	serverCert, err := tls.LoadX509KeyPair(h.cfg.Hub.TLSCert, h.cfg.Hub.TLSKey)
	if err != nil {
		return fmt.Errorf("hub: load server cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/spoke/push", h.handlePush)

	srv := &http.Server{
		Addr:              h.cfg.Hub.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", h.cfg.Hub.ListenAddr)
	if err != nil {
		return fmt.Errorf("hub: listen on %s: %w", h.cfg.Hub.ListenAddr, err)
	}
	tlsLn := tls.NewListener(ln, tlsCfg)

	go h.runEviction(ctx)
	if h.snapshotCh != nil {
		go h.runSnapshotWriter(ctx)
	}

	go func() { //nolint:gosec
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	h.logger.Info("hub federation server listening", "addr", h.cfg.Hub.ListenAddr)
	if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("hub server: %w", err)
	}
	return nil
}

// labelKeyPattern is the canonical Prometheus / OpenMetrics label-name shape:
// an ASCII letter or underscore followed by any number of ASCII letters,
// digits, or underscores. Names starting with `__` are reserved by Prometheus
// and rejected separately below. Compiled once at package init.
var labelKeyPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validationError wraps a validateSpokePayload failure with a machine-parseable
// reject reason. Handlers unwrap this to route the reject through the
// structured pushRejection JSON response so spokes (and dashboards) can branch
// on reason rather than parsing free-form message text. msg is the
// human-readable detail logged and included in the rejection detail map.
type validationError struct {
	reason metrics.RejectReason
	msg    string
}

func (e *validationError) Error() string { return e.msg }

func newValidationError(reason metrics.RejectReason, format string, args ...any) *validationError {
	return &validationError{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// validateLabelKey enforces the Prometheus label-name grammar plus the
// reserved-namespace rule. A malformed key would break /metrics line protocol
// on every subsequent scrape — the hub is the only enforcement point because
// mTLS authenticates WHO can push, not WHAT they push.
func validateLabelKey(k string) error {
	if k == "" {
		return newValidationError(rejectReasonInvalidLabelKey, "label key must not be empty")
	}
	// Size cap runs before the regex match so a 16 MiB key cannot force the
	// regex engine (or any future per-rune check) to walk the whole string.
	if len(k) > limits.MaxLabelKeyBytes {
		return newValidationError(rejectReasonInvalidLabelKey,
			"label key exceeds %d bytes", limits.MaxLabelKeyBytes)
	}
	if strings.HasPrefix(k, "__") {
		return newValidationError(rejectReasonInvalidLabelKey,
			"label key %q starts with reserved prefix \"__\"", k)
	}
	if !labelKeyPattern.MatchString(k) {
		return newValidationError(rejectReasonInvalidLabelKey,
			"label key %q does not match %s", k, labelKeyPattern.String())
	}
	return nil
}

// validateLabelValue rejects values containing characters that corrupt the
// OpenMetrics exposition line protocol (NUL, newline, carriage return) or
// other control characters that pass through Prometheus client_golang's
// escaping but render label-based dashboards unreadable. Non-control UTF-8
// (including quotes and backslashes — which the client library escapes
// correctly on emission) is allowed. The caller has already checked that the
// string is valid UTF-8 and within length bounds.
func validateLabelValue(v string) error {
	// Size cap runs before per-rune iteration so a 16 MiB value cannot force
	// ~4M iterations of the control-char check. Prometheus / OpenMetrics
	// impose no formal max on label value length (docs/remediation.md §3), but
	// client_golang defaults and Grafana Cloud Mimir limits operate well
	// under 4 KiB per value, so 4096 bytes is a safe upper bound.
	if len(v) > limits.MaxLabelValueBytes {
		return newValidationError(rejectReasonInvalidLabelValue,
			"label value exceeds %d bytes", limits.MaxLabelValueBytes)
	}
	for _, r := range v {
		if r == 0x00 || r == '\n' || r == '\r' {
			return newValidationError(rejectReasonInvalidLabelValue,
				"label value contains forbidden control char %#U", r)
		}
		// Reject all C0 controls (0x00..0x1F) and DEL (0x7F). The explicit
		// cases above are listed first so error messages point at the most
		// common injection vectors with their familiar names.
		if r < 0x20 || r == 0x7F {
			return newValidationError(rejectReasonInvalidLabelValue,
				"label value contains forbidden control char %#U", r)
		}
	}
	return nil
}

// validateMetricLabelString validates a string that becomes a Prometheus
// label *value* (not key) on a metric with a static label name. Currently
// applies to spoke-supplied edge port/device names and OOS-neighbour fields.
// Same rules as validateLabelValue, which now enforces the
// limits.MaxLabelValueBytes size cap before per-rune iteration — callers
// inherit that cap transitively. Upstream callers also apply field-specific
// caps (limits.MaxPortNameBytes, limits.MaxDeviceIDBytes) which are tighter;
// the cap inside validateLabelValue is the universal floor that prevents CPU
// DoS on any path that reaches here.
func validateMetricLabelString(s string) error {
	return validateLabelValue(s)
}

// validateSpokePayload checks semantic invariants that the JSON decoder and size
// guards cannot catch: empty/duplicate/overlong/non-UTF-8 device IDs, required
// edge fields, self-edges, overlong/non-UTF-8 port names, and Prometheus
// line-protocol safety for every spoke-supplied string that flows into a
// metric label name or value.
//
// Every returned error is a *validationError carrying a stable reject-reason
// code so the caller can route it through the structured pushRejection JSON
// response. Two reason flavours are emitted:
//   - rejectReasonInvalidLabelKey / rejectReasonInvalidLabelValue for failures
//     specifically about Prometheus line-protocol safety on a label name/value.
//   - rejectReasonStructuralInvalid for shape failures (empty required field,
//     oversize, invalid UTF-8 in non-label fields, duplicate device, self-edge).
//
// The handlePush *validationError invariant is load-bearing: handlePush panics
// on any non-*validationError return from this function (issue #19). Every new
// validation site MUST return newValidationError(...) — never plain fmt.Errorf.
func validateSpokePayload(p SpokePayload) error {
	seen := make(map[string]bool, len(p.Devices))
	for i, d := range p.Devices {
		if d.ID == "" {
			return newValidationError(rejectReasonStructuralInvalid,
				"device[%d]: device_id is empty", i)
		}
		if len(d.ID) > limits.MaxDeviceIDBytes {
			return newValidationError(rejectReasonStructuralInvalid,
				"device[%d]: device_id exceeds %d bytes", i, limits.MaxDeviceIDBytes)
		}
		if !utf8.ValidString(d.ID) {
			return newValidationError(rejectReasonStructuralInvalid,
				"device[%d]: device_id is not valid UTF-8", i)
		}
		if err := validateMetricLabelString(d.ID); err != nil {
			return newValidationError(rejectReasonInvalidLabelValue,
				"device[%d]: device_id: %s", i, err.Error())
		}
		if seen[d.ID] {
			return newValidationError(rejectReasonStructuralInvalid,
				"device[%d]: duplicate device_id %q", i, d.ID)
		}
		seen[d.ID] = true
		// Validate inventory string fields that flow into device_info labels
		// (vendor, model, os_version, site). The label *names* are static so
		// only the values need protocol-safety checks.
		for _, f := range []struct{ name, val string }{
			{"vendor", d.Vendor}, {"model", d.Model},
			{"os_version", d.OSVersion}, {"site", d.Site},
		} {
			if !utf8.ValidString(f.val) {
				return newValidationError(rejectReasonInvalidLabelValue,
					"device[%d]: %s is not valid UTF-8", i, f.name)
			}
			if err := validateMetricLabelString(f.val); err != nil {
				return newValidationError(rejectReasonInvalidLabelValue,
					"device[%d]: %s: %s", i, f.name, err.Error())
			}
		}
		for k, v := range d.Labels {
			if err := validateLabelKey(k); err != nil {
				return newValidationError(rejectReasonInvalidLabelKey,
					"device[%d]: %s", i, err.Error())
			}
			if !utf8.ValidString(v) {
				return newValidationError(rejectReasonInvalidLabelValue,
					"device[%d]: label %q value is not valid UTF-8", i, k)
			}
			if err := validateLabelValue(v); err != nil {
				return newValidationError(rejectReasonInvalidLabelValue,
					"device[%d]: label %q: %s", i, k, err.Error())
			}
		}
	}
	for i, e := range p.Edges {
		if e.SrcDevice == "" || e.SrcPort == "" || e.DstDevice == "" {
			return newValidationError(rejectReasonStructuralInvalid,
				"edge[%d]: src_device, src_port, and dst_device are required", i)
		}
		if e.SrcDevice == e.DstDevice {
			return newValidationError(rejectReasonStructuralInvalid,
				"edge[%d]: self-edge (src_device == dst_device == %q)", i, e.SrcDevice)
		}
		for _, f := range []struct{ name, val string }{
			{"src_device", e.SrcDevice}, {"src_port", e.SrcPort},
			{"dst_device", e.DstDevice}, {"dst_port", e.DstPort},
			{"discovery_proto", string(e.DiscoveryProto)}, {"link_kind", string(e.LinkKind)},
		} {
			if len(f.val) > limits.MaxPortNameBytes {
				return newValidationError(rejectReasonStructuralInvalid,
					"edge[%d]: %s exceeds %d bytes", i, f.name, limits.MaxPortNameBytes)
			}
			if !utf8.ValidString(f.val) {
				return newValidationError(rejectReasonStructuralInvalid,
					"edge[%d]: %s is not valid UTF-8", i, f.name)
			}
			if err := validateMetricLabelString(f.val); err != nil {
				return newValidationError(rejectReasonInvalidLabelValue,
					"edge[%d]: %s: %s", i, f.name, err.Error())
			}
		}
		// Edge.Metadata is a map[string]string that flows into OTLP
		// attribute names+values via internal/output/otlp/otlp.go:201 (key
		// prefixed with metadataAttrPrefix). Not a Prometheus label path —
		// TopologyCollector does not emit it. Threat surface is therefore:
		// log-line corruption (control chars), JSON encoding bloat (huge
		// values), and OTLP attribute pollution (oversized keys/values).
		// Validate accordingly:
		//   - Cap key and value size (matches snapshot.go's caps; #22).
		//   - Reject control characters in both keys and values.
		//   - Do NOT enforce the Prometheus label-name grammar on keys:
		//     production discovery code uses dotted keys like
		//     "bgp.remote_as" and "mpls_te.admin_status" that conform to
		//     OTLP attribute-name conventions but not Prometheus's
		//     [a-zA-Z_][a-zA-Z0-9_]* grammar. Enforcing the strict shape
		//     would reject every legitimate BGP/MPLS push.
		// Issue #25 closed a gap left by #4 / D26.
		for k, v := range e.Metadata {
			if k == "" {
				return newValidationError(rejectReasonInvalidLabelKey,
					"edge[%d]: metadata key must not be empty", i)
			}
			if len(k) > limits.MaxLabelKeyBytes {
				return newValidationError(rejectReasonInvalidLabelKey,
					"edge[%d]: metadata key exceeds %d bytes", i, limits.MaxLabelKeyBytes)
			}
			if !utf8.ValidString(k) {
				return newValidationError(rejectReasonInvalidLabelKey,
					"edge[%d]: metadata key is not valid UTF-8", i)
			}
			for _, r := range k {
				if r == 0x00 || r == '\n' || r == '\r' || r < 0x20 || r == 0x7F {
					return newValidationError(rejectReasonInvalidLabelKey,
						"edge[%d]: metadata key contains forbidden control char %#U", i, r)
				}
			}
			if !utf8.ValidString(v) {
				return newValidationError(rejectReasonInvalidLabelValue,
					"edge[%d]: metadata %q value is not valid UTF-8", i, k)
			}
			if err := validateLabelValue(v); err != nil {
				return newValidationError(rejectReasonInvalidLabelValue,
					"edge[%d]: metadata %q: %s", i, k, err.Error())
			}
		}
	}
	for i, n := range p.OutOfScope {
		for _, f := range []struct{ name, val string }{
			{"reporting_device", n.ReportingDevice},
			{"reporting_port", n.ReportingPort},
			{"neighbour_hint", n.NeighbourHint},
			{"proto", n.Proto},
		} {
			if len(f.val) > limits.MaxPortNameBytes {
				return newValidationError(rejectReasonStructuralInvalid,
					"out_of_scope[%d]: %s exceeds %d bytes", i, f.name, limits.MaxPortNameBytes)
			}
			if !utf8.ValidString(f.val) {
				return newValidationError(rejectReasonInvalidLabelValue,
					"out_of_scope[%d]: %s is not valid UTF-8", i, f.name)
			}
			if err := validateMetricLabelString(f.val); err != nil {
				return newValidationError(rejectReasonInvalidLabelValue,
					"out_of_scope[%d]: %s: %s", i, f.name, err.Error())
			}
		}
	}
	return nil
}

func (h *Hub) handlePush(w http.ResponseWriter, r *http.Request) {
	// Issue #68: continue the spoke's trace. Extract the W3C traceparent the
	// spoke injected into the request headers and start hub.handlePush as a
	// child of the spoke.push span, so the hub span shares the spoke's trace
	// ID. When the spoke is not tracing, no traceparent is present and this
	// span starts a fresh (unsampled-by-default) root.
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := tracing.Tracer().Start(ctx, "hub.handlePush")
	defer span.End()
	r = r.WithContext(ctx)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var payload SpokePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)) // 16 MiB
	if err := dec.Decode(&payload); err != nil {
		h.logger.Warn("hub: malformed spoke payload", "error", err)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large (max 16 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(payload.Devices) > maxDevicesPerPush || len(payload.Edges) > maxEdgesPerPush {
		http.Error(w, fmt.Sprintf("payload exceeds limits (max %d devices, %d edges)", maxDevicesPerPush, maxEdgesPerPush), http.StatusRequestEntityTooLarge)
		return
	}
	if err := validateSpokePayload(payload); err != nil {
		h.logger.Warn("hub: spoke payload failed semantic validation",
			"spoke_id", payload.SpokeID, "error", err)
		var verr *validationError
		if !errors.As(err, &verr) {
			// Invariant: every validateSpokePayload error path returns
			// *validationError (enforced by issue #19). If this branch fires,
			// a new validation site was added that bypassed
			// newValidationError — a code defect that would silently
			// mislabel rejects in the GraphUpdatesRejectedTotal counter and
			// strip the structured pushRejection wire contract. Panic so the
			// defect surfaces at the first request that hits it, rather than
			// degrading observability silently. http.Server recovers from
			// handler panics, so this affects only the offending request.
			panic(fmt.Sprintf("federation: validateSpokePayload returned non-*validationError: %T %v", err, err))
		}
		h.m.GraphUpdatesRejectedTotal.WithLabelValues(string(verr.reason)).Inc()
		// Structured reject: spokes branch on the reason enum, not text.
		writePushRejection(w, http.StatusBadRequest, verr.reason, map[string]any{
			"message": verr.msg,
		})
		return
	}
	if payload.SpokeID == "" {
		http.Error(w, "spoke_id required", http.StatusBadRequest)
		return
	}
	span.SetAttributes(attribute.String("spoke.id", payload.SpokeID))
	if len(payload.SpokeID) > 128 {
		http.Error(w, "spoke_id too long (max 128)", http.StatusBadRequest)
		return
	}
	for _, c := range payload.SpokeID {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			http.Error(w, "spoke_id contains invalid characters (allowed: a-z A-Z 0-9 - _ .)", http.StatusBadRequest)
			return
		}
	}

	// LD-21: bind spoke_id to the presenting mTLS client certificate's Common
	// Name so a spoke holding a valid cert cannot overwrite another spoke's
	// topology data. r.TLS is nil in unit tests (httptest has no TLS); in
	// production ClientAuth: RequireAndVerifyClientCert guarantees at least one
	// peer certificate is present.
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		certCN := r.TLS.PeerCertificates[0].Subject.CommonName
		if certCN != payload.SpokeID {
			h.logger.Warn("hub: spoke_id/cert CN mismatch — rejecting push",
				"spoke_id", payload.SpokeID,
				"cert_cn", certCN,
			)
			http.Error(w, fmt.Sprintf("spoke_id %q does not match client certificate CN %q", payload.SpokeID, certCN), http.StatusForbidden)
			return
		}
	}

	// Validate CycleAt: must be present, not in the future, and not older than
	// spoke_timeout (which would indicate a lost/replayed push).
	now := time.Now()
	if payload.CycleAt.IsZero() {
		http.Error(w, "cycle_at required", http.StatusBadRequest)
		return
	}
	if payload.CycleAt.After(now.Add(5 * time.Minute)) {
		http.Error(w, "cycle_at is too far in the future", http.StatusBadRequest)
		return
	}
	if now.Sub(payload.CycleAt) > h.cfg.SpokeTimeout {
		h.logger.Warn("hub: rejecting stale spoke payload",
			"spoke_id", payload.SpokeID,
			"cycle_at", payload.CycleAt,
			"age", now.Sub(payload.CycleAt),
			"spoke_timeout", h.cfg.SpokeTimeout,
		)
		http.Error(w, "cycle_at too stale", http.StatusBadRequest)
		return
	}

	// Build the combined graph with the new spoke included, but defer writing
	// h.spokes and updating the spoke-up metrics until after tryPublishMetrics
	// confirms the graph was accepted. This prevents a spoke from appearing "up"
	// in Prometheus when the combined graph exceeds the size budget and is never
	// published — which would otherwise make the spoke look healthy while
	// contributing zero edges to the topology.
	h.mu.Lock()
	prevEntry, hadPrev := h.spokes[payload.SpokeID]
	// Defense-in-depth rate limit: reject pushes that arrive sooner than
	// min_push_interval after the previous accepted push from the same
	// spoke_id. The check runs inside h.mu so two concurrent racing pushes
	// cannot both pass.
	if hadPrev && h.cfg.Hub.MinPushInterval > 0 {
		sinceLast := now.Sub(prevEntry.lastSeen)
		if sinceLast < h.cfg.Hub.MinPushInterval {
			h.mu.Unlock()
			retryAfter := h.cfg.Hub.MinPushInterval - sinceLast
			retrySecs := int(retryAfter.Seconds())
			if retrySecs < 1 {
				retrySecs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retrySecs))
			h.logger.Info("hub: rejecting push within min_push_interval",
				"spoke_id", payload.SpokeID,
				"since_last_push", sinceLast,
				"min_push_interval", h.cfg.Hub.MinPushInterval,
			)
			http.Error(w, "push too soon — observe min_push_interval", http.StatusTooManyRequests)
			return
		}
	}
	h.spokes[payload.SpokeID] = spokeEntry{payload: payload, lastSeen: now}
	spokes := h.spokesSnapshot()
	gen := h.publishGen.Add(1)
	h.mu.Unlock()
	combined, unmatchedCount := h.buildCombinedGraph(spokes)

	// LD-13: publishIfWinner clears GraphStale and commits the spoke liveness
	// gauges atomically under h.mu when the graph wins, so a concurrent scrape
	// never sees fresh edges alongside GraphStale=1 and a concurrent eviction
	// can never resurrect a deleted gauge.
	entry := spokeEntry{payload: payload, lastSeen: now}
	published, rejectReason := h.publishIfWinner(gen, combined, unmatchedCount, &acceptedPush{id: payload.SpokeID, entry: entry})
	if published {
		h.writeSnapshotAsync(combined)
		h.logger.Info("hub: spoke push accepted",
			"spoke_id", payload.SpokeID,
			"devices", len(payload.Devices),
			"edges", len(payload.Edges),
			"cycle_at", payload.CycleAt,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Graph was rejected: roll back the spoke entry so h.spokes reflects only
	// previously-accepted state, then return a 4xx that distinguishes the
	// reason. A 204 here would silently mislead the spoke into believing the
	// push succeeded. Both codes are 4xx-fatal in the spoke's retry policy so
	// the spoke does not burn retries on the same payload; the next discovery
	// cycle will produce fresh data.
	h.mu.Lock()
	if hadPrev {
		h.spokes[payload.SpokeID] = prevEntry
	} else {
		delete(h.spokes, payload.SpokeID)
	}
	h.mu.Unlock()
	h.logger.Warn("hub: spoke push rejected — combined graph not applied",
		"spoke_id", payload.SpokeID,
		"reject_reason", rejectReason,
		"combined_devices", len(combined.Devices),
		"combined_edges", len(combined.Edges),
		"max_devices", h.cfg.Hub.MaxGraphDevices,
		"max_edges", h.cfg.Hub.MaxGraphEdges,
		"cycle_at", payload.CycleAt,
	)
	writePushRejection(w, statusForRejectReason(rejectReason), rejectReason, map[string]any{
		"combined_devices": len(combined.Devices),
		"combined_edges":   len(combined.Edges),
		"max_devices":      h.cfg.Hub.MaxGraphDevices,
		"max_edges":        h.cfg.Hub.MaxGraphEdges,
	})
}

// pushRejection is the JSON body returned when a spoke push is accepted by the
// transport but the resulting graph is not applied to active hub state. reason
// is a stable machine-parseable code; detail is a free-form map for operator
// context. Schema: {"status":"rejected","reason":"<code>","detail":{...}}.
//
// Reason is typed metrics.RejectReason rather than bare string so the
// encoder cannot accept untyped-string smuggling at the call site; the
// JSON encoding of `type RejectReason string` is byte-identical to a bare
// string (pinned by TestRejectReasonWireValuesPinned in internal/metrics).
type pushRejection struct {
	Status string               `json:"status"` // always "rejected"
	Reason metrics.RejectReason `json:"reason"` // one of the metrics.RejectReason* constants
	Detail map[string]any       `json:"detail,omitempty"`
}

// statusForRejectReason maps a reject reason to its HTTP status code.
// 413 and 409 are 4xx so the spoke's retry policy treats them as fatal for
// this cycle — the next discovery cycle will produce fresh data. 503 is
// documented in the response contract for transient internal failures; no
// path currently emits it, but future reject reasons representing "we
// couldn't apply this right now, try later" (e.g. snapshot back-pressure,
// downstream sink stall) should map to the default arm here.
func statusForRejectReason(reason metrics.RejectReason) int {
	switch reason {
	case rejectReasonSizeBudgetExceeded:
		return http.StatusRequestEntityTooLarge // 413
	case rejectReasonStaleGeneration:
		return http.StatusConflict // 409
	case rejectReasonInvalidLabelKey, rejectReasonInvalidLabelValue, rejectReasonStructuralInvalid:
		return http.StatusBadRequest // 400: fatal — same payload will fail identically
	default:
		return http.StatusServiceUnavailable // 503: documented for transient internal failures
	}
}

func writePushRejection(w http.ResponseWriter, code int, reason metrics.RejectReason, detail map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(pushRejection{
		Status: "rejected",
		Reason: reason,
		Detail: detail,
	})
}

// spokesSnapshot returns a shallow copy of h.spokes. Caller must hold h.mu.
func (h *Hub) spokesSnapshot() map[string]spokeEntry {
	s := make(map[string]spokeEntry, len(h.spokes))
	for k, v := range h.spokes {
		s[k] = v
	}
	return s
}

// combinedGraphLocked is a convenience wrapper for tests that call it while
// already holding h.mu. Production paths use spokesSnapshot + buildCombinedGraph
// to move Reconcile outside the critical section.
func (h *Hub) combinedGraphLocked() discovery.Graph {
	g, _ := h.buildCombinedGraph(h.spokes)
	return g
}

// buildCombinedGraph constructs the unified discovery.Graph from the supplied
// spoke snapshot and the configured known inter-domain links. It runs a second
// graph.Reconcile pass so cross-boundary bidirectionality is detected at the
// hub level per LD-17. Does NOT require h.mu — call spokesSnapshot() under the
// lock first, then release h.mu before calling this function.
// The second return value is the count of unmatched OOS observations; callers
// should only publish this via HubOOSUnmatchedTotal after confirming the build
// wins the CAS in tryPublishMetrics.
func (h *Hub) buildCombinedGraph(spokes map[string]spokeEntry) (discovery.Graph, int) {
	var allDevices []discovery.Device
	seenDevices := make(map[string]bool)
	var allEdges []discovery.Edge

	spokeKeys := make([]string, 0, len(spokes))
	for k := range spokes {
		spokeKeys = append(spokeKeys, k)
	}
	sort.Strings(spokeKeys)

	for _, sk := range spokeKeys {
		entry := spokes[sk]
		for _, dev := range entry.payload.Devices {
			if !seenDevices[dev.ID] {
				seenDevices[dev.ID] = true
				allDevices = append(allDevices, dev)
			} else {
				h.logger.Warn("hub: duplicate device_id across spokes; using first alphabetically",
					"device_id", dev.ID, "spoke_id", sk)
			}
		}
		for _, e := range entry.payload.Edges {
			allEdges = append(allEdges, e)
			// A bidirectional edge in the spoke's pre-reconciled graph means
			// both sides were observed within that domain. Inject the reverse
			// observation so the hub's Reconcile pass also sees both sides and
			// preserves the bidirectional status.
			if e.Direction == discovery.DirectionBidirectional {
				rev := e
				rev.SrcDevice, rev.DstDevice = e.DstDevice, e.SrcDevice
				rev.SrcPort, rev.DstPort = e.DstPort, e.SrcPort
				allEdges = append(allEdges, rev)
			}
		}
	}

	// Cross-boundary edge detection via OOS name-matching (LD-15/LD-19
	// best-effort path). For each out-of-scope observation from spoke A
	// where NeighbourHint matches another spoke B's ReportingDevice, and B
	// has a matching reverse observation, synthesise a pair of one-sided
	// edges so the hub's Reconcile pass can detect bidirectionality.
	//
	// oosIndex uses a slice value to handle multi-port links (e.g. LAG bonds
	// where the same device pair appears on multiple port combinations).
	type oosKey struct{ device, hint string }
	type oosVal struct {
		reportingPort string
		proto         string
	}
	oosIndex := make(map[oosKey][]oosVal)

	// Detection pass: build canonical→[]raw maps to warn when distinct raw names
	// collide on the same canonical name (e.g. "core-sw-01.dc1" and
	// "core-sw-01.dc2" both normalise to "core-sw-01"). Logged at most once per
	// canonical name per merge cycle via the warnedNames guard.
	rawNamesForCanonical := make(map[string]map[string]struct{})
	for _, entry := range spokes {
		for _, n := range entry.payload.OutOfScope {
			for _, raw := range []string{n.ReportingDevice, n.NeighbourHint} {
				canon := h.canonicalizeDeviceName(raw)
				if rawNamesForCanonical[canon] == nil {
					rawNamesForCanonical[canon] = make(map[string]struct{})
				}
				rawNamesForCanonical[canon][raw] = struct{}{}
			}
		}
	}
	warnedNames := make(map[string]bool)
	for canon, rawSet := range rawNamesForCanonical {
		if len(rawSet) > 1 {
			if !warnedNames[canon] {
				warnedNames[canon] = true
				rawList := make([]string, 0, len(rawSet))
				for r := range rawSet {
					rawList = append(rawList, r)
				}
				h.logger.Warn("hub: ambiguous device name normalisation — multiple raw names map to the same canonical name; check for FQDN collisions",
					"canonical", canon,
					"raw_names", rawList,
				)
			}
		}
	}

	for _, entry := range spokes {
		for _, n := range entry.payload.OutOfScope {
			k := oosKey{h.canonicalizeDeviceName(n.ReportingDevice), h.canonicalizeDeviceName(n.NeighbourHint)}
			oosIndex[k] = append(oosIndex[k], oosVal{n.ReportingPort, n.Proto})
		}
	}

	type pairKey struct{ a, b string }
	type portPairKey struct{ aPort, bPort string }
	visited := make(map[pairKey]map[portPairKey]bool)

	var unmatchedCount int
	for k, locals := range oosIndex {
		remotes, ok := oosIndex[oosKey{k.hint, k.device}]
		if !ok {
			// Log at debug so operators can spot naming inconsistencies (e.g.
			// "Gi0/1" on one side vs "GigabitEthernet0/1" on the other) when
			// troubleshooting missing cross-domain edges.
			h.logger.Debug("hub: OOS neighbour has no reverse observation; possible naming mismatch",
				"device", k.device, "hint", k.hint)
			unmatchedCount++
			continue
		}

		a, b := k.device, k.hint
		if a > b {
			a, b = b, a
		}
		pk := pairKey{a, b}
		if visited[pk] == nil {
			visited[pk] = make(map[portPairKey]bool)
		}

		for _, local := range locals {
			for _, remote := range remotes {
				// Canonical port pair: port of the alphabetically-smaller device first,
				// so (sw-a,sw-b) and (sw-b,sw-a) iterations produce the same key.
				var aPort, bPort string
				if a == k.device {
					aPort, bPort = local.reportingPort, remote.reportingPort
				} else {
					aPort, bPort = remote.reportingPort, local.reportingPort
				}
				ppk := portPairKey{aPort, bPort}
				if visited[pk][ppk] {
					continue
				}
				visited[pk][ppk] = true

				proto := local.proto
				if proto == "" {
					proto = remote.proto
				}
				// Inject both sides so Reconcile sees len(sides) >= 2 → bidirectional.
				// proto here is a raw string from OutOfScopeNeighbour.Proto (spoke wire
				// format); cast to DiscoveryProtocol — Edge validation accepts any
				// non-empty string at this layer.
				allEdges = appendEdgePair(allEdges,
					k.device, local.reportingPort,
					k.hint, remote.reportingPort,
					discovery.DiscoveryProtocol(proto), discovery.LinkKindEthernet,
					discovery.ConfidenceMedium, 2,
				)
			}
		}
	}

	// Inject LD-19 known inter-domain links as authoritative overrides.
	// Rank 0 beats all protocol-observed edges so configured links always win.
	for i, link := range h.cfg.KnownInterDomainLinks {
		if !seenDevices[link.LocalDevice] || !seenDevices[link.RemoteDevice] {
			h.logger.Warn("hub: IDL endpoint not in combined graph; skipping",
				"link_idx", i,
				"local_device", link.LocalDevice,
				"remote_device", link.RemoteDevice,
			)
			continue
		}
		linkKind := discovery.LinkKind(link.LinkKind)
		if linkKind == "" {
			linkKind = discovery.LinkKindEthernet
		}
		allEdges = appendEdgePair(allEdges,
			link.LocalDevice, link.LocalPort,
			link.RemoteDevice, link.RemotePort,
			discovery.DiscoveryProtocolConfigured, linkKind,
			discovery.ConfidenceHigh, 0,
		)
	}

	reconciledEdges, _ := graph.Reconcile(allEdges)
	return discovery.Graph{
		Devices: allDevices,
		Edges:   reconciledEdges,
	}, unmatchedCount
}

// runEviction periodically evicts spokes that have not pushed within
// federation.spoke_timeout, per LD-18. Spoke liveness and link liveness are
// distinct failure modes; this path handles domain-level silence, not
// individual link instability.
func (h *Hub) runEviction(ctx context.Context) {
	if h.cfg.SpokeTimeout <= 0 {
		return // eviction disabled; avoids time.NewTicker(0) panic in tests
	}
	ticker := time.NewTicker(h.cfg.SpokeTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Per-tick recovery: evictSilentSpokes rebuilds and republishes
			// the combined graph, so a panic there (site hub_rebuild) must not
			// kill the eviction loop. Recover, count it, and let the next tick
			// try again. Wrapped in a closure so the deferred recover scopes to
			// one tick rather than the whole goroutine.
			func() {
				defer h.recoverGoroutine("hub_rebuild")
				h.evictSilentSpokes()
			}()
		}
	}
}

func (h *Hub) evictSilentSpokes() {
	now := time.Now()
	h.mu.Lock()
	var evicted []string
	for id, entry := range h.spokes {
		if now.Sub(entry.lastSeen) > h.cfg.SpokeTimeout {
			delete(h.spokes, id)
			evicted = append(evicted, id)
		}
	}
	// Delete gauge series while h.mu is still held so a concurrent handlePush
	// cannot race to re-add the series before we remove it. Deletion is cleaner
	// than Set(0): the series disappears immediately on eviction rather than
	// lingering with a stale 0 value until the next scrape interval.
	for _, id := range evicted {
		h.m.FederationSpokeUp.DeleteLabelValues(id)
		h.m.FederationSpokeLastPushUnix.DeleteLabelValues(id)
	}
	h.mu.Unlock()

	for _, id := range evicted {
		h.logger.Warn("hub: spoke evicted (no push within timeout)",
			"spoke_id", id,
			"timeout", h.cfg.SpokeTimeout,
		)
	}
	if len(evicted) > 0 {
		h.mu.Lock()
		spokes := h.spokesSnapshot()
		gen := h.publishGen.Add(1)
		h.mu.Unlock()
		combined, unmatchedCount := h.buildCombinedGraph(spokes)
		if published, _ := h.publishIfWinner(gen, combined, unmatchedCount, nil); published {
			h.writeSnapshotAsync(combined)
		}
	}
}

// publishMetrics atomically swaps the Topology collector snapshot and, on the
// first live push, clears GraphStale. The atomic pointer swap in
// TopologyCollector.Update means concurrent spoke pushes cannot produce an
// interleaved or empty scrape window.
func (h *Hub) publishMetrics(g discovery.Graph, clearStale bool) {
	if clearStale {
		h.m.GraphStale.Set(0)
	}
	h.m.Topology.Update(g)
}

// Package-local aliases for the typed metrics.RejectReason constants. The
// authoritative declarations — including the doc comments on each constant,
// the underlying wire strings, and the Valid() defense-in-depth check —
// live in internal/metrics/reject_reason.go. They are re-exported here so
// existing call sites in this file read naturally and a future reader can
// still find every reject-emitting path with
// `grep rejectReason internal/federation`.
//
// These are typed metrics.RejectReason values, not bare strings: callers
// that need the wire string (Prometheus WithLabelValues, JSON body) convert
// explicitly at the boundary via string(...), which surfaces any future
// untyped-string smuggling at compile time.
//
// New values are added only in a release that ships emission code + tests;
// see docs/operator/federation.md "Spoke push response contract".
const (
	rejectReasonStaleGeneration    = metrics.RejectReasonStaleGeneration
	rejectReasonSizeBudgetExceeded = metrics.RejectReasonSizeBudgetExceeded
	rejectReasonInvalidLabelKey    = metrics.RejectReasonInvalidLabelKey
	rejectReasonInvalidLabelValue  = metrics.RejectReasonInvalidLabelValue
	rejectReasonStructuralInvalid  = metrics.RejectReasonStructuralInvalid
)

// publishIfWinner publishes g only when gen is strictly greater than the last
// published generation, preventing a slow concurrent goroutine from overwriting
// a newer combined graph with an older snapshot. It runs ENTIRELY under h.mu;
// the order is load-bearing:
//
//  1. stale-generation check  → reject; generation untouched
//  2. size-budget check       → reject; generation UNTOUCHED  (issue #147 fix)
//  3. win: advance generation, and if accepted != nil commit the spoke entry,
//     its liveness gauges, and first-live/GraphStale — atomically with
//     Topology.Update (issue #147 fix, incl. the eviction-race reverse).
//
// Removing the old CAS loop is safe: publishGen.Add(1) is performed under h.mu
// at the single call site, so two callers can never hold equal generations.
func (h *Hub) publishIfWinner(gen uint64, g discovery.Graph, unmatched int, accepted *acceptedPush) (bool, metrics.RejectReason) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if gen <= h.lastPublishedGen {
		return false, rejectReasonStaleGeneration
	}

	maxEdges := h.cfg.Hub.MaxGraphEdges
	maxDevices := h.cfg.Hub.MaxGraphDevices
	if (maxEdges > 0 && len(g.Edges) > maxEdges) || (maxDevices > 0 && len(g.Devices) > maxDevices) {
		h.logger.Warn("graph update rejected: exceeds size budget",
			"edges", len(g.Edges), "max_edges", maxEdges,
			"devices", len(g.Devices), "max_devices", maxDevices)
		h.m.GraphUpdatesRejectedTotal.WithLabelValues(string(rejectReasonSizeBudgetExceeded)).Inc()
		return false, rejectReasonSizeBudgetExceeded // generation NOT advanced
	}

	h.lastPublishedGen = gen

	if accepted != nil {
		// Commit the spoke registration AND its liveness signals under the same
		// lock as the graph publish. If these gauges were set after the lock
		// released, a concurrent evictSilentSpokes could delete the entry+gauge
		// in between and we would resurrect a gauge for a spoke absent from both
		// h.spokes and the published graph (the #147 inconsistency, reversed).
		h.spokes[accepted.id] = accepted.entry
		h.m.FederationSpokeUp.WithLabelValues(accepted.id).Set(1)
		h.m.FederationSpokeLastPushUnix.WithLabelValues(accepted.id).Set(float64(accepted.entry.lastSeen.Unix()))
		if !h.firstLive.Load() {
			h.m.GraphStale.Set(0)
			h.firstLive.Store(true)
		}
	}

	h.m.HubOOSUnmatchedTotal.Set(float64(unmatched))
	h.m.Topology.Update(g)
	return true, ""
}

// IsReady reports whether the hub has received at least one live spoke push.
// Use this as the readiness signal for Kubernetes readiness probes: the hub
// can serve /metrics from the startup snapshot immediately, but it is only
// "ready" once at least one spoke has confirmed its topology.
func (h *Hub) IsReady() bool {
	return h.firstLive.Load()
}

// writeSnapshot persists the hub's current graph to disk (LD-13). A no-op
// when snapshotPath is empty. Hub snapshots omit credential cache and
// unconfirmed-age counters; those are spoke-side concerns.
func (h *Hub) writeSnapshot(g discovery.Graph) {
	if h.snapshotPath == "" {
		return
	}
	f := snapshot.File{
		Devices: g.Devices,
		Edges:   g.Edges,
	}
	if err := h.snapshotWriteFn(h.snapshotPath, f); err != nil {
		h.logger.Error("hub: snapshot write failed", "error", err)
		return
	}
	h.m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
}

// runSnapshotWriter is the single bounded snapshot writer goroutine for the
// hub. It drains h.snapshotCh one graph at a time, so an NFS stall cannot
// accumulate goroutines across spoke pushes.
func (h *Hub) runSnapshotWriter(ctx context.Context) {
	// Recover a panic in the snapshot writer so a bug in the write path
	// cannot crash the aggregator. One-shot: on recovery the writer exits;
	// subsequent writeSnapshotAsync calls then trip queue_full and count the
	// dropped snapshots, so the failure stays observable.
	defer h.recoverGoroutine("hub_snapshot_writer")
	var writeDone chan struct{} // non-nil while a write goroutine is in flight
	for {
		select {
		case <-ctx.Done():
			return
		case g := <-h.snapshotCh:
			// Collect result from any previously timed-out write that has now finished.
			// writeDone is always reassigned below before the next iteration's check,
			// so prior channel references don't leak across iterations.
			if writeDone != nil {
				select {
				case <-writeDone:
				default:
					h.m.SnapshotDropsTotal.WithLabelValues(string(metrics.SnapshotDropReasonWriteInFlight)).Inc()
					h.logger.Warn("hub: snapshot write still in flight; dropping snapshot (NFS stall?)")
					continue
				}
			}
			writeDone = make(chan struct{}, 1)
			go func(g discovery.Graph, done chan struct{}) {
				// Recover a panic in writeSnapshot so it cannot crash the
				// process; close(done) still runs via defer so the parent's
				// select unblocks on the success branch rather than timing
				// out. Registered first so it runs after the close.
				defer h.recoverGoroutine("hub_snapshot_writer")
				defer close(done)
				h.writeSnapshot(g)
			}(g, writeDone)
			select {
			case <-writeDone:
				// success path — writeDone naturally falls out of scope at the
				// next iteration's `writeDone = make(...)`.
			case <-time.After(h.snapshotWriteTimeout):
				h.logger.Warn("hub: snapshot write timed out (NFS stall?)", "timeout", h.snapshotWriteTimeout)
				// writeDone goroutine still running; next iteration will detect this.
			case <-ctx.Done():
				// writeDone was just assigned a non-nil channel above and the
				// success branch above is the only other case that could have
				// fired; so writeDone is definitively non-nil here. Wait for
				// the in-flight write or shutdown-grace timeout.
				select {
				case <-writeDone:
				case <-time.After(h.snapshotWriteTimeout):
					h.logger.Warn("hub: snapshot write did not complete before shutdown; data may be lost")
				}
				return
			}
		}
	}
}

// writeSnapshotAsync enqueues g for writing by the bounded runSnapshotWriter
// goroutine. If the channel is full (previous write still in flight), the new
// snapshot is dropped rather than spawning an additional goroutine.
func (h *Hub) writeSnapshotAsync(g discovery.Graph) {
	if h.snapshotCh == nil {
		return
	}
	select {
	case h.snapshotCh <- g:
	default:
		h.m.SnapshotDropsTotal.WithLabelValues(string(metrics.SnapshotDropReasonQueueFull)).Inc()
		h.logger.Warn("hub: snapshot write queue full; dropping (NFS stall?)")
	}
}

// normalizeDeviceName lowercases s and strips the domain suffix (everything after
// the first dot). This allows bare hostnames and FQDNs to match, but will
// incorrectly merge devices that share a hostname across different domains.
// A warning is emitted when this ambiguity is detected at merge time.
func normalizeDeviceName(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// canonicalizeDeviceName returns the canonical form of a device name for OOS
// neighbour matching. The default since v1.3.0 is strict matching: only
// case-folding is applied (preserving domain suffixes so "core-sw.dc1" and
// "core-sw.dc2" remain distinct). When LooseDeviceNameMatching is true, it
// delegates to normalizeDeviceName which strips domain suffixes — the
// pre-v1.3.0 behaviour for single-site reconciliation of FQDN/short pairs.
func (h *Hub) canonicalizeDeviceName(s string) string {
	if h.cfg.Hub.LooseDeviceNameMatching {
		return normalizeDeviceName(s)
	}
	return strings.ToLower(s)
}

// appendEdgePair appends a forward and reverse discovery.Edge to edges and
// returns the extended slice. Both edges share the same protocol, link kind,
// confidence, and precedence rank; direction is always unidirectional because
// the hub's Reconcile pass promotes to bidirectional when both sides are seen.
func appendEdgePair(edges []discovery.Edge, src, srcPort, dst, dstPort string, proto discovery.DiscoveryProtocol, linkKind discovery.LinkKind, confidence discovery.Confidence, rank int) []discovery.Edge {
	base := discovery.Edge{
		DiscoveryProto: proto,
		Direction:      discovery.DirectionUnidirectional,
		Confidence:     confidence,
		Adjacency:      discovery.AdjacencyUnknown,
		PrecedenceRank: rank,
		LinkKind:       linkKind,
		ObservedAt:     time.Now(),
	}
	fwd := base
	fwd.SrcDevice, fwd.SrcPort = src, srcPort
	fwd.DstDevice, fwd.DstPort = dst, dstPort
	rev := base
	rev.SrcDevice, rev.SrcPort = dst, dstPort
	rev.DstDevice, rev.DstPort = src, srcPort
	return append(edges, fwd, rev)
}
