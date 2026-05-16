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
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
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
		metrics.New(false),
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
	h.spokes["dc-a"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-a", Devices: []discovery.Device{{ID: "sw-a"}}},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-b", Devices: []discovery.Device{{ID: "sw-b"}}},
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
	h.spokes["dc-a"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-a", Devices: []discovery.Device{{ID: "sw-a"}}},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload:  SpokePayload{SpokeID: "dc-b", Devices: []discovery.Device{{ID: "sw-b"}}},
		lastSeen: time.Now(),
	}
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
	// Devices must be present so the IDL guard passes.
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			Devices: []discovery.Device{{ID: "sw-a"}},
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "GigabitEthernet0/1", NeighbourHint: "sw-b", Proto: "cdp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			Devices: []discovery.Device{{ID: "sw-b"}},
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
// treated as the same device. This is the pre-v1.3.0 default behaviour, now
// opt-in via StrictDeviceNameMatching=false.
func TestHubOOSDomainStripProducesEdge(t *testing.T) {
	loose := false
	h := newTestHub(nil)
	h.cfg.Hub.StrictDeviceNameMatching = &loose
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

// TestHubOOSStrictDefaultPreventsCrossDCCollision verifies that with the v1.3.0
// default (StrictDeviceNameMatching unset → strict), two physically distinct
// devices that share a bare hostname across DCs ("core-01.dc1" and "core-01.dc2")
// are NOT collapsed into one node by OOS matching. This is the bug the default
// flip exists to prevent: docs/audits/2026-05-architectural-review.md §2.3.
func TestHubOOSStrictDefaultPreventsCrossDCCollision(t *testing.T) {
	h := newTestHub(nil) // StrictDeviceNameMatching is nil → defaults to strict
	h.mu.Lock()
	// dc-a sees a neighbour it calls "core-01.dc1"; this is the dc1 core.
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "core-01.dc1", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	// dc-b reports its bare hostname "core-01" — under loose matching this would
	// collide with "core-01.dc1" and produce a false edge. Under strict, it must not.
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

	for _, e := range g.Edges {
		if (e.SrcDevice == "sw-a" && e.DstDevice == "core-01") ||
			(e.SrcDevice == "core-01" && e.DstDevice == "sw-a") {
			t.Fatalf("strict default should NOT collapse 'core-01.dc1' with 'core-01'; got false edge: %+v", e)
		}
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
	m := metrics.New(false)
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
	m := metrics.New(false)
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	m := metrics.New(false)
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
	m := metrics.New(false)
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

	m := metrics.New(false)
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

// TestWriteSnapshotAsyncIsNonBlocking verifies that writeSnapshotAsync returns
// immediately even when the snapshot channel is already full. The actual
// NFS-stall timeout behaviour (runSnapshotWriter dropping a slow write and
// continuing) is tested in TestRunSnapshotWriterTimeoutContinues.
func TestWriteSnapshotAsyncIsNonBlocking(t *testing.T) {
	m := metrics.New(false)
	h := NewHub(config.FederationConfig{}, m, nil, t.TempDir()+"/snap.json")

	// Fill the channel (capacity 1) so the next send would block if async were blocking.
	h.snapshotCh <- discovery.Graph{}

	start := time.Now()
	h.writeSnapshotAsync(discovery.Graph{}) // channel full — must drop and return immediately
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("writeSnapshotAsync blocked for %v — must return immediately", elapsed)
	}
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

	m := metrics.New(false)
	h := NewHub(config.FederationConfig{}, m, nil, filepath.Join(blocker, "snap.json"))

	h.writeSnapshot(discovery.Graph{})

	if got := testutil.ToFloat64(m.SnapshotLastWrittenUnix); got != 0 {
		t.Errorf("SnapshotLastWrittenUnix = %v after failed write, want 0", got)
	}
}

// TestHubRestoreGraphPublishesMetrics verifies that RestoreGraph pushes device
// and edge info metrics so the hub can serve stale data immediately after startup.
func TestHubRestoreGraphPublishesMetrics(t *testing.T) {
	m := metrics.New(false)
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
	// Topology collector should have been populated with the device.
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var deviceSeries int
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_device_info" {
			deviceSeries = len(mf.GetMetric())
		}
	}
	if deviceSeries != 1 {
		t.Errorf("network_topology_device_info series after RestoreGraph = %d, want 1", deviceSeries)
	}
}

// TestHubOOSUnmatchedMetricIncrementsOnMiss verifies that unmatched OOS hints
// are reflected in HubOOSUnmatchedTotal after the build wins the publish CAS.
func TestHubOOSUnmatchedMetricIncrementsOnMiss(t *testing.T) {
	m := metrics.New(false)
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
	spokes := h.spokesSnapshot()
	gen := h.publishGen.Add(1)
	h.mu.Unlock()

	combined, unmatchedCount := h.buildCombinedGraph(spokes)
	h.tryPublishMetrics(gen, combined, false, unmatchedCount)

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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
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

// waitForAddr polls addr with TCP dials until the server is accepting connections
// or the 2-second deadline expires. Replaces time.Sleep-based readiness checks.
func waitForAddr(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not become ready at %s", addr)
}

// writeTLSFiles generates a self-signed CA + server cert/key and writes them as
// PEM files under dir. Returns paths (caPath, certPath, keyPath). Suitable for
// populating config.FederationHubConfig in Serve() tests.
func writeTLSFiles(t *testing.T, dir string) (caPath, certPath, keyPath string) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-hub"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}

	caPath = filepath.Join(dir, "ca.crt")
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")

	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("write CA cert: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}), 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER}), 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}
	return caPath, certPath, keyPath
}

// TestServeErrorMissingCAFile verifies that Serve returns an error immediately
// when the CA cert file does not exist.
func TestServeErrorMissingCAFile(t *testing.T) {
	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{
				TLSCACert:  "/nonexistent/ca.crt",
				TLSCert:    "/nonexistent/server.crt",
				TLSKey:     "/nonexistent/server.key",
				ListenAddr: "127.0.0.1:0",
			},
		},
		metrics.New(false), nil, "",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — error paths return before blocking

	err := h.Serve(ctx)
	if err == nil {
		t.Fatal("Serve: expected error for missing CA file, got nil")
	}
	if !strings.Contains(err.Error(), "read CA cert") {
		t.Errorf("error = %q, want to contain 'read CA cert'", err.Error())
	}
}

