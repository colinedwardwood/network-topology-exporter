package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/tracing"
)

// CredentialCandidate pairs a fully-populated snmpwalk.Params with the name of
// the credential profile that produced it. The probe layer iterates candidates
// trying each in order until one succeeds.
type CredentialCandidate struct {
	Params      snmpwalk.Params
	ProfileName string
}

// CredentialCandidates returns the ordered list of credential candidates to try
// for ip+t. Order: the resolver's cached profile for ip first (LD-12 sticky
// cache), then any other profiles the resolver suggests for ip. If no
// credential profiles are configured, falls back to a single-community
// candidate built from modules.snmp.community_env; if that env is empty the
// caller is expected to fail closed.
func CredentialCandidates(cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig, logger *slog.Logger) []CredentialCandidate {
	port := uint16(t.Port) //nolint:gosec
	if port == 0 {
		port = 161
	}

	var candidates []CredentialCandidate
	seen := make(map[string]bool)
	if profileName, ok := resolver.CachedProfile(ip.String()); ok {
		if p, found := resolver.Profile(profileName); found {
			if params, err := ProfileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p); err == nil {
				candidates = append(candidates, CredentialCandidate{Params: params, ProfileName: profileName})
				seen[profileName] = true
			}
		}
	}

	// No profiles configured — use single-community from modules.snmp.community_env.
	// If the env var is unset, return no candidates so the caller fails closed.
	if len(cfg.Credentials.Profiles) == 0 {
		community := os.Getenv(cfg.Modules.SNMP.CommunityEnv)
		if community == "" {
			return candidates
		}
		return append(candidates, CredentialCandidate{
			Params: snmpwalk.Params{
				IP:      ip,
				Port:    port,
				Timeout: cfg.Discovery.TimeoutPerDevice,
				// Convert env-string to []byte so the discovery cycle can
				// zeroize the credential at end-of-cycle (issue #5). This
				// []byte is a fresh copy; the env-var storage owned by
				// os.Getenv is out of our reach.
				Community: []byte(community),
			},
		})
	}

	for _, name := range resolver.Resolve(ip) {
		if seen[name] {
			continue
		}
		p, ok := resolver.Profile(name)
		if !ok {
			continue
		}
		params, err := ProfileToParams(ip, port, cfg.Discovery.TimeoutPerDevice, p)
		if err != nil {
			logger.Warn("credential profile unusable; skipping",
				"profile", name, "ip", ip, "error", err)
			continue
		}
		candidates = append(candidates, CredentialCandidate{Params: params, ProfileName: name})
		seen[name] = true
	}
	return candidates
}

