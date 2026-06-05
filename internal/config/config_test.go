package config

import (
	"os"
	"path/filepath"
	"strings"
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

// Issue #69: debug_listen_addr defaults to empty (pprof endpoint OFF).
func TestDebugListenAddrDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Listen.DebugListenAddr != "" {
		t.Errorf("DebugListenAddr default = %q, want \"\" (off)", c.Listen.DebugListenAddr)
	}
}

// Issue #69: a set debug_listen_addr round-trips through Load.
func TestDebugListenAddrRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen:
  debug_listen_addr: 127.0.0.1:6060
targets: []
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Listen.DebugListenAddr != "127.0.0.1:6060" {
		t.Errorf("DebugListenAddr = %q, want 127.0.0.1:6060", c.Listen.DebugListenAddr)
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

func TestCredentialsRejectUnknownAuthProtocol(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: SHA256
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown auth protocol")
	}
}

func TestCredentialsRejectUnknownPrivProtocol(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      priv_protocol: AES256
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown priv protocol")
	}
}

func TestCredentialsNormalizesProtocolCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: sha-256
      auth_key_env: SNMP_V3_AUTH_KEY
      priv_protocol: aes-256
      priv_key_env: SNMP_V3_PRIV_KEY
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := c.Credentials.Profiles[0].AuthProtocol; got != "SHA-256" {
		t.Fatalf("AuthProtocol = %q, want SHA-256", got)
	}
	if got := c.Credentials.Profiles[0].PrivProtocol; got != "AES-256" {
		t.Fatalf("PrivProtocol = %q, want AES-256", got)
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

// LD-15–LD-20: federation validation.

func TestFederationDefaultRoleIsStandalone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Federation.Role != "standalone" {
		t.Errorf("default role = %q, want standalone", c.Federation.Role)
	}
}

func TestFederationRejectsUnknownRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("federation:\n  role: daemon\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown federation role")
	}
}

func TestFederationSpokeRequiresTLSFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: spoke
  spoke:
    spoke_id: dc-a
    hub_url: https://hub:9101
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for spoke role without TLS fields")
	}
}

func TestFederationSpokeRequiresSpokeID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: spoke
  spoke:
    hub_url: https://hub:9101
    tls_ca_cert: /ca.pem
    tls_cert: /spoke.crt
    tls_key: /spoke.key
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for spoke role without spoke_id")
	}
}

func TestFederationHubRequiresTLSFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: hub
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for hub role without TLS fields")
	}
}

func TestFederationKnownInterDomainLinksRequiresAllFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  known_inter_domain_links:
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-b
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for known_inter_domain_links entry with missing remote_port")
	}
}

func TestFederationKnownInterDomainLinksSelfLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  known_inter_domain_links:
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-a
      remote_port: Gi0/2
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for known_inter_domain_links entry where local_device == remote_device")
	}
}

func TestFederationKnownInterDomainLinksDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  known_inter_domain_links:
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-b
      remote_port: Gi0/1
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-b
      remote_port: Gi0/1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for duplicate known_inter_domain_links entries")
	}
}

func TestFederationKnownInterDomainLinksReverseDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  known_inter_domain_links:
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-b
      remote_port: Gi0/1
    - local_device: sw-b
      local_port: Gi0/1
      remote_device: sw-a
      remote_port: Gi0/1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for reverse-duplicate known_inter_domain_links entries")
	}
}

func TestFederationUncoordinatedIsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: uncoordinated
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("unexpected error for uncoordinated role: %v", err)
	}
}

// writeTLSStubs creates empty placeholder files for TLS CA cert, cert, and key
// in dir and returns their paths. Tests use these to satisfy the os.Stat check
// without needing real PKI material.
func writeTLSStubs(t *testing.T, dir string) (caPath, certPath, keyPath string) {
	t.Helper()
	caPath = filepath.Join(dir, "ca.pem")
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	for _, p := range []string{caPath, certPath, keyPath} {
		if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
			t.Fatalf("write TLS stub %s: %v", p, err)
		}
	}
	return caPath, certPath, keyPath
}

func TestFederationHubSpokeTimeoutTooShort(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	path := filepath.Join(dir, "config.yaml")
	// spoke_timeout = 60s, discovery.interval = 60s → 60s < 2×60s → reject.
	body := "discovery:\n  interval: 60s\nfederation:\n  role: hub\n  spoke_timeout: 60s\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: spoke_timeout < 2 × discovery.interval")
	}
}

func TestFederationHubSpokeTimeoutExactlyTwoIntervals(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	path := filepath.Join(dir, "config.yaml")
	// spoke_timeout = 120s = 2 × 60s → exactly at the boundary, should pass.
	body := "discovery:\n  interval: 60s\nfederation:\n  role: hub\n  spoke_timeout: 120s\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("unexpected error for spoke_timeout == 2 × interval: %v", err)
	}
}