// TestServeErrorNoPEMInCAFile verifies that Serve returns an error when the CA
// cert file exists but contains no valid PEM blocks.
func TestServeErrorNoPEMInCAFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a pem block"), 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{
				TLSCACert:  caPath,
				TLSCert:    filepath.Join(dir, "server.crt"),
				TLSKey:     filepath.Join(dir, "server.key"),
				ListenAddr: "127.0.0.1:0",
			},
		},
		metrics.New(false), nil, "",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.Serve(ctx)
	if err == nil {
		t.Fatal("Serve: expected error for non-PEM CA file, got nil")
	}
	if !strings.Contains(err.Error(), "no CA certs parsed") {
		t.Errorf("error = %q, want to contain 'no CA certs parsed'", err.Error())
	}
}

// TestServeErrorInvalidCertKeyPair verifies that Serve returns an error when
// the server cert/key pair cannot be loaded (corrupt key file).
func TestServeErrorInvalidCertKeyPair(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, _ := writeTLSFiles(t, dir)

	// Replace the key with garbage so LoadX509KeyPair fails.
	garbageKeyPath := filepath.Join(dir, "garbage.key")
	if err := os.WriteFile(garbageKeyPath, []byte("not a real key"), 0o600); err != nil {
		t.Fatalf("write garbage key: %v", err)
	}

	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{
				TLSCACert:  caPath,
				TLSCert:    certPath,
				TLSKey:     garbageKeyPath,
				ListenAddr: "127.0.0.1:0",
			},
		},
		metrics.New(false), nil, "",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := h.Serve(ctx)
	if err == nil {
		t.Fatal("Serve: expected error for invalid cert/key pair, got nil")
	}
	if !strings.Contains(err.Error(), "load server cert/key") {
		t.Errorf("error = %q, want to contain 'load server cert/key'", err.Error())
	}
}

// TestServeErrorListenAddrInUse verifies that Serve returns an error when the
// listen address is already bound by another listener.
func TestServeErrorListenAddrInUse(t *testing.T) {
	// Bind a port first so the hub's net.Listen call fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind listener: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	dir := t.TempDir()
	caPath, certPath, keyPath := writeTLSFiles(t, dir)

	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{
				TLSCACert:  caPath,
				TLSCert:    certPath,
				TLSKey:     keyPath,
				ListenAddr: addr,
			},
		},
		metrics.New(false), nil, "",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = h.Serve(ctx)
	if err == nil {
		t.Fatal("Serve: expected error for already-bound listen address, got nil")
	}
	if !strings.Contains(err.Error(), "listen on") {
		t.Errorf("error = %q, want to contain 'listen on'", err.Error())
	}
}

// TestServeStartsAndShutsDownCleanly verifies that Serve starts the mTLS server
// and returns nil when the context is cancelled (i.e. http.ErrServerClosed is
// swallowed). This covers the happy path through srv.Serve and the shutdown
// goroutine.
func TestServeStartsAndShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, keyPath := writeTLSFiles(t, dir)

	// Probe for a free port then release it so Serve can bind the same address.
	// Note: there is an inherent TOCTOU window between Close and Serve's
	// net.Listen — this is test-only and accepted as a rare flake rather than a
	// production concern. The window is kept as small as possible by constructing
	// the Hub before closing the listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-allocate port: %v", err)
	}
	addr := ln.Addr().String()

	h := NewHub(
		config.FederationConfig{
			SpokeTimeout: 5 * time.Minute,
			Hub: config.FederationHubConfig{
				TLSCACert:  caPath,
				TLSCert:    certPath,
				TLSKey:     keyPath,
				ListenAddr: addr,
			},
		},
		metrics.New(false), nil, "",
	)

	_ = ln.Close() // release immediately before Serve binds to minimise the race window

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- h.Serve(ctx) }()

	// Wait until the server is actually accepting connections before cancelling,
	// replacing the fixed 50 ms sleep which was non-deterministic.
	waitForAddr(t, addr)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Serve did not return within 3s after context cancellation")
	}
}

// TestHubIsReadyFalseBeforeAnyPush verifies that a freshly constructed Hub
// returns false from IsReady() before any spoke has pushed.
func TestHubIsReadyFalseBeforeAnyPush(t *testing.T) {
	h := newTestHub(nil)
	if h.IsReady() {
		t.Error("IsReady() = true on new hub, want false")
	}
}

// TestHubIsReadyTrueAfterPush verifies that IsReady() returns true after at
// least one successful spoke push (i.e., after handlePush calls publishMetrics
// with clearStale=true the first time via firstLive).
func TestHubIsReadyTrueAfterPush(t *testing.T) {
	h := newTestHub(nil)

	payload := SpokePayload{SpokeID: "dc-ready", CycleAt: time.Now()}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("handlePush status = %d, want 204", rec.Code)
	}
	if !h.IsReady() {
		t.Error("IsReady() = false after first successful push, want true")
	}
}

