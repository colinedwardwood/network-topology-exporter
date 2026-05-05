package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Discovery.Interval != 60*time.Second {
		t.Errorf("Interval default = %v, want 60s", c.Discovery.Interval)
	}
	if c.Discovery.Parallelism != 32 {
		t.Errorf("Parallelism default = %d, want 32", c.Discovery.Parallelism)
	}
}

func TestValidateRejectsBadSNMPVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
modules:
  snmp:
    enabled: true
    version: v4
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for snmp.version=v4")
	}
}

func TestTargetDefaultPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Targets[0].Port != 161 {
		t.Errorf("default port = %d, want 161", c.Targets[0].Port)
	}
}

// LD-11: refuse to start if a target sits outside the CIDR allow-list.
func TestScopeRejectsOutOfRangeTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
targets:
  - host: 198.51.100.10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected scope-violation error, got nil")
	}
}

// LD-11: targets without an allow-list is a misconfiguration, not "poll
// everything".
func TestScopeRequiresAllowListWhenTargetsPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
targets:
  - host: 198.51.100.10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error when targets are defined without an allow-list")
	}
}

// LD-12: assignment that names an undefined profile is a config error.
func TestCredentialsRejectUnknownProfileReference(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: edge-v2c
      type: snmp_v2c
      community_env: SNMP_COMMUNITY
  assignments:
    - cidr: 10.0.0.0/8
      profiles: [does-not-exist]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error referencing undefined profile")
	}
}

// LD-12: snmp_v3 profile without username_env doesn't validate.
func TestCredentialsRejectV3MissingUsername(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      auth_protocol: SHA-256
      auth_key_env: SNMP_V3_AUTH_KEY
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for snmp_v3 profile without username_env")
	}
}

// LD-13: snapshot always active; default path applied when path is unset.
func TestSnapshotDefaultPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Snapshot.Path == "" {
		t.Fatal("snapshot path should always have a default")
	}
}

func TestValidateIntervalNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  interval: -1s
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for discovery.interval=-1s")
	}
}

func TestValidateTimeoutNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  timeout_per_device: -1s
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for discovery.timeout_per_device=-1s")
	}
}

func TestValidateTargetPortOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Port 99999 must be rejected.
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
    port: 99999
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for port 99999")
	}

	// Port 0 must succeed — applyDefaults converts it to 161.
	body = `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
    port: 0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("expected port 0 to be valid, got: %v", err)
	}
	if c.Targets[0].Port != 161 {
		t.Errorf("port after default = %d, want 161", c.Targets[0].Port)
	}
}

// LD-14: TTL defaults to 3 cycles, matching the netmap default.
func TestUnconfirmedLinkTTLDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Discovery.UnconfirmedLinkTTLCycles != 3 {
		t.Errorf("UnconfirmedLinkTTLCycles default = %d, want 3", c.Discovery.UnconfirmedLinkTTLCycles)
	}
}

func TestCredentialsRejectV2cMissingCommunityEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for snmp_v2c profile without community_env")
	}
}

func TestCredentialsDuplicateProfileName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
    - name: p1
      type: snmp_v2c
      community_env: C
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate profile name")
	}
}

func TestCredentialsRejectUnknownProfileType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: telnet
  fallback_order: []
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown profile type")
	}
}

func TestCredentialsRejectAssignmentNeitherIPNorCIDR(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
  assignments:
    - profiles: [p1]
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for assignment with neither ip nor cidr")
	}
}

func TestCredentialsRejectAssignmentInvalidIP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
  assignments:
    - ip: "999.0.0.1"
      profiles: [p1]
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for assignment with invalid ip")
	}
}

func TestCredentialsRejectAssignmentInvalidCIDR(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
  assignments:
    - cidr: "not-a-cidr"
      profiles: [p1]
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for assignment with invalid cidr")
	}
}

// applyDefaults only replaces 0 with 5, so a negative value bypasses defaulting
// and reaches the validation check directly.
func TestCredentialsRejectLowTrialRate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
  fallback_order: [p1]
  trial_rate_per_second: -1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for trial_rate_per_second < 1")
	}
}

// applyDefaults only replaces 0 with 32, so a negative value reaches validate().
func TestValidateParallelismZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  parallelism: -1
targets: []
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for discovery.parallelism < 1")
	}
}

// applyDefaults only replaces 0 with 3, so a negative value reaches validate().
func TestValidateUnconfirmedLinkTTLZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  unconfirmed_link_ttl_cycles: -1
targets: []
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for discovery.unconfirmed_link_ttl_cycles < 1")
	}
}

func TestScopeRejectsInvalidCIDR(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - "bad-cidr"
targets:
  - host: 10.0.0.1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid cidr_allow_list entry")
	}
}

func TestCredentialsRejectEmptyProfileName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: ""
      type: snmp_v2c
      community_env: C
  fallback_order: []
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for profile with empty name")
	}
}

// TestCredentialsRejectUnknownProfileReference covers assignments; this covers
// fallback_order specifically.
func TestCredentialsFallbackRejectsUnknownName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
  fallback_order: [p2]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for fallback_order referencing undefined profile")
	}
}
