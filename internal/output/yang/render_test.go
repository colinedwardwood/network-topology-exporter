package yang

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grafana/network-topology-exporter/internal/discovery"
)

func bidiEdge(sd, sp, dd, dp string, proto discovery.DiscoveryProtocol) discovery.Edge {
	return discovery.Edge{
		SrcDevice: sd, SrcPort: sp, DstDevice: dd, DstPort: dp,
		DiscoveryProto: proto, Direction: discovery.DirectionBidirectional,
		Confidence: discovery.ConfidenceHigh, LinkKind: discovery.LinkKindEthernet,
		Adjacency: discovery.AdjacencyDirect,
	}
}

func TestRenderWorkedExample(t *testing.T) {
	g := &discovery.Graph{
		Devices: []discovery.Device{{ID: "leaf1"}, {ID: "spine1"}},
		Edges:   []discovery.Edge{bidiEdge("leaf1", "Gi0/1", "spine1", "Gi0/2", discovery.DiscoveryProtocolLLDP)},
	}
	b, err := Render(g, Config{NetworkID: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	net := doc.Networks.Network[0]
	if net.NetworkID != "lab" {
		t.Errorf("network-id = %q, want lab", net.NetworkID)
	}
	if len(net.Node) != 2 {
		t.Fatalf("nodes = %d, want 2", len(net.Node))
	}
	if len(net.Link) != 2 {
		t.Fatalf("links = %d, want 2 (bidirectional -> fwd+rev)", len(net.Link))
	}
	if net.Link[0].LinkID == net.Link[1].LinkID {
		t.Error("forward and reverse link-id must differ")
	}
	if !strings.Contains(string(b), `"ietf-l3-unicast-topology:l3-unicast-topology":{}`) {
		t.Error("missing l3-unicast network-type marker")
	}
}

func TestRenderReferentialIntegrityAndUniqueness(t *testing.T) {
	g := &discovery.Graph{
		Devices: []discovery.Device{{ID: "a"}},
		Edges: []discovery.Edge{
			bidiEdge("a", "p1", "ghost", "p9", discovery.DiscoveryProtocolLLDP),
			{SrcDevice: "a", SrcPort: "p1", DstDevice: "ghost", DstPort: "p9",
				DiscoveryProto: discovery.DiscoveryProtocolCDP, Direction: discovery.DirectionUnidirectional},
			{SrcDevice: "a", SrcPort: "", DstDevice: "ghost", DstPort: "",
				DiscoveryProto: discovery.DiscoveryProtocolFDB, Direction: discovery.DirectionUnidirectional},
		},
	}
	b, err := Render(g, Config{NetworkID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	net := doc.Networks.Network[0]
	nodeIDs := map[string]bool{}
	tpByNode := map[string]map[string]bool{}
	for _, n := range net.Node {
		if nodeIDs[n.NodeID] {
			t.Errorf("duplicate node-id %q", n.NodeID)
		}
		nodeIDs[n.NodeID] = true
		tpByNode[n.NodeID] = map[string]bool{}
		seen := map[string]bool{}
		for _, tp := range n.TerminationPoint {
			if seen[tp.TPID] {
				t.Errorf("duplicate tp-id %q under %q", tp.TPID, n.NodeID)
			}
			seen[tp.TPID] = true
			tpByNode[n.NodeID][tp.TPID] = true
		}
	}
	if !nodeIDs["ghost"] {
		t.Error("edge endpoint absent from Devices was not synthesized as a node")
	}
	linkIDs := map[string]bool{}
	for _, l := range net.Link {
		if linkIDs[l.LinkID] {
			t.Errorf("duplicate link-id %q", l.LinkID)
		}
		linkIDs[l.LinkID] = true
		if !nodeIDs[l.Source.SourceNode] {
			t.Errorf("link %q source-node %q has no node", l.LinkID, l.Source.SourceNode)
		}
		if !nodeIDs[l.Destination.DestNode] {
			t.Errorf("link %q dest-node %q has no node", l.LinkID, l.Destination.DestNode)
		}
		if l.Source.SourceTP != "" && !tpByNode[l.Source.SourceNode][l.Source.SourceTP] {
			t.Errorf("link %q source-tp %q not declared under %q", l.LinkID, l.Source.SourceTP, l.Source.SourceNode)
		}
		if l.Destination.DestTP != "" && !tpByNode[l.Destination.DestNode][l.Destination.DestTP] {
			t.Errorf("link %q dest-tp %q not declared under %q", l.LinkID, l.Destination.DestTP, l.Destination.DestNode)
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	g := &discovery.Graph{
		Devices: []discovery.Device{{ID: "b"}, {ID: "a"}},
		Edges: []discovery.Edge{
			bidiEdge("a", "p1", "b", "p2", discovery.DiscoveryProtocolLLDP),
			bidiEdge("b", "p3", "a", "p4", discovery.DiscoveryProtocolCDP),
		},
	}
	b1, _ := Render(g, Config{NetworkID: "x"})
	b2, _ := Render(g, Config{NetworkID: "x"})
	if string(b1) != string(b2) {
		t.Error("Render is not deterministic")
	}
}

func TestRenderEmptyGraph(t *testing.T) {
	b, err := Render(&discovery.Graph{}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Networks.Network) != 1 {
		t.Fatalf("want exactly one network, got %d", len(doc.Networks.Network))
	}
	if doc.Networks.Network[0].NetworkID != "network-topology-exporter" {
		t.Errorf("empty NetworkID should default; got %q", doc.Networks.Network[0].NetworkID)
	}
}

func TestRenderSelfLoopUnique(t *testing.T) {
	g := &discovery.Graph{
		Devices: []discovery.Device{{ID: "a"}},
		Edges: []discovery.Edge{
			{SrcDevice: "a", SrcPort: "p1", DstDevice: "a", DstPort: "p1",
				DiscoveryProto: discovery.DiscoveryProtocolLLDP, Direction: discovery.DirectionBidirectional},
		},
	}
	b, err := Render(g, Config{NetworkID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	_ = json.Unmarshal(b, &doc)
	ids := map[string]bool{}
	for _, l := range doc.Networks.Network[0].Link {
		if ids[l.LinkID] {
			t.Errorf("self-loop produced colliding link-id %q", l.LinkID)
		}
		ids[l.LinkID] = true
	}
}

func TestRenderUnicodeID(t *testing.T) {
	g := &discovery.Graph{
		Devices: []discovery.Device{{ID: "switch-Ωμέγα"}},
		Edges:   []discovery.Edge{bidiEdge("switch-Ωμέγα", "Gi0/1", "peer", "Gi0/2", discovery.DiscoveryProtocolLLDP)},
	}
	b, err := Render(g, Config{NetworkID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Error("unicode device id produced invalid JSON")
	}
}
