//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

// snmpV3Params returns walk parameters for the authPriv USM user created in
// testnode/start.sh (SHA auth, AES privacy). Credentials are e2e fixtures,
// not secrets.
func snmpV3Params(node string) snmpwalk.Params {
	return snmpwalk.Params{
		IP:        nodeIPs[node],
		Port:      161,
		Timeout:   10 * time.Second,
		V3:        true,
		Username:  "nte-e2e-v3",
		AuthProto: "SHA",
		AuthKey:   []byte("nte-auth-pass"),
		PrivProto: "AES",
		PrivKey:   []byte("nte-priv-pass"),
	}
}

// TestSNMPv3SystemWalk closes the v3 coverage gap: prior to this test the
// SNMPv3 path (USM engine discovery, SHA authentication, AES privacy) was
// exercised only at the credential-resolution unit level, never against a
// live agent. A v3 regression in gosnmp wiring or Params plumbing would have
// shipped silently.
func TestSNMPv3SystemWalk(t *testing.T) {
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		t.Run(node, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			dev, err := snmpwalk.Walk(ctx, snmpV3Params(node))
			if err != nil {
				t.Fatalf("snmp.Walk (v3 authPriv): %v", err)
			}
			if dev == nil {
				t.Fatal("Walk returned nil device")
			}
			if !strings.EqualFold(dev.ID, node) {
				t.Errorf("device ID = %q, want %q", dev.ID, node)
			}
			if dev.Vendor != "net-snmp" {
				t.Errorf("vendor = %q, want net-snmp", dev.Vendor)
			}
		})
	}
}

// TestSNMPv3LLDPWalk verifies a module table walk (not just the scalar system
// group) over the v3 transport: LLDP-MIB rows via GETBULK under USM authPriv
// must yield the same neighbours as the v2c walk in lldp_test.go.
func TestSNMPv3LLDPWalk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, _, err := lldp.Walk(ctx, snmpV3Params("spine1"), "spine1", nil)
	if err != nil {
		t.Fatalf("lldp.Walk (v3 authPriv): %v", err)
	}

	neighbours := lldpNeighbourSet(edges)
	for _, want := range []string{"leaf1", "leaf2"} {
		if !neighbours[strings.ToLower(want)] {
			t.Errorf("spine1 v3 LLDP missing neighbour %q; discovered: %v", want, sortedKeys(neighbours))
		}
	}
}

// TestSNMPv3WrongCredentialsRejected pins the negative path: a wrong AuthKey
// must fail the walk, proving the agent actually enforces USM authentication
// (i.e. the positive tests above are not passing through an unauthenticated
// fallback).
func TestSNMPv3WrongCredentialsRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := snmpV3Params("spine1")
	p.AuthKey = []byte("wrong-auth-pass")
	if _, err := snmpwalk.Walk(ctx, p); err == nil {
		t.Fatal("Walk succeeded with wrong v3 auth key; agent is not enforcing USM authentication")
	}
}
