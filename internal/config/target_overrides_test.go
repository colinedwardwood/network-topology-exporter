package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOverrideConfig writes body to a temp config file and loads it, returning
// the parsed config and any load error. Used by the issue #74 tests below.
func writeOverrideConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return Load(path)
}

// Issue #74: longest-prefix-match resolution. Overlapping CIDRs (/8, /16, /32)
// must resolve most-specific-first, and the matching override's module set must
// be returned.
func TestModulesForIPLongestPrefixWins(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.0.0.0/8
      modules: [lldp]
    - cidr: 10.1.0.0/16
      modules: [lldp, cdp]
    - cidr: 10.1.2.3/32
      modules: [lldp, cdp, bgp]
modules:
  lldp:
    enabled: true
  cdp:
    enabled: true
  bgp:
    enabled: true
targets:
  - host: 10.1.2.3
`
	c, err := writeOverrideConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		ip   string
		want map[string]bool
	}{
		{"10.1.2.3", map[string]bool{"lldp": true, "cdp": true, "bgp": true}}, // /32 wins
		{"10.1.9.9", map[string]bool{"lldp": true, "cdp": true}},              // /16 wins
		{"10.9.9.9", map[string]bool{"lldp": true}},                           // /8 wins
	}
	for _, tc := range cases {
		got, matched := c.ModulesForIP(net.ParseIP(tc.ip))
		if !matched {
			t.Errorf("ModulesForIP(%s) matched=false, want true", tc.ip)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("ModulesForIP(%s) = %v, want %v", tc.ip, got, tc.want)
			continue
		}
		for m := range tc.want {
			if !got[m] {
				t.Errorf("ModulesForIP(%s) missing module %q (got %v)", tc.ip, m, got)
			}
		}
	}
}

// An IP matching no override returns (nil, false) — the signal to run all
// enabled modules, today's unchanged default.
func TestModulesForIPNoMatch(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: [lldp]
modules:
  lldp:
    enabled: true
targets:
  - host: 10.9.9.9
`
	c, err := writeOverrideConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, matched := c.ModulesForIP(net.ParseIP("10.9.9.9"))
	if matched {
		t.Errorf("ModulesForIP(10.9.9.9) matched=true (got %v), want false", got)
	}
	if got != nil {
		t.Errorf("ModulesForIP(10.9.9.9) = %v, want nil", got)
	}
}

// With no target_overrides at all, ModulesForIP always returns (nil, false) so
// the discovery loop runs every enabled module — byte-for-byte today's default.
func TestModulesForIPNoOverridesConfigured(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
modules:
  lldp:
    enabled: true
targets:
  - host: 10.1.2.3
`
	c, err := writeOverrideConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Discovery.overrideResolver != nil {
		t.Fatal("overrideResolver should be nil when no overrides are configured")
	}
	if got, matched := c.ModulesForIP(net.ParseIP("10.1.2.3")); matched || got != nil {
		t.Errorf("ModulesForIP = (%v, %v), want (nil, false)", got, matched)
	}
}

// Equal-prefix tiebreak: two overrides with the SAME prefix length but different
// networks resolve deterministically by first-declared. (Exact-duplicate CIDRs
// are a separate, rejected case — see TestTargetOverrideRejectsDuplicateCIDR.)
func TestModulesForIPEqualPrefixTiebreak(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: [lldp]
    - cidr: 10.2.0.0/16
      modules: [cdp]
modules:
  lldp:
    enabled: true
  cdp:
    enabled: true
targets:
  - host: 10.1.0.1
`
	c, err := writeOverrideConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, matched := c.ModulesForIP(net.ParseIP("10.2.0.1"))
	if !matched || !got["cdp"] || got["lldp"] {
		t.Errorf("ModulesForIP(10.2.0.1) = (%v, %v), want cdp-only", got, matched)
	}
}

// Validation: an override referencing a globally-disabled module is rejected.
func TestTargetOverrideRejectsDisabledModule(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: [bgp]
modules:
  lldp:
    enabled: true
  bgp:
    enabled: false
targets:
  - host: 10.1.0.1
`
	_, err := writeOverrideConfig(t, body)
	if err == nil {
		t.Fatal("expected error for override referencing globally-disabled module")
	}
	if !strings.Contains(err.Error(), "not enabled globally") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Validation: a malformed CIDR is rejected.
func TestTargetOverrideRejectsMalformedCIDR(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: not-a-cidr
      modules: [lldp]
modules:
  lldp:
    enabled: true
targets:
  - host: 10.1.0.1
`
	_, err := writeOverrideConfig(t, body)
	if err == nil {
		t.Fatal("expected error for malformed cidr")
	}
	if !strings.Contains(err.Error(), "invalid cidr") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Validation: an empty modules list is rejected as ambiguous.
func TestTargetOverrideRejectsEmptyModules(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: []
modules:
  lldp:
    enabled: true
targets:
  - host: 10.1.0.1
`
	_, err := writeOverrideConfig(t, body)
	if err == nil {
		t.Fatal("expected error for empty modules list")
	}
	if !strings.Contains(err.Error(), "at least one module") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Validation: an unknown module name is rejected.
func TestTargetOverrideRejectsUnknownModule(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: [snmp]
modules:
  lldp:
    enabled: true
targets:
  - host: 10.1.0.1
`
	_, err := writeOverrideConfig(t, body)
	if err == nil {
		t.Fatal("expected error for unknown module name")
	}
	if !strings.Contains(err.Error(), "unknown module") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Validation: two overrides for the exact-same network are rejected.
func TestTargetOverrideRejectsDuplicateCIDR(t *testing.T) {
	body := `
discovery:
  scope:
    cidr_allow_list:
      - 10.0.0.0/8
  target_overrides:
    - cidr: 10.1.0.0/16
      modules: [lldp]
    - cidr: 10.1.0.0/16
      modules: [cdp]
modules:
  lldp:
    enabled: true
  cdp:
    enabled: true
targets:
  - host: 10.1.0.1
`
	_, err := writeOverrideConfig(t, body)
	if err == nil {
		t.Fatal("expected error for duplicate cidr")
	}
	if !strings.Contains(err.Error(), "duplicate cidr") {
		t.Errorf("unexpected error: %v", err)
	}
}
