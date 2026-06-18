package yang

import (
	"os"
	"testing"

	"github.com/grafana/network-topology-exporter/internal/discovery"
)

// TestGenerateValidationDoc writes a YANG-JSON instance to YANG_VALIDATE_OUT
// when set, for the CI yanglint step. No-op otherwise. The graph is adversarial
// on purpose: an edge endpoint absent from Devices (spine1), parallel
// multi-protocol edges, empty ports, and a unicode device id.
func TestGenerateValidationDoc(t *testing.T) {
	out := os.Getenv("YANG_VALIDATE_OUT")
	if out == "" {
		t.Skip("YANG_VALIDATE_OUT not set")
	}
	g := &discovery.Graph{
		Devices: []discovery.Device{
			{ID: "leaf1", Vendor: "cisco", OSVersion: "17.12"},
			{ID: "switch-Ωμέγα"},
		},
		Edges: []discovery.Edge{
			bidiEdge("leaf1", "Gi0/1", "spine1", "Gi0/2", discovery.DiscoveryProtocolLLDP),
			{SrcDevice: "leaf1", SrcPort: "Gi0/1", DstDevice: "spine1", DstPort: "Gi0/2",
				DiscoveryProto: discovery.DiscoveryProtocolCDP, Direction: discovery.DirectionUnidirectional},
			{SrcDevice: "leaf1", SrcPort: "", DstDevice: "switch-Ωμέγα", DstPort: "",
				DiscoveryProto: discovery.DiscoveryProtocolFDB, Direction: discovery.DirectionUnidirectional},
		},
	}
	b, err := Render(g, Config{NetworkID: "ci-adversarial"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil { //nolint:gosec // CI-controlled output path
		t.Fatal(err)
	}
}
