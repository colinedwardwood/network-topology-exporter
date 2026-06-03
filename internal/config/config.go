// Package config loads and validates the exporter's YAML configuration.
//
// Schema lives in config/example.yaml; semantics are documented in README.md
// under "Configuration".
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level exporter configuration.
type Config struct {
	Listen      ListenConfig      `yaml:"listen"`
	Discovery   DiscoveryConfig   `yaml:"discovery"`
	Modules     ModulesConfig     `yaml:"modules"`
	Credentials CredentialsConfig `yaml:"credentials"`
	Snapshot    SnapshotConfig    `yaml:"snapshot"`
	Federation  FederationConfig  `yaml:"federation"`
	Output      OutputConfig      `yaml:"output"`
	Targets     []TargetConfig    `yaml:"targets"`
}

// ListenConfig holds the HTTP/HTTPS listen address and optional auth config.
// WebConfigFile points at a Prometheus exporter-toolkit web-config YAML — the
// same schema operators know from snmp_exporter, node_exporter,
// blackbox_exporter, etc. (see
// https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md).
// The toolkit handles basic_auth, server TLS, and full mTLS in one file.
//
// When both fields are empty the server uses plain HTTP — the default and the
// canonical Prometheus convention of "scrape from a private network".
type ListenConfig struct {
	Addr          string `yaml:"addr"`            // default ":9100"
	WebConfigFile string `yaml:"web_config_file"` // exporter-toolkit web-config YAML
}

// OutputConfig holds optional push-mode output paths.
type OutputConfig struct {
	OTLP OTLPOutputConfig `yaml:"otlp"`
}

// OTLPOutputConfig configures the OTLP/HTTP push output.
type OTLPOutputConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Endpoint        string        `yaml:"endpoint"`
	Timeout         time.Duration `yaml:"timeout"`
	HeartbeatCycles int           `yaml:"heartbeat_cycles"`
}

// FederationConfig is the LD-15–LD-20 multi-instance coordination config.
// Role selects the operating mode; the remaining fields are mode-specific.
type FederationConfig struct {
	// Role selects the operating mode. Default "standalone" preserves existing
	// single-instance behaviour. "uncoordinated" adds boundary-observation
	// metrics for Mimir recording-rule stitching (LD-15). "spoke" and "hub"
	// configure the H-PCE-style push hierarchy (LD-16).
	Role string `yaml:"role"` // standalone | uncoordinated | spoke | hub

	// SpokeTimeout governs spoke eviction on the hub per LD-18: a spoke that
	// has not pushed within this duration has its edges removed from the
	// aggregated graph. Defaults to 3× discovery.interval.
	SpokeTimeout time.Duration `yaml:"spoke_timeout"`

	// KnownInterDomainLinks is the LD-19 static stitching override. Each
	// entry is a fully-qualified boundary link tuple; the hub injects these as
	// confirmed bidirectional edges regardless of automatic name-matching.
	KnownInterDomainLinks []InterDomainLink `yaml:"known_inter_domain_links"`

	// Hub holds the hub-side federation server settings (role: hub).
	Hub FederationHubConfig `yaml:"hub"`

	// Spoke holds the spoke-side settings (role: spoke).
	Spoke FederationSpokeConfig `yaml:"spoke"`
}

