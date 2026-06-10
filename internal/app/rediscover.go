package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// RediscoverOutcome is the per-target result of a forced out-of-cycle walk.
// The underlying strings double as the `outcome` label on
// network_topology_admin_rediscovery_total, so changing them is a metric
// contract change.
type RediscoverOutcome string

// RediscoverOutcome values: one per terminal state a single target walk can
// reach. out_of_scope is decided before any SNMP packet is sent.
const (
	RediscoverSuccess     RediscoverOutcome = "success"
	RediscoverTimeout     RediscoverOutcome = "timeout"
	RediscoverAuthFailure RediscoverOutcome = "auth_failure"
	RediscoverOutOfScope  RediscoverOutcome = "out_of_scope"
	RediscoverError       RediscoverOutcome = "error"
)

// RediscoverResult is the structured per-target outcome returned to the admin
// caller (and serialised to JSON by the HTTP handler).
type RediscoverResult struct {
	Target  string            `json:"target"`
	Outcome RediscoverOutcome `json:"outcome"`
	Edges   int               `json:"edges"`
	Error   string            `json:"error,omitempty"`
}

// Rediscoverer triggers out-of-cycle SNMP walks against individual in-scope
// targets in response to the admin endpoint (issue #73). It is intentionally
// read-only with respect to the published graph: a forced walk reports its
// per-target edge count to the caller but does NOT mutate prevGraph, the
// snapshot, or the unconfirmed-age counters owned by RunDiscoveryLoop. Those
// remain the exclusive property of the regular cycle, so the out-of-cycle walk
// cannot corrupt the published topology. The forced result becomes visible in
// /metrics on the next regular cycle, which will re-walk the (now fixed)
// device. See docs/operator/troubleshooting.md.
//
// Concurrency: every forced walk and every regular cycle acquire the same
// CycleMu, so SNMP discovery is fully serialised across the two paths — they
// never hit the credential resolver or the device concurrently, and there is
// no data race on resolver cache state. The trade-off is that a forced walk
// can block for up to one regular cycle (and vice versa); this is acceptable
// for a rare, operator-initiated admin call and avoids the invasive work of
// threading a priority queue through the parallelism pool.
type Rediscoverer struct {
	cfg         *config.Config
	m           *metrics.Metrics
	logger      *slog.Logger
	resolver    *credentials.Resolver
	allowedNets []*net.IPNet
	// CycleMu serialises forced walks against the regular discovery cycle.
	// Shared with RunDiscoveryLoop via LoopConfig.CycleMu.
	cycleMu *sync.Mutex
	// authConfigured is true when listen.web_config_file is set. The endpoint
	// is privileged: with no auth configured the handler returns 403 and no
	// walk runs. Default ground state (no auth) ⇒ no privileged endpoint.
	authConfigured bool

	// targetPortOverride maps an IP string to a non-default SNMP port for that
	// IP. Populated only by tests pointing at an in-process agent on a random
	// port; nil in production, where walkOne falls back to the configured
	// target's port (or 161).
	targetPortOverride map[string]uint16
}

// NewRediscoverer builds a Rediscoverer sharing cycleMu with the discovery
// loop. resolver may be the same resolver the loop uses; access is serialised
// by cycleMu. allowedNets is the parsed CIDR allow-list (LD-11).
func NewRediscoverer(
	cfg *config.Config,
	m *metrics.Metrics,
	logger *slog.Logger,
	resolver *credentials.Resolver,
	allowedNets []*net.IPNet,
	cycleMu *sync.Mutex,
	authConfigured bool,
) *Rediscoverer {
	return &Rediscoverer{
		cfg:            cfg,
		m:              m,
		logger:         logger,
		resolver:       resolver,
		allowedNets:    allowedNets,
		cycleMu:        cycleMu,
		authConfigured: authConfigured,
	}
}

// AuthConfigured reports whether the listener is auth-gated. The HTTP handler
// uses this to fail closed (403) when no auth is configured.
func (rd *Rediscoverer) AuthConfigured() bool { return rd.authConfigured }

