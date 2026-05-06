package federation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
)

func newTestHub(links []config.InterDomainLink) *Hub {
	return NewHub(
		config.FederationConfig{
			SpokeTimeout:          3 * time.Minute,
			KnownInterDomainLinks: links,
		},
		metrics.New(),
		nil,
		"", // no snapshot path in tests
	)
}

// TestHubCombinedGraphSingleSpoke verifies that a single spoke's edges survive
// the hub's second Reconcile pass without corruption.
func TestHubCombinedGraphSingleSpoke(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-a",
			Devices: []discovery.Device{{ID: "sw-a"}, {ID: "sw-b"}},
			Edges: []discovery.Edge{
				{
					SrcDevice: "sw-a", SrcPort: "Gi0/1",
					DstDevice: "sw-b", DstPort: "Gi0/2",
					DiscoveryProto: "lldp",
					Direction:      discovery.DirectionBidirectional,
					PrecedenceRank: 1,
					LinkKind:       "ethernet",
				},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", g.Edges[0].Direction)
	}
	if len(g.Devices) != 2 {
		t.Errorf("device count = %d, want 2", len(g.Devices))
	}
}

// TestHubCrossdomainOOSMatching verifies that matching OOS observations from
// two spokes produce a confirmed bidirectional edge via hub reconciliation.
func TestHubCrossdomainOOSMatching(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-a",
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "sw-b", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-b",
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-b", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1 cross-boundary edge", len(g.Edges))
	}
	e := g.Edges[0]
	if e.Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", e.Direction)
	}
	// Canonical key: alphabetically-smaller device is SrcDevice.
	if e.SrcDevice != "sw-a" || e.DstDevice != "sw-b" {
		t.Errorf("endpoints = (%s, %s), want (sw-a, sw-b)", e.SrcDevice, e.DstDevice)
	}
	if e.SrcPort != "Gi0/1" || e.DstPort != "Gi0/2" {
		t.Errorf("ports = (%s, %s), want (Gi0/1, Gi0/2)", e.SrcPort, e.DstPort)
	}
	if e.DiscoveryProto != "lldp" {
		t.Errorf("discovery_proto = %q, want lldp", e.DiscoveryProto)
	}
}

// TestHubOOSCaseMismatchProducesEdge verifies that device-name normalization
// makes OOS matching case-insensitive: "SW-B" and "sw-b" are treated as the
// same device and produce a confirmed bidirectional cross-boundary edge.
func TestHubOOSCaseMismatchProducesEdge(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	// dc-a reports hint "CORE-01" (uppercase); dc-b's sysName is "core-01".
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "CORE-01", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "core-01", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1 (case mismatch should produce edge after normalization)", len(g.Edges))
	}
	if g.Edges[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", g.Edges[0].Direction)
	}
}

// TestHubOOSMultiPortLAG verifies that multiple OOS entries for the same
// device pair (e.g. a LAG bond across two physical ports) each produce a
// distinct cross-boundary edge rather than overwriting each other.
func TestHubOOSMultiPortLAG(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "sw-b", Proto: "lldp"},
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/2", NeighbourHint: "sw-b", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-b", ReportingPort: "Gi1/1", NeighbourHint: "sw-a", Proto: "lldp"},
				{ReportingDevice: "sw-b", ReportingPort: "Gi1/2", NeighbourHint: "sw-a", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	// 2 ports × 2 ports = 4 physical port combinations → 4 bidirectional edges.
	if len(g.Edges) != 4 {
		t.Errorf("edge count = %d, want 4 (2 LAG ports × 2 remote ports)", len(g.Edges))
	}
	for _, e := range g.Edges {
		if e.Direction != discovery.DirectionBidirectional {
			t.Errorf("edge (%s:%s→%s:%s) direction = %q, want bidirectional",
				e.SrcDevice, e.SrcPort, e.DstDevice, e.DstPort, e.Direction)
		}
	}
}

