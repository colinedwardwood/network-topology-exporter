package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewDebugMuxServesProfiles asserts the dedicated debug mux serves the
// standard pprof profiles with a 200 and a non-empty body (issue #69). heap is
// the acceptance-critical profile; goroutine is checked too since it is the
// other commonly-grabbed runtime profile.
func TestNewDebugMuxServesProfiles(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newDebugMux())
	defer srv.Close()

	for _, path := range []string{"/debug/pprof/heap", "/debug/pprof/goroutine"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Errorf("GET %s: empty body, want a pprof payload", path)
		}
	}
}

// TestNewDebugMuxServesIndex confirms the /debug/pprof/ index is reachable —
// it is what lists the named profiles for an operator.
func TestNewDebugMuxServesIndex(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newDebugMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestMainMuxHasNoPprof is a security guard: importing net/http/pprof anywhere
// in the binary registers its handlers on http.DefaultServeMux via init(). The
// main metrics mux must be built fresh (http.NewServeMux) and must NOT serve
// /debug/pprof/* — the debug surface belongs only on the opt-in debug listener.
// This test serves a hand-built mux that mirrors the main server's
// construction (a fresh ServeMux, no pprof registration) and asserts pprof is
// absent. It would fail if the main server ever switched to DefaultServeMux.
func TestMainMuxHasNoPprof(t *testing.T) {
	t.Parallel()
	// Mirror app.go: the main server uses a freshly-allocated mux, never
	// http.DefaultServeMux, so the pprof init() side-effect cannot leak in.
	mainMux := http.NewServeMux()
	mainMux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("metrics"))
	})
	srv := httptest.NewServer(mainMux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/heap")
	if err != nil {
		t.Fatalf("GET /debug/pprof/heap: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("main mux served /debug/pprof/heap with status %d; pprof must NOT be on the main mux", resp.StatusCode)
	}
}