// FederationHubConfig holds the hub's federation server settings.
// TLS fields are file paths, not inline PEM. Per LD-20, all three must be set.
type FederationHubConfig struct {
	// ListenAddr is the address the hub listens on for spoke pushes.
	// Separate from web.listen-address so Prometheus scrapes do not require mTLS.
	ListenAddr string `yaml:"listen_addr"`
	TLSCACert  string `yaml:"tls_ca_cert"` // path to PEM CA certificate for spoke client verification
	TLSCert    string `yaml:"tls_cert"`    // path to PEM server certificate
	TLSKey     string `yaml:"tls_key"`     // path to PEM server private key
	// MaxGraphEdges, if > 0, rejects combined-graph updates with more edges than
	// this limit. Protects scrape latency and memory from runaway topologies.
	MaxGraphEdges int `yaml:"max_graph_edges"`
	// MaxGraphDevices, if > 0, rejects combined-graph updates with more devices
	// than this limit. Protects scrape latency and memory from runaway topologies.
	MaxGraphDevices int `yaml:"max_graph_devices"`
	// LooseDeviceNameMatching enables domain-suffix stripping during OOS
	// neighbour matching. The zero value (false) is the safe default since
	// v1.3.0: "core-sw.dc1.example.com" and "core-sw.dc2.example.com" remain
	// distinct. Set to true to restore the pre-v1.3.0 behaviour for single-site
	// deployments where short and FQDN forms of the same device must reconcile
	// to one node — both forms normalise to "core-sw".
	LooseDeviceNameMatching bool `yaml:"loose_device_name_matching"`
	// MinPushInterval rejects per-spoke pushes received sooner than this
	// duration after the previous accepted push from the same spoke_id, with
	// a 429 Too Many Requests response and a Retry-After header. Defense in
	// depth on top of mTLS to bound the cost of a misconfigured or malicious
	// spoke that ignores its own push backoff. 0 (default) disables the
	// check; set to roughly half the spoke's discovery_interval for a sane
	// floor. Must be strictly less than spoke_timeout.
	MinPushInterval time.Duration `yaml:"min_push_interval"`
}

// FederationSpokeConfig holds the spoke's settings.
// TLS fields are file paths, not inline PEM. Per LD-20, all three must be set.
type FederationSpokeConfig struct {
	// SpokeID is a unique identifier for this spoke instance. Used as the key
	// in the hub's spoke-edge store and in federation metric labels.
	SpokeID string `yaml:"spoke_id"`
	// HubURL is the base URL of the hub federation server, e.g. https://hub:9101.
	HubURL    string `yaml:"hub_url"`
	TLSCACert string `yaml:"tls_ca_cert"` // path to PEM CA certificate for hub server verification
	TLSCert   string `yaml:"tls_cert"`    // path to PEM spoke client certificate
	TLSKey    string `yaml:"tls_key"`     // path to PEM spoke client private key
}

// InterDomainLink is one LD-19 static boundary-link override. LocalDevice,
// LocalPort, RemoteDevice, and RemotePort are required. LinkKind defaults to
// "ethernet" when omitted.
type InterDomainLink struct {
	LocalDevice  string `yaml:"local_device"`
	LocalPort    string `yaml:"local_port"`
	RemoteDevice string `yaml:"remote_device"`
	RemotePort   string `yaml:"remote_port"`
	LinkKind     string `yaml:"link_kind,omitempty"` // defaults to "ethernet"
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

	// CycleBudgetFraction caps the discovery cycle at this fraction of Interval.
	// Goroutines still running when the budget expires are cancelled via context.
	// Default 0.8 (80% of Interval). Must be in (0, 1].
	CycleBudgetFraction float64 `yaml:"cycle_budget_fraction"`

	// TimeoutPerModule caps each individual module walk within a device's
	// per-device timeout. 0 means no additional cap (default). When set, a
	// slow LLDP walk cannot consume the full timeout_per_device for all modules.
	TimeoutPerModule time.Duration `yaml:"timeout_per_module"`

	// MaxGraphDevices, if > 0, rejects local graph updates with more devices
	// than this value. Mirrors FederationHubConfig.MaxGraphDevices for
	// standalone and spoke mode.
	MaxGraphDevices int `yaml:"max_graph_devices"`

	// MaxGraphEdges, if > 0, rejects local graph updates with more edges than
	// this value. Mirrors FederationHubConfig.MaxGraphEdges for standalone
	// and spoke mode.
	MaxGraphEdges int `yaml:"max_graph_edges"`
}

// ScopeConfig is the LD-11 polling-scope guard. CIDRAllowList is mandatory
// when any module is enabled; the empty list is treated as a misconfiguration
// rather than as "poll everything".
type ScopeConfig struct {
	CIDRAllowList []string `yaml:"cidr_allow_list"`
}

// FDBConfig holds FDB-module-specific tuning knobs. MaxVlans caps the number
// of VLANs that walkVlanCommunityFdbs will iterate on IOS devices; beyond
// that limit the walk is truncated and a warning is logged.
type FDBConfig struct {
	Enabled  bool `yaml:"enabled"`
	MaxVlans int  `yaml:"max_vlans"`
}

