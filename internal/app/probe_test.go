package app

import (
	"net"
	"testing"
	"time"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/credentials"
)

// TestProfileToParamsV2c builds v2c params from a profile whose community env is
// set, and errors when the env is empty.
func TestProfileToParamsV2c(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	t.Setenv("PT_COMM", "public")
	p := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "PT_COMM", Retries: 2, ContextName: "ctx"}
	params, err := ProfileToParams(ip, 161, time.Second, p)
	if err != nil {
		t.Fatalf("ProfileToParams: %v", err)
	}
	if string(params.Community) != "public" {
		t.Errorf("community = %q, want public", params.Community)
	}
	if params.Retries != 2 || params.ContextName != "ctx" {
		t.Errorf("retries/context = %d/%q, want 2/ctx", params.Retries, params.ContextName)
	}
	if params.V3 {
		t.Error("V3 should be false for v2c profile")
	}

	empty := config.CredentialProfile{Type: config.ProfileTypeSNMPv2c, CommunityEnv: "PT_COMM_UNSET"}
	if _, err := ProfileToParams(ip, 161, time.Second, empty); err == nil {
		t.Error("expected error for empty community env, got nil")
	}
}

// TestProfileToParamsV3 builds v3 params and errors when a referenced auth/priv
// env is empty.
func TestProfileToParamsV3(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	t.Setenv("PT_USER", "snmpuser")
	t.Setenv("PT_AUTH", "authpass")
	t.Setenv("PT_PRIV", "privpass")
	p := config.CredentialProfile{
		Type:         config.ProfileTypeSNMPv3,
		UsernameEnv:  "PT_USER",
		AuthKeyEnv:   "PT_AUTH",
		PrivKeyEnv:   "PT_PRIV",
		AuthProtocol: "SHA",
		PrivProtocol: "AES",
	}
	params, err := ProfileToParams(ip, 161, time.Second, p)
	if err != nil {
		t.Fatalf("ProfileToParams v3: %v", err)
	}
	if !params.V3 {
		t.Error("V3 should be true")
	}
	if params.Username != "snmpuser" || string(params.AuthKey) != "authpass" || string(params.PrivKey) != "privpass" {
		t.Errorf("v3 creds not populated: %+v", params)
	}
	if params.AuthProto != "SHA" || params.PrivProto != "AES" {
		t.Errorf("auth/priv proto = %q/%q, want SHA/AES", params.AuthProto, params.PrivProto)
	}
}

// TestProfileToParamsV3EmptyUsername errors when the username env is empty.
func TestProfileToParamsV3EmptyUsername(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	p := config.CredentialProfile{Type: config.ProfileTypeSNMPv3, UsernameEnv: "PT_USER_UNSET"}
	if _, err := ProfileToParams(ip, 161, time.Second, p); err == nil {
		t.Error("expected error for empty username env, got nil")
	}
}

// TestProfileToParamsUnknownType rejects an unknown profile type.
func TestProfileToParamsUnknownType(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	if _, err := ProfileToParams(ip, 161, time.Second, config.CredentialProfile{Type: "bogus"}); err == nil {
		t.Error("expected error for unknown profile type, got nil")
	}
}

// TestCredentialCandidatesLegacyCommunity returns one community candidate from
// modules.snmp.community_env when no profiles are configured, and none when the
// env is unset (fail-closed).
func TestCredentialCandidatesLegacyCommunity(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	t.Setenv("CC_COMM", "public")
	cfg := testConfig(t, "CC_COMM")
	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	cands := CredentialCandidates(cfg, resolver, ip, config.TargetConfig{}, slogDiscard())
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	if string(cands[0].Params.Community) != "public" {
		t.Errorf("community = %q, want public", cands[0].Params.Community)
	}
	if cands[0].Params.Port != 161 {
		t.Errorf("default port = %d, want 161", cands[0].Params.Port)
	}

	// Unset env -> fail closed with no candidates.
	cfgUnset := testConfig(t, "CC_COMM_UNSET")
	resolver2, err := credentials.New(cfgUnset.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	if cands := CredentialCandidates(cfgUnset, resolver2, ip, config.TargetConfig{}, slogDiscard()); len(cands) != 0 {
		t.Errorf("candidates = %d, want 0 (fail closed)", len(cands))
	}
}
