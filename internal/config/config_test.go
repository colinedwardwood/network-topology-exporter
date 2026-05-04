package config

import (
	"os"
	"path/filepath"
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