// WebConfigHasClientAuth reports whether the Prometheus exporter-toolkit
// web-config at path actually authenticates the CLIENT — i.e. it defines
// basic_auth_users or requires a client certificate. Plain server TLS
// (tls_server_config without a client-cert-requiring client_auth_type)
// encrypts the channel but does NOT authenticate the caller, so it must not
// gate the privileged /admin/rediscover endpoint. Fails closed: an empty path
// or an unreadable/unparseable config returns false (endpoint stays 403).
func WebConfigHasClientAuth(path string) bool {
	if path == "" {
		return false
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return false
	}
	var wc struct {
		BasicAuthUsers  map[string]string `yaml:"basic_auth_users"`
		TLSServerConfig struct {
			ClientAuthType string `yaml:"client_auth_type"`
		} `yaml:"tls_server_config"`
	}
	if err := yaml.Unmarshal(b, &wc); err != nil {
		return false
	}
	if len(wc.BasicAuthUsers) > 0 {
		return true
	}
	// Only these two require the client to present a certificate. RequestClientCert
	// and VerifyClientCertIfGiven make the cert optional → not authentication.
	switch wc.TLSServerConfig.ClientAuthType {
	case "RequireAnyClientCert", "RequireAndVerifyClientCert":
		return true
	}
	return false
}

// Rediscover runs a forced out-of-cycle walk against each target IP and
// returns one result per target in input order. Out-of-scope targets are
// rejected before any SNMP traffic is sent. The whole batch runs under
// CycleMu so it is serialised against the regular discovery cycle.
func (rd *Rediscoverer) Rediscover(ctx context.Context, targets []string) []RediscoverResult {
	results := make([]RediscoverResult, 0, len(targets))

	// First pass: scope-check every target without holding the cycle mutex.
	// Out-of-scope targets never reach SNMP, mirroring the LD-11 guard in
	// RunCycle and the admin contract "no scope expansion via admin call".
	type pending struct {
		idx int
		ip  net.IP
	}
	var toWalk []pending
	for _, raw := range targets {
		res := RediscoverResult{Target: raw}
		ip := net.ParseIP(raw)
		if ip == nil {
			res.Outcome = RediscoverError
			res.Error = "not a valid IP address"
			rd.record(res.Outcome)
			results = append(results, res)
			continue
		}
		if len(rd.allowedNets) == 0 || !snmpwalk.IPInNets(ip, rd.allowedNets) {
			res.Outcome = RediscoverOutOfScope
			res.Error = "target is outside discovery.scope.cidr_allow_list (LD-11)"
			rd.record(res.Outcome)
			results = append(results, res)
			continue
		}
		results = append(results, res) // outcome filled in after the walk
		toWalk = append(toWalk, pending{idx: len(results) - 1, ip: ip})
	}

	if len(toWalk) == 0 {
		return results
	}

	// Serialise the SNMP work against the regular cycle, but acquire CycleMu
	// PER TARGET rather than around the whole batch: a forced rediscover of many
	// (or slow/unreachable) targets must not monopolise the mutex and stall the
	// regular discovery cycle for the entire batch. Locking per target bounds
	// the contiguous hold to one device's walk and lets the regular cycle
	// interleave between targets, while still guaranteeing the two paths never
	// hit a device concurrently.
	for _, p := range toWalk {
		select {
		case <-ctx.Done():
			results[p.idx].Outcome = RediscoverTimeout
			results[p.idx].Error = ctx.Err().Error()
			rd.record(RediscoverTimeout)
			continue
		default:
		}
		rd.cycleMu.Lock()
		outcome, edges, err := rd.walkOne(ctx, p.ip)
		rd.cycleMu.Unlock()
		results[p.idx].Outcome = outcome
		results[p.idx].Edges = edges
		if err != nil {
			results[p.idx].Error = err.Error()
		}
		rd.record(outcome)
	}
	return results
}

