package app_test

// Tests relocated verbatim from cmd/topology-exporter/main_test.go (#171):
// they exercise exported internal/app behaviour, not main-level wiring,
// and belong with the package they test. External test package (app_test)
// so the `app.` call sites move unchanged.

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/app"
	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

func TestProfileToParams(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	timeout := 5 * time.Second
	port := uint16(161)

	t.Run("v2c_ok", func(t *testing.T) {
		t.Setenv("TEST_COMM", "secret")
		p := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMM"}
		params, err := app.ProfileToParams(ip, port, timeout, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(params.Community) != "secret" {
			t.Errorf("Community = %q, want secret", params.Community)
		}
	})

	t.Run("v2c_empty_env", func(t *testing.T) {
		t.Setenv("TEST_COMM_EMPTY", "")
		p := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TEST_COMM_EMPTY"}
		_, err := app.ProfileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for empty community env, got nil")
		}
	})

	t.Run("v3_ok", func(t *testing.T) {
		t.Setenv("TEST_USER", "admin")
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_USER",
		}
		params, err := app.ProfileToParams(ip, port, timeout, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !params.V3 {
			t.Error("V3 should be true")
		}
		if params.Username != "admin" {
			t.Errorf("Username = %q, want admin", params.Username)
		}
	})

	t.Run("v3_empty_username", func(t *testing.T) {
		t.Setenv("TEST_USER_EMPTY", "")
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_USER_EMPTY",
		}
		_, err := app.ProfileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for empty username env, got nil")
		}
	})

	t.Run("v3_empty_auth_key_env", func(t *testing.T) {
		t.Setenv("TEST_V3_USER_AUTHTEST", "admin")
		// TEST_V3_AUTH_KEY_UNSET is deliberately never set.
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_V3_USER_AUTHTEST",
			AuthKeyEnv:  "TEST_V3_AUTH_KEY_UNSET",
		}
		_, err := app.ProfileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for unset auth_key_env, got nil")
		}
		if want := `env "TEST_V3_AUTH_KEY_UNSET" is empty or unset`; err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("v3_empty_priv_key_env", func(t *testing.T) {
		t.Setenv("TEST_V3_USER_PRIVTEST", "admin")
		t.Setenv("TEST_V3_AUTH_KEY_PRIVTEST", "authsecret")
		// TEST_V3_PRIV_KEY_UNSET is deliberately never set.
		p := config.CredentialProfile{
			Type:        config.ProfileTypeSNMPv3,
			UsernameEnv: "TEST_V3_USER_PRIVTEST",
			AuthKeyEnv:  "TEST_V3_AUTH_KEY_PRIVTEST",
			PrivKeyEnv:  "TEST_V3_PRIV_KEY_UNSET",
		}
		_, err := app.ProfileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for unset priv_key_env, got nil")
		}
		if want := `env "TEST_V3_PRIV_KEY_UNSET" is empty or unset`; err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("unknown_type", func(t *testing.T) {
		p := config.CredentialProfile{Type: "snmp_v1"}
		_, err := app.ProfileToParams(ip, port, timeout, p)
		if err == nil {
			t.Fatal("expected error for unknown profile type, got nil")
		}
	})
}

// TestCredentialCandidatesNoProfiles exercises the legacy single-community
// fallback path when no credential profiles are configured.
func TestCredentialCandidatesNoProfiles(t *testing.T) {
	t.Setenv("SNMP_COMMUNITY", "public")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{CommunityEnv: "SNMP_COMMUNITY"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			// No Profiles — triggers legacy path.
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 161}
	candidates := app.CredentialCandidates(cfg, resolver, ip, target, slog.Default())

	if len(candidates) == 0 {
		t.Fatal("expected at least one candidate from legacy path, got none")
	}
	if string(candidates[0].Params.Community) != "public" {
		t.Errorf("community = %q, want public", candidates[0].Params.Community)
	}
}

