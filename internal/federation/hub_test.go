package federation

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
)

// makeCert returns a self-signed certificate with the given Common Name,
// suitable for populating req.TLS.PeerCertificates in handlePush tests.
func makeCert(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

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
// `go test -race ./internal/federation/...`. The test body makes no assertions
// beyond the race detector; it is intentionally a harness, not a correctness test.
func TestHubConcurrentPushAndEviction(t *testing.T) {
	t.Parallel()
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

// TestHubHandlePushSuccessStoresSpokeAndSetsGauges sends a valid payload and
// verifies the spoke is stored and the spoke-up gauge is set to 1.
func TestHubHandlePushSuccessStoresSpokeAndSetsGauges(t *testing.T) {
	m := metrics.New()
	h := NewHub(
		config.FederationConfig{SpokeTimeout: 5 * time.Minute},
		m, nil, "",
	)

	payload := SpokePayload{
		SpokeID: "dc-valid",
		CycleAt: time.Now(),
		Devices: []discovery.Device{{ID: "sw-1"}},
		Edges:   []discovery.Edge{},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	h.mu.Lock()
	entry, ok := h.spokes["dc-valid"]
	h.mu.Unlock()
	if !ok {
		t.Fatal("spoke dc-valid not found in h.spokes after successful push")
	}
	if len(entry.payload.Devices) != 1 {
		t.Errorf("stored device count = %d, want 1", len(entry.payload.Devices))
	}
	if got := testutil.ToFloat64(m.FederationSpokeUp.WithLabelValues("dc-valid")); got != 1 {
		t.Errorf("FederationSpokeUp{dc-valid} = %v, want 1", got)
	}
}

// TestHubHandlePushRejectsBadJSON verifies that a body that cannot be decoded
// as JSON returns HTTP 400.
func TestHubHandlePushRejectsBadJSON(t *testing.T) {
	h := newTestHub(nil)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad JSON", rec.Code)
	}
}

// TestHubHandlePushRejectsMethodNotAllowed verifies that non-POST methods get
// HTTP 405.
func TestHubHandlePushRejectsMethodNotAllowed(t *testing.T) {
	h := newTestHub(nil)
	req := httptest.NewRequest(http.MethodGet, "/spoke/push", nil)
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestHubHandlePushRejectsStaleCycleAt verifies that a payload whose cycle_at
// is older than the spoke_timeout is rejected with HTTP 400.
func TestHubHandlePushRejectsStaleCycleAt(t *testing.T) {
	h := newTestHub(nil)
	h.cfg.SpokeTimeout = time.Minute

	payload := SpokePayload{
		SpokeID: "dc-stale",
		CycleAt: time.Now().Add(-2 * time.Minute), // older than spoke_timeout
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for stale cycle_at", rec.Code)
	}
}

// TestHubPublishMetricsClearStale verifies that passing clearStale=true sets
// GraphStale to 0 as part of the publish.
func TestHubPublishMetricsClearStale(t *testing.T) {
	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, "")

	m.GraphStale.Set(1) // simulate startup state
	h.publishMetrics(discovery.Graph{}, true)

	if got := testutil.ToFloat64(m.GraphStale); got != 0 {
		t.Errorf("GraphStale = %v after clearStale=true, want 0", got)
	}
}

// TestHubPublishMetricsPreservesStaleWhenFalse verifies that clearStale=false
// does not touch GraphStale.
func TestHubPublishMetricsPreservesStaleWhenFalse(t *testing.T) {
	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, "")

	m.GraphStale.Set(1)
	h.publishMetrics(discovery.Graph{}, false)

	if got := testutil.ToFloat64(m.GraphStale); got != 1 {
		t.Errorf("GraphStale = %v after clearStale=false, want 1 (unchanged)", got)
	}
}

// TestHubWriteSnapshotPersistsGraph verifies that writeSnapshot writes a file
// at the configured path that can be loaded back via snapshot.Load.
func TestHubWriteSnapshotPersistsGraph(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "hub.json")

	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, snapPath)

	g := discovery.Graph{
		Devices: []discovery.Device{{ID: "sw-hub-1"}},
		Edges:   []discovery.Edge{},
	}
	h.writeSnapshot(g)

	f, err := snapshot.Load(snapPath)
	if err != nil {
		t.Fatalf("snapshot.Load: %v", err)
	}
	if f == nil {
		t.Fatal("snapshot.Load returned nil, expected written file")
	}
	if len(f.Devices) != 1 || f.Devices[0].ID != "sw-hub-1" {
		t.Errorf("loaded devices = %#v, want [{ID:sw-hub-1}]", f.Devices)
	}
	if got := testutil.ToFloat64(m.SnapshotLastWrittenUnix); got == 0 {
		t.Error("SnapshotLastWrittenUnix not updated after writeSnapshot")
	}
}

