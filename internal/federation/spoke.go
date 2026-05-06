package federation

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// Spoke pushes the local domain's pre-reconciled graph to the hub after each
// discovery cycle per LD-17. Transport is push per LD-16. mTLS is required
// per LD-20: the spoke presents a client certificate; the hub verifies it
// against its configured CA before accepting the payload.
type Spoke struct {
	cfg    config.FederationConfig
	client *http.Client
	logger *slog.Logger
	m      *metrics.Metrics
}

// NewSpoke constructs a Spoke with an mTLS-capable HTTP client. Returns an
// error if any of the TLS files cannot be read or parsed.
func NewSpoke(cfg config.FederationConfig, logger *slog.Logger, m *metrics.Metrics) (*Spoke, error) {
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
		MinVersion:   tls.VersionTLS12,
	}
	return &Spoke{
		cfg: cfg,
		client: &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   30 * time.Second,
		},
		logger: logger,
		m:      m,
	}, nil
}

// spokePushBaseBackoff is the initial retry delay in Push. Overridden in tests
// to avoid multi-second waits.
var spokePushBaseBackoff = time.Second

// Push serialises payload and POSTs it to the hub. It retries up to three
// times with exponential backoff starting at spokePushBaseBackoff. A cancelled
// context aborts immediately without retrying.
func (s *Spoke) Push(ctx context.Context, payload SpokePayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("spoke: marshal payload: %w", err)
	}

	const maxAttempts = 3
	backoff := spokePushBaseBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.post(ctx, b); err == nil {
			return nil
		}
		s.logger.Warn("spoke: push attempt failed",
			"attempt", attempt,
			"max", maxAttempts,
			"error", err,
		)
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	if s.m != nil {
		s.m.FederationSpokePushFailuresTotal.Inc()
	}
	return fmt.Errorf("spoke: push failed after %d attempts: %w", maxAttempts, err)
}

func (s *Spoke) post(ctx context.Context, body []byte) error {
	url := s.cfg.Spoke.HubURL + "/spoke/push"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	}
	return nil
}