// BGPConfig holds BGP-module-specific tuning. DisableV2MIB is the kill-switch
// for the BGP4-V2-MIB / vendor-specific peer-table walkers that surface IPv6
// BGP adjacencies; RFC 4273 BGP4-MIB is IPv4-only. The zero value (false) is
// the safe default since v1.3.0: v2 walkers run. Set to true to fall back to
// RFC 4273-only behaviour if a vendor regression appears.
type BGPConfig struct {
	Enabled      bool `yaml:"enabled"`
	DisableV2MIB bool `yaml:"disable_v2_mib"`
}

// ModulesConfig toggles individual discovery modules. Each module's spec
// citation lives in the corresponding internal/discovery/<name>/<name>.go header.
type ModulesConfig struct {
	SNMP ModuleSNMP   `yaml:"snmp"`
	LLDP ModuleToggle `yaml:"lldp"`
	CDP  ModuleToggle `yaml:"cdp"`
	BGP  BGPConfig    `yaml:"bgp"`
	OSPF ModuleToggle `yaml:"ospf"`
	// ARP is enrichment, not an edge source — it walks ipNetToMediaTable to
	// build a MAC→IP map used to backfill DstPorts on FDB-derived edges
	// when LLDP/CDP do not name the neighbour. Defaults to disabled, consistent
	// with every other module toggle; only useful when modules.fdb.enabled is
	// also true. main emits a startup warning when FDB is on and ARP is off.
	ARP    ModuleToggle `yaml:"arp"`
	FDB    FDBConfig    `yaml:"fdb"`
	ISIS   ModuleToggle `yaml:"isis"`
	MPLSTE ModuleToggle `yaml:"mpls_te"`
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
	Profiles           []CredentialProfile    `yaml:"profiles"`
	Assignments        []CredentialAssignment `yaml:"assignments"`
	FallbackOrder      []string               `yaml:"fallback_order"`
	TrialRatePerSecond int                    `yaml:"trial_rate_per_second"`
}

// Profile type constants for CredentialProfile.Type.
const (
	ProfileTypeSNMPv2c = "snmp_v2c"
	ProfileTypeSNMPv3  = "snmp_v3"
)

// CredentialProfile is one named credential the exporter can try against
// a device. Type selects which *Env fields are consulted.
type CredentialProfile struct {
	Name         string `yaml:"name"`
	Type         string `yaml:"type"` // snmp_v2c | snmp_v3
	CommunityEnv string `yaml:"community_env,omitempty"`
	UsernameEnv  string `yaml:"username_env,omitempty"`
	AuthProtocol string `yaml:"auth_protocol,omitempty"` // SHA (recommended) | SHA-256 | SHA-384 | SHA-512 | MD5 (deprecated, broken)
	AuthKeyEnv   string `yaml:"auth_key_env,omitempty"`
	PrivProtocol string `yaml:"priv_protocol,omitempty"` // AES (recommended) | AES-192 | AES-256 | DES (deprecated, broken)
	PrivKeyEnv   string `yaml:"priv_key_env,omitempty"`
	// Retries is the number of SNMP retries per request. Defaults to 1.
	Retries int `yaml:"retries"`
	// ContextName is the SNMPv3 context name. Empty string means no context (default).
	ContextName string `yaml:"context_name,omitempty"`
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
// Unknown YAML keys are rejected (KnownFields mode) so that removed
// deprecated keys produce a clear parse error rather than being silently
// ignored.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen.Addr == "" {
		c.Listen.Addr = ":9100"
	}
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
	if c.Discovery.CycleBudgetFraction == 0 {
		c.Discovery.CycleBudgetFraction = 0.8
	}
	if c.Modules.SNMP.Version == "" {
		c.Modules.SNMP.Version = "v2c"
	}
	if c.Credentials.TrialRatePerSecond == 0 {
		c.Credentials.TrialRatePerSecond = 5
	}
	for i := range c.Credentials.Profiles {
		if c.Credentials.Profiles[i].Retries == 0 {
			c.Credentials.Profiles[i].Retries = 1
		}
	}
	if c.Modules.FDB.MaxVlans == 0 {
		c.Modules.FDB.MaxVlans = 100
	}
	if c.Snapshot.Path == "" {
		c.Snapshot.Path = "/var/lib/network-topology-exporter/snapshot.json"
	}
	for i := range c.Targets {
		if c.Targets[i].Port == 0 {
			c.Targets[i].Port = 161
		}
	}
	if c.Federation.Role == "" {
		c.Federation.Role = "standalone"
	}
	if c.Federation.SpokeTimeout == 0 {
		c.Federation.SpokeTimeout = 3 * c.Discovery.Interval
	}
	if c.Federation.Hub.ListenAddr == "" {
		c.Federation.Hub.ListenAddr = ":9101"
	}
	if c.Output.OTLP.HeartbeatCycles == 0 {
		c.Output.OTLP.HeartbeatCycles = 10
	}
	if c.Output.OTLP.Timeout == 0 {
		c.Output.OTLP.Timeout = 10 * time.Second
	}
}

