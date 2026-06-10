package config

import (
	"time"
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

	// DebugListenAddr, when non-empty, enables a SEPARATE listener serving
	// net/http/pprof at /debug/pprof/* (issue #69). Empty (the default) means
	// OFF: no debug listener is created and there is zero runtime overhead. The
	// debug surface has NO auth and NO TLS — the same model as node_exporter's
	// debug endpoints — so it MUST NOT be exposed to the internet. Bind it to
	// localhost or a management interface only (e.g. "127.0.0.1:6060"). It is
	// never served on the main metrics listener (listen.addr).
	DebugListenAddr string `yaml:"debug_listen_addr"`
}

// OutputConfig holds optional output paths beyond Prometheus /metrics.
type OutputConfig struct {
	OTLP OTLPOutputConfig `yaml:"otlp"`
	YANG YANGOutputConfig `yaml:"yang"`
}

// YANGOutputConfig configures the RFC 8345 YANG-JSON pull endpoint (#75).
// Disabled by default; when enabled, GET /topology/yang renders the current
// reconciled topology as RFC 8345/8346 YANG-JSON.
type YANGOutputConfig struct {
	Enabled   bool   `yaml:"enabled"`    // default false
	NetworkID string `yaml:"network_id"` // RFC 8345 network-id; default applied in defaults
}

// OTLPOutputConfig configures the OTLP push output.
type OTLPOutputConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Endpoint        string        `yaml:"endpoint"`
	Timeout         time.Duration `yaml:"timeout"`
	HeartbeatCycles int           `yaml:"heartbeat_cycles"`

	// Protocol selects the OTLP transport: "http" (default) or "grpc".
	// Empty preserves the pre-SDK behaviour (OTLP/HTTP). The payload encoding
	// is always protobuf — the OpenTelemetry Go SDK exporters do not implement
	// OTLP/JSON. This is a wire-format change from the pre-v1.5.0 hand-rolled
	// JSON path; OTLP receivers must accept protobuf (the OTLP default).
	Protocol OTLPProtocol `yaml:"protocol"`

	// Traces configures opt-in OpenTelemetry tracing of the exporter's own
	// discovery cycle (issue #68). It reuses Endpoint, Protocol, and auth from
	// this block — tracing has no endpoint of its own.
	Traces OTLPTracesConfig `yaml:"traces"`
}

// OTLPTracesConfig configures the opt-in trace signal layered on the OTLP
// output. Tracing is disabled by default; when enabled it exports spans of the
// discovery cycle over the same Endpoint/Protocol as OTLP metrics and logs.
type OTLPTracesConfig struct {
	// Enabled turns on tracing of the discovery cycle. Default false: when
	// off, instrumentation calls resolve to the OTel no-op tracer and emit
	// nothing.
	Enabled bool `yaml:"enabled"`
	// SampleRate is the head-sampling ratio in [0,1] applied to the root
	// discovery.cycle span; child spans inherit the parent decision
	// (ParentBased). Default 0.1. Set to 1.0 to sample every cycle (useful for
	// low-cardinality debugging) or to a small fraction in production.
	//
	// A pointer so a config that omits sample_rate gets the 0.1 default while a
	// config that explicitly sets sample_rate: 0.0 (sample nothing) is honoured
	// rather than silently overridden by the default.
	SampleRate *float64 `yaml:"sample_rate"`
}

// FederationConfig is the LD-15–LD-20 multi-instance coordination config.
// Role selects the operating mode; the remaining fields are mode-specific.
type FederationConfig struct {
	// Role selects the operating mode. Default "standalone" preserves existing
	// single-instance behaviour. "uncoordinated" adds boundary-observation
	// metrics for Mimir recording-rule stitching (LD-15). "spoke" and "hub"
	// configure the H-PCE-style push hierarchy (LD-16).
	Role Role `yaml:"role"` // standalone | uncoordinated | spoke | hub

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

	// HA configures opt-in native hub high-availability (issue #71). The zero
	// value (Enabled:false) preserves single-hub behaviour exactly.
	HA HubHAConfig `yaml:"ha"`
}

