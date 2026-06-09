package config

import (
	"net"
	"sort"
)

// moduleGloballyEnabled reports whether module m is enabled via modules.<m>.enabled.
// m is assumed to be one of scopableModules (validated upstream).
func (c *Config) moduleGloballyEnabled(m string) bool {
	switch m {
	case "lldp":
		return c.Modules.LLDP.Enabled
	case "cdp":
		return c.Modules.CDP.Enabled
	case "fdb":
		return c.Modules.FDB.Enabled
	case "ospf":
		return c.Modules.OSPF.Enabled
	case "bgp":
		return c.Modules.BGP.Enabled
	case "isis":
		return c.Modules.ISIS.Enabled
	case "mpls_te":
		return c.Modules.MPLSTE.Enabled
	default:
		return false
	}
}

// targetOverride is the parsed form of one TargetOverride: the CIDR is parsed
// into a *net.IPNet once at validate() time, the prefix length is cached for
// the longest-prefix sort, and the module set is materialised into a lookup map.
type targetOverride struct {
	net       *net.IPNet
	prefixLen int
	modules   map[string]bool
}

// targetOverrideResolver answers "which modules may run against this IP" by
// longest-prefix match over the configured overrides. Built once during
// validate(); read-only and safe for concurrent use thereafter. It mirrors the
// most-specific-CIDR-wins model in internal/credentials (Resolver.cidrs).
type targetOverrideResolver struct {
	overrides []targetOverride
}

// buildTargetOverrideResolver parses the overrides and sorts them
// longest-prefix-first so ModulesForIP returns the most-specific match on its
// first hit. Tiebreak: when two overrides have an equal prefix length, the
// first-declared (lower config index) wins — sort.SliceStable preserves config
// order for equal keys, and validate() rejects exact-duplicate CIDRs so an
// ambiguous equal-prefix overlap of the *same* network can never silently
// shadow. CIDRs are assumed already validated by validateTargetOverrides.
func buildTargetOverrideResolver(overrides []TargetOverride) *targetOverrideResolver {
	if len(overrides) == 0 {
		return nil
	}
	r := &targetOverrideResolver{overrides: make([]targetOverride, 0, len(overrides))}
	for _, o := range overrides {
		_, n, err := net.ParseCIDR(o.CIDR)
		if err != nil {
			continue // unreachable: validated upstream.
		}
		ones, _ := n.Mask.Size()
		mods := make(map[string]bool, len(o.Modules))
		for _, m := range o.Modules {
			mods[m] = true
		}
		r.overrides = append(r.overrides, targetOverride{net: n, prefixLen: ones, modules: mods})
	}
	// Most-specific CIDR wins — longest prefix first. SliceStable keeps
	// config order for equal prefix lengths (first-declared tiebreak).
	sort.SliceStable(r.overrides, func(i, j int) bool {
		return r.overrides[i].prefixLen > r.overrides[j].prefixLen
	})
	return r
}

// ModulesForIP returns the set of module names permitted to run against ip,
// resolved by longest-prefix match over discovery.target_overrides. The
// returned map is the override's module set (already intersected at config-load
// time only against the canonical/enabled checks in validation — callers must
// still AND against the live modules.<m>.enabled gate). When no override
// matches ip, it returns (nil, false), the signal to run ALL globally-enabled
// modules (today's unchanged default). The returned map must be treated as
// read-only.
func (c *Config) ModulesForIP(ip net.IP) (allowed map[string]bool, matched bool) {
	r := c.Discovery.overrideResolver
	if r == nil {
		return nil, false
	}
	for _, o := range r.overrides {
		if o.net.Contains(ip) {
			return o.modules, true
		}
	}
	return nil, false
}

// BuildTargetOverrideResolver validates discovery.target_overrides and rebuilds
// the cached longest-prefix resolver consulted by ModulesForIP. Load() calls
// this during validate(); it is also exported so code that constructs a Config
// programmatically (e.g. tests) can populate the resolver without re-parsing a
// YAML file. Returns the same errors as the load-time validation path.
func (c *Config) BuildTargetOverrideResolver() error {
	return c.validateTargetOverrides()
}
