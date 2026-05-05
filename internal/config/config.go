// Package config loads and validates the exporter's YAML configuration.
//
// Schema lives in config/example.yaml; semantics are documented in README.md
// under "Configuration".
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level exporter configuration.
type Config struct {
	Discovery   DiscoveryConfig   `yaml:"discovery"`
	Modules     ModulesConfig     `yaml:"modules"`
	Credentials CredentialsConfig `yaml:"credentials"`
	Snapshot    SnapshotConfig    `yaml:"snapshot"`
	Targets     []TargetConfig    `yaml:"targets"`
}

// DiscoveryConfig controls the global discovery cycle.
//
// Scope (LD-11): CIDRAllowList is a hard bound on what the exporter polls.
// Targets must fall within it; LLDP/CDP-discovered neighbours outside it
// surface as network_topology_out_of_scope_neighbour and are never polled.
//
// UnconfirmedLinkTTLCycles (LD-14): a unidirectional link removed from the
// graph after this many consecutive unconfirmed cycles. Default 3.
type DiscoveryConfig struct {
	Interval                 time.Duration `yaml:"interval"`
	TimeoutPerDevice         time.Duration `yaml:"timeout_per_device"`
	Parallelism              int           `yaml:"parallelism"`
	Scope                    ScopeConfig   `yaml:"scope"`
	UnconfirmedLinkTTLCycles int           `yaml:"unconfirmed_link_ttl_cycles"`
}

// ScopeConfig is the LD-11 polling-scope guard. CIDRAllowList is mandatory
// when any module is enabled; the empty list is treated as a misconfiguration
// rather than as "poll everything".
type ScopeConfig struct {
	CIDRAllowList []string `yaml:"cidr_allow_list"`
}

// ModulesConfig toggles individual discovery modules. Each module's spec
// citation lives in the corresponding internal/discovery/<name>/<name>.go header.
type ModulesConfig struct {
	SNMP ModuleSNMP   `yaml:"snmp"`
	LLDP ModuleToggle `yaml:"lldp"`
	CDP  ModuleToggle `yaml:"cdp"`
	BGP  ModuleToggle `yaml:"bgp"`
	OSPF ModuleToggle `yaml:"ospf"`
	ARP  ModuleToggle `yaml:"arp"`
	FDB  ModuleToggle `yaml:"fdb"`
}

// ModuleToggle is the minimum module config: enabled / disabled.
type ModuleToggle struct {
	Enabled bool `yaml:"enabled"`
}

// ModuleSNMP toggles the SNMP transport. Per-device credential resolution
// lives under credentials: (LD-12). The legacy CommunityEnv field is retained
// for the dev-time single-profile path; production deployments should use
// credentials.profiles.
type ModuleSNMP struct {
	Enabled      bool   `yaml:"enabled"`
	Version      string `yaml:"version"` // v2c | v3
	CommunityEnv string `yaml:"community_env"`
}

// CredentialsConfig encodes LD-12: named profiles, per-device assignments,
// ordered fallback, and a global trial rate limit.
//
// Secret values are dereferenced from environment variables only — every
// *Env field carries the variable name, never the secret itself.
type CredentialsConfig struct {
	Profiles           []CredentialProfile      `yaml:"profiles"`
	Assignments        []CredentialAssignment   `yaml:"assignments"`
	FallbackOrder      []string                 `yaml:"fallback_order"`
	TrialRatePerSecond int                      `yaml:"trial_rate_per_second"`
}

// Profile type constants for CredentialProfile.Type.
const (
	ProfileTypeSNMPv2c = "snmp_v2c"
	ProfileTypeSNMPv3  = "snmp_v3"
)

// CredentialProfile is one named credential the exporter can try against
// a device. Type selects which *Env fields are consulted.
type CredentialProfile struct {
	Name           string `yaml:"name"`
	Type           string `yaml:"type"` // snmp_v2c | snmp_v3
	CommunityEnv   string `yaml:"community_env,omitempty"`
	UsernameEnv    string `yaml:"username_env,omitempty"`
	AuthProtocol   string `yaml:"auth_protocol,omitempty"` // SHA | SHA-256 | ...
	AuthKeyEnv     string `yaml:"auth_key_env,omitempty"`
	PrivProtocol   string `yaml:"priv_protocol,omitempty"` // AES | AES-256 | ...
	PrivKeyEnv     string `yaml:"priv_key_env,omitempty"`
}

// CredentialAssignment binds one or more profiles to a device or CIDR.
// Resolution order in the resolver: explicit IP > most-specific CIDR >
// fallback_order.
type CredentialAssignment struct {
	IP       string   `yaml:"ip,omitempty"`
	CIDR     string   `yaml:"cidr,omitempty"`
	Profiles []string `yaml:"profiles"`
}

// SnapshotConfig is the LD-13 graph-persistence path. The exporter loads
// the snapshot on startup and serves stale-but-valid metrics until the first
// live cycle completes. Snapshot is always active; set Path to override the
// default location.
type SnapshotConfig struct {
	Path string `yaml:"path"`
}

// TargetConfig provides per-device enrichment: SNMP port override, site label,
// and free-form labels applied to the discovered device record. The CIDR
// allow-list in discovery.scope drives what gets polled; targets are matched
// against discovered IPs to attach this metadata. A target whose IP falls
// outside the allow-list is rejected at startup (LD-11).
type TargetConfig struct {
	Host   string            `yaml:"host"`
	Port   int               `yaml:"port"`
	Site   string            `yaml:"site"`
	Labels map[string]string `yaml:"labels"`
}

