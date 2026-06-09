package yang

import (
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// GraphSource yields the current immutable topology graph. Satisfied by
// *metrics.TopologyCollector.
type GraphSource interface {
	CurrentGraph() *discovery.Graph
}

type renderCache struct {
	key   *discovery.Graph
	bytes []byte
}

const contentType = "application/yang-data+json"

// Handler serves the current topology as RFC 8345 YANG-JSON. It returns 503
// until ready; 405 (+ Allow) on non-GET/HEAD; HEAD returns headers with no
// body. Renders are cached by graph pointer identity, so repeated GETs of an
// unchanged graph are O(1). Cache state is an atomic.Pointer over an immutable
// struct, so concurrent GETs are race-free.
func Handler(src GraphSource, ready func() bool, cfg Config) http.HandlerFunc {
	var cache atomic.Pointer[renderCache]
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if ready == nil || !ready() {
			http.Error(w, "topology not ready", http.StatusServiceUnavailable)
			return
		}
		g := src.CurrentGraph()
		c := cache.Load()
		if c == nil || c.key != g {
			b, err := Render(g, cfg)
			if err != nil {
				http.Error(w, "render error", http.StatusInternalServerError)
				return
			}
			c = &renderCache{key: g, bytes: b}
			cache.Store(c)
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(c.bytes)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(c.bytes)
	}
}
