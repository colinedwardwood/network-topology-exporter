package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/network-topology-exporter/internal/config"
	"github.com/grafana/network-topology-exporter/internal/metrics"
)

// TestRegisterYANG_Enabled asserts that, with output.yang.enabled true and the
// server ready, GET /topology/yang returns 200 with the RFC 8345 YANG-JSON
// content type. It mirrors the real Run wiring: a metrics.New collector
// (m.Topology), the readiness func that also feeds /readyz, and a plain
// http.ServeMux, all passed to the same registerYANG helper Run uses.
func TestRegisterYANG_Enabled(t *testing.T) {
	t.Parallel()

	m := metrics.New(false)
	ready := func() bool { return true } // simulate post-first-cycle readiness

	mux := http.NewServeMux()
	registerYANG(mux, m.Topology, ready, config.YANGOutputConfig{Enabled: true, NetworkID: "test-net"})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/topology/yang")
	if err != nil {
		t.Fatalf("GET /topology/yang: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/yang-data+json" {
		t.Fatalf("Content-Type = %q, want application/yang-data+json", ct)
	}
}

// TestRegisterYANG_Disabled asserts that, with output.yang.enabled false, the
// route is never registered, so GET /topology/yang 404s.
func TestRegisterYANG_Disabled(t *testing.T) {
	t.Parallel()

	m := metrics.New(false)
	ready := func() bool { return true }

	mux := http.NewServeMux()
	registerYANG(mux, m.Topology, ready, config.YANGOutputConfig{Enabled: false})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/topology/yang")
	if err != nil {
		t.Fatalf("GET /topology/yang: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (route should be unregistered)", resp.StatusCode, http.StatusNotFound)
	}
}