// WalkSystemWithCredentials probes ip with each credential candidate until one
// succeeds or all fail. On success returns the device, the winning params (the
// caller is responsible for calling params.Zeroize() once module walks finish),
// the profile name that won, and a nil error. On failure returns the last
// error observed.
//
// Issue #5: credentials of every losing candidate are zeroized before this
// function returns. The winning candidate's credentials stay alive only
// because the caller will use them for module walks; the caller MUST zeroize
// them after those walks complete.
func WalkSystemWithCredentials(ctx context.Context, cfg *config.Config, resolver *credentials.Resolver, ip net.IP, t config.TargetConfig, logger *slog.Logger) (*discovery.Device, snmpwalk.Params, string, error) {
	// Issue #68: credentials.resolve span. Records how many candidates the
	// fallback ladder produced, how many were tried, and the cumulative
	// trial-limiter wait. Child of target.poll. No-op when tracing is disabled.
	ctx, span := tracing.Tracer().Start(ctx, "credentials.resolve")
	defer span.End()

	candidates := CredentialCandidates(cfg, resolver, ip, t, logger)
	span.SetAttributes(attribute.Int("credential.candidates", len(candidates)))
	if len(candidates) == 0 {
		span.SetStatus(codes.Error, "no usable credential profiles")
		return nil, snmpwalk.Params{}, "", fmt.Errorf("no usable credential profiles for %s", ip)
	}

	// Credential zeroization (issue #5): every candidate carries SNMP secret
	// bytes. On every exit path we overwrite the secrets of every candidate
	// we will NOT return. The winning candidate's secrets stay alive until
	// the caller zeroizes them after module walks complete.
	zeroizeFromIdx := func(start int) {
		for i := start; i < len(candidates); i++ {
			candidates[i].Params.Zeroize()
		}
	}

	var lastErr error
	allTimedOut := true // true until we see a non-timeout failure
	var trials int
	var limiterWait time.Duration
	for i := range candidates {
		c := &candidates[i]
		trials++
		// Measure how long the LD-12 trial limiter made this trial wait so the
		// span surfaces token-bucket back-pressure during credential resolve.
		acquireStart := time.Now()
		if err := resolver.AcquireTrial(ctx); err != nil {
			limiterWait += time.Since(acquireStart)
			span.SetAttributes(
				attribute.Int("credential.trials", trials),
				attribute.Float64("credential.limiter_wait_seconds", limiterWait.Seconds()),
			)
			span.RecordError(err)
			span.SetStatus(codes.Error, "trial limiter acquire failed")
			zeroizeFromIdx(i)
			return nil, snmpwalk.Params{}, "", err
		}
		limiterWait += time.Since(acquireStart)
		trialCtx, cancel := context.WithTimeout(ctx, cfg.Discovery.TimeoutPerDevice)
		dev, err := snmpwalk.Walk(trialCtx, c.Params)
		cancel()
		if err == nil {
			span.SetAttributes(
				attribute.Int("credential.trials", trials),
				attribute.Float64("credential.limiter_wait_seconds", limiterWait.Seconds()),
				attribute.String("credential.winning_profile", c.ProfileName),
			)
			// Caller owns c.Params now and must zeroize it after module walks.
			zeroizeFromIdx(i + 1)
			return dev, c.Params, c.ProfileName, nil
		}
		lastErr = err
		// This candidate is finished — zeroize its credentials.
		c.Params.Zeroize()
		if ctx.Err() != nil {
			// Parent context done (SIGTERM or cycle budget expiry) — stop immediately.
			zeroizeFromIdx(i + 1)
			return nil, snmpwalk.Params{}, "", ctx.Err()
		}
		// SNMP v2c agents silently drop packets with a wrong community string —
		// the client gets DeadlineExceeded just as if the device were unreachable.
		// Always try the next candidate. Only call RecordFailure when at least one
		// failure was clearly not a timeout (i.e. the device is reachable but the
		// credential was wrong). Timing out on all candidates preserves the cache.
		if !errors.Is(err, context.DeadlineExceeded) {
			allTimedOut = false
		}
	}
	// Don't invalidate the cache when all failures were timeouts: a timeout
	// means the device was unreachable this cycle (not an auth failure), so
	// the cached profile is still likely correct.
	if !allTimedOut {
		resolver.RecordFailure(ip.String())
	}
	span.SetAttributes(
		attribute.Int("credential.trials", trials),
		attribute.Float64("credential.limiter_wait_seconds", limiterWait.Seconds()),
	)
	if lastErr != nil {
		span.RecordError(lastErr)
		span.SetStatus(codes.Error, "all credential candidates failed")
	}
	return nil, snmpwalk.Params{}, "", lastErr
}

// ProfileToParams converts a config.CredentialProfile into a populated
// snmpwalk.Params for ip+port+timeout. Returns an error if a required env-var
// (community, username, auth/priv key) is empty.
func ProfileToParams(ip net.IP, port uint16, timeout time.Duration, p config.CredentialProfile) (snmpwalk.Params, error) {
	params := snmpwalk.Params{
		IP:          ip,
		Port:        port,
		Timeout:     timeout,
		Retries:     p.Retries,
		ContextName: p.ContextName,
	}
	switch p.Type {
	case config.ProfileTypeSNMPv2c:
		// SNMP credentials are held as []byte so the discovery cycle can
		// zeroize them at end-of-cycle (issue #5). Each []byte conversion
		// below is a fresh copy of the env-var bytes.
		community := os.Getenv(p.CommunityEnv)
		if community == "" {
			return params, fmt.Errorf("env %q is empty", p.CommunityEnv)
		}
		params.Community = []byte(community)
	case config.ProfileTypeSNMPv3:
		params.V3 = true
		params.Username = os.Getenv(p.UsernameEnv)
		if params.Username == "" {
			return params, fmt.Errorf("env %q is empty", p.UsernameEnv)
		}
		authKey := os.Getenv(p.AuthKeyEnv)
		if p.AuthKeyEnv != "" && authKey == "" {
			return params, fmt.Errorf("env %q is empty or unset", p.AuthKeyEnv)
		}
		params.AuthKey = []byte(authKey)
		privKey := os.Getenv(p.PrivKeyEnv)
		if p.PrivKeyEnv != "" && privKey == "" {
			return params, fmt.Errorf("env %q is empty or unset", p.PrivKeyEnv)
		}
		params.PrivKey = []byte(privKey)
		// Auth/priv protocol names are config-level (not secret); passed as
		// strings so callers don't need to import gosnmp directly.
		params.AuthProto = p.AuthProtocol
		params.PrivProto = p.PrivProtocol
	default:
		return params, fmt.Errorf("unknown profile type %q", p.Type)
	}
	return params, nil
}

// ModuleWalkFn is the per-protocol walker signature used by RunCycle.
type ModuleWalkFn func(context.Context, snmpwalk.Params, string, []*net.IPNet) ([]discovery.Edge, []discovery.OutOfScopeNeighbour, error)

// Module pairs a protocol identifier with its enabled flag and walker function.
type Module struct {
	Proto   string
	Enabled bool
	Walk    ModuleWalkFn
}
