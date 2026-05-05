package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewRegistersExpectedMetrics(t *testing.T) {
	m := New()

	m.DeviceInfo.WithLabelValues("dev-1", "cisco", "C9300", "17.6.4", "lab").Set(1)

	const want = `
# HELP network_device_info One series per discovered device. Value is always 1; inventory data is in the labels.
# TYPE network_device_info gauge
network_device_info{device_id="dev-1",model="C9300",os_version="17.6.4",site="lab",vendor="cisco"} 1
`
	if err := testutil.GatherAndCompare(m.Registry(), strings.NewReader(want), "network_device_info"); err != nil {
		t.Fatalf("metric mismatch: %v", err)
	}
}

func TestTopologyConflictTotalIsRegistered(t *testing.T) {
	m := New()

	m.TopologyConflictTotal.WithLabelValues("port_name_mismatch").Inc()

	const want = `
# HELP network_topology_conflict_total Source disagreements detected during reconciliation, by conflict type.
# TYPE network_topology_conflict_total counter
network_topology_conflict_total{conflict_type="port_name_mismatch"} 1
`
	if err := testutil.GatherAndCompare(m.Registry(), strings.NewReader(want), "network_topology_conflict_total"); err != nil {
		t.Fatalf("metric mismatch: %v", err)
	}
}

func TestMetricNamespaceConsistency(t *testing.T) {
	m := New()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	for _, mf := range mfs {
		name := mf.GetName()
		// Skip standard Go/process collectors.
		if strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "process_") {
			continue
		}
		if !strings.HasPrefix(name, "network_") {
			t.Errorf("metric %q does not use network_ prefix", name)
		}
	}
}

func TestRegistryExposesGoCollector(t *testing.T) {
	m := New()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sawGo bool
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), "go_") {
			sawGo = true
			break
		}
	}
	if !sawGo {
		t.Fatal("expected at least one go_* metric from the standard Go collector")
	}
}
