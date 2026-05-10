package metrics_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

// expectedMetricNames lists every metric the exporter must expose.
// Add new metrics here when they are added to metrics.go.
// Removing or renaming a metric requires a deliberate update here
// and a CHANGELOG entry.
var expectedMetricNames = []string{
	"network_topology_device_info",
	"network_topology_device_uptime_seconds",
	"network_topology_edge_info",
	"network_topology_out_of_scope_neighbours_total",
	"network_topology_graph_edges_total",
	"network_topology_graph_devices_total",
	"network_topology_change_total",
	"network_topology_conflict_total",
	"network_topology_graph_stale",
	"network_topology_snapshot_last_written_timestamp_seconds",
	"network_topology_snapshot_loaded_devices_total",
	"network_topology_discovery_devices_total",
	"network_topology_discovery_cycle_duration_seconds",
	"network_topology_discovery_module_duration_seconds",
	"network_topology_snmp_walks_total",
	"network_topology_discovery_decode_issues_total",
	"network_topology_discovery_quarantined_rows_total",
	"network_topology_discovery_degraded_total",
	"network_topology_discovery_hard_fail_total",
	"network_topology_credential_trials_total",
	"network_topology_otlp_push_total",
	"network_topology_federation_spoke_up",
	"network_topology_federation_spoke_last_push_timestamp_seconds",
	"network_topology_federation_spoke_push_failures_total",
	"network_topology_graph_updates_rejected_total",
	"network_topology_hub_oos_unmatched_total",
	"network_topology_last_scrape_duration_seconds",
	"network_topology_last_scrape_samples_total",
	"network_topology_module_last_status",
	"network_topology_fdb_suppressed_macs_total",
	"network_topology_goroutines",
	"network_topology_snapshot_queue_depth",
}

func TestMetricSchemaStable(t *testing.T) {
	m := metrics.New(false)

	// Use Describe rather than Gather so that Vec/Histogram metrics with no
	// observations still appear. prometheus.Registry implements
	// prometheus.Collector and forwards Describe to every registered collector.
	//
	// Desc.String() format (from client_golang source):
	//   Desc{fqName: "<name>", help: "<help>", constLabels: {...}, variableLabels: {...}}
	descCh := make(chan *prometheus.Desc, 1024)
	m.Registry().Describe(descCh)
	close(descCh)

	registered := make(map[string]bool)
	for desc := range descCh {
		if name := fqNameFromDesc(desc); name != "" {
			registered[name] = true
		}
	}

	for _, name := range expectedMetricNames {
		if !registered[name] {
			t.Errorf("expected metric %q not registered", name)
		}
	}
}

// fqNameFromDesc extracts the metric name from the canonical Desc.String()
// representation: Desc{fqName: "<name>", ...}.
func fqNameFromDesc(d *prometheus.Desc) string {
	s := d.String()
	const prefix = `Desc{fqName: "`
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}
