package yang

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

type fakeSource struct {
	mu sync.Mutex
	g  *discovery.Graph
}

func (f *fakeSource) CurrentGraph() *discovery.Graph {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.g
}
func (f *fakeSource) set(g *discovery.Graph) { f.mu.Lock(); f.g = g; f.mu.Unlock() }

func newReady(v bool) func() bool { return func() bool { return v } }

func TestHandler200WhenReady(t *testing.T) {
	src := &fakeSource{g: &discovery.Graph{Devices: []discovery.Device{{ID: "a"}}}}
	h := Handler(src, newReady(true), Config{NetworkID: "n"})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/topology/yang", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/yang-data+json" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestHandler503BeforeReady(t *testing.T) {
	src := &fakeSource{g: &discovery.Graph{}}
	h := Handler(src, newReady(false), Config{})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/topology/yang", nil))
	if rr.Code != 503 {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandler405(t *testing.T) {
	h := Handler(&fakeSource{g: &discovery.Graph{}}, newReady(true), Config{})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPost, "/topology/yang", nil))
	if rr.Code != 405 {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if a := rr.Header().Get("Allow"); a != "GET, HEAD" {
		t.Errorf("Allow = %q, want GET, HEAD", a)
	}
}

func TestHandlerHEADNoBody(t *testing.T) {
	src := &fakeSource{g: &discovery.Graph{Devices: []discovery.Device{{ID: "a"}}}}
	h := Handler(src, newReady(true), Config{})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodHead, "/topology/yang", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("HEAD wrote a body of %d bytes", rr.Body.Len())
	}
	if rr.Header().Get("Content-Length") == "" {
		t.Error("HEAD must set Content-Length")
	}
}

func TestHandlerCacheStableBytes(t *testing.T) {
	src := &fakeSource{g: &discovery.Graph{Devices: []discovery.Device{{ID: "a"}}}}
	h := Handler(src, newReady(true), Config{})
	get := func() string {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/topology/yang", nil))
		return rr.Body.String()
	}
	first, second := get(), get()
	if first != second {
		t.Error("repeated GET of unchanged graph returned different bytes")
	}
}

func TestHandlerRaceConcurrentGET(t *testing.T) {
	src := &fakeSource{g: &discovery.Graph{Devices: []discovery.Device{{ID: "a"}}}}
	h := Handler(src, newReady(true), Config{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%4 == 0 {
				src.set(&discovery.Graph{Devices: []discovery.Device{{ID: "a"}, {ID: "b"}}})
			}
			rr := httptest.NewRecorder()
			h(rr, httptest.NewRequest(http.MethodGet, "/topology/yang", nil))
		}(i)
	}
	wg.Wait()
}