// TestScopeAllowsHostnameTarget verifies that a target with a hostname (not an IP)
// passes scope validation even when there is a CIDR allow-list. Resolution is
// deferred to poll time.
func TestScopeAllowsHostnameTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
targets:
  - host: router.example.com
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected hostname target to pass scope validation, got: %v", err)
	}
}

// TestNormalizePrivProtocolEmptyIsValid verifies that an SNMPv3 profile with
// no priv_protocol (empty string) is valid and normalises to "".
func TestNormalizePrivProtocolEmptyIsValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("expected success for v3 profile without priv_protocol, got: %v", err)
	}
	if got := c.Credentials.Profiles[0].PrivProtocol; got != "" {
		t.Errorf("PrivProtocol = %q, want empty", got)
	}
}

// TestLoadReturnsErrorForMissingFile verifies that Load returns a non-nil error
// when the specified path does not exist.
func TestLoadReturnsErrorForMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/that/does/not/exist/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestLoadReturnsErrorForInvalidYAML verifies that Load returns an error when
// the file contains invalid YAML.
func TestLoadReturnsErrorForInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("{{invalid yaml"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// TestLoadReturnsErrorForInvalidConfig verifies that valid YAML that fails
// validation (snmp version "v4") causes Load to return an error.
func TestLoadReturnsErrorForInvalidConfig(t *testing.T) {
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
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for snmp.version=v4, got nil")
	}
}

// TestNormalizeAuthProtocolMD5ReturnsError verifies that an SNMPv3 profile
// using auth_protocol MD5 (cryptographically broken) causes Load to return an error.
func TestNormalizeAuthProtocolMD5ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: MD5
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for auth_protocol MD5 (cryptographically broken)")
	}
}

// TestNormalizeAuthProtocolUnknownReturnsError verifies that an unrecognized
// auth protocol string causes Load to return an error.
func TestNormalizeAuthProtocolUnknownReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: BLOWFISH
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown auth_protocol")
	}
}

// TestNormalizePrivProtocolDESReturnsError verifies that an SNMPv3 profile
// using priv_protocol DES (cryptographically broken) causes Load to return an error.
func TestNormalizePrivProtocolDESReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      priv_protocol: DES
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for priv_protocol DES (cryptographically broken)")
	}
}

// TestNormalizePrivProtocolUnknownReturnsError verifies that an unrecognized
// priv protocol string causes Load to return an error.
func TestNormalizePrivProtocolUnknownReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      priv_protocol: 3DES
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown priv_protocol")
	}
}

// TestValidateFederationSpokeMissingHubURL verifies that a spoke role config
// without hub_url causes Load to return an error.
func TestValidateFederationSpokeMissingHubURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: spoke
  spoke:
    spoke_id: dc-a
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for spoke role missing hub_url")
	}
}

// TestValidateFederationSpokeMissingTLS verifies that a spoke config with
// hub_url set but missing tls fields causes Load to return an error.
func TestValidateFederationSpokeMissingTLS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: spoke
  spoke:
    spoke_id: dc-a
    hub_url: https://hub:9101
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for spoke role missing TLS fields")
	}
}

// TestValidateFederationHubMissingTLS verifies that a hub role config without
// TLS fields causes Load to return an error.
func TestValidateFederationHubMissingTLS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: hub
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for hub role missing TLS fields")
	}
}

// TestValidateFederationHubSpokeTimeoutTooSmall verifies that a hub config
// with spoke_timeout shorter than 2×discovery.interval causes Load to return an error.
func TestValidateFederationHubSpokeTimeoutTooSmall(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	path := filepath.Join(dir, "config.yaml")
	body := "discovery:\n  interval: 60s\nfederation:\n  role: hub\n  spoke_timeout: 60s\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: spoke_timeout < 2 × discovery.interval")
	}
}

// TestValidateFederationUnknownRole verifies that an unrecognized federation
// role causes Load to return an error.
func TestValidateFederationUnknownRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("federation:\n  role: unknown_role\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown federation role")
	}
}

// TestValidateTargetsDuplicateHostPort verifies that two targets with the same
// host:port pair are rejected at validation time.
func TestValidateTargetsDuplicateHostPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
    port: 161
  - host: 198.51.100.20
    port: 161
  - host: 198.51.100.10
    port: 161
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate host:port, got nil")
	}
	if !strings.Contains(err.Error(), "duplicates") {
		t.Errorf("error %q should mention 'duplicates'", err.Error())
	}
	if !strings.Contains(err.Error(), "198.51.100.10") {
		t.Errorf("error %q should identify the duplicate host", err.Error())
	}
}

// TestValidateTargetsSameHostDifferentPort verifies that two targets sharing the
// same host but different ports are accepted (not considered duplicates).
func TestValidateTargetsSameHostDifferentPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
    port: 161
  - host: 198.51.100.10
    port: 1161
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected same host with different ports to be valid, got: %v", err)
	}
}