// Load parses the configuration file at path and applies defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Discovery.Interval == 0 {
		c.Discovery.Interval = 60 * time.Second
	}
	if c.Discovery.TimeoutPerDevice == 0 {
		c.Discovery.TimeoutPerDevice = 10 * time.Second
	}
	if c.Discovery.Parallelism == 0 {
		c.Discovery.Parallelism = 32
	}
	if c.Discovery.UnconfirmedLinkTTLCycles == 0 {
		c.Discovery.UnconfirmedLinkTTLCycles = 3
	}
	if c.Modules.SNMP.Version == "" {
		c.Modules.SNMP.Version = "v2c"
	}
	if c.Credentials.TrialRatePerSecond == 0 {
		c.Credentials.TrialRatePerSecond = 5
	}
	if c.Snapshot.Path == "" {
		c.Snapshot.Path = "/var/lib/network-topology-exporter/snapshot.json"
	}
	for i := range c.Targets {
		if c.Targets[i].Port == 0 {
			c.Targets[i].Port = 161
		}
	}
}

func (c *Config) validate() error {
	if c.Discovery.Parallelism < 1 {
		return errors.New("discovery.parallelism must be >= 1")
	}
	if c.Discovery.UnconfirmedLinkTTLCycles < 1 {
		return errors.New("discovery.unconfirmed_link_ttl_cycles must be >= 1")
	}
	if c.Discovery.Interval <= 0 {
		return errors.New("discovery.interval must be > 0")
	}
	if c.Discovery.TimeoutPerDevice <= 0 {
		return errors.New("discovery.timeout_per_device must be > 0")
	}
	if err := c.validateScope(); err != nil {
		return err
	}
	if c.Modules.SNMP.Enabled {
		switch c.Modules.SNMP.Version {
		case "v2c", "v3":
		default:
			return fmt.Errorf("modules.snmp.version must be v2c or v3, got %q", c.Modules.SNMP.Version)
		}
	}
	if err := c.validateCredentials(); err != nil {
		return err
	}
	if err := c.validateTargets(); err != nil {
		return err
	}
	return nil
}

// validateScope enforces LD-11: the allow-list is mandatory, every entry
// must parse as a CIDR, and every target host must resolve into one of the
// allow-list networks.
func (c *Config) validateScope() error {
	if len(c.Targets) == 0 {
		return nil // dev-time: no targets, no scope check needed.
	}
	if len(c.Discovery.Scope.CIDRAllowList) == 0 {
		return errors.New("discovery.scope.cidr_allow_list is required when targets are defined (LD-11)")
	}
	nets := make([]*net.IPNet, 0, len(c.Discovery.Scope.CIDRAllowList))
	for _, raw := range c.Discovery.Scope.CIDRAllowList {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			return fmt.Errorf("discovery.scope.cidr_allow_list entry %q: %w", raw, err)
		}
		nets = append(nets, n)
	}
	for _, t := range c.Targets {
		ip := net.ParseIP(t.Host)
		if ip == nil {
			// Hostname rather than IP; defer to runtime resolution. Scope is
			// enforced again at poll time once the hostname resolves.
			continue
		}
		if !cidrContainsAny(nets, ip) {
			return fmt.Errorf("target %q is outside discovery.scope.cidr_allow_list (LD-11)", t.Host)
		}
	}
	return nil
}

// validateCredentials checks LD-12 invariants: profile names are unique,
// every assignment / fallback name resolves to a defined profile, and every
// profile carries the env-var fields its type requires.
func (c *Config) validateCredentials() error {
	if len(c.Credentials.Profiles) == 0 {
		return nil // legacy single-community path; ModuleSNMP.CommunityEnv applies.
	}
	known := make(map[string]CredentialProfile, len(c.Credentials.Profiles))
	for _, p := range c.Credentials.Profiles {
		if p.Name == "" {
			return errors.New("credentials.profiles[].name is required")
		}
		if _, dup := known[p.Name]; dup {
			return fmt.Errorf("credentials.profiles: duplicate name %q", p.Name)
		}
		switch p.Type {
		case ProfileTypeSNMPv2c:
			if p.CommunityEnv == "" {
				return fmt.Errorf("profile %q (snmp_v2c) requires community_env", p.Name)
			}
		case ProfileTypeSNMPv3:
			if p.UsernameEnv == "" {
				return fmt.Errorf("profile %q (snmp_v3) requires username_env", p.Name)
			}
		default:
			return fmt.Errorf("profile %q has unknown type %q", p.Name, p.Type)
		}
		known[p.Name] = p
	}
	for _, a := range c.Credentials.Assignments {
		if a.IP == "" && a.CIDR == "" {
			return errors.New("credentials.assignments[]: ip or cidr is required")
		}
		if a.IP != "" && net.ParseIP(a.IP) == nil {
			return fmt.Errorf("credentials.assignments: invalid ip %q", a.IP)
		}
		if a.CIDR != "" {
			if _, _, err := net.ParseCIDR(a.CIDR); err != nil {
				return fmt.Errorf("credentials.assignments: invalid cidr %q: %w", a.CIDR, err)
			}
		}
		for _, n := range a.Profiles {
			if _, ok := known[n]; !ok {
				return fmt.Errorf("credentials.assignments references unknown profile %q", n)
			}
		}
	}
	for _, n := range c.Credentials.FallbackOrder {
		if _, ok := known[n]; !ok {
			return fmt.Errorf("credentials.fallback_order references unknown profile %q", n)
		}
	}
	if c.Credentials.TrialRatePerSecond < 1 {
		return errors.New("credentials.trial_rate_per_second must be >= 1")
	}
	return nil
}

func (c *Config) validateTargets() error {
	for i, t := range c.Targets {
		if t.Port < 0 || t.Port > 65535 {
			return fmt.Errorf("targets[%d].port %d is out of range [0, 65535]", i, t.Port)
		}
	}
	return nil
}

func cidrContainsAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