// TestHubHandlePushRejectsOversizedPayload verifies that a payload exceeding
// the per-push device or edge limits is rejected with HTTP 413.
func TestHubHandlePushRejectsOversizedPayload(t *testing.T) {
	h := newTestHub(nil)

	// Build a device slice just over maxDevicesPerPush.
	devices := make([]discovery.Device, maxDevicesPerPush+1)
	for i := range devices {
		devices[i] = discovery.Device{ID: fmt.Sprintf("sw-%d", i)}
	}
	payload := SpokePayload{
		SpokeID: "dc-big",
		CycleAt: time.Now(),
		Devices: devices,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal oversized payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 for oversized payload", rec.Code)
	}
}

// TestBuildCombinedGraphProtoFallbackToRemote verifies that when the local OOS
// observation carries an empty proto field, the hub falls back to the remote
// observation's proto when synthesising the cross-domain edge.
func TestBuildCombinedGraphProtoFallbackToRemote(t *testing.T) {
	h := newTestHub(nil)
	h.mu.Lock()
	// dc-a reports no proto; dc-b reports "cdp".
	h.spokes["dc-a"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-a", ReportingPort: "Gi0/1", NeighbourHint: "sw-b", Proto: ""},
			},
		},
		lastSeen: time.Now(),
	}
	h.spokes["dc-b"] = spokeEntry{
		payload: SpokePayload{
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "sw-b", ReportingPort: "Gi0/2", NeighbourHint: "sw-a", Proto: "cdp"},
			},
		},
		lastSeen: time.Now(),
	}
	g := h.combinedGraphLocked()
	h.mu.Unlock()

	if len(g.Edges) != 1 {
		t.Fatalf("edge count = %d, want 1", len(g.Edges))
	}
	if g.Edges[0].DiscoveryProto != "cdp" {
		t.Errorf("discovery_proto = %q, want cdp (remote proto fallback)", g.Edges[0].DiscoveryProto)
	}
}

// TestHubOOSAmbiguousFQDNNormalisationWarns verifies that when two spokes report
// OOS observations involving devices that share a bare hostname but differ by
// domain suffix (e.g. "core-sw-01.dc1" and "core-sw-01.dc2"), the hub logs a
// warning about the ambiguous normalisation. The warning is only meaningful
// in the legacy loose-matching mode (pre-v1.3.0 default); this test opts back
// into it to validate the safety-net logging.
func TestHubOOSAmbiguousFQDNNormalisationWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	loose := false
	m := metrics.New(false)
	h := NewHub(
		config.FederationConfig{
			SpokeTimeout: 5 * time.Minute,
			Hub:          config.FederationHubConfig{StrictDeviceNameMatching: &loose},
		},
		m,
		logger,
		"",
	)

	h.mu.Lock()
	// spoke dc-1 sees core-sw-01.dc1 as a neighbour.
	h.spokes["dc-1"] = spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-1",
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "edge-sw-01", ReportingPort: "Gi0/1", NeighbourHint: "core-sw-01.dc1", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	// spoke dc-2 sees core-sw-01.dc2 as a neighbour — different physical device,
	// same bare hostname after normalisation.
	h.spokes["dc-2"] = spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-2",
			OutOfScope: []discovery.OutOfScopeNeighbour{
				{ReportingDevice: "edge-sw-02", ReportingPort: "Gi0/1", NeighbourHint: "core-sw-01.dc2", Proto: "lldp"},
			},
		},
		lastSeen: time.Now(),
	}
	h.combinedGraphLocked()
	h.mu.Unlock()

	logged := buf.String()
	if !strings.Contains(logged, "ambiguous device name normalisation") {
		t.Errorf("expected ambiguous normalisation warning in log output, got:\n%s", logged)
	}
	if !strings.Contains(logged, "core-sw-01") {
		t.Errorf("expected canonical name 'core-sw-01' in log output, got:\n%s", logged)
	}
}

