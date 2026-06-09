package yang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// Every Go wire constant must have a matching enum in ntx-topology.yang, so the
// schema and the renderer can never drift (a new protocol without an enum fails
// CI, not production).
func TestNtxEnumParity(t *testing.T) {
	path := filepath.Join("..", "..", "..", "yang", "ntx-topology@2026-06-09.yang")
	b, err := os.ReadFile(path) // #nosec G304 -- path is a fixed in-repo test fixture, no user input
	if err != nil {
		t.Fatalf("read ntx module: %v", err)
	}
	mod := string(b)
	has := func(v string) bool { return strings.Contains(mod, `enum "`+v+`"`) }

	for _, v := range []discovery.DiscoveryProtocol{
		discovery.DiscoveryProtocolLLDP, discovery.DiscoveryProtocolCDP, discovery.DiscoveryProtocolBGP,
		discovery.DiscoveryProtocolOSPF, discovery.DiscoveryProtocolFDB, discovery.DiscoveryProtocolISIS,
		discovery.DiscoveryProtocolMPLSTE, discovery.DiscoveryProtocolConfigured,
	} {
		if !has(v.String()) {
			t.Errorf("discovery-protocol enum missing %q", v)
		}
	}
	for _, v := range []discovery.LinkKind{
		discovery.LinkKindEthernet, discovery.LinkKindMPLSTE, discovery.LinkKindIP, discovery.LinkKindLogical,
	} {
		if !has(v.String()) {
			t.Errorf("link-kind enum missing %q", v)
		}
	}
	for _, v := range []discovery.Confidence{discovery.ConfidenceHigh, discovery.ConfidenceMedium, discovery.ConfidenceLow} {
		if !has(v.String()) {
			t.Errorf("confidence enum missing %q", v)
		}
	}
	for _, v := range []discovery.Adjacency{discovery.AdjacencyDirect, discovery.AdjacencyIndirect, discovery.AdjacencyUnknown} {
		if !has(v.String()) {
			t.Errorf("adjacency enum missing %q", v)
		}
	}
}