// TestCredentialCandidatesPortZero verifies that a target with Port=0 gets the
// default SNMP port (161) injected by credentialCandidates.
func TestCredentialCandidatesPortZero(t *testing.T) {
	t.Setenv("SNMP_COMMUNITY", "public")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{CommunityEnv: "SNMP_COMMUNITY"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			// No profiles — takes the legacy path where port defaults to 161.
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 0} // Port intentionally 0
	candidates := app.CredentialCandidates(cfg, resolver, ip, target, slog.Default())

	if len(candidates) == 0 {
		t.Fatal("expected candidate with default port, got none")
	}
	if candidates[0].Params.Port != 161 {
		t.Errorf("port = %d, want 161 (default for Port=0 target)", candidates[0].Params.Port)
	}
}

// ── walkSystemWithCredentials tests ──────────────────────────────────────────

// TestWalkSystemWithCredentialsEmptyCandidates verifies that
// walkSystemWithCredentials returns an error when there are no credential
// candidates (e.g. all profiles fail profileToParams).
func TestWalkSystemWithCredentialsEmptyCandidates(t *testing.T) {
	// Empty community env so profileToParams returns an error for every profile,
	// resulting in zero usable candidates.
	t.Setenv("EMPTY_COMMUNITY", "")

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "bad",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "EMPTY_COMMUNITY",
				},
			},
			FallbackOrder: []string{"bad"},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: 161}

	dev, _, _, err := app.WalkSystemWithCredentials(context.Background(), cfg, resolver, ip, target, slog.Default())
	if err == nil {
		t.Fatal("expected error from empty candidates, got nil")
	}
	if dev != nil {
		t.Errorf("expected nil device, got %v", dev)
	}
}

// TestWalkSystemWithCredentialsAllTimeout verifies that when every credential
// attempt times out, the credential cache is preserved (RecordFailure is NOT
// called).
func TestWalkSystemWithCredentialsAllTimeout(t *testing.T) {
	// Use a very short timeout so every attempt times out quickly.
	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 1 * time.Millisecond,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "p1", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "TC_COMM_1"},
			},
			FallbackOrder: []string{"p1"},
		},
	}
	t.Setenv("TC_COMM_1", "public")

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	// Point at a port that has nothing listening — will time out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // close immediately so SNMP gets connection-refused or timeout

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: port}

	_, _, _, err = app.WalkSystemWithCredentials(context.Background(), cfg, resolver, ip, target, slog.Default())
	if err == nil {
		t.Fatal("expected error from all-timeout walk, got nil")
	}

	// When all failures are timeouts, RecordFailure must not invalidate the
	// cache. Since there was no prior cached profile, CachedProfile should
	// still return nothing.
	if _, ok := resolver.CachedProfile("127.0.0.1"); ok {
		t.Error("expected no cached profile after all-timeout failures, but one was found")
	}
}

// TestWalkSystemWithCredentialsNonTimeoutFailure verifies that when the parent
// context is already cancelled, AcquireTrial returns context.Canceled and
// walkSystemWithCredentials propagates that error immediately.
func TestWalkSystemWithCredentialsNonTimeoutFailure(t *testing.T) {
	t.Setenv("NC_COMM", "public")

	addr := snmptest.Start(t, "public", systemPDUs("sw-nc"))
	_, port := snmptest.ParseAddr(addr)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			TimeoutPerDevice: 5 * time.Second,
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{Name: "ok", Type: config.ProfileTypeSNMPv2c, CommunityEnv: "NC_COMM"},
			},
			FallbackOrder: []string{"ok"},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	// Cancel the context immediately before calling walkSystemWithCredentials.
	// AcquireTrial will receive the cancelled context and return context.Canceled,
	// which causes an immediate return without going through allTimedOut logic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ip := net.ParseIP("127.0.0.1")
	target := config.TargetConfig{Host: "127.0.0.1", Port: int(port)}

	_, _, _, err = app.WalkSystemWithCredentials(ctx, cfg, resolver, ip, target, slog.Default())
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