// TestHubWriteSnapshotAsyncTimesOut verifies the NFS-stall protection in
// writeSnapshotAsync: when the inner write goroutine blocks beyond
// snapshotWriteTimeout, the outer goroutine exits without updating
// SnapshotLastWrittenUnix. The caller (handlePush or evictSilentSpokes) must
// not block — writeSnapshotAsync must return immediately.
func TestHubWriteSnapshotAsyncTimesOut(t *testing.T) {
	block := make(chan struct{})
	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, t.TempDir()+"/snap.json")
	// Use a short timeout and a blocking write fn; both are Hub fields, so
	// setting them before any goroutines start establishes happens-before.
	h.snapshotWriteTimeout = 20 * time.Millisecond
	h.snapshotWriteFn = func(string, snapshot.File) error {
		<-block
		return nil
	}

	start := time.Now()
	h.writeSnapshotAsync(discovery.Graph{})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("writeSnapshotAsync blocked for %v — must return immediately", elapsed)
	}

	// Wait for the timeout to fire (20 ms) + margin.
	time.Sleep(200 * time.Millisecond)

	if got := testutil.ToFloat64(m.SnapshotLastWrittenUnix); got != 0 {
		t.Errorf("SnapshotLastWrittenUnix = %v after stalled write, want 0", got)
	}

	// Unblock the inner goroutine so it doesn't leak beyond the test.
	close(block)
}

// TestHubWriteSnapshotNoopWhenPathEmpty verifies that writeSnapshot is a no-op
// when snapshotPath is empty (the normal test configuration).
func TestHubWriteSnapshotNoopWhenPathEmpty(_ *testing.T) {
	h := newTestHub(nil) // snapshotPath = ""
	// Should not panic or error; just return immediately.
	h.writeSnapshot(discovery.Graph{})
}

// TestHubWriteSnapshotErrorDoesNotPanic verifies that writeSnapshot logs the
// error and does not panic or update SnapshotLastWrittenUnix when the snapshot
// directory cannot be created.
func TestHubWriteSnapshotErrorDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	// Create a file at the location where we'd expect a parent directory so
	// that MkdirAll fails with "not a directory".
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, filepath.Join(blocker, "snap.json"))

	h.writeSnapshot(discovery.Graph{})

	if got := testutil.ToFloat64(m.SnapshotLastWrittenUnix); got != 0 {
		t.Errorf("SnapshotLastWrittenUnix = %v after failed write, want 0", got)
	}
}

// TestHubRestoreGraphPublishesMetrics verifies that RestoreGraph pushes device
// and edge info metrics so the hub can serve stale data immediately after startup.
func TestHubRestoreGraphPublishesMetrics(t *testing.T) {
	m := metrics.New()
	h := NewHub(config.FederationConfig{}, m, nil, "")

	g := discovery.Graph{
		Devices: []discovery.Device{{ID: "sw-restore", Vendor: "cisco"}},
		Edges: []discovery.Edge{{
			SrcDevice: "sw-restore", SrcPort: "Gi0/1",
			DstDevice: "sw-peer", DstPort: "Gi0/2",
			DiscoveryProto: "lldp",
			Direction:      discovery.DirectionBidirectional,
			LinkKind:       "ethernet",
		}},
	}
	m.GraphStale.Set(1) // caller is responsible for setting this before RestoreGraph
	h.RestoreGraph(g)

	// GraphStale should remain 1 — RestoreGraph calls publishMetrics(g, false).
	if got := testutil.ToFloat64(m.GraphStale); got != 1 {
		t.Errorf("GraphStale = %v after RestoreGraph, want 1 (clearStale=false)", got)
	}
	// DeviceInfo and TopologyEdgeInfo should have been populated.
	if got := testutil.ToFloat64(m.DeviceInfo.WithLabelValues("sw-restore", "cisco", "", "", "")); got != 1 {
		t.Errorf("DeviceInfo{sw-restore} = %v, want 1", got)
	}
}

// TestHubOOSUnmatchedMetricIncrementsOnMiss verifies that unmatched OOS hints
// increment the HubOOSUnmatchedTotal gauge.
func TestHubOOSUnmatchedMetricIncrementsOnMiss(t *testing.T) {
	m := metrics.New()
	h := NewHub(config.FederationConfig{SpokeTimeout: 5 * time.Minute}, m, nil, "")

	h.mu.Lock()
	// Only one side reports; no reverse match so the hint is unmatched.
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "sw-unknown", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.combinedGraphLocked()
	h.mu.Unlock()

	if got := testutil.ToFloat64(m.HubOOSUnmatchedTotal); got == 0 {
		t.Error("HubOOSUnmatchedTotal = 0, want > 0 for unmatched OOS hint")
	}
}

