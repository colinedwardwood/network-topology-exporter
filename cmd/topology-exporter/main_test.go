package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/grafana/network-topology-exporter/internal/app"
)

// TestRunVersionFlag exercises the --version short-circuit in run().
func TestRunVersionFlag(t *testing.T) {
	code := app.Run(context.Background(), []string{"--version"})
	if code != 0 {
		t.Errorf("--version: exit code = %d, want 0", code)
	}
}

// TestRunUnknownFlag verifies that an unrecognised flag causes run() to return 1.
func TestRunUnknownFlag(t *testing.T) {
	code := app.Run(context.Background(), []string{"--no-such-flag"})
	if code != 1 {
		t.Errorf("unknown flag: exit code = %d, want 1", code)
	}
}

// TestRunMissingConfigFile verifies that run() returns 1 when the config file
// does not exist.
func TestRunMissingConfigFile(t *testing.T) {
	code := app.Run(context.Background(), []string{"--config.file=/nonexistent/path.yaml"})
	if code != 1 {
		t.Errorf("missing config: exit code = %d, want 1", code)
	}
}

// TestRunInvalidYAMLConfig verifies that run() returns 1 when the config file
// contains invalid YAML.
func TestRunInvalidYAMLConfig(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "bad-config-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	_, _ = fmt.Fprint(f, "discovery:\n  interval: [this is not a duration\n")
	_ = f.Close()

	code := app.Run(context.Background(), []string{"--config.file=" + f.Name()})
	if code != 1 {
		t.Errorf("invalid YAML: exit code = %d, want 1", code)
	}
}

// TestRunLogLevelFlag exercises the --log.level flag in run(), covering the
// newLogger switch branches that produce debug, warn, and error level loggers.
// We deliberately use a nonexistent config so run() returns immediately after
// logging (which is what we care about — reaching newLogger with each level).
func TestRunLogLevelFlag(t *testing.T) {
	for _, level := range []string{"debug", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			code := app.Run(context.Background(), []string{
				"--log.level=" + level,
				"--config.file=/nonexistent/path.yaml",
			})
			if code != 1 {
				t.Errorf("--log.level=%s: exit code = %d, want 1 (config not found)", level, code)
			}
		})
	}
}

// TestRunHubModeInvalidTLS verifies that run() returns 1 when hub mode is
// configured with nonexistent TLS certificate files. Config validation checks
// file existence at startup before any goroutine is launched.
func TestRunHubModeInvalidTLS(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "hub.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
federation:
  role: hub
  spoke_timeout: 180s
  hub:
    listen_addr: 127.0.0.1:0
    tls_ca_cert: /nonexistent/ca.pem
    tls_cert: /nonexistent/hub.crt
    tls_key: /nonexistent/hub.key
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port for the metrics listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := app.Run(ctx, []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	if code != 1 {
		t.Errorf("hub invalid TLS: exit code = %d, want 1", code)
	}
}

// TestRunSpokeModeInvalidTLS verifies that run() returns 1 when spoke mode is
// configured with nonexistent TLS certificate files. NewSpoke reads the cert
// files synchronously, so the error surfaces before any goroutine is launched.
func TestRunSpokeModeInvalidTLS(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "spoke.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 10s
  parallelism: 1
  scope:
    cidr_allow_list: []
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
federation:
  role: spoke
  spoke_timeout: 180s
  spoke:
    spoke_id: test-spoke
    hub_url: https://127.0.0.1:9999
    tls_ca_cert: /nonexistent/ca.pem
    tls_cert: /nonexistent/spoke.crt
    tls_key: /nonexistent/spoke.key
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port so web.listen-address does not conflict with anything.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	code := app.Run(context.Background(), []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	if code != 1 {
		t.Errorf("spoke invalid TLS: exit code = %d, want 1", code)
	}
}

// TestRunListenPortConflict verifies that run() returns 0 (clean shutdown) when
// the HTTP listen address is already bound. The HTTP server goroutine cancels
// the context on failure, causing run() to drain and exit via the normal path.
func TestRunListenPortConflict(t *testing.T) {
	// Hold a port open so ListenAndServe fails.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blocker listen: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	listenAddr := blocker.Addr().String()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "standalone.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code := app.Run(ctx, []string{
		"--config.file=" + cfgPath,
		"--web.listen-address=" + listenAddr,
	})
	// The HTTP server goroutine cancels the context on bind failure, triggering
	// the normal clean-shutdown path which returns 0.
	if code != 0 {
		t.Errorf("port conflict: exit code = %d, want 0", code)
	}
}