// TestValidateTargetsEmptyLabelKey verifies that a target with an empty label
// key is rejected at validation time (it would produce an invalid Prometheus label).
func TestValidateTargetsEmptyLabelKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 198.51.100.0/24
targets:
  - host: 198.51.100.10
    port: 161
    labels:
      "": "value"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty label key, got nil")
	}
	if !strings.Contains(err.Error(), "label key must not be empty") {
		t.Errorf("error %q should mention 'label key must not be empty'", err.Error())
	}
}

func TestFederationKnownLinkOptionalLinkKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// link_kind is optional; omitting it is valid.
	body := `
federation:
  known_inter_domain_links:
    - local_device: sw-a
      local_port: Gi0/1
      remote_device: sw-b
      remote_port: Gi0/2
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for link without link_kind: %v", err)
	}
	if c.Federation.KnownInterDomainLinks[0].LinkKind != "" {
		t.Errorf("LinkKind = %q, want empty (default applied by hub at injection time)", c.Federation.KnownInterDomainLinks[0].LinkKind)
	}
}

// TestValidateHTTPEndpoint verifies validateHTTPEndpoint rejects bad schemes and
// empty endpoints, and accepts valid http/https URLs.
func TestValidateHTTPEndpoint(t *testing.T) {
	invalid := []string{
		"",
		"file:///var/run/otlp.sock",
		"ftp://collector:21",
		"://no-scheme",
	}
	for _, tc := range invalid {
		if err := validateHTTPEndpoint(tc); err == nil {
			t.Errorf("validateHTTPEndpoint(%q) = nil, want error", tc)
		}
	}

	valid := []string{
		"http://alloy:4318",
		"https://collector:4318",
		"http://127.0.0.1:4318",
		"https://otelcol.example.com:4318/v1/traces",
	}
	for _, tc := range valid {
		if err := validateHTTPEndpoint(tc); err != nil {
			t.Errorf("validateHTTPEndpoint(%q) = %v, want nil", tc, err)
		}
	}
}

// TestOTLPHeartbeatCyclesDefault verifies that HeartbeatCycles defaults to 10
// when not set in the config.
func TestOTLPHeartbeatCyclesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Output.OTLP.HeartbeatCycles != 10 {
		t.Errorf("HeartbeatCycles default = %d, want 10", c.Output.OTLP.HeartbeatCycles)
	}
}

// TestOTLPProtocolDefault verifies the #82 backward-compatible default: an OTLP
// block with only endpoint set defaults protocol=http (payload encoding is
// always protobuf — there is no encoding config key).
func TestOTLPProtocolDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
    endpoint: http://collector:4318
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Output.OTLP.Protocol != "http" {
		t.Errorf("Protocol default = %q, want http", c.Output.OTLP.Protocol)
	}
}

// TestOTLPValidationRejectsBadProtocol verifies an unknown protocol is rejected.
func TestOTLPValidationRejectsBadProtocol(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
    endpoint: http://collector:4318
    protocol: ftp
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown otlp protocol, got nil")
	}
}

// TestOTLPRejectsRemovedEncodingKey verifies the removed `encoding` key is now
// rejected as an unknown field (the SDK is protobuf-only; there is no encoding
// option). This guards against the key silently reappearing.
func TestOTLPRejectsRemovedEncodingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
    endpoint: http://collector:4318
    encoding: protobuf
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error for removed otlp encoding key, got nil")
	}
}

// TestOTLPGRPCEndpointAcceptsBareAuthority verifies that a grpc protocol allows
// a bare host:port endpoint (no http/https scheme required).
func TestOTLPGRPCEndpointAcceptsBareAuthority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
    protocol: grpc
    endpoint: collector:4317
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load with grpc bare authority: %v", err)
	}
}

// TestOTLPValidationRejectsEnabledWithoutEndpoint verifies that enabling OTLP
// without an endpoint causes Load to return an error.
func TestOTLPValidationRejectsEnabledWithoutEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for OTLP enabled without endpoint, got nil")
	}
}

// TestOTLPValidationRejectsBadScheme verifies that enabling OTLP with an
// unsupported URL scheme causes Load to return an error.
func TestOTLPValidationRejectsBadScheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: true
    endpoint: ftp://collector:21
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for OTLP enabled with ftp:// endpoint, got nil")
	}
}

// TestOTLPValidationRejectsNegativeHeartbeatCyclesWhenDisabled verifies that a
// negative heartbeat_cycles value is rejected even when OTLP is disabled.
func TestOTLPValidationRejectsNegativeHeartbeatCyclesWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
output:
  otlp:
    enabled: false
    heartbeat_cycles: -1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for heartbeat_cycles: -1 with OTLP disabled, got nil")
	}
}

func TestValidateHTTPSEndpoint(t *testing.T) {
	invalid := []string{
		"",
		"http://hub:9101",
		"ftp://hub:9101",
		"://no-scheme",
	}
	for _, tc := range invalid {
		if err := validateHTTPSEndpoint(tc); err == nil {
			t.Errorf("validateHTTPSEndpoint(%q) = nil, want error", tc)
		}
	}
	valid := []string{
		"https://hub:9101",
		"https://hub.example.com:9101",
	}
	for _, tc := range valid {
		if err := validateHTTPSEndpoint(tc); err != nil {
			t.Errorf("validateHTTPSEndpoint(%q) = %v, want nil", tc, err)
		}
	}
}

// D8: FDB MaxVlans validation tests.

func TestFDBMaxVlansZeroFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
modules:
  fdb:
    max_vlans: 0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// max_vlans=0 is below the minimum of 1 (after applyDefaults sets 0→100, this
	// only fires when the user explicitly sets a value that applyDefaults won't touch).
	// We need to set it to a negative value to bypass applyDefaults.
	// Instead, test the negative path directly.
	c := &Config{Modules: ModulesConfig{FDB: FDBConfig{MaxVlans: 0}}}
	c.applyDefaults()
	// After applyDefaults, MaxVlans should be 100 (default).
	if c.Modules.FDB.MaxVlans != 100 {
		t.Errorf("MaxVlans default = %d, want 100", c.Modules.FDB.MaxVlans)
	}
	// Manually set to -1 to test validation directly.
	c.Modules.FDB.MaxVlans = -1
	if err := c.validateFDB(); err == nil {
		t.Fatal("expected error for max_vlans=-1, got nil")
	}
	_ = path // suppress unused warning
}

func TestFDBMaxVlansTooHighFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
modules:
  fdb:
    max_vlans: 5000
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for max_vlans=5000, got nil")
	}
}

func TestFDBMaxVlansValidPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
modules:
  fdb:
    max_vlans: 50
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for max_vlans=50: %v", err)
	}
	if c.Modules.FDB.MaxVlans != 50 {
		t.Errorf("MaxVlans = %d, want 50", c.Modules.FDB.MaxVlans)
	}
}

func TestFDBMaxVlansDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Modules.FDB.MaxVlans != 100 {
		t.Errorf("MaxVlans default = %d, want 100", c.Modules.FDB.MaxVlans)
	}
}

// D15: Retries validation tests.

func TestCredentialRetriesNegativeFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
      retries: -1
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for retries=-1, got nil")
	}
}

func TestCredentialRetriesPositivePasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: p1
      type: snmp_v2c
      community_env: C
      retries: 3
  fallback_order: [p1]
  trial_rate_per_second: 1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for retries=3: %v", err)
	}
	if c.Credentials.Profiles[0].Retries != 3 {
		t.Errorf("Retries = %d, want 3", c.Credentials.Profiles[0].Retries)
	}
}

func TestValidateFederationSpokeRejectsInsecureHubURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
federation:
  role: spoke
  spoke:
    spoke_id: dc-a
    hub_url: http://hub:9101
    tls_ca_cert: /nonexistent/ca.pem
    tls_cert: /nonexistent/spoke.crt
    tls_key: /nonexistent/spoke.key
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for http:// spoke hub_url, got nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q should mention https", err.Error())
	}
}

// ── listen TLS validation tests ───────────────────────────────────────────────

// TestListenAddrDefault verifies that the listen address defaults to ":9100".
func TestListenAddrDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Listen.Addr != ":9100" {
		t.Errorf("Listen.Addr default = %q, want :9100", c.Listen.Addr)
	}
}

// TestListenTLSCertFileRemovedKey verifies that the removed tls_cert_file key
// produces a parse error (removed in v1.5.0; use listen.web_config_file).
func TestListenTLSCertFileRemovedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen:
  tls_cert_file: /some/cert.pem
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error for removed key tls_cert_file, got nil")
	}
}

// TestListenTLSKeyFileRemovedKey verifies that the removed tls_key_file key
// produces a parse error (removed in v1.5.0; use listen.web_config_file).
func TestListenTLSKeyFileRemovedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen:
  tls_key_file: /some/key.pem
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error for removed key tls_key_file, got nil")
	}
}

// TestListenTLSBothRemovedKeys verifies that using both removed tls_cert_file
// and tls_key_file keys produces a parse error (removed in v1.5.0).
func TestListenTLSBothRemovedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
listen:
  tls_cert_file: /nonexistent/cert.pem
  tls_key_file: /nonexistent/key.pem
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error for removed keys tls_cert_file/tls_key_file, got nil")
	}
}

// TestListenWebConfigFileAccepted verifies that setting web_config_file alone
// (no tls_cert_file/tls_key_file) loads cleanly.
func TestListenWebConfigFileAccepted(t *testing.T) {
	dir := t.TempDir()
	webCfgPath := filepath.Join(dir, "web-config.yml")
	if err := os.WriteFile(webCfgPath, []byte("# empty web-config is valid\n"), 0o600); err != nil {
		t.Fatalf("write web-config: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "listen:\n  web_config_file: " + webCfgPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("expected valid config with web_config_file, got: %v", err)
	}
	if c.Listen.WebConfigFile != webCfgPath {
		t.Errorf("Listen.WebConfigFile = %q, want %q", c.Listen.WebConfigFile, webCfgPath)
	}
}

// TestListenWebConfigFileMissingFails verifies that a non-existent
// web_config_file path causes Load to return an error.
func TestListenWebConfigFileMissingFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "listen:\n  web_config_file: /nonexistent/web-config.yml\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected error for non-existent web_config_file, got nil")
	}
}

// TestListenWebConfigFileWithLegacyTLSRejected verifies that setting
// tls_cert_file/tls_key_file alongside web_config_file is rejected — these
// keys were removed in v1.5.0 and produce a parse error regardless.
func TestListenWebConfigFileWithLegacyTLSRejected(t *testing.T) {
	dir := t.TempDir()
	webCfgPath := filepath.Join(dir, "web-config.yml")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	for _, p := range []string{webCfgPath, certPath, keyPath} {
		if err := os.WriteFile(p, []byte("dummy"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "listen:\n  web_config_file: " + webCfgPath + "\n  tls_cert_file: " + certPath + "\n  tls_key_file: " + keyPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(cfgPath); err == nil {
		t.Fatal("expected error when removed tls_cert_file/tls_key_file keys are present, got nil")
	}
}

// TestListenTLSNeitherSet verifies that omitting both TLS fields is valid
// (plain HTTP mode).
func TestListenTLSNeitherSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected plain-HTTP config to be valid, got: %v", err)
	}
}

// TestListenTLSCertExistsKeyMissingRemovedKey verifies that using the removed
// tls_cert_file/tls_key_file keys always produces a parse error (removed in
// v1.5.0), regardless of whether the referenced files exist.
func TestListenTLSCertExistsKeyMissingRemovedKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, []byte("dummy cert"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	body := "listen:\n  tls_cert_file: " + certPath + "\n  tls_key_file: /nonexistent/key.pem\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error for removed keys tls_cert_file/tls_key_file, got nil")
	}
}

// TestValidateCycleBudgetFractionOutOfRange verifies that
// discovery.cycle_budget_fraction values outside (0, 1] are rejected.
func TestValidateCycleBudgetFractionOutOfRange(t *testing.T) {
	cases := []string{
		// > 1
		"discovery:\n  cycle_budget_fraction: 1.5\n",
		// exactly 0 — applyDefaults only replaces 0.0 with 0.8, so the YAML
		// value 0.0 is replaced by the default; use a small negative value to
		// bypass the defaulting and reach the validation guard directly.
	}
	for _, body := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("cycle_budget_fraction config %q: expected error, got nil", body)
		}
	}

	// Negative value bypasses applyDefaults (which only replaces 0) and
	// directly hits the validation guard.
	c := &Config{}
	c.applyDefaults()
	c.Discovery.CycleBudgetFraction = -0.1
	if err := c.validate(); err == nil {
		t.Fatal("expected error for cycle_budget_fraction=-0.1, got nil")
	}
}

// TestDiscoveryMaxGraphFieldsParsed verifies that max_graph_devices and
// max_graph_edges are correctly read from YAML and stored in DiscoveryConfig.
func TestDiscoveryMaxGraphFieldsParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
discovery:
  max_graph_devices: 500
  max_graph_edges: 2000
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Discovery.MaxGraphDevices != 500 {
		t.Errorf("MaxGraphDevices = %d, want 500", c.Discovery.MaxGraphDevices)
	}
	if c.Discovery.MaxGraphEdges != 2000 {
		t.Errorf("MaxGraphEdges = %d, want 2000", c.Discovery.MaxGraphEdges)
	}
}

// TestDiscoveryMaxGraphFieldsDefaultToZero verifies that omitting the fields
// results in a zero value (disabled / no limit).
func TestDiscoveryMaxGraphFieldsDefaultToZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Discovery.MaxGraphDevices != 0 {
		t.Errorf("MaxGraphDevices default = %d, want 0 (unlimited)", c.Discovery.MaxGraphDevices)
	}
	if c.Discovery.MaxGraphEdges != 0 {
		t.Errorf("MaxGraphEdges default = %d, want 0 (unlimited)", c.Discovery.MaxGraphEdges)
	}
}

// TestValidateTimeoutPerModuleNegative verifies that a negative
// discovery.timeout_per_module causes Load to return an error.
func TestValidateTimeoutPerModuleNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "discovery:\n  timeout_per_module: -1s\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for timeout_per_module=-1s, got nil")
	}
}

// TestValidateHTTPEndpointNoHost verifies that validateHTTPEndpoint rejects a
// URL that has a valid scheme but an empty host (covers the "host is required"
// error path).
func TestValidateHTTPEndpointNoHost(t *testing.T) {
	// "http:///path" parses successfully with scheme "http" but empty host.
	if err := validateHTTPEndpoint("http:///path"); err == nil {
		t.Fatal("expected error for http:///path (empty host), got nil")
	}
}

// TestValidateHTTPSEndpointNoHost verifies that validateHTTPSEndpoint rejects
// a URL that has scheme "https" but an empty host.
func TestValidateHTTPSEndpointNoHost(t *testing.T) {
	if err := validateHTTPSEndpoint("https:///path"); err == nil {
		t.Fatal("expected error for https:///path (empty host), got nil")
	}
}

// TestDiscoveryMaxGraphDevicesNegativeFails verifies that a negative
// discovery.max_graph_devices causes Load to return an error.
func TestDiscoveryMaxGraphDevicesNegativeFails(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	c.Discovery.MaxGraphDevices = -1
	err := c.validate()
	if err == nil {
		t.Fatal("expected error for max_graph_devices=-1, got nil")
	}
	if !strings.Contains(err.Error(), "max_graph_devices") {
		t.Errorf("error %q should mention max_graph_devices", err.Error())
	}
}

// TestDiscoveryMaxGraphEdgesNegativeFails verifies that a negative
// discovery.max_graph_edges causes Load to return an error.
func TestDiscoveryMaxGraphEdgesNegativeFails(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	c.Discovery.MaxGraphEdges = -1
	err := c.validate()
	if err == nil {
		t.Fatal("expected error for max_graph_edges=-1, got nil")
	}
	if !strings.Contains(err.Error(), "max_graph_edges") {
		t.Errorf("error %q should mention max_graph_edges", err.Error())
	}
}

// TestDiscoveryPerTargetPDURateNegativeFails verifies that a negative
// discovery.per_target_pdu_rate_per_second causes validate to return an error
// (issue #72). The default (0 = unlimited) is exercised implicitly by every
// other config test that omits the field.
func TestDiscoveryPerTargetPDURateNegativeFails(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	c.Discovery.PerTargetPDURatePerSecond = -1
	err := c.validate()
	if err == nil {
		t.Fatal("expected error for per_target_pdu_rate_per_second=-1, got nil")
	}
	if !strings.Contains(err.Error(), "per_target_pdu_rate_per_second") {
		t.Errorf("error %q should mention per_target_pdu_rate_per_second", err.Error())
	}
}

// TestDiscoveryPerTargetPDURateDefaultUnlimited verifies the field defaults to
// 0 (unlimited) — applyDefaults must not set it, preserving today's behaviour.
func TestDiscoveryPerTargetPDURateDefaultUnlimited(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if c.Discovery.PerTargetPDURatePerSecond != 0 {
		t.Errorf("PerTargetPDURatePerSecond default = %d, want 0 (unlimited)", c.Discovery.PerTargetPDURatePerSecond)
	}
}

// TestFederationHubMaxGraphDevicesNegativeFails verifies that a negative
// federation.hub.max_graph_devices causes Load to return an error.
func TestFederationHubMaxGraphDevicesNegativeFails(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	path := filepath.Join(dir, "config.yaml")
	body := "discovery:\n  interval: 60s\nfederation:\n  role: hub\n  spoke_timeout: 180s\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n    max_graph_devices: -1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for federation.hub.max_graph_devices=-1, got nil")
	}
	if !strings.Contains(err.Error(), "max_graph_devices") {
		t.Errorf("error %q should mention max_graph_devices", err.Error())
	}
}

// TestFederationHubMaxGraphEdgesNegativeFails verifies that a negative
// federation.hub.max_graph_edges causes Load to return an error.
func TestFederationHubMaxGraphEdgesNegativeFails(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	path := filepath.Join(dir, "config.yaml")
	body := "discovery:\n  interval: 60s\nfederation:\n  role: hub\n  spoke_timeout: 180s\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n    max_graph_edges: -1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for federation.hub.max_graph_edges=-1, got nil")
	}
	if !strings.Contains(err.Error(), "max_graph_edges") {
		t.Errorf("error %q should mention max_graph_edges", err.Error())
	}
}

// ── federation TLS file-existence tests ──────────────────────────────────────

// TestFederationSpokeRejectsNonExistentTLSFiles verifies that a spoke config
// with syntactically valid but non-existent TLS file paths causes Load to
// return an error naming the missing field.
func TestFederationSpokeRejectsNonExistentTLSFiles(t *testing.T) {
	cases := []struct {
		name    string
		ca      string
		cert    string
		key     string
		wantErr string
	}{
		{
			name:    "missing ca cert",
			ca:      "/nonexistent/ca.pem",
			cert:    "", // will be replaced with real file
			key:     "", // will be replaced with real file
			wantErr: "federation.spoke.tls_ca_cert",
		},
		{
			name:    "missing tls cert",
			ca:      "", // real
			cert:    "/nonexistent/spoke.crt",
			key:     "", // real
			wantErr: "federation.spoke.tls_cert",
		},
		{
			name:    "missing tls key",
			ca:      "", // real
			cert:    "", // real
			key:     "/nonexistent/spoke.key",
			wantErr: "federation.spoke.tls_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			realCA, realCert, realKey := writeTLSStubs(t, dir)
			ca := tc.ca
			if ca == "" {
				ca = realCA
			}
			cert := tc.cert
			if cert == "" {
				cert = realCert
			}
			key := tc.key
			if key == "" {
				key = realKey
			}
			cfgPath := filepath.Join(dir, "config.yaml")
			body := "federation:\n  role: spoke\n  spoke:\n    spoke_id: dc-a\n    hub_url: https://hub:9101\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(cfgPath)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestFederationHubRejectsNonExistentTLSFiles verifies that a hub config with
// syntactically valid but non-existent TLS file paths causes Load to return an
// error naming the missing field.
func TestFederationHubRejectsNonExistentTLSFiles(t *testing.T) {
	cases := []struct {
		name    string
		ca      string
		cert    string
		key     string
		wantErr string
	}{
		{
			name:    "missing ca cert",
			ca:      "/nonexistent/ca.pem",
			cert:    "",
			key:     "",
			wantErr: "federation.hub.tls_ca_cert",
		},
		{
			name:    "missing tls cert",
			ca:      "",
			cert:    "/nonexistent/hub.crt",
			key:     "",
			wantErr: "federation.hub.tls_cert",
		},
		{
			name:    "missing tls key",
			ca:      "",
			cert:    "",
			key:     "/nonexistent/hub.key",
			wantErr: "federation.hub.tls_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			realCA, realCert, realKey := writeTLSStubs(t, dir)
			ca := tc.ca
			if ca == "" {
				ca = realCA
			}
			cert := tc.cert
			if cert == "" {
				cert = realCert
			}
			key := tc.key
			if key == "" {
				key = realKey
			}
			cfgPath := filepath.Join(dir, "config.yaml")
			// spoke_timeout must be >= 2 × interval; use defaults (interval=60s, spoke_timeout=3×60s=180s).
			body := "federation:\n  role: hub\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(cfgPath)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestFederationSpokeValidWithExistingTLSFiles verifies that a fully-configured
// spoke role with real (stub) TLS files on disk passes validation.
func TestFederationSpokeValidWithExistingTLSFiles(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "federation:\n  role: spoke\n  spoke:\n    spoke_id: dc-a\n    hub_url: https://hub:9101\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(cfgPath); err != nil {
		t.Fatalf("expected valid spoke config with existing TLS files, got: %v", err)
	}
}

// TestCredentialsRejectV3AuthProtocolWithoutAuthKeyEnv verifies that an SNMPv3
// profile with auth_protocol set but no auth_key_env causes Load to return an error.
func TestCredentialsRejectV3AuthProtocolWithoutAuthKeyEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: SHA-256
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for snmp_v3 profile with auth_protocol but no auth_key_env")
	}
}

// TestCredentialsRejectV3PrivProtocolWithoutPrivKeyEnv verifies that an SNMPv3
// profile with priv_protocol set but no priv_key_env causes Load to return an error.
func TestCredentialsRejectV3PrivProtocolWithoutPrivKeyEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
credentials:
  profiles:
    - name: core-v3
      type: snmp_v3
      username_env: SNMP_V3_USER
      auth_protocol: SHA-256
      auth_key_env: SNMP_V3_AUTH_KEY
      priv_protocol: AES
  fallback_order: [core-v3]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for snmp_v3 profile with priv_protocol but no priv_key_env")
	}
}

// TestFederationHubValidWithExistingTLSFiles verifies that a fully-configured
// hub role with real (stub) TLS files on disk passes validation.
func TestFederationHubValidWithExistingTLSFiles(t *testing.T) {
	dir := t.TempDir()
	ca, cert, key := writeTLSStubs(t, dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	// Use default spoke_timeout (3 × interval = 3 × 60s = 180s ≥ 2 × 60s).
	body := "federation:\n  role: hub\n  hub:\n    tls_ca_cert: " + ca + "\n    tls_cert: " + cert + "\n    tls_key: " + key + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(cfgPath); err != nil {
		t.Fatalf("expected valid hub config with existing TLS files, got: %v", err)
	}
}

// --- loose_device_name_matching tests (issue #10) ---
//
// The v1.3.0 *bool `strict_device_name_matching` field was replaced in v1.4.0
// by a zero-value-default `loose_device_name_matching bool`. The old YAML key
// is still accepted for one minor release with a deprecation warning. These
// tests cover the four semantic outcomes plus the new-key path.

// TestLooseDeviceNameMatchingNewKey: setting the new key to true is honoured.
func TestLooseDeviceNameMatchingNewKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("federation:\n  hub:\n    loose_device_name_matching: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.Federation.Hub.LooseDeviceNameMatching {
		t.Errorf("LooseDeviceNameMatching = false, want true")
	}
}

// TestLooseDeviceNameMatchingNewKeyDefault: omitting the key yields strict
// matching (the safe default).
func TestLooseDeviceNameMatchingNewKeyDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Federation.Hub.LooseDeviceNameMatching {
		t.Errorf("LooseDeviceNameMatching = true, want false (default = strict)")
	}
}

// TestStrictDeviceNameMatchingRemovedKeyTrue: the removed key strict_device_name_matching=true
// must produce a parse error (removed in v1.5.0; use loose_device_name_matching instead).
func TestStrictDeviceNameMatchingRemovedKeyTrue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("federation:\n  hub:\n    strict_device_name_matching: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error for removed key strict_device_name_matching, got nil")
	}
}

// TestStrictDeviceNameMatchingRemovedKeyFalse: the removed key strict_device_name_matching=false
// must produce a parse error (removed in v1.5.0; use loose_device_name_matching instead).
func TestStrictDeviceNameMatchingRemovedKeyFalse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("federation:\n  hub:\n    strict_device_name_matching: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error for removed key strict_device_name_matching, got nil")
	}
}

// --- disable_v2_mib tests (issue #10) ---
//
// The v1.3.0 *bool `use_v2_mib` field was replaced in v1.4.0 by a
// zero-value-default `disable_v2_mib bool`. The old YAML key is still
// accepted for one minor release with a deprecation warning.

// TestDisableV2MIBNewKey: setting the new key to true is honoured.
func TestDisableV2MIBNewKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("modules:\n  bgp:\n    disable_v2_mib: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !c.Modules.BGP.DisableV2MIB {
		t.Errorf("DisableV2MIB = false, want true")
	}
}

// TestDisableV2MIBNewKeyDefault: omitting the key yields v2 enabled (the safe
// default).
func TestDisableV2MIBNewKeyDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Modules.BGP.DisableV2MIB {
		t.Errorf("DisableV2MIB = true, want false (default = v2 enabled)")
	}
}

// TestUseV2MIBRemovedKeyTrue: the removed key use_v2_mib=true must produce a
// parse error (removed in v1.5.0; use disable_v2_mib instead).
func TestUseV2MIBRemovedKeyTrue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("modules:\n  bgp:\n    use_v2_mib: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error for removed key use_v2_mib, got nil")
	}
}

// TestUseV2MIBRemovedKeyFalse: the removed key use_v2_mib=false must produce
// a parse error (removed in v1.5.0; use disable_v2_mib instead).
func TestUseV2MIBRemovedKeyFalse(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("modules:\n  bgp:\n    use_v2_mib: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected parse error for removed key use_v2_mib, got nil")
	}
}

// TestRemovedKeysProduceParseError verifies that configs using any of the three
// removed deprecated keys (strict_device_name_matching, use_v2_mib,
// tls_cert_file, tls_key_file) produce a parse error from Load (removed in
// v1.5.0; the YAML loader uses KnownFields(true) to enforce this).
func TestRemovedKeysProduceParseError(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "strict_device_name_matching with use_v2_mib",
			yaml: "federation:\n  hub:\n    strict_device_name_matching: false\nmodules:\n  bgp:\n    use_v2_mib: false\n",
		},
		{
			name: "strict_device_name_matching alone",
			yaml: "federation:\n  hub:\n    strict_device_name_matching: true\n",
		},
		{
			name: "use_v2_mib alone",
			yaml: "modules:\n  bgp:\n    use_v2_mib: true\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(cfgPath)
			if err == nil {
				t.Fatalf("expected parse error for removed deprecated key(s), got nil")
			}
		})
	}
}

// TestTracesDefaultSampleRate verifies output.otlp.traces defaults: disabled
// with a 0.1 sample rate when the block is omitted (issue #68).
func TestTracesDefaultSampleRate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("targets: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Output.OTLP.Traces.Enabled {
		t.Error("traces.enabled default = true, want false")
	}
	if c.Output.OTLP.Traces.SampleRate == nil || *c.Output.OTLP.Traces.SampleRate != 0.1 {
		t.Errorf("traces.sample_rate default = %v, want 0.1", c.Output.OTLP.Traces.SampleRate)
	}
}

// TestTracesExplicitZeroSampleRateHonoured verifies an explicit sample_rate of
// 0.0 is preserved rather than overridden by the 0.1 default (the reason the
// field is a pointer).
func TestTracesExplicitZeroSampleRateHonoured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
targets: []
output:
  otlp:
    traces:
      enabled: true
      sample_rate: 0.0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Output.OTLP.Traces.SampleRate == nil || *c.Output.OTLP.Traces.SampleRate != 0.0 {
		t.Errorf("traces.sample_rate = %v, want explicit 0.0", c.Output.OTLP.Traces.SampleRate)
	}
}

// TestTracesSampleRateOutOfRangeRejected verifies sample_rate outside [0,1] is
// a validation error.
func TestTracesSampleRateOutOfRangeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
targets: []
output:
  otlp:
    traces:
      sample_rate: 1.5
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for traces.sample_rate=1.5")
	}
}

// TestExampleConfigLoadsCleanly verifies that config/example.yaml loads without
// error under KnownFields mode. This is the schema-parity guard: any key in
// example.yaml that is not a real struct tag causes a parse error here (#63).
func TestExampleConfigLoadsCleanly(t *testing.T) {
	// Resolve relative to this file's package directory.
	path := filepath.Join("..", "..", "config", "example.yaml")
	if _, err := Load(path); err != nil {
		t.Fatalf("config/example.yaml failed to load: %v", err)
	}
}
