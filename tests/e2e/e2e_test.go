//go:build e2e

// Package e2e exercises the exporter's discovery stack against a real
// network topology deployed by containerlab. Tests are gated behind the
// "e2e" build tag so they never run as part of `go test ./...`.
//
// Prerequisites:
//   - Docker running (or compatible container runtime)
//   - containerlab installed: https://containerlab.dev/install/
//   - ghcr.io/nokia/srlinux:24.7.2 pulled (CI pre-pulls; local: docker pull)
//
// Run locally:
//
//	CLAB_SUDO=1 go test ./tests/e2e/... -tags e2e -v -timeout 15m
//
// Set CLAB_SUDO=1 when containerlab requires root (Linux default install).
// Omit it when running as root or when containerlab is in rootless mode.
package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
)

const (
	topoName      = "nte-e2e"
	topoFile      = "clab-topology.yml"
	snmpCommunity = "public"
)

// nodeIPs holds the management-plane IP of each SR Linux node, populated by
// TestMain before any test function runs.
var nodeIPs map[string]net.IP

func TestMain(m *testing.M) {
	fmt.Println("e2e: deploying containerlab topology", topoName)
	if err := clabRun("deploy", "--topo", topoFile, "--reconfigure"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: containerlab deploy failed:", err)
		os.Exit(1)
	}

	var initErr error
	nodeIPs, initErr = getNodeIPs()
	if initErr != nil {
		fmt.Fprintln(os.Stderr, "e2e: get node IPs:", initErr)
		_ = clabRun("destroy", "--name", topoName, "--cleanup")
		os.Exit(1)
	}
	for node, ip := range nodeIPs {
		fmt.Printf("e2e: %s → %s\n", node, ip)
	}

	fmt.Println("e2e: waiting for SNMP to become available (up to 2m)...")
	if err := waitForSNMP(2 * time.Minute); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: SNMP readiness timeout:", err)
		_ = clabRun("destroy", "--name", topoName, "--cleanup")
		os.Exit(1)
	}

	// LLDP hello timer is 30 s; wait 45 s to ensure all adjacencies are formed.
	fmt.Println("e2e: waiting 45s for LLDP convergence...")
	time.Sleep(45 * time.Second)

	code := m.Run()

	fmt.Println("e2e: destroying topology")
	_ = clabRun("destroy", "--name", topoName, "--cleanup")
	os.Exit(code)
}

// clabRun executes a containerlab subcommand, streaming output to stderr.
func clabRun(args ...string) error {
	cmd := clabCmd(args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// clabCmd builds an exec.Cmd for containerlab, prepending sudo when
// CLAB_SUDO is set (required on most Linux installs where containerlab
// needs elevated privileges for network namespace creation).
func clabCmd(args ...string) *exec.Cmd { //nolint:gosec
	if os.Getenv("CLAB_SUDO") != "" {
		return exec.Command("sudo", append([]string{"containerlab"}, args...)...) //nolint:gosec
	}
	return exec.Command("containerlab", args...) //nolint:gosec
}

// getNodeIPs resolves the management-plane IP of each containerlab node by
// parsing docker inspect output. Containerlab names containers
// "clab-<topo>-<node>" and attaches them to the shared "clab" management
// network; we pick the first non-empty IP across all attached networks.
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

// waitForSNMP polls all nodes until SNMP sysUpTime replies successfully or
// the deadline is exceeded.
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
			fmt.Printf("e2e: SNMP ready on all %d nodes\n", ready)
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

// snmpAlive does a single sysUpTime GET to check whether SNMP is reachable.
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