func (c *Config) validateListen() error {
	if c.Listen.WebConfigFile != "" {
		if _, err := os.Stat(c.Listen.WebConfigFile); err != nil {
			return fmt.Errorf("listen.web_config_file %q: %w", c.Listen.WebConfigFile, err)
		}
	}
	return nil
}

func (c *Config) validate() error {
	if err := c.validateListen(); err != nil {
		return err
	}
	if c.Discovery.Parallelism < 1 {
		return errors.New("discovery.parallelism must be >= 1")
	}
	if c.Discovery.UnconfirmedLinkTTLCycles < 1 {
		return errors.New("discovery.unconfirmed_link_ttl_cycles must be >= 1")
	}
	if c.Discovery.CycleBudgetFraction <= 0 || c.Discovery.CycleBudgetFraction > 1 {
		return errors.New("discovery.cycle_budget_fraction must be in (0, 1]")
	}
	if c.Discovery.TimeoutPerModule < 0 {
		return errors.New("discovery.timeout_per_module must be >= 0")
	}
	if c.Discovery.TimeoutPerModule > 0 && c.Discovery.TimeoutPerModule >= c.Discovery.TimeoutPerDevice {
		return errors.New("discovery.timeout_per_module must be less than discovery.timeout_per_device")
	}
	if c.Discovery.MaxGraphDevices < 0 {
		return errors.New("discovery.max_graph_devices must be >= 0 (0 = unlimited)")
	}
	if c.Discovery.MaxGraphEdges < 0 {
		return errors.New("discovery.max_graph_edges must be >= 0 (0 = unlimited)")
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
	if err := c.validateFDB(); err != nil {
		return err
	}
	if err := c.validateFederation(); err != nil {
		return err
	}
	if c.Output.OTLP.Timeout < 0 {
		return fmt.Errorf("output.otlp.timeout must be >= 0 (0 uses the default)")
	}
	if c.Output.OTLP.HeartbeatCycles < 1 {
		return errors.New("output.otlp.heartbeat_cycles must be >= 1")
	}
	if c.Output.OTLP.Enabled {
		if err := validateHTTPEndpoint(c.Output.OTLP.Endpoint); err != nil {
			return fmt.Errorf("output.otlp.endpoint: %w", err)
		}
	}
	return nil
}

func validateHTTPEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	return nil
}

func validateHTTPSEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("endpoint is required")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("host is required")
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
	if c.Credentials.TrialRatePerSecond < 1 {
		return errors.New("credentials.trial_rate_per_second must be >= 1")
	}
	if len(c.Credentials.Profiles) == 0 {
		return nil // legacy single-community path; ModuleSNMP.CommunityEnv applies.
	}
	known := make(map[string]CredentialProfile, len(c.Credentials.Profiles))
	for i := range c.Credentials.Profiles {
		p := c.Credentials.Profiles[i]
		if p.Name == "" {
			return errors.New("credentials.profiles[].name is required")
		}
		if _, dup := known[p.Name]; dup {
			return fmt.Errorf("credentials.profiles: duplicate name %q", p.Name)
		}
		if p.Retries < 0 {
			return fmt.Errorf("profile %q: retries must be >= 0", p.Name)
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
			authProto, err := normalizeAuthProtocol(p.AuthProtocol)
			if err != nil {
				return fmt.Errorf("profile %q: %w", p.Name, err)
			}
			privProto, err := normalizePrivProtocol(p.PrivProtocol)
			if err != nil {
				return fmt.Errorf("profile %q: %w", p.Name, err)
			}
			if authProto != "" && p.AuthKeyEnv == "" {
				return fmt.Errorf("snmpv3 profile %q: auth_protocol requires auth_key_env", p.Name)
			}
			if privProto != "" && p.PrivKeyEnv == "" {
				return fmt.Errorf("snmpv3 profile %q: priv_protocol requires priv_key_env", p.Name)
			}
			p.AuthProtocol = authProto
			p.PrivProtocol = privProto
		default:
			return fmt.Errorf("profile %q has unknown type %q", p.Name, p.Type)
		}
		c.Credentials.Profiles[i] = p
		known[p.Name] = p
	}
	for i, a := range c.Credentials.Assignments {
		if a.IP == "" && a.CIDR == "" {
			return errors.New("credentials.assignments[]: ip or cidr is required")
		}
		if a.IP != "" && a.CIDR != "" {
			return fmt.Errorf("credentials.assignments[%d]: ip and cidr are mutually exclusive", i)
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
	return nil
}

func normalizeAuthProtocol(raw string) (string, error) {
	switch v := strings.ToUpper(strings.TrimSpace(raw)); v {
	case "":
		return "", nil
	case "SHA", "SHA-256", "SHA-384", "SHA-512":
		return v, nil
	case "MD5":
		return "", errors.New("auth_protocol MD5 is cryptographically broken; use SHA or SHA-256")
	default:
		return "", fmt.Errorf("unknown auth_protocol %q; allowed: SHA, SHA-256, SHA-384, SHA-512", raw)
	}
}

func normalizePrivProtocol(raw string) (string, error) {
	switch v := strings.ToUpper(strings.TrimSpace(raw)); v {
	case "":
		return "", nil
	case "AES", "AES-192", "AES-256":
		return v, nil
	case "DES":
		return "", errors.New("priv_protocol DES is cryptographically broken; use AES or AES-256")
	default:
		return "", fmt.Errorf("unknown priv_protocol %q; allowed: AES, AES-192, AES-256", raw)
	}
}

func (c *Config) validateTargets() error {
	type hostPort struct {
		host string
		port int
	}
	seen := make(map[hostPort]int, len(c.Targets))
	for i, t := range c.Targets {
		if t.Host == "" {
			return fmt.Errorf("targets[%d].host must not be empty", i)
		}
		if t.Port < 1 || t.Port > 65535 {
			return fmt.Errorf("targets[%d].port %d is out of range [1, 65535]", i, t.Port)
		}
		key := hostPort{host: t.Host, port: t.Port}
		if first, dup := seen[key]; dup {
			return fmt.Errorf("targets[%d] duplicates targets[%d] (host:port %s:%d)", i, first, t.Host, t.Port)
		}
		seen[key] = i
		for k := range t.Labels {
			if k == "" {
				return fmt.Errorf("targets[%d]: label key must not be empty", i)
			}
		}
	}
	return nil
}

// validateFederation enforces LD-15–LD-20 constraints on the federation config.
func (c *Config) validateFederation() error {
	if c.Federation.SpokeTimeout < 0 {
		return fmt.Errorf("federation.spoke_timeout must be >= 0 (0 uses the default of 3× discovery.interval)")
	}
	switch c.Federation.Role {
	case "standalone", "uncoordinated":
		// no extra required fields
	case "spoke":
		if c.Federation.Spoke.SpokeID == "" {
			return errors.New("federation.spoke.spoke_id is required for spoke role")
		}
		if err := validateHTTPSEndpoint(c.Federation.Spoke.HubURL); err != nil {
			return fmt.Errorf("federation.spoke.hub_url: %w", err)
		}
		if c.Federation.Spoke.TLSCACert == "" || c.Federation.Spoke.TLSCert == "" || c.Federation.Spoke.TLSKey == "" {
			return errors.New("federation.spoke.tls_ca_cert, tls_cert, and tls_key are all required for spoke role (LD-20)")
		}
		for _, pair := range []struct{ field, path string }{
			{"federation.spoke.tls_ca_cert", c.Federation.Spoke.TLSCACert},
			{"federation.spoke.tls_cert", c.Federation.Spoke.TLSCert},
			{"federation.spoke.tls_key", c.Federation.Spoke.TLSKey},
		} {
			if _, err := os.Stat(pair.path); err != nil {
				return fmt.Errorf("%s: %w", pair.field, err)
			}
		}
	case "hub":
		if c.Federation.Hub.TLSCACert == "" || c.Federation.Hub.TLSCert == "" || c.Federation.Hub.TLSKey == "" {
			return errors.New("federation.hub.tls_ca_cert, tls_cert, and tls_key are all required for hub role (LD-20)")
		}
		for _, pair := range []struct{ field, path string }{
			{"federation.hub.tls_ca_cert", c.Federation.Hub.TLSCACert},
			{"federation.hub.tls_cert", c.Federation.Hub.TLSCert},
			{"federation.hub.tls_key", c.Federation.Hub.TLSKey},
		} {
			if _, err := os.Stat(pair.path); err != nil {
				return fmt.Errorf("%s: %w", pair.field, err)
			}
		}
		if c.Federation.Hub.MaxGraphDevices < 0 {
			return errors.New("federation.hub.max_graph_devices must be >= 0 (0 = unlimited)")
		}
		if c.Federation.Hub.MaxGraphEdges < 0 {
			return errors.New("federation.hub.max_graph_edges must be >= 0 (0 = unlimited)")
		}
		if c.Federation.Hub.MinPushInterval < 0 {
			return errors.New("federation.hub.min_push_interval must be >= 0 (0 disables the rate limit)")
		}
		if c.Federation.Hub.MinPushInterval > 0 && c.Federation.SpokeTimeout > 0 && c.Federation.Hub.MinPushInterval >= c.Federation.SpokeTimeout {
			return fmt.Errorf("federation.hub.min_push_interval (%s) must be < federation.spoke_timeout (%s) or spokes will be evicted before they can re-push", c.Federation.Hub.MinPushInterval, c.Federation.SpokeTimeout)
		}
		// LD-18: spoke_timeout shorter than 2× the discovery interval causes
		// spokes to be spuriously evicted before they have completed two cycles.
		if c.Federation.SpokeTimeout < 2*c.Discovery.Interval {
			return fmt.Errorf("federation.spoke_timeout (%s) must be >= 2× discovery.interval (%s) to prevent spurious eviction (LD-18)", c.Federation.SpokeTimeout, c.Discovery.Interval)
		}
	default:
		return fmt.Errorf("federation.role must be standalone, uncoordinated, spoke, or hub; got %q", c.Federation.Role)
	}
	seen := make(map[string]int)
	for i, link := range c.Federation.KnownInterDomainLinks {
		if link.LocalDevice == "" || link.LocalPort == "" || link.RemoteDevice == "" || link.RemotePort == "" {
			return fmt.Errorf("federation.known_inter_domain_links[%d]: local_device, local_port, remote_device, and remote_port are all required", i)
		}
		if link.LocalDevice == link.RemoteDevice {
			return fmt.Errorf("federation.known_inter_domain_links[%d]: local_device and remote_device must differ", i)
		}
		fwd := link.LocalDevice + "|" + link.LocalPort + "|" + link.RemoteDevice + "|" + link.RemotePort
		rev := link.RemoteDevice + "|" + link.RemotePort + "|" + link.LocalDevice + "|" + link.LocalPort
		for _, key := range []string{fwd, rev} {
			if seenIdx, ok := seen[key]; ok {
				return fmt.Errorf("federation.known_inter_domain_links[%d]: duplicate of entry [%d]", i, seenIdx)
			}
		}
		seen[fwd] = i
		seen[rev] = i
	}
	return nil
}

func (c *Config) validateFDB() error {
	if c.Modules.FDB.MaxVlans < 1 {
		return errors.New("fdb.max_vlans must be at least 1")
	}
	if c.Modules.FDB.MaxVlans > 4096 {
		return errors.New("fdb.max_vlans must be at most 4096")
	}
	return nil
}

// EmitDeprecationWarnings is a no-op stub retained so call-sites in main
// continue to compile without modification. All three deprecated config keys
// (use_v2_mib, strict_device_name_matching, tls_cert_file/tls_key_file) were
// removed in v1.5.0; the YAML loader now rejects them with a parse error.
// Returns false always.
func (c *Config) EmitDeprecationWarnings(_ *slog.Logger) bool {
	return false
}

func cidrContainsAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
