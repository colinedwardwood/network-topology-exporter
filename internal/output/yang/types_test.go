package yang

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentJSONKeys(t *testing.T) {
	doc := Document{Networks: Networks{Network: []Network{{
		NetworkID:    "n1",
		NetworkTypes: NetworkTypes{},
		Node:         []Node{{NodeID: "d1", Vendor: "cisco"}},
		Link: []Link{{
			LinkID:            "d1:p1-d2:p2",
			Source:            Source{SourceNode: "d1", SourceTP: "p1"},
			Destination:       Destination{DestNode: "d2", DestTP: "p2"},
			DiscoveryProtocol: "lldp",
		}},
	}}}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"ietf-network:networks"`,
		`"ietf-l3-unicast-topology:l3-unicast-topology":{}`,
		`"ietf-network-topology:link"`,
		`"ntx-topology:vendor":"cisco"`,
		`"ntx-topology:discovery-protocol":"lldp"`,
		`"source-node":"d1"`,
		`"dest-tp":"p2"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled doc missing %s\n got: %s", want, s)
		}
	}
}
