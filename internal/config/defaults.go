package config

import (
	"time"
)

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
	// Liveness staleness gate defaults to 3 cycles. The field is a pointer so an
	// unset value (nil) gets the default 3, while an explicit 0 in YAML is
	// preserved as "disable the gate" — the two are otherwise indistinguishable
	// for a plain int. See DiscoveryConfig.LivenessMaxStaleCycles.
	if c.Discovery.LivenessMaxStaleCycles == nil {
		def := 3
		c.Discovery.LivenessMaxStaleCycles = &def
	}
	// Session-pool idle TTL defaults to 5 × interval (issue #83). Computed after
	// the interval default above so a fully-defaulted config still gets a sane
	// non-zero TTL. Only meaningful when the pool is enabled, but harmless to set
	// regardless.
	if c.Discovery.SNMP.SessionPool.MaxIdle == 0 {
		c.Discovery.SNMP.SessionPool.MaxIdle = 5 * c.Discovery.Interval
	}
	if c.Modules.SNMP.Version == "" {
		c.Modules.SNMP.Version = SNMPVersionV2c
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
		c.Federation.Role = RoleStandalone
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
	if c.Output.OTLP.Protocol == "" {
		c.Output.OTLP.Protocol = OTLPProtocolHTTP
	}
	if c.Output.OTLP.Traces.SampleRate == nil {
		def := 0.1
		c.Output.OTLP.Traces.SampleRate = &def
	}
	if c.Output.YANG.NetworkID == "" {
		c.Output.YANG.NetworkID = "network-topology-exporter"
	}
}
