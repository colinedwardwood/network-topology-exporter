//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExporterBinary(t *testing.T) {
	dir := t.TempDir()

	// Pick a free port by binding :0 and immediately releasing it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	listenAddr := ln.Addr().String()
	ln.Close()

	snapshotPath := dir + "/snapshot.json"
	configPath := dir + "/config.yaml"

	// Build the CIDR allow-list wide enough to contain all node IPs.
	// Each containerlab management IP is a /32 written as a /24 covering it.
	cidrs := make([]string, 0, len(nodeIPs))
	seen := make(map[string]bool)
	for _, ip := range nodeIPs {
		ip4 := ip.To4()
		if ip4 == nil {
			cidrs = append(cidrs, ip.String()+"/128")
			continue
		}
		// Collapse to a /24 so a single CIDR covers all nodes on the same subnet.
		subnet := fmt.Sprintf("%d.%d.%d.0/24", ip4[0], ip4[1], ip4[2])
		if !seen[subnet] {
			seen[subnet] = true
			cidrs = append(cidrs, subnet)
		}
	}
	cidrYAML := ""
	for _, c := range cidrs {
		cidrYAML += "      - " + c + "\n"
	}

	targetsYAML := ""
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		targetsYAML += fmt.Sprintf("  - host: %s\n    port: 161\n", nodeIPs[node])
	}

	cfg := fmt.Sprintf(`discovery:
  interval: 15s
  timeout_per_device: 10s
  parallelism: 8
  scope:
    cidr_allow_list:
%s
modules:
  snmp:
    enabled: true
    version: v2c
  lldp:
    enabled: true
snapshot:
  path: %s
targets:
%s`, cidrYAML, snapshotPath, targetsYAML)

	if err := os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	binPath := dir + "/topology-exporter"
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/topology-exporter")
	// go build must run from the module root, not the test working directory.
	buildCmd.Dir = "../../"
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build binary: %v", err)
	}

	// The binary uses modules.snmp.community_env only when no profiles are
	// configured. We leave profiles empty so the legacy single-community path
	// is active; set the env var to "public".
	cmd := exec.Command(binPath, //nolint:gosec
		"-config.file", configPath,
		"-web.listen-address", listenAddr,
		"-log.level", "debug",
	)
	cmd.Env = append(os.Environ(), "SNMP_COMMUNITY=public")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start exporter: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	baseURL := "http://" + listenAddr
	if err := pollReady(baseURL+"/readyz", 60*time.Second); err != nil {
		t.Fatalf("exporter /readyz: %v", err)
	}

	resp, err := http.Get(baseURL + "/metrics") //nolint:noctx
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics returned %d", resp.StatusCode)
	}

	metrics := string(body)

	// network_topology_edge_info labels: src_device, src_port, dst_device,
	// dst_port, discovery_proto, link_type, direction.
	// LLDP reports edges from each device's perspective; the reconciler
	// normalises to bidirectional pairs. Assert the spine1↔leaf1 and
	// spine1↔leaf2 edges are present in at least one direction.
	spine1ID := nodeIPs["spine1"].String()
	leaf1ID := nodeIPs["leaf1"].String()
	leaf2ID := nodeIPs["leaf2"].String()

	assertEdge(t, metrics, spine1ID, leaf1ID)
	assertEdge(t, metrics, spine1ID, leaf2ID)
}

// assertEdge checks that at least one network_topology_edge_info series exists
// with the given device IDs as src_device/dst_device in either order.
func assertEdge(t *testing.T, metrics, devA, devB string) {
	t.Helper()
	fwd := fmt.Sprintf(`src_device="%s"`, devA) + ` ` // a→b
	rev := fmt.Sprintf(`src_device="%s"`, devB) + ` ` // b→a

	// Check for co-occurrence on the same line rather than bare substring
	// presence, to avoid false positives from other metric names.
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, "network_topology_edge_info{") {
			continue
		}
		hasFwd := strings.Contains(line, fmt.Sprintf(`src_device="%s"`, devA)) &&
			strings.Contains(line, fmt.Sprintf(`dst_device="%s"`, devB))
		hasRev := strings.Contains(line, fmt.Sprintf(`src_device="%s"`, devB)) &&
			strings.Contains(line, fmt.Sprintf(`dst_device="%s"`, devA))
		if hasFwd || hasRev {
			return
		}
	}
	_ = fwd
	_ = rev
	t.Errorf("no network_topology_edge_info edge found between %s and %s", devA, devB)
}

// pollReady polls url until it returns HTTP 200 or the deadline is exceeded.
func pollReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(url) //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s", timeout)
		}
		time.Sleep(2 * time.Second)
	}
}