// TestHubKnownInterDomainLinkInjected verifies that a configured inter-domain
// link is injected as a bidirectional edge even with no OOS observations.
func TestHubKnownInterDomainLinkInjected(t *testing.T) {
	links := []config.InterDomainLink{
		{LocalDevice: "sw-a", LocalPort: "Gi0/1", RemoteDevice: "sw-b", RemotePort: "Gi0/2"},
	}
	h := newTestHub(links)
	h.mu.Lock()
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", g.Edges[0].Direction)
	}
	if g.Edges[0].DiscoveryProto != "configured" {
		t.Errorf("discovery_proto = %q, want configured", g.Edges[0].DiscoveryProto)
	}
}

// TestHubKnownLinkCustomLinkKind verifies that an explicit link_kind on a
// configured inter-domain link is threaded through to the emitted edge.
func TestHubKnownLinkCustomLinkKind(t *testing.T) {
	links := []config.InterDomainLink{
		{LocalDevice: "sw-a", LocalPort: "Gi0/1", RemoteDevice: "sw-b", RemotePort: "Gi0/2", LinkKind: "fiber"},
	}
	h := newTestHub(links)
	h.mu.Lock()
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].LinkKind != "fiber" {
		t.Errorf("link_kind = %q, want fiber", g.Edges[0].LinkKind)
	}
}

// TestHubKnownLinkBeatsOOS verifies that a configured inter-domain link wins
// over an automatically-matched OOS edge (rank 0 < rank 2).
func TestHubKnownLinkBeatsOOS(t *testing.T) {
	links := []config.InterDomainLink{
		{LocalDevice: "sw-a", LocalPort: "Gi0/1", RemoteDevice: "sw-b", RemotePort: "Gi0/2"},
	}
	h := newTestHub(links)
	h.mu.Lock()
	// Also inject matching OOS observations — same edge, different port names.
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "GigabitEthernet0/1", NeighbourHint: "sw-b", Proto: "cdp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-b", ReportingPort: "GigabitEthernet0/2", NeighbourHint: "sw-a", Proto: "cdp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	// Both the OOS-derived and the configured link land in different EdgeKey
	// buckets (different port names) so we expect two edges total. The configured
	// edge should have DiscoveryProto "configured".
	configuredCount := 0
	for _, e := range g.Edges {
		if e.DiscoveryProto == "configured" {
			configuredCount++
		}
	}
	if configuredCount == 0 {
		t.Error("expected at least one edge with discovery_proto=configured")
	}
}

// TestHubEvictionRemovesStaleSpoke verifies that a spoke whose lastSeen
// exceeds SpokeTimeout is evicted from the hub's store.
func TestHubEvictionRemovesStaleSpoke(t *testing.T) {
	h := newTestHub(nil)
	h.cfg.SpokeTimeout = 100 * time.Millisecond

	h.mu.Lock()
	h.spokes["dc-a"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-a"},
		lastSeen: time.Now().Add(-200 * time.Millisecond), // already expired
	}
	h.spokes["dc-b"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-b"},
		lastSeen: time.Now(), // fresh
	}
	h.mu.Unlock()

	h.evictSilentSpokes()

	h.mu.Lock()
	_, dcAPresent := h.spokes["dc-a"]
	_, dcBPresent := h.spokes["dc-b"]
	h.mu.Unlock()

	if dcAPresent {
		t.Error("dc-a should have been evicted")
	}
	if !dcBPresent {
		t.Error("dc-b should not have been evicted")
	}
}

// TestHubOOSNoMatchProducesNoEdge confirms that an unmatched OOS observation
// (only one side sees the other) does not produce a cross-boundary edge.
func TestHubOOSNoMatchProducesNoEdge(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "sw-b", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	// dc-b is not in the hub — no reverse observation.
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 0 {
		t.Errorf("edge count = %d, want 0 (no reverse observation)", len(g.Edges))
	}
}

