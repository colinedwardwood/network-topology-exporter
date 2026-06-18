package federation

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/loglimit"
	"github.com/grafana/network-topology-exporter/internal/metrics"
	"github.com/grafana/network-topology-exporter/internal/tracing"
)

// fatalPushError wraps an error that should not be retried (e.g. a 4xx
// response from the hub). Push checks for this type and aborts immediately
// rather than exhausting all retry attempts.
type fatalPushError struct{ err error }

func (e fatalPushError) Error() string { return e.err.Error() }

// Spoke pushes the local domain's pre-reconciled graph to the hub after each
// discovery cycle per LD-17. Transport is push per LD-16. mTLS is required
// per LD-20: the spoke presents a client certificate; the hub verifies it
// against its configured CA before accepting the payload.
type Spoke struct {
	cfg     config.FederationConfig
	client  *http.Client
	logger  *slog.Logger
	limiter *loglimit.Limiter
	m       *metrics.Metrics
	pushURL string
	// contentEncoding is the Content-Encoding applied to push bodies:
	// "gzip" or "" (identity). Derived from federation.spoke.compression
	// in NewSpoke. Topology JSON compresses 10–20×, which keeps large
	// graphs far below the hub's body cap (the pre-compression worst case
	// at the per-push count limits is ~19 MiB) and cuts WAN egress.
	contentEncoding string
}

// NewSpoke constructs a Spoke with an mTLS-capable HTTP client. Returns an
// error if any of the TLS files cannot be read or parsed. limiter may be
// nil; when nil, retry-attempt Warns fall through to the wrapped logger
// directly (no suppression).
func NewSpoke(cfg config.FederationConfig, logger *slog.Logger, limiter *loglimit.Limiter, m *metrics.Metrics) (*Spoke, error) {
	caCert, err := os.ReadFile(cfg.Spoke.TLSCACert)
	if err != nil {
		return nil, fmt.Errorf("spoke: read CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("spoke: no CA certs parsed from %q", cfg.Spoke.TLSCACert)
	}
	clientCert, err := tls.LoadX509KeyPair(cfg.Spoke.TLSCert, cfg.Spoke.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("spoke: load client cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		RootCAs:      caPool,
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS13,
	}
	pushURL, err := buildSpokeURL(cfg.Spoke.HubURL)
	if err != nil {
		return nil, fmt.Errorf("spoke: %w", err)
	}
	// "" (programmatic construction that bypassed config defaults) gets the
	// same gzip default as applyDefaults, so compression-on is the only way
	// to get a Spoke without asking for "none" explicitly.
	encoding := ""
	if cfg.Spoke.Compression != "none" {
		encoding = "gzip"
	}
	return &Spoke{
		cfg: cfg,
		client: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   30 * time.Second,
		},
		logger:          logger,
		limiter:         limiter,
		m:               m,
		pushURL:         pushURL,
		contentEncoding: encoding,
	}, nil
}

// spokePushBaseBackoff is the initial retry delay in Push. Overridden in tests
// to avoid multi-second waits.
var spokePushBaseBackoff = time.Second

// jitteredBackoff returns a random duration in [d/2, d). Equal-jitter spreads
// the retry instants of spokes whose pushes failed at the same moment (e.g. a
// hub restart) so the hub is not hit by synchronised retry waves, while
// keeping the floor at half the nominal delay so retries still back off.
func jitteredBackoff(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half) //nolint:gosec // G404: retry-timing jitter, not a security boundary; crypto/rand would be waste
}