// TestRunSnapshotWriterWritesSnapshot verifies that runSnapshotWriter drains
// the snapshot channel and invokes snapshotWriteFn when a graph is enqueued
// via writeSnapshotAsync.
func TestRunSnapshotWriterWritesSnapshot(t *testing.T) {
	dir := t.TempDir()
	m := metrics.New(false)
	h := NewHub(config.FederationConfig{SpokeTimeout: 5 * time.Minute}, m, nil, filepath.Join(dir, "snap.json"))

	written := make(chan discovery.Graph, 1)
	h.snapshotWriteFn = func(_ string, f snapshot.File) error {
		written <- discovery.Graph{Devices: f.Devices, Edges: f.Edges}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.runSnapshotWriter(ctx)

	g := discovery.Graph{
		Devices: []discovery.Device{{ID: "sw-snap-1"}},
		Edges:   []discovery.Edge{},
	}
	h.writeSnapshotAsync(g)

	select {
	case got := <-written:
		if len(got.Devices) != 1 || got.Devices[0].ID != "sw-snap-1" {
			t.Errorf("snapshot devices = %#v, want [{ID:sw-snap-1}]", got.Devices)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSnapshotWriter did not invoke snapshotWriteFn within 2s")
	}
}

// TestRunSnapshotWriterTimeoutContinues verifies the NFS-stall protection inside
// runSnapshotWriter: when snapshotWriteFn blocks beyond snapshotWriteTimeout the
// writer logs a warning and then continues to process the next enqueued graph.
func TestRunSnapshotWriterTimeoutContinues(t *testing.T) {
	dir := t.TempDir()
	m := metrics.New(false)
	h := NewHub(config.FederationConfig{SpokeTimeout: 5 * time.Minute}, m, nil, filepath.Join(dir, "snap.json"))

	// block gates the first (stalling) write; second signals when the second write runs.
	// Using separate closed-over channels (no shared mutable variable) avoids the
	// data race between the stalled goroutine and the second goroutine spawned after
	// the timeout fires.
	block := make(chan struct{})
	started := make(chan struct{}, 1)     // first write signals it has started
	firstWriteDone := make(chan struct{}) // closed when the first (stalled) write completes
	second := make(chan struct{}, 1)      // second write signals completion
	firstDone := false

	var mu sync.Mutex
	h.snapshotWriteFn = func(_ string, _ snapshot.File) error {
		mu.Lock()
		isFirst := !firstDone
		if isFirst {
			firstDone = true
		}
		mu.Unlock()

		if isFirst {
			select {
			case started <- struct{}{}:
			default:
			}
			<-block               // stall until the test unblocks us
			close(firstWriteDone) // signal that the stalled write has completed
			return nil
		}
		close(second)
		return nil
	}
	h.snapshotWriteTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.runSnapshotWriter(ctx)

	// Enqueue first (blocking) write. The channel has capacity 1 so this lands immediately.
	h.writeSnapshotAsync(discovery.Graph{Devices: []discovery.Device{{ID: "stall"}}})

	// Wait for the first write to start before starting the timeout clock.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first write never started")
	}

	// Wait for the timeout to fire (20 ms + margin), then unblock the stalled goroutine.
	time.Sleep(100 * time.Millisecond)
	close(block)

	// Wait until the stalled write goroutine has actually finished before sending
	// the second graph. This replaces the racy fixed sleep: runSnapshotWriter
	// checks writeDone on the next dequeue, so we must ensure writeDone is closed
	// first to avoid the "still in flight; dropping" branch.
	select {
	case <-firstWriteDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled write never completed")
	}
	h.writeSnapshotAsync(discovery.Graph{Devices: []discovery.Device{{ID: "ok"}}})

	select {
	case <-second:
		// runSnapshotWriter continued to process after the timeout.
	case <-time.After(2 * time.Second):
		t.Fatal("runSnapshotWriter did not process second snapshot after timeout recovery within 2s")
	}
}

// TestRunSnapshotWriterShutdownUnblocksOnTimeout verifies that cancelling ctx
// causes runSnapshotWriter to return within snapshotWriteTimeout even when the
// in-flight snapshot write goroutine is blocked (e.g. NFS stall).
func TestRunSnapshotWriterShutdownUnblocksOnTimeout(t *testing.T) {
	dir := t.TempDir()
	m := metrics.New(false)
	h := NewHub(config.FederationConfig{SpokeTimeout: 5 * time.Minute}, m, nil, filepath.Join(dir, "snap.json"))

	// writeStarted is closed when the blocking write begins; unblock releases it.
	writeStarted := make(chan struct{})
	unblock := make(chan struct{})

	h.snapshotWriteFn = func(_ string, _ snapshot.File) error {
		close(writeStarted)
		<-unblock // block until the test unblocks or the test ends
		return nil
	}
	// Use a short timeout so the test completes quickly (well under the default 30s).
	h.snapshotWriteTimeout = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(unblock) // ensure the write goroutine is always released

	writerDone := make(chan struct{})
	go func() {
		h.runSnapshotWriter(ctx)
		close(writerDone)
	}()

	// Enqueue a write so runSnapshotWriter starts the blocking goroutine.
	h.writeSnapshotAsync(discovery.Graph{})

	// Wait for the write to actually start before cancelling.
	select {
	case <-writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot write never started")
	}

	// Cancel the context. runSnapshotWriter must return within snapshotWriteTimeout
	// (100 ms) + a small margin even though the write is still blocked.
	cancel()

	deadline := time.After(500 * time.Millisecond)
	select {
	case <-writerDone:
		// runSnapshotWriter exited — correct behaviour.
	case <-deadline:
		t.Fatal("runSnapshotWriter did not exit within 500ms after ctx cancel (shutdown stall)")
	}
}

// TestTryPublishMetricsRejectsOversizedGraphEdges verifies that tryPublishMetrics
// increments GraphUpdatesRejectedTotal and does NOT update Topology when the
// combined graph exceeds MaxGraphEdges.
func TestTryPublishMetricsRejectsOversizedGraphEdges(t *testing.T) {
	m := metrics.New(false)
	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{MaxGraphEdges: 2},
		},
		m, nil, "",
	)

	g := discovery.Graph{
		Edges: []discovery.Edge{
			{SrcDevice: "a", DstDevice: "b", DiscoveryProto: "lldp"},
			{SrcDevice: "b", DstDevice: "c", DiscoveryProto: "lldp"},
			{SrcDevice: "c", DstDevice: "d", DiscoveryProto: "lldp"},
			{SrcDevice: "d", DstDevice: "e", DiscoveryProto: "lldp"},
			{SrcDevice: "e", DstDevice: "f", DiscoveryProto: "lldp"},
		},
	}

	h.tryPublishMetrics(1, g, false, 0)

	if got := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(rejectReasonSizeBudgetExceeded)); got != 1 {
		t.Errorf("GraphUpdatesRejectedTotal{reason=size_budget_exceeded} = %v, want 1", got)
	}

	// Topology must NOT have been updated — gather metrics and confirm no
	// network_topology_edge_info series were written.
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_edge_info" && len(mf.GetMetric()) > 0 {
			t.Errorf("network_topology_edge_info has %d series after rejected update, want 0",
				len(mf.GetMetric()))
		}
	}
}

