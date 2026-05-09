package metrics

import (
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

const maxLabelLen = 128

func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > maxLabelLen {
		return s[:maxLabelLen]
	}
	return s
}

// TopologyCollector implements prometheus.Collector. It holds an atomic
// pointer to the current discovery.Graph and generates ConstMetrics at
// scrape time, eliminating the Reset()+repopulate race inherent in GaugeVec.
// Concurrent Collect calls are safe: each reads the same immutable snapshot.
type TopologyCollector struct {
	snap            atomic.Pointer[discovery.Graph]
	emitBoundaryObs bool

	deviceInfoDesc   *prometheus.Desc
	deviceUptimeDesc *prometheus.Desc
	edgeInfoDesc     *prometheus.Desc
	oosCountDesc     *prometheus.Desc
	boundaryObsDesc  *prometheus.Desc
	graphEdgesDesc   *prometheus.Desc
	graphDevicesDesc *prometheus.Desc

	scrapeDuration prometheus.Gauge
	scrapeSamples  prometheus.Gauge
}

func newTopologyCollector(emitBoundaryObs bool, scrapeDuration, scrapeSamples prometheus.Gauge) *TopologyCollector {
	c := &TopologyCollector{
		emitBoundaryObs: emitBoundaryObs,
		scrapeDuration:  scrapeDuration,
		scrapeSamples:   scrapeSamples,
		deviceInfoDesc: prometheus.NewDesc(
			"network_device_info",
			"One series per discovered device. Value is always 1; inventory data is in the labels.",
			[]string{"device_id", "vendor", "model", "os_version", "site"},
			nil,
		),
		deviceUptimeDesc: prometheus.NewDesc(
			"network_device_uptime_seconds",
			"Per-device uptime from the SNMP SYSTEM group (sysUpTime).",
			[]string{"device_id"},
			nil,
		),
		edgeInfoDesc: prometheus.NewDesc(
			"network_topology_edge_info",
			"One series per discovered topology edge. Value is always 1.",
			[]string{"src_device", "src_port", "dst_device", "dst_port", "discovery_proto", "link_type", "direction"},
			nil,
		),
		oosCountDesc: prometheus.NewDesc(
			"network_topology_out_of_scope_neighbours_total",
			"Count of LLDP/CDP-discovered neighbours whose IP falls outside the LD-11 CIDR allow-list. Detail in log lines.",
			nil,
			nil,
		),
		boundaryObsDesc: prometheus.NewDesc(
			"network_topology_boundary_observation_info",
			"LD-15 uncoordinated mode: one series per out-of-scope boundary observation. "+
				"peer_a is always the alphabetically-smaller endpoint. "+
				"A Mimir recording rule fires count by(peer_a,peer_b,proto)(...)==2 for confirmed cross-boundary edges.",
			[]string{"peer_a", "peer_b", "reporting_device", "src_port", "proto"},
			nil,
		),
		graphEdgesDesc: prometheus.NewDesc(
			"network_topology_graph_edges_total",
			"Current number of reconciled edges in the active topology graph.",
			nil, nil,
		),
		graphDevicesDesc: prometheus.NewDesc(
			"network_topology_graph_devices_total",
			"Current number of devices in the active topology graph.",
			nil, nil,
		),
	}
	empty := discovery.Graph{}
	c.snap.Store(&empty)
	return c
}

// Update atomically swaps the graph snapshot. The next Collect call reads
// the new graph with no empty-window gap.
func (c *TopologyCollector) Update(g discovery.Graph) {
	c.snap.Store(&g)
}

// Describe sends all metric descriptors to ch.
func (c *TopologyCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.deviceInfoDesc
	ch <- c.deviceUptimeDesc
	ch <- c.edgeInfoDesc
	ch <- c.oosCountDesc
	ch <- c.boundaryObsDesc
	ch <- c.graphEdgesDesc
	ch <- c.graphDevicesDesc
}

// Collect generates metrics from the current snapshot. May be called
// concurrently by the Prometheus HTTP handler; safe because snap is an
// atomic pointer and ConstMetrics are immutable.
func (c *TopologyCollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	g := c.snap.Load()

	samples := 0
	for _, d := range g.Devices {
		ch <- prometheus.MustNewConstMetric(
			c.deviceInfoDesc, prometheus.GaugeValue, 1,
			sanitizeLabel(d.ID), sanitizeLabel(d.Vendor), sanitizeLabel(d.Model),
			sanitizeLabel(d.OSVersion), sanitizeLabel(d.Site),
		)
		ch <- prometheus.MustNewConstMetric(
			c.deviceUptimeDesc, prometheus.GaugeValue, d.Uptime.Seconds(),
			sanitizeLabel(d.ID),
		)
		samples += 2
	}

	for _, e := range g.Edges {
		ch <- prometheus.MustNewConstMetric(
			c.edgeInfoDesc, prometheus.GaugeValue, 1,
			sanitizeLabel(e.SrcDevice), sanitizeLabel(e.SrcPort),
			sanitizeLabel(e.DstDevice), sanitizeLabel(e.DstPort),
			e.DiscoveryProto, e.LinkKind, string(e.Direction),
		)
		samples++
	}

	ch <- prometheus.MustNewConstMetric(
		c.oosCountDesc, prometheus.GaugeValue, float64(len(g.OutOfScope)),
	)
	samples++

	if c.emitBoundaryObs {
		for _, n := range g.OutOfScope {
			peerA, peerB := canonicalPair(
				sanitizeLabel(n.ReportingDevice),
				sanitizeLabel(n.NeighbourHint),
			)
			ch <- prometheus.MustNewConstMetric(
				c.boundaryObsDesc, prometheus.GaugeValue, 1,
				peerA, peerB,
				sanitizeLabel(n.ReportingDevice), sanitizeLabel(n.ReportingPort),
				n.Proto,
			)
			samples++
		}
	}

	ch <- prometheus.MustNewConstMetric(c.graphEdgesDesc, prometheus.GaugeValue, float64(len(g.Edges)))
	ch <- prometheus.MustNewConstMetric(c.graphDevicesDesc, prometheus.GaugeValue, float64(len(g.Devices)))
	samples += 2

	if c.scrapeDuration != nil {
		c.scrapeDuration.Set(time.Since(start).Seconds())
	}
	if c.scrapeSamples != nil {
		c.scrapeSamples.Set(float64(samples))
	}
}

// canonicalPair returns (a, b) with the alphabetically-smaller value first.
// Used by LD-15 boundary observations so the Mimir recording rule matches
// from either side with a stable canonical pair ordering.
func canonicalPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}
