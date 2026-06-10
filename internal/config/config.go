// Package config loads and validates the exporter's YAML configuration.
//
// Schema lives in config/example.yaml; semantics are documented in README.md
// under "Configuration".
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

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