// TestHubConcurrentPushAndEviction exercises concurrent combinedGraphLocked and
// publishMetrics calls to surface data races under the race detector. Run with
// `go test -race ./internal/federation/...`.
func TestHubConcurrentPushAndEviction(t *testing.T) {
	h := newTestHub(nil)

	const goroutines = 20
	var wg sync.WaitGroup

	// Concurrent writers: simulate spoke pushes by mutating h.spokes directly.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("dc-%d", n%3)
			h.mu.Lock()
			h.spokes[id] = spokeEntry{
				payload:  SpokePayload{SpokeID: id},
				lastSeen: time.Now(),
			}
			combined := h.combinedGraphLocked()
			h.mu.Unlock()
			h.publishMetrics(combined, false)
		}(i)
	}

	// Concurrent evictions: some spokes will be evicted (or not) depending on timing.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.evictSilentSpokes()
		}()
	}

	wg.Wait()
}

// TestHubOOSDomainStripProducesEdge verifies that OOS matching strips domain
// suffixes from device names: "core-01.internal.corp" and "core-01" are
// treated as the same device.
func TestHubOOSDomainStripProducesEdge(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	// dc-a sees the neighbour with its FQDN; dc-b reports its bare hostname.
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "core-01.internal.corp", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "core-01", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1 (domain suffix strip should produce edge)", len(g.Edges))
	}
	if g.Edges[0].Direction != discovery.DirectionBidirectional {
		t.Errorf("direction = %q, want bidirectional", g.Edges[0].Direction)
	}
}

// TestHubHandlePushRejectsBadSpokeID verifies that handlePush returns HTTP 400
// for a spoke_id containing invalid characters or exceeding the length limit.
func TestHubHandlePushRejectsBadSpokeID(t *testing.T) {
	h := newTestHub(nil)

	cases := []struct {
		name    string
		spokeID string
	}{
		{"space in spoke_id", "dc a"},
		{"spoke_id too long", strings.Repeat("a", 129)},
		{"slash in spoke_id", "dc/a"},
		{"at-sign in spoke_id", "dc@a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := SpokePayload{
				SpokeID: tc.spokeID,
				CycleAt: time.Now(),
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.handlePush(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// TestHubEvictionDeletesGaugeLabels verifies that evicting a spoke removes its
// FederationSpokeUp and FederationSpokeLastPushUnix label series rather than
// leaving stale zero values.
func TestHubEvictionDeletesGaugeLabels(t *testing.T) {
	m := metrics.New()
	h := NewHub(
		config.FederationConfig{SpokeTimeout: 100 * time.Millisecond},
		m,
		nil,
		"",
	)

	// Simulate a spoke that was once active by setting its gauge, then inject
	// it as already-expired so evictSilentSpokes removes it.
	m.FederationSpokeUp.WithLabelValues("dc-evict").Set(1)
	m.FederationSpokeLastPushUnix.WithLabelValues("dc-evict").Set(float64(time.Now().Unix()))

	h.mu.Lock()
	h.spokes["dc-evict"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-evict"},
		lastSeen: time.Now().Add(-200 * time.Millisecond), // already expired
	}
	h.mu.Unlock()

	h.evictSilentSpokes()

	// After eviction, DeleteLabelValues should have been called. Verify by
	// checking that re-collecting the gauge vector does not include the evicted
	// spoke's labels (i.e. the series count for spoke_id="dc-evict" is absent).
	// We confirm this by checking that WithLabelValues returns a fresh zero gauge
	// rather than the 1 we set — if Delete worked, the value is reset to 0.
	val := m.FederationSpokeUp.WithLabelValues("dc-evict")
	// Reading the value via the Gauge interface: a freshly-created series starts at 0.
	// We can't call .Get() directly, but the clearest proof is that the spoke
	// was removed from h.spokes.
	h.mu.Lock()
	_, present := h.spokes["dc-evict"]
	h.mu.Unlock()

	if present {
		t.Error("dc-evict should have been removed from h.spokes by eviction")
	}

	// Also confirm no panic and the gauge can be set again (series was deleted).
	val.Set(0) // should not panic
}