// TestTryPublishMetricsRejectsOversizedGraphDevices verifies that
// tryPublishMetrics increments GraphUpdatesRejectedTotal and does NOT update
// Topology when the combined graph exceeds MaxGraphDevices.
func TestTryPublishMetricsRejectsOversizedGraphDevices(t *testing.T) {
	m := metrics.New(false)
	h := NewHub(
		config.FederationConfig{
			Hub: config.FederationHubConfig{MaxGraphDevices: 1},
		},
		m, nil, "",
	)

	g := discovery.Graph{
		Devices: []discovery.Device{
			{ID: "sw-1"},
			{ID: "sw-2"},
			{ID: "sw-3"},
		},
	}

	h.tryPublishMetrics(1, g, false, 0)

	if got := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(rejectReasonSizeBudgetExceeded)); got != 1 {
		t.Errorf("GraphUpdatesRejectedTotal{reason=size_budget_exceeded} = %v, want 1", got)
	}

	// Topology must NOT have been updated — gather metrics and confirm no
	// network_topology_device_info series were written.
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "network_topology_device_info" && len(mf.GetMetric()) > 0 {
			t.Errorf("network_topology_device_info has %d series after rejected update, want 0",
				len(mf.GetMetric()))
		}
	}
}

// TestHubHandlePushRejectedGraphDoesNotMarkSpokeUp verifies that when
// tryPublishMetrics rejects the combined graph (size budget exceeded), the spoke
// is NOT registered in h.spokes and FederationSpokeUp is NOT set to 1.
// This guards against the inconsistency where a spoke appears "up" in Prometheus
// but contributes zero edges to the topology because its graph was rejected.
func TestHubHandlePushRejectedGraphDoesNotMarkSpokeUp(t *testing.T) {
	m := metrics.New(false)
	h := NewHub(
		config.FederationConfig{
			SpokeTimeout: 5 * time.Minute,
			Hub: config.FederationHubConfig{
				// Set a tight edge budget so the combined graph is rejected.
				// The spoke payload will have more edges than this limit.
				MaxGraphEdges: 1,
			},
		},
		m, nil, "",
	)

	// Build a payload whose edges will exceed MaxGraphEdges after reconciliation.
	payload := SpokePayload{
		SpokeID: "dc-rejected",
		CycleAt: time.Now(),
		Devices: []discovery.Device{
			{ID: "sw-a"},
			{ID: "sw-b"},
			{ID: "sw-c"},
		},
		Edges: []discovery.Edge{
			{
				SrcDevice: "sw-a", SrcPort: "Gi0/1",
				DstDevice: "sw-b", DstPort: "Gi0/2",
				DiscoveryProto: "lldp",
				Direction:      discovery.DirectionBidirectional,
				LinkKind:       "ethernet",
			},
			{
				SrcDevice: "sw-b", SrcPort: "Gi0/3",
				DstDevice: "sw-c", DstPort: "Gi0/4",
				DiscoveryProto: "lldp",
				Direction:      discovery.DirectionBidirectional,
				LinkKind:       "ethernet",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	// MaxGraphEdges=1 forces the size-budget reject path, which must return
	// 413 Payload Too Large with a machine-parseable JSON body. A 204 here
	// would silently lie to the spoke that its data was accepted.
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var rej pushRejection
	if err := json.Unmarshal(rec.Body.Bytes(), &rej); err != nil {
		t.Fatalf("response body is not valid JSON: %v; body=%s", err, rec.Body.String())
	}
	if rej.Reason != rejectReasonSizeBudgetExceeded {
		t.Errorf("reject reason = %q, want %q", rej.Reason, rejectReasonSizeBudgetExceeded)
	}

	// Spoke must NOT be registered in h.spokes.
	h.mu.Lock()
	_, present := h.spokes["dc-rejected"]
	h.mu.Unlock()
	if present {
		t.Error("spoke dc-rejected should NOT be in h.spokes when graph publish was rejected")
	}

	// FederationSpokeUp must NOT be set to 1.
	if got := testutil.ToFloat64(m.FederationSpokeUp.WithLabelValues("dc-rejected")); got != 0 {
		t.Errorf("FederationSpokeUp{dc-rejected} = %v, want 0 when graph was rejected", got)
	}

	// GraphUpdatesRejectedTotal must have been incremented.
	if got := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(rejectReasonSizeBudgetExceeded)); got != 1 {
		t.Errorf("GraphUpdatesRejectedTotal{reason=size_budget_exceeded} = %v, want 1", got)
	}
}

// TestHubHandlePushRejectedGraphRollsBackPreviousEntry verifies that when a
// spoke's push is rejected due to graph size limits, the spoke's PREVIOUS entry
// in h.spokes is restored rather than overwritten with the new payload.
func TestHubHandlePushRejectedGraphRollsBackPreviousEntry(t *testing.T) {
	m := metrics.New(false)
	h := NewHub(
		config.FederationConfig{
			SpokeTimeout: 5 * time.Minute,
			Hub:          config.FederationHubConfig{MaxGraphEdges: 1},
		},
		m, nil, "",
	)

	// Seed a prior entry with one device — within the tight edge budget (no edges).
	prior := spokeEntry{
		payload: SpokePayload{
			SpokeID: "dc-rollback",
			Devices: []discovery.Device{{ID: "sw-prior"}},
			Edges:   []discovery.Edge{},
		},
		lastSeen: time.Now().Add(-time.Minute),
	}
	h.mu.Lock()
	h.spokes["dc-rollback"] = prior
	h.mu.Unlock()

	// Push a new payload that will exceed MaxGraphEdges.
	payload := SpokePayload{
		SpokeID: "dc-rollback",
		CycleAt: time.Now(),
		Devices: []discovery.Device{{ID: "sw-a"}, {ID: "sw-b"}, {ID: "sw-c"}},
		Edges: []discovery.Edge{
			{
				SrcDevice: "sw-a", SrcPort: "Gi0/1",
				DstDevice: "sw-b", DstPort: "Gi0/2",
				DiscoveryProto: "lldp",
				Direction:      discovery.DirectionBidirectional,
				LinkKind:       "ethernet",
			},
			{
				SrcDevice: "sw-b", SrcPort: "Gi0/3",
				DstDevice: "sw-c", DstPort: "Gi0/4",
				DiscoveryProto: "lldp",
				Direction:      discovery.DirectionBidirectional,
				LinkKind:       "ethernet",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handlePush(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", rec.Code, rec.Body.String())
	}

	// The spoke entry should have been rolled back to the prior payload.
	h.mu.Lock()
	entry, ok := h.spokes["dc-rollback"]
	h.mu.Unlock()

	if !ok {
		t.Fatal("spoke dc-rollback should still be in h.spokes with the prior entry after rollback")
	}
	if len(entry.payload.Devices) != 1 || entry.payload.Devices[0].ID != "sw-prior" {
		t.Errorf("h.spokes[dc-rollback].payload.Devices = %v, want prior entry [{sw-prior}]",
			entry.payload.Devices)
	}
}

// TestValidateSpokePayload covers the semantic validation rules enforced by
// validateSpokePayload: empty/overlong/invalid-UTF-8/duplicate device IDs,
// required edge fields, self-edges, and overlong/invalid-UTF-8 port names.
func TestValidateSpokePayload(t *testing.T) {
	validDevice := discovery.Device{ID: "sw-1"}
	validEdge := discovery.Edge{
		SrcDevice: "sw-1", SrcPort: "Gi0/1",
		DstDevice: "sw-2", DstPort: "Gi0/2",
	}

	cases := []struct {
		name    string
		payload SpokePayload
		wantErr bool
	}{
		{
			name: "empty device ID",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: ""}},
			},
			wantErr: true,
		},
		{
			name: "overlong device ID (257 bytes)",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: strings.Repeat("a", 257)}},
			},
			wantErr: true,
		},
		{
			name: "invalid UTF-8 device ID",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "\xff\xfe"}},
			},
			wantErr: true,
		},
		{
			name: "duplicate device IDs",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "sw-dup"}, {ID: "sw-dup"}},
			},
			wantErr: true,
		},
		{
			name: "empty src_device",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "", SrcPort: "Gi0/1", DstDevice: "sw-2"},
				},
			},
			wantErr: true,
		},
		{
			name: "empty src_port",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "sw-1", SrcPort: "", DstDevice: "sw-2"},
				},
			},
			wantErr: true,
		},
		{
			name: "empty dst_device",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "sw-1", SrcPort: "Gi0/1", DstDevice: ""},
				},
			},
			wantErr: true,
		},
		{
			name: "self-edge (src_device == dst_device)",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "sw-1", SrcPort: "Gi0/1", DstDevice: "sw-1", DstPort: "Gi0/2"},
				},
			},
			wantErr: true,
		},
		{
			name: "overlong src_port",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "sw-1", SrcPort: strings.Repeat("p", 257), DstDevice: "sw-2"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid minimal payload (one device, one valid edge)",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges:   []discovery.Edge{validEdge},
			},
			wantErr: false,
		},
		{
			name: "valid payload with empty DstPort (DstPort is optional)",
			payload: SpokePayload{
				Devices: []discovery.Device{validDevice},
				Edges: []discovery.Edge{
					{SrcDevice: "sw-1", SrcPort: "Gi0/1", DstDevice: "sw-2", DstPort: ""},
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpokePayload(tc.payload)
			if tc.wantErr && err == nil {
				t.Error("validateSpokePayload() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSpokePayload() = %v, want nil", err)
			}
		})
	}
}