// Push serialises payload and POSTs it to the hub. It retries up to five
// times with exponential backoff starting at spokePushBaseBackoff (nominally
// 1s, 2s, 4s, 8s between the five attempts; each delay is equal-jittered to
// [d/2, d) so spokes that failed simultaneously do not retry in lockstep).
// A cancelled context aborts immediately without retrying. The 5-attempt
// window (#71 §7) is sized so a push in flight during an HA hub leader-flip
// survives until the new leader's push Service is ready, rather than being
// deferred to the next discovery cycle.
func (s *Spoke) Push(ctx context.Context, payload SpokePayload) error {
	// Issue #68: spoke.push span. The post() helper injects this span's W3C
	// traceparent into each outbound HTTP request, so the hub's hub.handlePush
	// span continues the same trace. No-op when tracing is disabled (the
	// no-op span's SpanContext is not sampled, so the propagator injects
	// nothing).
	ctx, span := tracing.Tracer().Start(ctx, "spoke.push",
		trace.WithAttributes(
			attribute.String("spoke.id", payload.SpokeID),
			attribute.Int("spoke.devices", len(payload.Devices)),
			attribute.Int("spoke.edges", len(payload.Edges)),
		))
	defer span.End()

	b, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal payload failed")
		return fmt.Errorf("spoke: marshal payload: %w", err)
	}
	if s.contentEncoding == "gzip" {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(b); err != nil {
			return fmt.Errorf("spoke: gzip payload: %w", err)
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("spoke: gzip payload: %w", err)
		}
		b = buf.Bytes()
	}

	const maxAttempts = 5
	backoff := spokePushBaseBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.post(ctx, b); err == nil {
			return nil
		}
		var fatal fatalPushError
		if errors.As(err, &fatal) {
			if s.m != nil {
				s.m.FederationSpokePushFailuresTotal.Inc()
			}
			return err
		}
		// Rate-limit per (hub URL, attempt-index) so a chronically
		// unreachable hub does not flood the log every cycle (issue
		// #16). attempt is part of the key so a healthy spoke that
		// occasionally retries still emits at each attempt level; only
		// a *chronic* failure on the same attempt repeats and gets
		// suppressed.
		key := "spoke_push_attempt|" + s.pushURL + "|" + strconv.Itoa(attempt)
		msg := "spoke: push attempt failed"
		attrs := []any{"attempt", attempt, "max", maxAttempts, "error", err}
		if s.limiter != nil {
			s.limiter.Warn(ctx, key, msg, attrs...)
		} else {
			s.logger.WarnContext(ctx, msg, attrs...)
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(jitteredBackoff(backoff)):
			}
			backoff *= 2
		}
	}
	if s.m != nil {
		s.m.FederationSpokePushFailuresTotal.Inc()
	}
	return fmt.Errorf("spoke: push failed after %d attempts: %w", maxAttempts, err)
}

// buildSpokeURL joins baseURL with the /spoke/push path using net/url so that
// trailing slashes and path prefixes are handled correctly.
func buildSpokeURL(baseURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse hub URL: %w", err)
	}
	if base.Scheme != "https" {
		return "", fmt.Errorf("hub URL must use HTTPS, got %q", base.Scheme)
	}
	if base.Host == "" {
		return "", fmt.Errorf("hub URL has no host (missing \"//\"?): %q", baseURL)
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	return base.ResolveReference(&url.URL{Path: "spoke/push"}).String(), nil
}

func (s *Spoke) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.pushURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.contentEncoding != "" {
		req.Header.Set("Content-Encoding", s.contentEncoding)
	}
	// Issue #68: inject the W3C traceparent (and baggage) of the active
	// spoke.push span into the outbound headers so the hub can continue the
	// trace. When tracing is disabled the active span is the OTel no-op whose
	// SpanContext is not valid, so the propagator writes no headers.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("push to hub: %w", err)
	}
	// Read up to 4 KiB of body so operators see the hub's reject reason in
	// error logs. The hub emits JSON for graph-publish rejections and plain
	// text for other 4xx; both fit comfortably in this budget.
	const maxBodySnippet = 4 << 10
	bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySnippet))
	_, _ = io.Copy(io.Discard, resp.Body) // drain remainder so the connection can be reused
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	trimmed := strings.TrimSpace(string(bodySnippet))
	if trimmed == "" {
		err = fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	} else {
		err = fmt.Errorf("hub returned HTTP %d: %s", resp.StatusCode, trimmed)
	}
	// 4xx errors (except 429 Too Many Requests) are client errors that will
	// not succeed on retry, so wrap them as fatal to stop the retry loop.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return fatalPushError{err}
	}
	return err
}