// HubHAConfig is the opt-in native-HA block for federation.hub. When Enabled,
// 2+ hub replicas elect one leader (k8s Lease); only the leader accepts spoke
// pushes and serves authoritative /metrics. See docs/superpowers/specs/2026-06-09-hub-ha-design.md.
type HubHAConfig struct {
	Enabled        bool          `yaml:"enabled"`
	LeaseName      string        `yaml:"lease_name"`
	LeaseNamespace string        `yaml:"lease_namespace"` // "" ⇒ pod namespace at runtime
	LeaseDuration  time.Duration `yaml:"lease_duration"`
	RenewDeadline  time.Duration `yaml:"renew_deadline"`
	RetryPeriod    time.Duration `yaml:"retry_period"`
	// SnapshotShared marks the snapshot path as a shared RWX volume so a new
	// leader warm-starts from the previous leader's snapshot. Optional.
	SnapshotShared bool `yaml:"snapshot_shared"`
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

	// PerTargetPDURatePerSecond caps the steady-state SNMP request (PDU) rate
	// issued against a single device during a discovery cycle. A misconfigured
	// high parallelism with all modules enabled can otherwise drive hundreds of
	// PDUs/sec at one target and self-DoS its SNMP daemon (issue #72). The cap
	// is applied per device per cycle (per-target isolation — limiters are not
	// shared across devices). 0 (default) means unlimited, preserving today's
	// behaviour. Must be >= 0.
	PerTargetPDURatePerSecond int `yaml:"per_target_pdu_rate_per_second"`

	// TargetOverrides scopes discovery to a subset of the globally-enabled
	// modules on a per-CIDR basis (issue #74). For an IP matching an override's
	// cidr, ONLY the listed modules run (intersected with modules.<m>.enabled);
	// an IP matching no override runs ALL enabled modules — today's behaviour,
	// unchanged. Heterogeneous fleets use this so e.g. BGP is not walked against
	// an access switch that does not speak BGP (avoiding a guaranteed
	// mib_unimplemented walker outcome). Most-specific CIDR wins (longest prefix
	// match), the same precedence model as credentials.assignments.
	TargetOverrides []TargetOverride `yaml:"target_overrides"`

	// overrideResolver is the parsed, longest-prefix-sorted form of
	// TargetOverrides, built once during validate() so the discovery loop never
	// re-parses CIDRs per device per cycle. nil when no overrides are
	// configured (the unchanged default path).
	overrideResolver *targetOverrideResolver

	// SNMP holds discovery-layer SNMP transport tuning that is not per-module
	// (the per-module on/off toggles live under modules.snmp). Currently this is
	// just the opt-in session pool (issue #83).
	SNMP DiscoverySNMPConfig `yaml:"snmp"`

	// LivenessMaxStaleCycles gates the /healthz liveness probe on discovery
	// staleness. When the most recent cycle is older than
	// interval × liveness_max_stale_cycles, /healthz returns 503 so Kubernetes
	// restarts a process whose discovery loop has wedged (a deadlocked loop can
	// no longer update its own last-cycle timestamp). Default 3. A value of 0
	// disables the gate, restoring the prior behaviour where /healthz is always
	// 200 once the process is up. The gate is inert in pure-hub mode regardless
	// of this value, since a hub runs no local discovery loop.
	//
	// A pointer so the loader can distinguish "unset" (nil → default 3) from an
	// explicit 0 (disable the gate). After applyDefaults this is always non-nil;
	// read it via LivenessMaxStaleCyclesValue. Must be >= 0.
	LivenessMaxStaleCycles *int `yaml:"liveness_max_stale_cycles"`
}

// LivenessMaxStaleCyclesValue returns the resolved liveness-gate cycle count.
// applyDefaults guarantees the field is non-nil, but this accessor is nil-safe
// (returns 0 → gate disabled) so callers never dereference a nil pointer.
func (d DiscoveryConfig) LivenessMaxStaleCyclesValue() int {
	if d.LivenessMaxStaleCycles == nil {
		return 0
	}
	return *d.LivenessMaxStaleCycles
}

// DiscoverySNMPConfig groups discovery-wide SNMP transport options.
type DiscoverySNMPConfig struct {
	SessionPool SessionPoolConfig `yaml:"session_pool"`
}

// SessionPoolConfig configures the opt-in per-target SNMP session pool (issue
// #83). When disabled (the default), every (target × module) walk opens and
// closes a fresh UDP session, exactly as before. When enabled, a target reuses
// one session across its modules within a cycle and across cycles, keyed by
// (target IP, credential profile), cutting socket / conntrack churn on large
// fleets (see docs/operator/scale.md). The session is bounded at one per
// (target × profile) (~50KB each), so memory cost scales with fleet size, not
// module count.
type SessionPoolConfig struct {
	// Enabled turns the pool on. Default false: behaviour is byte-identical to
	// pre-#83 (fresh session per walk).
	Enabled bool `yaml:"enabled"`

	// MaxIdle is how long a pooled session may sit unused before the background
	// evictor closes it. 0 (the default) means "5 × discovery.interval",
	// computed in applyDefaults. Must be >= 0. Closing an idle session also
	// clears the credentials it held (issue #5 retention bound).
	MaxIdle time.Duration `yaml:"max_idle"`
}

// TargetOverride restricts the modules that run against IPs inside CIDR to the
// listed Modules (issue #74). See DiscoveryConfig.TargetOverrides for the full
// semantics. Each module name must be one of the canonical module identifiers
// (lldp, cdp, fdb, ospf, bgp, isis, mpls_te) and must be enabled globally; an
// empty Modules list is rejected as ambiguous.
type TargetOverride struct {
	CIDR    string   `yaml:"cidr"`
	Modules []string `yaml:"modules"`
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
	Enabled      bool        `yaml:"enabled"`
	Version      SNMPVersion `yaml:"version"` // v2c | v3
	CommunityEnv string      `yaml:"community_env"`
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

// Profile type constants for CredentialProfile.Type are defined as the named
// ProfileType enum in enums.go.

// CredentialProfile is one named credential the exporter can try against
// a device. Type selects which *Env fields are consulted.
type CredentialProfile struct {
	Name         string      `yaml:"name"`
	Type         ProfileType `yaml:"type"` // snmp_v2c | snmp_v3
	CommunityEnv string      `yaml:"community_env,omitempty"`
	UsernameEnv  string      `yaml:"username_env,omitempty"`
	AuthProtocol string      `yaml:"auth_protocol,omitempty"` // SHA (recommended) | SHA-256 | SHA-384 | SHA-512 | MD5 (deprecated, broken)
	AuthKeyEnv   string      `yaml:"auth_key_env,omitempty"`
	PrivProtocol string      `yaml:"priv_protocol,omitempty"` // AES (recommended) | AES-192 | AES-256 | DES (deprecated, broken)
	PrivKeyEnv   string      `yaml:"priv_key_env,omitempty"`
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
