//go:build e2e_srl

// Package srl exercises the exporter's discovery stack against a real SR Linux
// topology deployed by containerlab. Tests are gated behind the "e2e_srl" build
// tag so they never run as part of `go test ./...`.
//
// Prerequisites:
//   - Docker running on an x86-64 host (SR Linux is x86-only)
//   - containerlab installed: https://containerlab.dev/install/
//   - SR Linux image pulled: docker pull ghcr.io/nokia/srlinux:24.7.2
//
// Run with:
//
//	CLAB_SUDO=1 go test ./tests/e2e/srl/... -tags e2e_srl -v -count=1 -timeout 20m
//
// LLDP-via-SNMP on SR Linux: not supported by the vendor.
//
// SR Linux 24.7.2 (and verified across the 24.x series) does NOT implement
// the standard IEEE 802.1AB LLDP MIB at OID 1.0.8802.1.1.2 via its SNMP
// daemon. It also does not implement the classic TIMETRA-LLDP-MIB at
// 1.3.6.1.4.1.6527.3.1.2.43 that SR-OS exposes. LLDP data on SR Linux is
// only available via gNMI or JSON-RPC at the /system/lldp YANG path.
//
// Reproduced 2026-05-25 against ghcr.io/nokia/srlinux:24.7.2 with the
// same SNMP startup config that containerlab applies via the kind:srl
// default template (snmpv2.cfg). The SNMP system group (sysName, sysDescr,
// sysObjectID) is exposed and works — TestSNMPSystemWalk continues to
// pass. Standard LLDP MIB returns `No Such Object available on this agent
// at this OID` for every probe.
//
// The exporter's LLDP walker code (`internal/discovery/lldp/lldp.go`) is
// IEEE 802.1AB-2016 compliant; no walker change can recover LLDP topology
// from a vendor that doesn't expose the MIB. The four LLDP tests in this
// package are therefore skipped pending gNMI-based discovery, which is
// tracked at v2.0.0. See README § Discovery modules and issue #46.
package srl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

const (
	topoName      = "nte-e2e-srl"
	topoFile      = "../clab-srl-topology.yml"
	snmpCommunity = "public"
)

// srlLLDPSkipMsg explains why the LLDP-via-SNMP tests in this package are
// skipped. See the package-level doc comment for the full root cause.
const srlLLDPSkipMsg = "SR Linux 24.7.2 does not implement the standard IEEE 802.1AB LLDP MIB (1.0.8802.1.1.2) via SNMP. LLDP data is exposed via gNMI / JSON-RPC only. Tracked at #46; future path is gNMI discovery (v2.0.0)."

var nodeIPs map[string]net.IP

func TestMain(m *testing.M) {
	fmt.Println("e2e-srl: deploying containerlab topology", topoName)
	if err := clabRun("deploy", "--topo", topoFile, "--reconfigure"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-srl: containerlab deploy failed:", err)
		os.Exit(1)
	}

	var initErr error
	nodeIPs, initErr = getNodeIPs()
	if initErr != nil {
		fmt.Fprintln(os.Stderr, "e2e-srl: get node IPs:", initErr)
		_ = clabRun("destroy", "--name", topoName, "--cleanup")
		os.Exit(1)
	}
	for node, ip := range nodeIPs {
		fmt.Printf("e2e-srl: %s → %s\n", node, ip)
	}

	fmt.Println("e2e-srl: waiting for SNMP to become available (up to 3m)...")
	if err := waitForSNMP(3 * time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-srl: SNMP readiness timeout:", err)
		_ = clabRun("destroy", "--name", topoName, "--cleanup")
		os.Exit(1)
	}

	// SR Linux LLDP convergence is slower than lldpd; 60 s ensures all
	// adjacencies are established before the first test reads neighbour tables.
	fmt.Println("e2e-srl: waiting 60s for LLDP convergence...")
	time.Sleep(60 * time.Second)

	code := m.Run()

	fmt.Println("e2e-srl: destroying topology")
	_ = clabRun("destroy", "--name", topoName, "--cleanup")
	os.Exit(code)
}

