//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
%s
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
%s`, buildCIDRList(nodeIPs), snapshotPath, buildTargetList(nodeIPs, []string{"spine1", "leaf1", "leaf2"}))

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
	// network_topology_edge_info uses sysName as device ID, not IP address.
	assertEdge(t, metrics, "spine1", "leaf1")
	assertEdge(t, metrics, "spine1", "leaf2")
}

// assertEdge checks that at least one network_topology_edge_info series exists
// with the given device IDs as src_device/dst_device in either order.
func assertEdge(t *testing.T, metrics, devA, devB string) {
	t.Helper()
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
