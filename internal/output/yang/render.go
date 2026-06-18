package yang

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/grafana/network-topology-exporter/internal/discovery"
)

const defaultNetworkID = "network-topology-exporter"

// Config controls rendering.
type Config struct {
	NetworkID string
}

// Render maps a reconciled graph to RFC 8345/8346 YANG-JSON (RFC 7951). Pure
// and deterministic: identical input -> byte-identical output. It guarantees
// referential integrity (every referenced node/tp is declared) and key
// uniqueness (node-id, tp-id within a node, link-id) — yanglint does NOT
// enforce these (require-instance false; pattern-free inet:uri keys).
func Render(g *discovery.Graph, cfg Config) ([]byte, error) {
	nid := cfg.NetworkID
	if nid == "" {
		nid = defaultNetworkID
	}

	type nodeAcc struct {
		dev *discovery.Device
		tps map[string]bool
	}
	nodes := map[string]*nodeAcc{}
	ensure := func(id string) *nodeAcc {
		if id == "" {
			return nil
		}
		n := nodes[id]
		if n == nil {
			n = &nodeAcc{tps: map[string]bool{}}
			nodes[id] = n
		}
		return n
	}
	for i := range g.Devices {
		d := &g.Devices[i]
		if d.ID == "" {
			continue
		}
		ensure(d.ID).dev = d
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		if e.SrcDevice == "" || e.DstDevice == "" {
			continue
		}
		if sp := e.SrcPort; sp != "" {
			ensure(e.SrcDevice).tps[sp] = true
		} else {
			ensure(e.SrcDevice)
		}
		if dp := e.DstPort; dp != "" {
			ensure(e.DstDevice).tps[dp] = true
		} else {
			ensure(e.DstDevice)
		}
	}

	nodeIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)
	outNodes := make([]Node, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		acc := nodes[id]
		n := Node{NodeID: id}
		if acc.dev != nil {
			n.Vendor, n.Model, n.OSVersion, n.Site = acc.dev.Vendor, acc.dev.Model, acc.dev.OSVersion, acc.dev.Site
		}
		tps := make([]string, 0, len(acc.tps))
		for p := range acc.tps {
			tps = append(tps, p)
		}
		sort.Strings(tps)
		for _, p := range tps {
			n.TerminationPoint = append(n.TerminationPoint, TerminationPoint{TPID: p})
		}
		outNodes = append(outNodes, n)
	}

	used := map[string]bool{}
	mkLinkID := func(sn, stp, dn, dtp, proto string) string {
		base := fmt.Sprintf("%s:%s-%s:%s", sn, stp, dn, dtp)
		id := base
		if used[id] {
			id = base + "#" + proto
		}
		for n := 2; used[id]; n++ {
			id = fmt.Sprintf("%s#%s#%d", base, proto, n)
		}
		used[id] = true
		return id
	}
	var outLinks []Link
	addLink := func(sn, stp, dn, dtp string, e *discovery.Edge) {
		outLinks = append(outLinks, Link{
			LinkID:            mkLinkID(sn, stp, dn, dtp, e.DiscoveryProto.String()),
			Source:            Source{SourceNode: sn, SourceTP: stp},
			Destination:       Destination{DestNode: dn, DestTP: dtp},
			DiscoveryProtocol: e.DiscoveryProto.String(),
			LinkKind:          e.LinkKind.String(),
			Confidence:        e.Confidence.String(),
			Adjacency:         e.Adjacency.String(),
		})
	}
	edges := make([]discovery.Edge, len(g.Edges))
	copy(edges, g.Edges)
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		ka := a.SrcDevice + "\x00" + a.SrcPort + "\x00" + a.DstDevice + "\x00" + a.DstPort + "\x00" + a.DiscoveryProto.String()
		kb := b.SrcDevice + "\x00" + b.SrcPort + "\x00" + b.DstDevice + "\x00" + b.DstPort + "\x00" + b.DiscoveryProto.String()
		return ka < kb
	})
	for i := range edges {
		e := &edges[i]
		if e.SrcDevice == "" || e.DstDevice == "" {
			continue
		}
		addLink(e.SrcDevice, e.SrcPort, e.DstDevice, e.DstPort, e)
		if e.Direction == discovery.DirectionBidirectional {
			addLink(e.DstDevice, e.DstPort, e.SrcDevice, e.SrcPort, e)
		}
	}

	doc := Document{Networks: Networks{Network: []Network{{
		NetworkID:    nid,
		NetworkTypes: NetworkTypes{},
		Node:         outNodes,
		Link:         outLinks,
	}}}}
	return json.Marshal(doc)
}
