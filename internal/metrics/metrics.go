// Package metrics declares every Prometheus metric the exporter emits.
//
// All metrics are registered against a single registry exposed on /metrics.
// Cardinality budget and label semantics are documented in README.md and in
// docs/metrics.md.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics bundles every collector the exporter exposes. One instance per
// process; passed into discovery modules so they can update series.
type Metrics struct {
	registry *prometheus.Registry

	DeviceInfo                 *prometheus.GaugeVec
	DeviceUptimeSeconds        *prometheus.GaugeVec
	TopologyEdgeInfo           *prometheus.GaugeVec
	TopologyEdgeUtilization    *prometheus.GaugeVec
	TopologyChangeTotal        *prometheus.CounterVec
	DiscoveryDevicesTotal      *prometheus.GaugeVec
	DiscoveryCycleDuration     prometheus.Histogram
	DiscoveryModuleDuration    *prometheus.HistogramVec
}

// New builds and registers the exporter's metric set.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	// Standard process / Go collectors so /metrics is useful from minute one.
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		DeviceInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_device_info",
			Help: "One series per discovered device. The label set is the inventory record.",
		}, []string{"device_id", "vendor", "model", "os_version", "site", "parent_device"}),
		DeviceUptimeSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_device_uptime_seconds",
			Help: "Per-device uptime from the SYSTEM group (sysUpTime).",
		}, []string{"device_id"}),
		TopologyEdgeInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_edge_info",
			Help: "One series per discovered topology edge.",
		}, []string{"src_device", "src_port", "dst_device", "dst_port", "discovery_proto", "link_type"}),
		TopologyEdgeUtilization: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "network_topology_edge_utilization_ratio",
			Help: "Edge utilization 0..1, joined with IF-MIB rates when both ends are known.",
		}, []string{"src_device", "src_port", "dst_device", "dst_port", "discovery_proto", "link_type"}),
		TopologyChangeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "network_topology_change_total",
			Help: "Topology mutations between discovery cycles.",
		}, []string{"change_kind", "discovery_proto"}),
		DiscoveryDevicesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "topology_discovery_devices_total",
			Help: "Per-cycle device-discovery outcome.",
		}, []string{"status"}),
		DiscoveryCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "topology_discovery_cycle_duration_seconds",
			Help:    "End-to-end discovery cycle wall time.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 10),
		}),
		DiscoveryModuleDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "topology_discovery_module_duration_seconds",
			Help:    "Per-module wall time within a cycle.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
		}, []string{"module"}),
	}

	reg.MustRegister(
		m.DeviceInfo,
		m.DeviceUptimeSeconds,
		m.TopologyEdgeInfo,
		m.TopologyEdgeUtilization,
		m.TopologyChangeTotal,
		m.DiscoveryDevicesTotal,
		m.DiscoveryCycleDuration,
		m.DiscoveryModuleDuration,
	)

	return m
}

// Registry returns the underlying Prometheus registry, for use by promhttp.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