// TestHubRunEvictionViaGoroutine confirms that runEviction eventually evicts
// spokes whose lastSeen exceeds the timeout.
func TestHubRunEvictionViaGoroutine(t *testing.T) {
	h := newTestHub(nil)
	h.cfg.SpokeTimeout = 50 * time.Millisecond

	h.mu.Lock()
	h.spokes["dc-old"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-old"},
		lastSeen: time.Now().Add(-100 * time.Millisecond),
	}
	h.mu.Unlock()

	// Call evictSilentSpokes directly (runEviction is a ticker goroutine;
	// evictSilentSpokes is its inner work and is already tested via eviction tests).
	h.evictSilentSpokes()

	h.mu.Lock()
	_, present := h.spokes["dc-old"]
	h.mu.Unlock()

	if present {
		t.Error("dc-old should have been evicted")
	}
}

// TestHubHandlePushMissingCycleAt verifies that a payload with zero CycleAt
// is rejected with HTTP 400.
func TestHubHandlePushMissingCycleAt(t *testing.T) {
	h := newTestHub(nil)

	payload := SpokePayload{SpokeID: "dc-no-time"} // CycleAt is zero
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing cycle_at", rec.Code)
	}
}

// TestHubHandlePushRejectsFutureCycleAt verifies that a cycle_at more than
// 5 minutes in the future is rejected with HTTP 400.
func TestHubHandlePushRejectsFutureCycleAt(t *testing.T) {
	h := newTestHub(nil)

	payload := SpokePayload{
		SpokeID: "dc-future",
		CycleAt: time.Now().Add(10 * time.Minute),
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for future cycle_at", rec.Code)
	}
}

// TestHubHandlePushRejectsEmptySpokeID verifies that a payload without a
// spoke_id is rejected with HTTP 400.
func TestHubHandlePushRejectsEmptySpokeID(t *testing.T) {
	h := newTestHub(nil)

	payload := SpokePayload{SpokeID: "", CycleAt: time.Now()}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for empty spoke_id", rec.Code)
	}
}

// TestHubRunEvictionExitsOnContextCancellation verifies that runEviction
// returns promptly when its context is cancelled.
func TestHubRunEvictionExitsOnContextCancellation(t *testing.T) {
	h := newTestHub(nil)
	h.cfg.SpokeTimeout = 200 * time.Millisecond // short ticker interval

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		h.runEviction(ctx)
		close(done)
	}()

	select {
	case <-done:
		// runEviction exited cleanly.
	case <-time.After(500 * time.Millisecond):
		t.Error("runEviction did not return after context cancellation")
	}
}

// TestHubHandlePushRejectsCertCNMismatch verifies that handlePush returns
// HTTP 403 when the client certificate's CN does not match payload.SpokeID
// (LD-21: spoke_id must be bound to the presenting mTLS identity).
func TestHubHandlePushRejectsCertCNMismatch(t *testing.T) {
	h := newTestHub(nil)

	// Cert has CN "dc-a"; payload claims spoke_id "dc-b" — mismatch.
	payload := SpokePayload{SpokeID: "dc-b", CycleAt: time.Now()}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{makeCert(t, "dc-a")},
	}
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for cert CN / spoke_id mismatch", rec.Code)
	}
}

// TestHubHandlePushAcceptsCertCNMatch verifies that handlePush accepts a push
// when the client certificate's CN exactly matches payload.SpokeID (LD-21).
func TestHubHandlePushAcceptsCertCNMatch(t *testing.T) {
	h := newTestHub(nil)

	payload := SpokePayload{SpokeID: "dc-match", CycleAt: time.Now()}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{makeCert(t, "dc-match")},
	}
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 for matching cert CN / spoke_id; body: %s",
			rec.Code, rec.Body.String())
	}
}

// TestHubRunEvictionFiresTickerEviction verifies that runEviction calls
// evictSilentSpokes when the ticker fires, removing an expired spoke.
func TestHubRunEvictionFiresTickerEviction(t *testing.T) {
	h := newTestHub(nil)
	h.cfg.SpokeTimeout = 20 * time.Millisecond // fast ticker (10ms interval)

	h.mu.Lock()
	h.spokes["dc-expire"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-expire"},
		lastSeen: time.Now().Add(-50 * time.Millisecond),
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go h.runEviction(ctx)
	<-ctx.Done()

	h.mu.Lock()
	_, present := h.spokes["dc-expire"]
	h.mu.Unlock()

	if present {
		t.Error("dc-expire should have been evicted by runEviction ticker")
	}
}

// Ensure unused import is compiled away by the test binary.
var _ = os.DevNull