func clabRun(args ...string) error {
	cmd := clabCmd(args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// clabCmd builds an exec.Cmd for a containerlab subcommand. CLAB_SUDO=1 is the
// expected mode in CI; CLAB_DOCKER=1 is provided for completeness even though
// SR Linux is x86-only and won't run on macOS/Apple Silicon.
func clabCmd(args ...string) *exec.Cmd { //nolint:gosec
	if os.Getenv("CLAB_DOCKER") != "" {
		wd, _ := os.Getwd()
		dockerArgs := []string{
			"run", "--rm", "--privileged",
			"--network", "host",
			"--pid", "host",
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-v", wd + ":" + wd,
			"-w", wd,
			"ghcr.io/srl-labs/clab",
			"containerlab",
		}
		dockerArgs = append(dockerArgs, args...)
		return exec.Command("docker", dockerArgs...) //nolint:gosec
	}
	if os.Getenv("CLAB_SUDO") != "" {
		return exec.Command("sudo", append([]string{"containerlab"}, args...)...) //nolint:gosec
	}
	return exec.Command("containerlab", args...) //nolint:gosec
}

func getNodeIPs() (map[string]net.IP, error) {
	nodes := []string{"spine1", "leaf1", "leaf2"}
	result := make(map[string]net.IP, len(nodes))

	for _, node := range nodes {
		container := "clab-" + topoName + "-" + node
		out, err := exec.Command("docker", "inspect", container).Output() //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("docker inspect %s: %w", container, err)
		}

		var info []struct {
			NetworkSettings struct {
				Networks map[string]struct {
					IPAddress string
				}
			}
		}
		if err := json.Unmarshal(out, &info); err != nil || len(info) == 0 {
			return nil, fmt.Errorf("parse docker inspect for %s: %w", container, err)
		}

		var ip net.IP
		for _, cfg := range info[0].NetworkSettings.Networks {
			if parsed := net.ParseIP(cfg.IPAddress); parsed != nil {
				ip = parsed
				break
			}
		}
		if ip == nil {
			return nil, fmt.Errorf("no IP found for container %s (networks: %v)", container, info[0].NetworkSettings.Networks)
		}
		result[node] = ip
	}
	return result, nil
}

func waitForSNMP(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ready := 0
		for _, ip := range nodeIPs {
			if snmpAlive(ip) {
				ready++
			}
		}
		if ready == len(nodeIPs) {
			fmt.Printf("e2e-srl: SNMP ready on all %d nodes\n", ready)
			return nil
		}
		if time.Now().After(deadline) {
			var notReady []string
			for node, ip := range nodeIPs {
				if !snmpAlive(ip) {
					notReady = append(notReady, node+"("+ip.String()+")")
				}
			}
			return fmt.Errorf("SNMP not ready after %s on: %s", timeout, strings.Join(notReady, ", "))
		}
		time.Sleep(5 * time.Second)
	}
}

func snmpAlive(ip net.IP) bool {
	client := &gsnmp.GoSNMP{
		Target:    ip.String(),
		Port:      161,
		Community: snmpCommunity,
		Version:   gsnmp.Version2c,
		Timeout:   3 * time.Second,
		Retries:   0,
		Transport: "udp",
	}
	if err := client.Connect(); err != nil {
		return false
	}
	defer func() { _ = client.Conn.Close() }()
	result, err := client.Get([]string{".1.3.6.1.2.1.1.3.0"}) // sysUpTime
	return err == nil && len(result.Variables) > 0
}

func snmpParams(node string) snmpwalk.Params {
	return snmpwalk.Params{
		IP:        nodeIPs[node],
		Port:      161,
		Timeout:   10 * time.Second,
		// snmpwalk.Params.Community is []byte (issue #5: zeroization).
		// gsnmp.GoSNMP.Community above stays a plain string because gosnmp's
		// own struct hasn't moved to []byte upstream.
		Community: []byte(snmpCommunity),
	}
}

// TestSNMPSystemWalk verifies that the SNMP system group walk returns a valid
// Device for each SR Linux node. SR Linux advertises enterprise OID
// 1.3.6.1.4.1.6527.*, which maps to "nokia".
func TestSNMPSystemWalk(t *testing.T) {
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		t.Run(node, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			dev, err := snmpwalk.Walk(ctx, snmpParams(node))
			if err != nil {
				t.Fatalf("snmp.Walk: %v", err)
			}
			if dev == nil {
				t.Fatal("Walk returned nil device")
			}
			if !strings.EqualFold(dev.ID, node) {
				t.Errorf("device ID = %q, want %q (sysName should match containerlab node name)", dev.ID, node)
			}
			if dev.Vendor != "nokia" {
				t.Errorf("vendor = %q, want nokia (enterprise OID 6527)", dev.Vendor)
			}
		})
	}
}

