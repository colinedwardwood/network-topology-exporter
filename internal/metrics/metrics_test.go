package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewRegistersExpectedMetrics(t *testing.T) {
	m := New()

	m.DeviceInfo.WithLabelValues("dev-1", "cisco", "C9300", "17.6.4", "lab", "").Set(1)

	const want = `
# HELP network_device_info One series per discovered device. The label set is the inventory record.
# TYPE network_device_info gauge
network_device_info{device_id="dev-1",model="C9300",os_version="17.6.4",parent_device="",site="lab",vendor="cisco"} 1
`
	if err := testutil.GatherAndCompare(m.Registry(), strings.NewReader(want), "network_device_info"); err != nil {
		t.Fatalf("metric mismatch: %v", err)
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
