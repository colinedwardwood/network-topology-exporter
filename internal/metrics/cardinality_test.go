package metrics_test

import (
	"fmt"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

func TestCardinalityBudget(t *testing.T) {
	const (
		nDevices = 500
		nEdges   = 500 // one ring topology edge per device
	)

	// Build synthetic graph
	var devices []discovery.Device
	var edges []discovery.Edge
	for i := 0; i < nDevices; i++ {
		devices = append(devices, discovery.Device{ID: fmt.Sprintf("router-%03d", i)})
	}
	for i := 0; i < nEdges; i++ {
		edges = append(edges, discovery.Edge{
			SrcDevice:      fmt.Sprintf("router-%03d", i),
			SrcPort:        "Gi0/0",
			DstDevice:      fmt.Sprintf("router-%03d", (i+1)%nDevices),
			DstPort:        "Gi0/1",
			DiscoveryProto: "lldp",
			Direction:      discovery.DirectionBidirectional,
			Confidence:     discovery.ConfidenceHigh,
		})
	}

	m := metrics.New(false)
	m.Topology.Update(discovery.Graph{Devices: devices, Edges: edges})

	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	var total int
	for _, mf := range mfs {
		total += len(mf.GetMetric())
	}

	// Budget: 2 series per device (info + uptime) + 1 per edge + fixed exporter overhead.
	// Fixed overhead is all non-topology metrics; cap at 200 to catch any future explosions.
	const (
		deviceSeries  = nDevices * 2
		edgeSeries    = nEdges
		fixedOverhead = 200
		budget        = deviceSeries + edgeSeries + fixedOverhead
	)
	if total > budget {
		t.Errorf("cardinality budget exceeded: %d series emitted > %d budget (devices=%d, edges=%d, overhead=%d)",
			total, budget, deviceSeries, edgeSeries, fixedOverhead)
	}
}