// TestLLDPSpine1SeesLeafs verifies that spine1 discovers LLDP edges to both
// leaf1 (via e1-1) and leaf2 (via e1-2).
func TestLLDPSpine1SeesLeafs(t *testing.T) {
	t.Skip(srlLLDPSkipMsg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, oos, err := lldp.Walk(ctx, snmpParams("spine1"), "spine1", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(spine1): %v", err)
	}
	if len(oos) > 0 {
		t.Logf("spine1 OOS neighbours: %d (passed nil allowedNets so all should be in-scope)", len(oos))
	}

	neighbours := lldpNeighbourSet(edges)
	for _, want := range []string{"leaf1", "leaf2"} {
		if !neighbours[strings.ToLower(want)] {
			t.Errorf("spine1 LLDP missing neighbour %q; discovered: %v", want, sortedKeys(neighbours))
		}
	}
	for _, e := range edges {
		if e.DiscoveryProto != "lldp" {
			t.Errorf("edge discovery_proto = %q, want lldp", e.DiscoveryProto)
		}
		if e.Direction != discovery.DirectionUnidirectional {
			t.Errorf("pre-reconcile direction = %q, want unidirectional", e.Direction)
		}
		if e.SrcDevice != "spine1" {
			t.Errorf("edge SrcDevice = %q, want spine1", e.SrcDevice)
		}
	}
}

// TestLLDPLeaf1SeesSpine verifies that leaf1 has exactly one LLDP neighbour
// (spine1), since it is connected only on e1-1.
func TestLLDPLeaf1SeesSpine(t *testing.T) {
	t.Skip(srlLLDPSkipMsg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, _, err := lldp.Walk(ctx, snmpParams("leaf1"), "leaf1", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(leaf1): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("leaf1 edge count = %d, want 1; neighbours: %v", len(edges), lldpNeighbourSet(edges))
	}
	if !strings.EqualFold(edges[0].DstDevice, "spine1") {
		t.Errorf("leaf1 LLDP peer = %q, want spine1", edges[0].DstDevice)
	}
}

// TestLLDPLeaf2SeesSpine verifies that leaf2 has exactly one LLDP neighbour
// (spine1), since it is connected only on e1-1.
func TestLLDPLeaf2SeesSpine(t *testing.T) {
	t.Skip(srlLLDPSkipMsg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, _, err := lldp.Walk(ctx, snmpParams("leaf2"), "leaf2", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(leaf2): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("leaf2 edge count = %d, want 1; neighbours: %v", len(edges), lldpNeighbourSet(edges))
	}
	if !strings.EqualFold(edges[0].DstDevice, "spine1") {
		t.Errorf("leaf2 LLDP peer = %q, want spine1", edges[0].DstDevice)
	}
}

// TestLLDPFullGraphReconciles builds the combined edge set from all three
// nodes and verifies both spine-leaf links are seen from both ends —
// the prerequisite for graph.Reconcile to promote them to bidirectional.
func TestLLDPFullGraphReconciles(t *testing.T) {
	t.Skip(srlLLDPSkipMsg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var allEdges []discovery.Edge
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		edges, _, err := lldp.Walk(ctx, snmpParams(node), node, nil)
		if err != nil {
			t.Fatalf("lldp.Walk(%s): %v", node, err)
		}
		allEdges = append(allEdges, edges...)
	}

	from := make(map[string][]string)
	for _, e := range allEdges {
		from[e.SrcDevice] = append(from[e.SrcDevice], e.DstDevice)
	}
	t.Logf("pre-reconcile edges by source: %v", from)

	biCount := 0
	for _, e := range allEdges {
		if e.Direction == discovery.DirectionBidirectional {
			biCount++
		}
	}
	if biCount > 0 {
		t.Logf("note: %d edges already marked bidirectional before Reconcile (unexpected but not fatal)", biCount)
	}

	// All 3 nodes × 1-2 links each = 4 raw observations (spine1→leaf1,
	// leaf1→spine1, spine1→leaf2, leaf2→spine1).
	if len(allEdges) < 4 {
		t.Errorf("total pre-reconcile edges = %d, want >= 4 (2 links × 2 directions)", len(allEdges))
	}
}

func lldpNeighbourSet(edges []discovery.Edge) map[string]bool {
	s := make(map[string]bool, len(edges))
	for _, e := range edges {
		s[strings.ToLower(e.DstDevice)] = true
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
