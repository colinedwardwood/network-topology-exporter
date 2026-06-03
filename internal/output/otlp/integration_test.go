package otlp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/output/otlp"
)

// TestOTLPIntegration exercises a real export against a live OTLP receiver
// (e.g. Grafana Alloy or an OpenTelemetry Collector). It is skipped unless
// OTLP_INTEGRATION_ENDPOINT is set, because CI and the sandbox have no
// collector. This is the manual follow-up the maintainer must run to satisfy
// the live-collector acceptance criterion in issue #82.
//
// Run with, for example:
//
//	OTLP_INTEGRATION_ENDPOINT=http://localhost:4318 \
//	OTLP_INTEGRATION_PROTOCOL=http \
//	go test ./internal/output/otlp/ -run TestOTLPIntegration -v
//
// then confirm the metrics (network_topology_edge_info /
// network_topology_device_info) and the topology-change log records arrive at
// the collector. Repeat with OTLP_INTEGRATION_PROTOCOL=grpc against :4317.
func TestOTLPIntegration(t *testing.T) {
	endpoint := os.Getenv("OTLP_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OTLP_INTEGRATION_ENDPOINT to run the live-collector integration test")
	}
	protocol := otlp.Protocol(os.Getenv("OTLP_INTEGRATION_PROTOCOL"))

	ctx := context.Background()
	exp, err := otlp.New(ctx, otlp.Config{
		Endpoint:   endpoint,
		Protocol:   protocol,
		Timeout:    5 * time.Second,
		InstanceID: "integration-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = exp.Shutdown(ctx) })

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{SrcDevice: "sw-a", SrcPort: "Gi0/1", DstDevice: "sw-b", DstPort: "Gi0/2", DiscoveryProto: "lldp", LinkKind: "ethernet"},
		},
		Devices: []discovery.Device{{ID: "sw-a", Vendor: "Cisco"}, {ID: "sw-b"}},
	}
	if err := exp.PushGraph(ctx, g); err != nil {
		t.Fatalf("PushGraph: %v", err)
	}

	after := g.Edges[0]
	changes := []graph.EdgeChange{{Kind: graph.ChangeAdded, After: &after}}
	if err := exp.PushChanges(ctx, changes); err != nil {
		t.Fatalf("PushChanges: %v", err)
	}
}