// RediscoverResults adapts Rediscover to the httpx.Rediscoverer interface,
// boxing each typed result so the httpx package need not import app. A
// per-batch deadline (ResultsTimeout) bounds how long one admin request can
// hold CycleMu even if the request context has no deadline of its own.
func (rd *Rediscoverer) RediscoverResults(ctx context.Context, targets []string) []any {
	ctx, cancel := context.WithTimeout(ctx, ResultsTimeout)
	defer cancel()
	typed := rd.Rediscover(ctx, targets)
	out := make([]any, len(typed))
	for i := range typed {
		out[i] = typed[i]
	}
	return out
}

// walkOne probes a single in-scope IP with the configured credentials and the
// enabled module set, returning the outcome and the number of raw edges the
// walk produced. It mirrors the per-device probe in RunCycle but does not
// publish anything into the graph.
func (rd *Rediscoverer) walkOne(ctx context.Context, ip net.IP) (RediscoverOutcome, int, error) {
	// Build the TargetConfig for this IP. If a configured target matches the IP
	// exactly, reuse its port (and implicitly its credentials via the resolver);
	// otherwise default the port to 161 (CredentialCandidates fills 0 → 161).
	// The admin call addresses by IP, so no per-target site/label enrichment is
	// attached — the forced walk only reports an outcome, it does not publish a
	// Device record.
	t := config.TargetConfig{Host: ip.String()}
	for _, ct := range rd.cfg.Targets {
		if ct.Host == ip.String() {
			t.Port = ct.Port
			break
		}
	}
	if rd.targetPortOverride != nil {
		if p, ok := rd.targetPortOverride[ip.String()]; ok {
			t.Port = int(p)
		}
	}

	dev, params, profileName, err := WalkSystemWithCredentials(ctx, rd.cfg, rd.resolver, ip, t, rd.logger)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return RediscoverTimeout, 0, err
		}
		return RediscoverAuthFailure, 0, err
	}
	defer params.Zeroize()
	rd.resolver.RecordSuccess(ip.String(), profileName)

	devCtx, cancel := context.WithTimeout(ctx, rd.cfg.Discovery.TimeoutPerDevice)
	defer cancel()

	params.MaxVlans = rd.cfg.Modules.FDB.MaxVlans
	params.Vendor = dev.Vendor
	params.UseBGPV2MIB = !rd.cfg.Modules.BGP.DisableV2MIB

	// Route the per-module walk through the shared walkModules helper (issue
	// #153) so the rediscover and regular-cycle module sets can no longer
	// drift. walkModules applies the same #74 cfg.ModulesForIP(ip) per-target
	// scope intersection the inline loop used to, so behaviour is unchanged.
	//
	// Intentionally minimal instrumentation (issue #153 / spec C2): the admin
	// rediscover path passes nil metrics+tracer and leaves params.WalkerMetrics
	// unset, so it emits no per-cycle walker/decode/module-duration metrics
	// (feeding them would corrupt the per-cycle series) and installs no #72
	// rate limiter (keeps the forced diagnostic walk fast). This is deliberate
	// isolation, not drift. The returned oos/moduleStatus are ignored: a forced
	// walk only reports its edge count and outcome, it does not publish.
	edges, _, _ := walkModules(devCtx, rd.cfg, *dev, ip, params, rd.allowedNets,
		moduleInstrumentation{logger: rd.logger, host: ip.String()})
	edgeCount := len(edges)
	return RediscoverSuccess, edgeCount, nil
}

func (rd *Rediscoverer) record(outcome RediscoverOutcome) {
	if rd.m == nil {
		return
	}
	rd.m.AdminRediscoveryTotal.WithLabelValues(string(outcome)).Inc()
}

// ResultsTimeout is a defensive cap on a single admin rediscover request so a
// large target list cannot hold CycleMu indefinitely. Each target still gets
// its own per-device timeout via TimeoutPerDevice; this bounds the batch.
const ResultsTimeout = 2 * time.Minute
