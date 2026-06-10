package app

import (
	"log/slog"
	"runtime/debug"

	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// recoverGoroutine is the shared panic-recovery body for the exporter's
// long-lived background goroutines. Without it, a panic in any one of them
// crashes the whole process — and in hub mode that destroys the aggregated
// graph for every spoke. Used as the first deferred call at the top of a
// goroutine body:
//
//	go func() {
//		defer recoverGoroutine("snapshot_writer", logger, m)
//		// ... goroutine work ...
//	}()
//
// On a panic it mirrors the per-device recover block in cycle.go: it logs the
// panic value plus the stack trace at Error level, increments the
// network_topology_panics_total{site} counter so the bug is never hidden
// silently, and then returns cleanly (it does NOT re-panic), so the goroutine
// dies gracefully rather than taking the process down with it.
//
// site must be one of the closed, low-cardinality strings documented on
// metrics.Metrics.PanicsRecoveredTotal (discovery_loop, snapshot_writer,
// otlp_push, hub_serve, hub_rebuild, hub_elector, fdb_vlan_walk, stale_watchdog, ...).
//
// Both logger and m are tolerated as nil (logger falls back to slog.Default;
// a nil m skips the counter) so the helper is safe to use from goroutines
// started before those are fully wired and from tests.
func recoverGoroutine(site string, logger *slog.Logger, m *metrics.Metrics) {
	r := recover()
	if r == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("background goroutine panicked; recovered",
		"site", site,
		"panic", r,
		"stack", string(debug.Stack()),
	)
	if m != nil {
		m.PanicsRecoveredTotal.WithLabelValues(site).Inc()
	}
}