// TestRunStandaloneContextCancelled verifies that run() performs a clean
// shutdown and returns 0 when the passed context is cancelled. Uses a config
// with no targets so the discovery cycle completes immediately.
func TestRunStandaloneContextCancelled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "standalone.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
`, dir)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Bind a random port to guarantee no conflict.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{
			"--config.file=" + cfgPath,
			"--web.listen-address=" + listenAddr,
		})
	}()

	// Give the discovery loop time to complete its first cycle (no targets,
	// essentially instant) before cancelling.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("standalone cancel: exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s after context cancel")
	}
}

// ── newLogger tests ───────────────────────────────────────────────────────────

// generateSelfSignedCert generates a self-signed ECDSA certificate valid for
// localhost and writes cert.pem / key.pem into dir. Returns the paths.
func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certFile, keyFile
}

// TestRunTLSMetricsRemovedKeys verifies that a config using the removed
// listen.tls_cert_file / listen.tls_key_file keys causes the process to exit
// non-zero (config load fails at startup). Removed in v1.5.0; use
// listen.web_config_file instead.
func TestRunTLSMetricsRemovedKeys(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	cfgPath := filepath.Join(dir, "config.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
listen:
  addr: 127.0.0.1:0
  tls_cert_file: %s
  tls_key_file: %s
`, dir, certFile, keyFile)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"--config.file=" + cfgPath})
	}()

	select {
	case code := <-done:
		if code == 0 {
			t.Error("run() exit code = 0; expected non-zero (config with removed tls_cert_file/tls_key_file should fail)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s; expected immediate failure on bad config")
	}
}

// TestRunWebConfigBasicAuth verifies that when listen.web_config_file points
// at a Prometheus exporter-toolkit web-config with basic_auth_users defined,
// unauthenticated GET /metrics returns 401 and an authenticated request
// returns 200. Closes the original gap from #3 — /metrics now supports
// authentication out of the box.
func TestRunWebConfigBasicAuth(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listenAddr := ln.Addr().String()
	_ = ln.Close()

	// bcrypt cost 4 is the lowest valid cost — fine for a unit test, and avoids
	// the multi-hundred-millisecond hashing cost the toolkit default (10)
	// incurs on every test run.
	const password = "topology-test-pass" //nolint:gosec // synthetic basic-auth credential for in-process exporter-toolkit web-config test; not used outside this test
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), 4)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	bcryptHash := string(hashBytes)

	webCfgPath := filepath.Join(dir, "web-config.yml")
	webCfg := fmt.Sprintf(`
tls_server_config:
  cert_file: %s
  key_file: %s
basic_auth_users:
  scraper: %q
`, certFile, keyFile, bcryptHash)
	if err := os.WriteFile(webCfgPath, []byte(webCfg), 0o600); err != nil {
		t.Fatalf("write web-config: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	cfgContent := fmt.Sprintf(`
discovery:
  interval: 60s
  timeout_per_device: 1s
  parallelism: 1
modules:
  snmp:
    enabled: false
snapshot:
  path: %s/snapshot.json
listen:
  addr: %s
  web_config_file: %s
`, dir, listenAddr, webCfgPath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"--config.file=" + cfgPath})
	}()

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		Timeout: 5 * time.Second,
	}
	url := "https://" + listenAddr + "/metrics"

	// Wait for the listener to come up, then assert 401 without credentials.
	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = client.Get(url) //nolint:noctx
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("web-config /metrics not reachable within deadline: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("unauthenticated GET /metrics: status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Authenticated request should return 200.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("build request: %v", err)
	}
	req.SetBasicAuth("scraper", password)
	authResp, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("authenticated GET /metrics: %v", err)
	}
	defer func() { _ = authResp.Body.Close() }()
	if authResp.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET /metrics: status = %d, want 200", authResp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("run() exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s after cancel")
	}
}
