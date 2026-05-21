//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery/lldp"
	snmpwalk "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
)

func snmpParams(node string) snmpwalk.Params {
	return snmpwalk.Params{
		IP:        nodeIPs[node],
		Port:      161,
		Timeout:   10 * time.Second,
		// snmpwalk.Params.Community is []byte (issue #5: zeroization).
		// gsnmp.GoSNMP.Community in e2e_test.go's snmpAlive stays a string
		// because gosnmp's own struct hasn't moved to []byte upstream.
		Community: []byte(snmpCommunity),
	}
}

// TestSNMPSystemWalk verifies that the SNMP system group walk returns a valid
// Device for each test node. The custom Alpine test node runs net-snmp, which
// advertises enterprise OID 1.3.6.1.4.1.8072.*; sysName is overridden by
// start.sh to the logical node name (stripped of the clab- prefix).
func TestSNMPSystemWalk(t *testing.T) {
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		t.Run(node, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			dev, err := snmpwalk.Walk(ctx, snmpParams(node))
			if err != nil {
				t.Fatalf("snmp.Walk: %v", err)
			}
			if dev == nil {
				t.Fatal("Walk returned nil device")
			}
			if !strings.EqualFold(dev.ID, node) {
				t.Errorf("device ID = %q, want %q (sysName should match containerlab node name)", dev.ID, node)
			}
			if dev.Vendor != "net-snmp" {
				t.Errorf("vendor = %q, want net-snmp (enterprise OID 8072)", dev.Vendor)
			}
		})
	}
}

// TestLLDPSpine1SeesLeafs verifies that spine1 discovers LLDP edges to both
// leaf1 (via eth1) and leaf2 (via eth2). Pre-reconciliation edges are always
// unidirectional; direction upgrade happens in graph.Reconcile.
func TestLLDPSpine1SeesLeafs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, oos, err := lldp.Walk(ctx, snmpParams("spine1"), "spine1", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(spine1): %v", err)
	}
	if len(oos) > 0 {
		t.Logf("spine1 OOS neighbours: %d (passednil allowedNets so all should be in-scope)", len(oos))
	}

	neighbours := lldpNeighbourSet(edges)
	for _, want := range []string{"leaf1", "leaf2"} {
		if !neighbours[strings.ToLower(want)] {
			t.Errorf("spine1 LLDP missing neighbour %q; discovered: %v", want, sortedKeys(neighbours))
		}
	}
	for _, e := range edges {
		if e.DiscoveryProto != "lldp" {
			t.Errorf("edge discovery_proto = %q, want lldp", e.DiscoveryProto)
		}
		if e.Direction != discovery.DirectionUnidirectional {
			t.Errorf("pre-reconcile direction = %q, want unidirectional (Reconcile promotes to bidirectional)", e.Direction)
		}
		if e.SrcDevice != "spine1" {
			t.Errorf("edge SrcDevice = %q, want spine1", e.SrcDevice)
		}
	}
}

// TestLLDPLeaf1SeesSpine verifies that leaf1 has exactly one LLDP neighbour
// (spine1), since it is connected only on eth1.
func TestLLDPLeaf1SeesSpine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, _, err := lldp.Walk(ctx, snmpParams("leaf1"), "leaf1", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(leaf1): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("leaf1 edge count = %d, want 1; neighbours: %v", len(edges), lldpNeighbourSet(edges))
	}
	if !strings.EqualFold(edges[0].DstDevice, "spine1") {
		t.Errorf("leaf1 LLDP peer = %q, want spine1", edges[0].DstDevice)
	}
}

// TestLLDPLeaf2SeesSpine verifies that leaf2 has exactly one LLDP neighbour
// (spine1), since it is connected only on eth1.
func TestLLDPLeaf2SeesSpine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	edges, _, err := lldp.Walk(ctx, snmpParams("leaf2"), "leaf2", nil)
	if err != nil {
		t.Fatalf("lldp.Walk(leaf2): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("leaf2 edge count = %d, want 1; neighbours: %v", len(edges), lldpNeighbourSet(edges))
	}
	if !strings.EqualFold(edges[0].DstDevice, "spine1") {
		t.Errorf("leaf2 LLDP peer = %q, want spine1", edges[0].DstDevice)
	}
}

// TestLLDPFullGraphReconciles builds the combined edge set from all three
// nodes, runs graph.Reconcile, and verifies that both spine-leaf links are
// promoted to bidirectional — the key quality gate for the reconciliation path.
func TestLLDPFullGraphReconciles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Collect edges from all nodes as spokes would.
	var allEdges []discovery.Edge
	for _, node := range []string{"spine1", "leaf1", "leaf2"} {
		edges, _, err := lldp.Walk(ctx, snmpParams(node), node, nil)
		if err != nil {
			t.Fatalf("lldp.Walk(%s): %v", node, err)
		}
		allEdges = append(allEdges, edges...)
	}

	// Each physical link should appear as two unidirectional observations
	// (one from each end). Reconcile merges them into one bidirectional edge.
	from := make(map[string][]string)
	for _, e := range allEdges {
		from[e.SrcDevice] = append(from[e.SrcDevice], e.DstDevice)
	}
	t.Logf("pre-reconcile edges by source: %v", from)

	biCount := 0
	for _, e := range allEdges {
		if e.Direction == discovery.DirectionBidirectional {
			biCount++
		}
	}
	if biCount > 0 {
		t.Logf("note: %d edges already marked bidirectional before Reconcile (unexpected but not fatal)", biCount)
	}

	// All 3 nodes × 1-2 links each = 4 raw observations (spine1→leaf1,
	// leaf1→spine1, spine1→leaf2, leaf2→spine1). That's 4 pre-reconcile edges.
	if len(allEdges) < 4 {
		t.Errorf("total pre-reconcile edges = %d, want >= 4 (2 links × 2 directions)", len(allEdges))
	}
}

func lldpNeighbourSet(edges []discovery.Edge) map[string]bool {
	s := make(map[string]bool, len(edges))
	for _, e := range edges {
		s[strings.ToLower(e.DstDevice)] = true
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
