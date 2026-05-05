package main

import (
	"context"
	"log/slog"
	"sort"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/credentials"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

func systemPDUs(sysName string) []gsnmp.SnmpPDU {
	return []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte(sysName)},
	}
}

func TestRunCycleTwoDevices(t *testing.T) {
	t.Setenv("TEST_COMMUNITY", "public")

	addr1 := snmptest.Start(t, "public", systemPDUs("sw-01"))
	addr2 := snmptest.Start(t, "public", systemPDUs("sw-02"))

	_, port1 := snmptest.ParseAddr(addr1)
	_, port2 := snmptest.ParseAddr(addr2)

	cfg := &config.Config{
		Discovery: config.DiscoveryConfig{
			Interval:                 60 * time.Second,
			TimeoutPerDevice:         5 * time.Second,
			Parallelism:              4,
			UnconfirmedLinkTTLCycles: 3,
			Scope: config.ScopeConfig{
				CIDRAllowList: []string{"127.0.0.0/8"},
			},
		},
		Modules: config.ModulesConfig{
			SNMP: config.ModuleSNMP{Enabled: true, Version: "v2c"},
		},
		Credentials: config.CredentialsConfig{
			TrialRatePerSecond: 100,
			Profiles: []config.CredentialProfile{
				{
					Name:         "default",
					Type:         config.ProfileTypeSNMPv2c,
					CommunityEnv: "TEST_COMMUNITY",
				},
			},
			FallbackOrder: []string{"default"},
		},
		Snapshot: config.SnapshotConfig{
			Path: t.TempDir() + "/snapshot.json",
		},
		Targets: []config.TargetConfig{
			{Host: "127.0.0.1", Port: int(port1)},
			{Host: "127.0.0.1", Port: int(port2)},
		},
	}

	resolver, err := credentials.New(cfg.Credentials)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}

	allowedNets := snmpwalk.ParseCIDRs(cfg.Discovery.Scope.CIDRAllowList)

	g, _, _ := runCycle(context.Background(), slog.Default(), cfg, metrics.New(), resolver, allowedNets, nil)

	if len(g.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(g.Devices))
	}

	ids := make([]string, len(g.Devices))
	for i, d := range g.Devices {
		ids[i] = d.ID
	}
	sort.Strings(ids)

	if ids[0] != "sw-01" {
		t.Errorf("expected device ID sw-01, got %q", ids[0])
	}
	if ids[1] != "sw-02" {
		t.Errorf("expected device ID sw-02, got %q", ids[1])
	}
}