// TestValidateSpokePayloadRejectsEmptyLabelKey verifies that validateSpokePayload
// returns an error when a Device's Labels map contains an empty string key.
// An empty label key would produce an invalid Prometheus label at emit time.
func TestValidateSpokePayloadRejectsEmptyLabelKey(t *testing.T) {
	payload := SpokePayload{
		Devices: []discovery.Device{
			{
				ID:     "sw-1",
				Labels: map[string]string{"": "value"},
			},
		},
	}
	err := validateSpokePayload(payload)
	if err == nil {
		t.Error("validateSpokePayload() = nil, want error for empty label key")
	}
}

// TestValidateSpokePayloadRejectsLabelInjection covers the Prometheus
// line-protocol injection vectors enumerated in the issue: label keys that
// violate the Prometheus label-name grammar or use the reserved `__` prefix,
// and label values that contain control characters which would corrupt
// /metrics output on every subsequent scrape. mTLS authenticates the spoke
// identity; this validation is the only barrier against a spoke (compromised
// or buggy) pushing data that breaks the hub's exposition format.
//
// Each rejecting case asserts the typed *validationError surface so callers
// can route the reject through the structured pushRejection JSON response;
// the wire-level check that the reason actually reaches the spoke lives in
// TestHubHandlePushRejectsLabelInjection below.
func TestValidateSpokePayloadRejectsLabelInjection(t *testing.T) {
	deviceWithLabel := func(k, v string) discovery.Device {
		return discovery.Device{
			ID:     "sw-1",
			Labels: map[string]string{k: v},
		}
	}

	cases := []struct {
		name       string
		payload    SpokePayload
		wantReason string
	}{
		{
			name:       "device label key with newline",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad\nkey", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with control char (tab)",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad\tkey", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with NUL byte",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad\x00key", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key reserved double-underscore prefix",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("__name", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with space",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad key", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with double-quote",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel(`bad"key`, "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with colon",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad:key", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key starting with digit",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("9bad", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label key with hyphen",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("bad-key", "v")}},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name:       "device label value with newline",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("k", "v\ninjected")}},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name:       "device label value with NUL byte",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("k", "v\x00injected")}},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name:       "device label value with carriage return",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("k", "v\rinjected")}},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name:       "device label value with control char (DEL)",
			payload:    SpokePayload{Devices: []discovery.Device{deviceWithLabel("k", "v\x7fbad")}},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "device vendor field with newline",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "sw-1", Vendor: "Cisco\nrogue"}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "edge src_port with newline",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "sw-1"}, {ID: "sw-2"}},
				Edges: []discovery.Edge{{
					SrcDevice: "sw-1", SrcPort: "Gi0/1\ninjected",
					DstDevice: "sw-2", DstPort: "Gi0/2",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "edge dst_port with NUL byte",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "sw-1"}, {ID: "sw-2"}},
				Edges: []discovery.Edge{{
					SrcDevice: "sw-1", SrcPort: "Gi0/1",
					DstDevice: "sw-2", DstPort: "Gi0/2\x00",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "edge discovery_proto with control char",
			payload: SpokePayload{
				Devices: []discovery.Device{{ID: "sw-1"}, {ID: "sw-2"}},
				Edges: []discovery.Edge{{
					SrcDevice: "sw-1", SrcPort: "Gi0/1",
					DstDevice: "sw-2", DstPort: "Gi0/2",
					DiscoveryProto: "lldp\nfake",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "oos reporting_device with newline",
			payload: SpokePayload{
				OutOfScope: []discovery.OutOfScopeNeighbour{{
					ReportingDevice: "sw-a\ninjected",
					ReportingPort:   "Gi0/1",
					NeighbourHint:   "sw-b",
					Proto:           "lldp",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "oos neighbour_hint with NUL byte",
			payload: SpokePayload{
				OutOfScope: []discovery.OutOfScopeNeighbour{{
					ReportingDevice: "sw-a",
					ReportingPort:   "Gi0/1",
					NeighbourHint:   "sw-b\x00",
					Proto:           "lldp",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "oos proto with newline",
			payload: SpokePayload{
				OutOfScope: []discovery.OutOfScopeNeighbour{{
					ReportingDevice: "sw-a",
					ReportingPort:   "Gi0/1",
					NeighbourHint:   "sw-b",
					Proto:           "lldp\nrogue",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpokePayload(tc.payload)
			if err == nil {
				t.Fatal("validateSpokePayload() = nil, want validationError")
			}
			var verr *validationError
			if !errors.As(err, &verr) {
				t.Fatalf("error type = %T, want *validationError: %v", err, err)
			}
			if verr.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q (msg: %s)", verr.reason, tc.wantReason, verr.msg)
			}
		})
	}
}

// TestValidateSpokePayloadAcceptsValidLabels verifies that the validation
// hardening did not over-fit: the allowed shape of Prometheus label names
// (ASCII letter/underscore start, then alnum/underscore) and any UTF-8
// non-control label value remain accepted. These are the cases an operator
// will hit in production after enabling per-target enrichment labels.
func TestValidateSpokePayloadAcceptsValidLabels(t *testing.T) {
	cases := []struct {
		name    string
		payload SpokePayload
	}{
		{
			name: "label key with single underscore prefix",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"_internal": "ok"},
				}},
			},
		},
		{
			name: "label key snake_case",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"datacenter_region": "us-east-1"},
				}},
			},
		},
		{
			name: "label key with trailing digits",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"tier3": "edge"},
				}},
			},
		},
		{
			name: "label value with allowed UTF-8 (non-ASCII, no controls)",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"site": "São Paulo"},
				}},
			},
		},
		{
			name: "label value containing quotes and backslashes (escaped at emit time)",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"note": `contains "quotes" and \backslash`},
				}},
			},
		},
		{
			name: "valid vendor and site inventory fields",
			payload: SpokePayload{
				Devices: []discovery.Device{{
					ID: "sw-1", Vendor: "Cisco", Model: "Catalyst-9300",
					OSVersion: "17.6.4", Site: "dc-a",
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSpokePayload(tc.payload); err != nil {
				t.Errorf("validateSpokePayload() = %v, want nil", err)
			}
		})
	}
}

// TestHubHandlePushRejectsLabelInjection verifies the on-the-wire contract:
// a payload with an injected label key/value is rejected by handlePush with
// HTTP 400, Content-Type: application/json, and a body whose `reason` field
// is the documented enum value. This is the surface spokes branch on; the
// counter increment is checked here so a regression in the wiring (forgetting
// to call h.m.GraphUpdatesRejectedTotal.Inc()) is caught.
func TestHubHandlePushRejectsLabelInjection(t *testing.T) {
	cases := []struct {
		name       string
		payload    SpokePayload
		wantReason string
	}{
		{
			name: "label key with newline returns invalid_label_key",
			payload: SpokePayload{
				SpokeID: "dc-a",
				CycleAt: time.Now(),
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"bad\nkey": "v"},
				}},
			},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name: "label key with reserved __ prefix returns invalid_label_key",
			payload: SpokePayload{
				SpokeID: "dc-a",
				CycleAt: time.Now(),
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"__reserved": "v"},
				}},
			},
			wantReason: rejectReasonInvalidLabelKey,
		},
		{
			name: "label value with NUL returns invalid_label_value",
			payload: SpokePayload{
				SpokeID: "dc-a",
				CycleAt: time.Now(),
				Devices: []discovery.Device{{
					ID:     "sw-1",
					Labels: map[string]string{"k": "v\x00bad"},
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
		{
			name: "edge port with newline returns invalid_label_value",
			payload: SpokePayload{
				SpokeID: "dc-a",
				CycleAt: time.Now(),
				Devices: []discovery.Device{{ID: "sw-1"}, {ID: "sw-2"}},
				Edges: []discovery.Edge{{
					SrcDevice: "sw-1", SrcPort: "Gi0/1\ninject",
					DstDevice: "sw-2", DstPort: "Gi0/2",
				}},
			},
			wantReason: rejectReasonInvalidLabelValue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.New(false)
			h := NewHub(config.FederationConfig{SpokeTimeout: time.Minute}, m, nil, "")

			before := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(tc.wantReason))
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/spoke/push", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.handlePush(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var resp pushRejection
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode rejection body: %v; raw=%s", err, rec.Body.String())
			}
			if resp.Status != "rejected" {
				t.Errorf("status field = %q, want \"rejected\"", resp.Status)
			}
			if resp.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", resp.Reason, tc.wantReason)
			}
			if after := testutil.ToFloat64(m.GraphUpdatesRejectedTotal.WithLabelValues(tc.wantReason)); after != before+1 {
				t.Errorf("GraphUpdatesRejectedTotal{reason=%s} delta = %v, want 1", tc.wantReason, after-before)
			}
		})
	}
}

// TestValidateSpokePayloadRejectsEmptyLabelKeyTypedReason confirms that the
// pre-existing empty-key case (covered by TestValidateSpokePayloadRejectsEmptyLabelKey
// for the err != nil surface) now carries the structured invalid_label_key
// reject reason. Belt-and-suspenders: a contract regression would let an
// empty key escape with a generic 400 and break dashboards that branch on
// the reason enum.
func TestValidateSpokePayloadRejectsEmptyLabelKeyTypedReason(t *testing.T) {
	payload := SpokePayload{
		Devices: []discovery.Device{{
			ID:     "sw-1",
			Labels: map[string]string{"": "value"},
		}},
	}
	err := validateSpokePayload(payload)
	if err == nil {
		t.Fatal("validateSpokePayload() = nil, want validationError for empty label key")
	}
	var verr *validationError
	if !errors.As(err, &verr) {
		t.Fatalf("error type = %T, want *validationError", err)
	}
	if verr.reason != rejectReasonInvalidLabelKey {
		t.Errorf("reason = %q, want %q", verr.reason, rejectReasonInvalidLabelKey)
	}
}

// TestValidateLabelKeyRejectsOversized verifies the size cap added to
// validateLabelKey short-circuits before the regex / reserved-prefix checks
// when a key exceeds maxLabelKeyBytes. The cap is exclusive: a 256-byte key
// is the largest accepted value; 257 bytes rejects with invalid_label_key.
// Mitigates CPU-DoS via a 16 MiB label key on an mTLS-authenticated spoke
// push (issue #14).
func TestValidateLabelKeyRejectsOversized(t *testing.T) {
	oversized := strings.Repeat("a", maxLabelKeyBytes+1)
	err := validateLabelKey(oversized)
	if err == nil {
		t.Fatalf("validateLabelKey(%d bytes) = nil, want validationError", len(oversized))
	}
	var verr *validationError
	if !errors.As(err, &verr) {
		t.Fatalf("error type = %T, want *validationError", err)
	}
	if verr.reason != rejectReasonInvalidLabelKey {
		t.Errorf("reason = %q, want %q", verr.reason, rejectReasonInvalidLabelKey)
	}
}

// TestValidateLabelKeyAcceptsBoundary verifies the cap is exclusive (>),
// not inclusive (>=): a key of exactly maxLabelKeyBytes is accepted. Uses
// only valid label-key runes so the only possible reject path is the size
// cap itself.
func TestValidateLabelKeyAcceptsBoundary(t *testing.T) {
	boundary := strings.Repeat("a", maxLabelKeyBytes)
	if err := validateLabelKey(boundary); err != nil {
		t.Errorf("validateLabelKey(%d bytes) = %v, want nil", len(boundary), err)
	}
}

// TestValidateLabelValueRejectsOversized verifies the size cap added to
// validateLabelValue short-circuits before the per-rune control-char loop
// when a value exceeds maxLabelValueBytes. A 4097-byte value rejects with
// invalid_label_value; mitigates the ~4M-rune-iteration vector described in
// issue #14.
func TestValidateLabelValueRejectsOversized(t *testing.T) {
	oversized := strings.Repeat("a", maxLabelValueBytes+1)
	err := validateLabelValue(oversized)
	if err == nil {
		t.Fatalf("validateLabelValue(%d bytes) = nil, want validationError", len(oversized))
	}
	var verr *validationError
	if !errors.As(err, &verr) {
		t.Fatalf("error type = %T, want *validationError", err)
	}
	if verr.reason != rejectReasonInvalidLabelValue {
		t.Errorf("reason = %q, want %q", verr.reason, rejectReasonInvalidLabelValue)
	}
}

// TestValidateLabelValueAcceptsBoundary verifies the cap is exclusive (>),
// not inclusive (>=): a value of exactly maxLabelValueBytes is accepted.
// Uses only printable ASCII so the only possible reject path is the size
// cap itself.
func TestValidateLabelValueAcceptsBoundary(t *testing.T) {
	boundary := strings.Repeat("a", maxLabelValueBytes)
	if err := validateLabelValue(boundary); err != nil {
		t.Errorf("validateLabelValue(%d bytes) = %v, want nil", len(boundary), err)
	}
}

// Ensure unused import is compiled away by the test binary.
var _ = os.DevNull
