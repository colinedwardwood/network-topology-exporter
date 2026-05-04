// Package config loads and validates the exporter's YAML configuration.
//
// Schema lives in config/example.yaml; semantics are documented in README.md
// under "Configuration".
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level exporter configuration.
type Config struct {
	Discovery DiscoveryConfig          `yaml:"discovery"`
	Modules   ModulesConfig            `yaml:"modules"`
	Loki      LokiConfig               `yaml:"loki"`
	OTel      OTelConfig               `yaml:"otel"`
	NetBox    NetBoxConfig             `yaml:"netbox"`
	Targets   []TargetConfig           `yaml:"targets"`
}

// DiscoveryConfig controls the global discovery cycle.
type DiscoveryConfig struct {
	Interval         time.Duration `yaml:"interval"`
	TimeoutPerDevice time.Duration `yaml:"timeout_per_device"`
	Parallelism      int           `yaml:"parallelism"`
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

// ModuleSNMP holds the SNMP credentials referenced by every walking module.
type ModuleSNMP struct {
	Enabled       bool   `yaml:"enabled"`
	Version       string `yaml:"version"` // v2c | v3
	CommunityEnv  string `yaml:"community_env"`
}

// LokiConfig controls topology change-event push.
type LokiConfig struct {
	Enabled bool              `yaml:"enabled"`
	URL     string            `yaml:"url"`
	Labels  map[string]string `yaml:"labels"`
}

// OTelConfig controls OTLP trace export for discovery cycles.
type OTelConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
	Insecure bool   `yaml:"insecure"`
}

// NetBoxConfig controls the optional NetBox writeback integration.
type NetBoxConfig struct {
	Enabled     bool   `yaml:"enabled"`
	URL         string `yaml:"url"`
	APITokenEnv string `yaml:"api_token_env"`
	DefaultSite string `yaml:"default_site"`
}

// TargetConfig is one device to discover.
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
	if c.Modules.SNMP.Version == "" {
		c.Modules.SNMP.Version = "v2c"
	}
	if c.Loki.Enabled && c.Loki.Labels == nil {
		c.Loki.Labels = map[string]string{"job": "topology-exporter"}
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
	if c.Modules.SNMP.Enabled {
		switch c.Modules.SNMP.Version {
		case "v2c", "v3":
		default:
			return fmt.Errorf("modules.snmp.version must be v2c or v3, got %q", c.Modules.SNMP.Version)
		}
	}
	if c.Loki.Enabled && c.Loki.URL == "" {
		return errors.New("loki.enabled but loki.url is empty")
	}
	if c.OTel.Enabled && c.OTel.Endpoint == "" {
		return errors.New("otel.enabled but otel.endpoint is empty")
	}
	if c.NetBox.Enabled && c.NetBox.URL == "" {
		return errors.New("netbox.enabled but netbox.url is empty")
	}
	return nil
}
