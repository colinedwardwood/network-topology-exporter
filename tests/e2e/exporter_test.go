//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestExporterBinary(t *testing.T) {
	dir := t.TempDir()
	listenAddr := freeAddr(t)
	snapshotPath := dir + "/snapshot.json"
	configPath := dir + "/config.yaml"

	cfg := fmt.Sprintf(`discovery:
  interval: 15s
  timeout_per_device: 10s
  parallelism: 8
  scope:
    cidr_allow_list:
      - 0.0.0.0/0
      - ::/0
modules:
  snmp:
    enabled: true
    version: v2c
    community_env: SNMP_COMMUNITY
  lldp:
    enabled: true
snapshot:
  path: %s
targets:
%s`, snapshotPath, buildTargetList(nodeIPs, []string{"spine1", "leaf1", "leaf2"}))

	if err := os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// The binary uses modules.snmp.community_env only when no profiles are
	// configured. We leave profiles empty so the legacy single-community path
	// is active; set the env var to "public".
	cmd := exec.Command(testBinPath, //nolint:gosec
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

	// /readyz returning 200 only means the HTTP server is up; the first
	// discovery cycle and LLDP convergence on the containerlab side can still
	// take 30-60s. Poll /metrics for both edges until they appear or we hit
	// the deadline (network_topology_edge_info uses sysName as device ID).
	pollEdges(t, baseURL+"/metrics", [][2]string{
		{"spine1", "leaf1"},
		{"spine1", "leaf2"},
	}, 90*time.Second)
}

// containsEdge reports whether metrics has at least one
// network_topology_edge_info series with the given device IDs as
// src_device/dst_device in either order.
func containsEdge(metrics, devA, devB string) bool {
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, "network_topology_edge_info{") {
			continue
		}
		hasFwd := strings.Contains(line, fmt.Sprintf(`src_device="%s"`, devA)) &&
			strings.Contains(line, fmt.Sprintf(`dst_device="%s"`, devB))
		hasRev := strings.Contains(line, fmt.Sprintf(`src_device="%s"`, devB)) &&
			strings.Contains(line, fmt.Sprintf(`dst_device="%s"`, devA))
		if hasFwd || hasRev {
			return true
		}
	}
	return false
}

// pollEdges polls metricsURL until every (devA, devB) pair is present as a
// network_topology_edge_info series, or the deadline is exceeded. On timeout
// it fails the test with the list of pairs that never appeared.
func pollEdges(t *testing.T, metricsURL string, pairs [][2]string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastMissing []string
	for {
		metrics, err := fetchMetrics(metricsURL)
		if err == nil {
			missing := lastMissing[:0]
			for _, p := range pairs {
				if !containsEdge(metrics, p[0], p[1]) {
					missing = append(missing, fmt.Sprintf("%s↔%s", p[0], p[1]))
				}
			}
			if len(missing) == 0 {
				return
			}
			sort.Strings(missing)
			lastMissing = missing
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("pollEdges %s: last fetch error after %s: %v", metricsURL, timeout, err)
			}
			t.Fatalf("pollEdges %s: edges never appeared within %s: %s", metricsURL, timeout, strings.Join(lastMissing, ", "))
		}
		time.Sleep(2 * time.Second)
	}
}

// fetchMetrics GETs url and returns the body as a string. Non-200 responses
// return an error so the caller can keep polling.
func fetchMetrics(url string) (string, error) {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
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
