// Package metrics declares every Prometheus metric the exporter emits.
//
// All metrics are registered against a single registry exposed on /metrics.
// Full label reference: docs/metrics.md.
//
// Cardinality rules:
//   - No metric uses a raw IP address as a label value.
//   - Interface names appear only on edge series (src_port, dst_port), where
//     they are scoped to a specific device pair — not standalone.
//   - neighbour_hint is free-form text and lives in log lines, not labels.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics bundles every collector the exporter exposes. One instance per
// process; passed into the discovery loop so it can update series.
type Metrics struct {
	registry *prometheus.Registry

	// Topology is a custom Collector that holds an atomic graph snapshot and
	// generates device, edge, uptime, OOS-count, and boundary-observation
	// metrics on-the-fly at scrape time (no Reset race).
	Topology *TopologyCollector

	// Event-driven counters and histograms — updated by the discovery loop,
	// not derived from the graph snapshot.
	TopologyChangeTotal   *prometheus.CounterVec
	TopologyConflictTotal *prometheus.CounterVec

	// LD-13: graph freshness signals.
	GraphStale                 prometheus.Gauge
	SnapshotLastWrittenUnix    prometheus.Gauge
	SnapshotLoadedDevicesTotal prometheus.Gauge

	// Discovery cycle health. All aggregates — no per-device label values.
	DiscoveryDevicesTotal   *prometheus.GaugeVec
	DiscoveryCycleDuration  prometheus.Histogram
	DiscoveryModuleDuration *prometheus.HistogramVec
	SNMPWalksTotal          *prometheus.CounterVec
	CredentialTrialsTotal   *prometheus.CounterVec
	OTLPPushTotal           *prometheus.CounterVec

	// LD-16/LD-18: hub-mode spoke-liveness signals. Only populated when
	// federation.role is hub; registered always so the metric is present
	// in the schema even on non-hub instances.
	FederationSpokeUp           *prometheus.GaugeVec // {spoke_id} — 1 active, 0 evicted
	FederationSpokeLastPushUnix *prometheus.GaugeVec // {spoke_id}

	// LD-17: spoke-mode push failure counter. Incremented when all retries
	// are exhausted. Alert on rate > 0 to detect silent channel breakage.
	FederationSpokePushFailuresTotal prometheus.Counter

	// Hub OOS matching health: non-zero means cross-domain auto-detection is
	// partially failing; operators should add known_inter_domain_links entries.
	HubOOSUnmatchedTotal prometheus.Gauge
}

// New builds and registers the exporter's metric set. emitBoundaryObs should
// be true only when federation.role is "uncoordinated" (LD-15).
func New(emitBoundaryObs bool) *Metrics {
	reg := prometheus.NewRegistry()

	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		Topology: newTopologyCollector(emitBoundaryObs),
		TopologyChangeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_change_total",
			Help: "Topology mutations between discovery cycles. Resets on restart; use increase() not rate().",
		}, []string{"change_kind", "discovery_proto"}),
		TopologyConflictTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_conflict_total",
			Help: "Source disagreements detected during reconciliation, by conflict type.",
		}, []string{"conflict_type"}),
		GraphStale: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_graph_stale",
			Help: "1 while serving the LD-13 snapshot on startup; 0 once the first live cycle completes.",
		}),
		SnapshotLastWrittenUnix: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_snapshot_last_written_unix",
			Help: "Wall-clock time of the most recent successful LD-13 snapshot write.",
		}),
		SnapshotLoadedDevicesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_snapshot_loaded_devices_total",
			Help: "Device count loaded from the LD-13 snapshot at startup.",
		}),
		DiscoveryDevicesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_discovery_devices_total",
			Help: "Per-cycle device-discovery outcome count.",
		}, []string{"status"}), // success | failed | timeout
		DiscoveryCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "network_topology_discovery_cycle_duration_seconds",
			Help:    "End-to-end discovery cycle wall time.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 10),
		}),
		DiscoveryModuleDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "network_topology_discovery_module_duration_seconds",
			Help:    "Per-module wall time within a discovery cycle.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
		}, []string{"module"}),
		SNMPWalksTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_snmp_walks_total",
			Help: "SNMP walk attempts, partitioned by terminal status.",
		}, []string{"status"}), // ok | timeout | error
		CredentialTrialsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_credential_trials_total",
			Help: "Credential trial attempts under the LD-12 rate limiter.",
		}, []string{"status"}), // ok | failed
		OTLPPushTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_otlp_push_total",
			Help: "OTLP push attempts by outcome (ok, error, dropped). Alert on error or dropped rate > 0.",
		}, []string{"status"}),
		FederationSpokeUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_federation_spoke_up",
			Help: "LD-18 hub mode: 1 while a spoke is active (pushed within federation.spoke_timeout), 0 after eviction.",
		}, []string{"spoke_id"}),
		FederationSpokeLastPushUnix: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_federation_spoke_last_push_unix",
			Help: "LD-18 hub mode: wall-clock time of the most recent push from each spoke.",
		}, []string{"spoke_id"}),
		FederationSpokePushFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "network_topology_federation_spoke_push_failures_total",
			Help: "Total number of spoke push attempts that failed after all retries. Alert on rate > 0.",
		}),
		HubOOSUnmatchedTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_hub_oos_unmatched_total",
			Help: "Count of OOS neighbour observations with no reverse match in the last hub graph rebuild. Non-zero means cross-domain auto-detection is partially failing; add known_inter_domain_links entries.",
		}),
	}

	reg.MustRegister(
		m.Topology,
		m.TopologyChangeTotal,
		m.TopologyConflictTotal,
		m.GraphStale,
		m.SnapshotLastWrittenUnix,
		m.SnapshotLoadedDevicesTotal,
		m.DiscoveryDevicesTotal,
		m.DiscoveryCycleDuration,
		m.DiscoveryModuleDuration,
		m.SNMPWalksTotal,
		m.CredentialTrialsTotal,
		m.OTLPPushTotal,
		m.FederationSpokeUp,
		m.FederationSpokeLastPushUnix,
		m.FederationSpokePushFailuresTotal,
		m.HubOOSUnmatchedTotal,
	)

	return m
}

// Registry returns the underlying Prometheus registry, for use by promhttp.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
