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
)

// Metrics bundles every collector the exporter exposes. One instance per
// process; passed into the discovery loop so it can update series.
type Metrics struct {
	registry *prometheus.Registry

	// Topology inventory.
	DeviceInfo          *prometheus.GaugeVec
	DeviceUptimeSeconds *prometheus.GaugeVec
	TopologyEdgeInfo    *prometheus.GaugeVec
	TopologyChangeTotal *prometheus.CounterVec

	// LD-11: count of out-of-scope neighbours seen in the current cycle.
	// Detail (which device, which port, what hint) goes in log lines.
	OutOfScopeNeighboursTotal prometheus.Gauge

	// LD-13: graph freshness signals.
	GraphStale                 prometheus.Gauge
	SnapshotLastWrittenUnix    prometheus.Gauge
	SnapshotLoadedDevicesTotal prometheus.Gauge

	// Discovery cycle health. All aggregates — no per-device label values.
	DiscoveryDevicesTotal      *prometheus.GaugeVec
	DiscoveryCycleDuration     prometheus.Histogram
	DiscoveryModuleDuration    *prometheus.HistogramVec
	SNMPWalksTotal             *prometheus.CounterVec
	CredentialTrialsTotal      *prometheus.CounterVec
}

// New builds and registers the exporter's metric set.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		DeviceInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_device_info",
			Help: "One series per discovered device. Value is always 1; inventory data is in the labels.",
		}, []string{"device_id", "vendor", "model", "os_version", "site"}),
		DeviceUptimeSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_device_uptime_seconds",
			Help: "Per-device uptime from the SNMP SYSTEM group (sysUpTime).",
		}, []string{"device_id"}),
		TopologyEdgeInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_edge_info",
			Help: "One series per discovered topology edge. Value is always 1.",
		}, []string{"src_device", "src_port", "dst_device", "dst_port", "discovery_proto", "link_type", "direction"}),
		TopologyChangeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_change_total",
			Help: "Topology mutations between discovery cycles. Resets on restart; use increase() not rate().",
		}, []string{"change_kind", "discovery_proto"}),
		OutOfScopeNeighboursTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "network_topology_out_of_scope_neighbours_total",
			Help: "Count of LLDP/CDP-discovered neighbours whose IP falls outside the LD-11 CIDR allow-list. Detail in log lines.",
		}),
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
	}

	reg.MustRegister(
		m.DeviceInfo,
		m.DeviceUptimeSeconds,
		m.TopologyEdgeInfo,
		m.TopologyChangeTotal,
		m.OutOfScopeNeighboursTotal,
		m.GraphStale,
		m.SnapshotLastWrittenUnix,
		m.SnapshotLoadedDevicesTotal,
		m.DiscoveryDevicesTotal,
		m.DiscoveryCycleDuration,
		m.DiscoveryModuleDuration,
		m.SNMPWalksTotal,
		m.CredentialTrialsTotal,
	)

	return m
}

// Registry returns the underlying Prometheus registry, for use by promhttp.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
