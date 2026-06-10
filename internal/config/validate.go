package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

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
	if c.Discovery.PerTargetPDURatePerSecond < 0 {
		return errors.New("discovery.per_target_pdu_rate_per_second must be >= 0 (0 = unlimited)")
	}
	if c.Discovery.SNMP.SessionPool.MaxIdle < 0 {
		return errors.New("discovery.snmp.session_pool.max_idle must be >= 0 (0 = default 5× interval)")
	}
	if c.Discovery.Interval <= 0 {
		return errors.New("discovery.interval must be > 0")
	}
	if c.Discovery.LivenessMaxStaleCyclesValue() < 0 {
		return errors.New("discovery.liveness_max_stale_cycles must be >= 0 (0 = liveness staleness gate disabled)")
	}
	if c.Discovery.TimeoutPerDevice <= 0 {
		return errors.New("discovery.timeout_per_device must be > 0")
	}
	if err := c.validateScope(); err != nil {
		return err
	}
	if err := c.validateTargetOverrides(); err != nil {
		return err
	}
	if c.Modules.SNMP.Enabled {
		switch c.Modules.SNMP.Version {
		case SNMPVersionV2c, SNMPVersionV3:
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
	switch c.Output.OTLP.Protocol {
	case "", OTLPProtocolHTTP, OTLPProtocolGRPC:
	default:
		return fmt.Errorf("output.otlp.protocol must be http or grpc, got %q", c.Output.OTLP.Protocol)
	}
	if sr := c.Output.OTLP.Traces.SampleRate; sr != nil && (*sr < 0 || *sr > 1) {
		return fmt.Errorf("output.otlp.traces.sample_rate must be in [0, 1], got %v", *sr)
	}
	if c.Output.OTLP.Enabled {
		// gRPC endpoints are bare host:port authorities (or a URL); HTTP
		// endpoints must be a full http/https URL as before.
		if c.Output.OTLP.Protocol == OTLPProtocolGRPC {
			if c.Output.OTLP.Endpoint == "" {
				return errors.New("output.otlp.endpoint: endpoint is required")
			}
		} else if err := validateHTTPEndpoint(c.Output.OTLP.Endpoint); err != nil {
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
	case RoleStandalone, RoleUncoordinated:
		// no extra required fields
	case RoleSpoke:
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
	case RoleHub:
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

// scopableModules is the canonical set of per-target-scopable module names,
// matching the module dispatch table in internal/app/cycle.go and the toggles
// on ModulesConfig. snmp/arp are intentionally excluded: snmp is the transport
// (always required to reach a device) and arp is enrichment driven by fdb, not
// an independently dispatched edge source.
var scopableModules = map[string]struct{}{
	"lldp":    {},
	"cdp":     {},
	"fdb":     {},
	"ospf":    {},
	"bgp":     {},
	"isis":    {},
	"mpls_te": {},
}

// validateTargetOverrides enforces the issue #74 invariants: every cidr parses,
// every referenced module is a recognised name AND enabled globally, and the
// modules list is non-empty. On success it builds the cached resolver.
func (c *Config) validateTargetOverrides() error {
	if len(c.Discovery.TargetOverrides) == 0 {
		c.Discovery.overrideResolver = nil
		return nil
	}
	seenCIDR := make(map[string]int, len(c.Discovery.TargetOverrides))
	for i, o := range c.Discovery.TargetOverrides {
		_, n, err := net.ParseCIDR(o.CIDR)
		if err != nil {
			return fmt.Errorf("discovery.target_overrides[%d]: invalid cidr %q: %w", i, o.CIDR, err)
		}
		// Reject exact-duplicate CIDRs: two overrides for the same network have
		// equal prefix length and would make the winner depend on declaration
		// order alone — an ambiguity better surfaced as a config error.
		key := n.String()
		if first, dup := seenCIDR[key]; dup {
			return fmt.Errorf("discovery.target_overrides[%d]: duplicate cidr %q (already declared at [%d])", i, o.CIDR, first)
		}
		seenCIDR[key] = i
		// An empty modules list is ambiguous: if an operator wants nothing
		// polled at a CIDR they should exclude it from discovery.scope instead.
		if len(o.Modules) == 0 {
			return fmt.Errorf("discovery.target_overrides[%d] (cidr %q): modules must list at least one module (to poll nothing, remove the CIDR from discovery.scope)", i, o.CIDR)
		}
		seenMod := make(map[string]struct{}, len(o.Modules))
		for _, m := range o.Modules {
			if _, ok := scopableModules[m]; !ok {
				return fmt.Errorf("discovery.target_overrides[%d] (cidr %q): unknown module %q (allowed: bgp, cdp, fdb, isis, lldp, mpls_te, ospf)", i, o.CIDR, m)
			}
			if _, dup := seenMod[m]; dup {
				return fmt.Errorf("discovery.target_overrides[%d] (cidr %q): duplicate module %q", i, o.CIDR, m)
			}
			seenMod[m] = struct{}{}
			// Acceptance criterion: an override may only narrow, never widen —
			// every referenced module must be enabled globally.
			if !c.moduleGloballyEnabled(m) {
				return fmt.Errorf("discovery.target_overrides[%d] (cidr %q): module %q is not enabled globally (set modules.%s.enabled: true or remove it from the override)", i, o.CIDR, m, m)
			}
		}
	}
	c.Discovery.overrideResolver = buildTargetOverrideResolver(c.Discovery.TargetOverrides)
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
