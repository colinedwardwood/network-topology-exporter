//go:build bench

// Scale benchmarks for the /metrics render path. These exercise the same
// code a Prometheus scrape walks: TopologyCollector.Collect plus the
// text-format encoder behind promhttp.HandlerFor. They are intentionally
// gated behind the `bench` build tag (run with `go test -tags=bench
// -bench=BenchmarkMetricsRender`) so they do not run in the default test
// suite and never block CI.
//
// Output drives docs/operator/scale.md. Methodology:
//
//   - Disable CPU frequency scaling on the test host before running
//     (`sudo cpupower frequency-set -g performance` on Linux).
//   - Pin to a single core (`taskset -c 0 go test -tags=bench ...`).
//   - Run with -count=5 and report the median.
//
// Each benchmark prints two custom metrics:
//
//	ns/op            — wall time for one /metrics render
//	bytes/scrape     — size of the rendered response body
//
// See `scripts/run-scale-bench.sh` for a wrapper that captures the run
// environment alongside the numbers.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// buildSyntheticGraph mirrors the helper in internal/metrics's tests but is
// duplicated here so the bench file is self-contained and the synthesis
// strategy can drift independently of the unit-test fixture if needed.
// Devices, ports, and labels are generated deterministically so payload
// size is stable across runs.
func buildSyntheticGraph(numEdges int) discovery.Graph {
	devices := make([]discovery.Device, 0, numEdges+1)
	edges := make([]discovery.Edge, 0, numEdges)

	devices = append(devices, discovery.Device{
		ID:        "sw-001",
		Vendor:    "cisco",
		Model:     "nexus-9000",
		OSVersion: "9.3(10)",
		Site:      "dc1",
		Uptime:    3600 * time.Second,
	})

	for i := range numEdges {
		dstID := fmt.Sprintf("sw-%05d", i+2)
		devices = append(devices, discovery.Device{
			ID:        dstID,
			Vendor:    "arista",
			Model:     "7050cx3",
			OSVersion: "4.28.0F",
			Site:      "dc1",
			Uptime:    time.Duration(i+1) * time.Second,
		})
		edges = append(edges, discovery.Edge{
			SrcDevice:      "sw-001",
			SrcPort:        fmt.Sprintf("GigabitEthernet0/%d", i%48),
			DstDevice:      dstID,
			DstPort:        "GigabitEthernet0/0",
			DiscoveryProto: discovery.DiscoveryProtocolLLDP,
			LinkKind:       discovery.LinkKindEthernet,
			Direction:      discovery.DirectionBidirectional,
		})
	}

	return discovery.Graph{Devices: devices, Edges: edges}
}

func benchmarkMetricsRender(b *testing.B, numEdges int) {
	m := metrics.New(false)
	m.Topology.Update(buildSyntheticGraph(numEdges))

	handler := promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{Registry: m.Registry()})

	// One warm-up render to absorb cold-cache and first-time allocator
	// effects; this matches what a real Prometheus server sees after the
	// first scrape.
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/metrics", nil) //nolint:noctx
	handler.ServeHTTP(rec, req)
	warmBytes := rec.Body.Len()

	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/metrics", nil) //nolint:noctx
		handler.ServeHTTP(rec, req)
		// Drain the body the same way a scraper would.
		_, _ = io.Copy(io.Discard, rec.Body)
	}
	b.StopTimer()

	b.ReportMetric(float64(warmBytes), "bytes/scrape")
}

func BenchmarkMetricsRender1kEdges(b *testing.B)  { benchmarkMetricsRender(b, 1_000) }
func BenchmarkMetricsRender5kEdges(b *testing.B)  { benchmarkMetricsRender(b, 5_000) }
func BenchmarkMetricsRender10kEdges(b *testing.B) { benchmarkMetricsRender(b, 10_000) }
func BenchmarkMetricsRender25kEdges(b *testing.B) { benchmarkMetricsRender(b, 25_000) }
func BenchmarkMetricsRender50kEdges(b *testing.B) { benchmarkMetricsRender(b, 50_000) }
