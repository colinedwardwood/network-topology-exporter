package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
)

type spokeEntry struct {
	payload  SpokePayload
	lastSeen time.Time
}

// Hub aggregates SpokePayload pushes from spoke instances, reconciles the
// combined edge set across all spoke domains, and updates the shared
// Prometheus metrics with the unified topology. Per LD-16, spokes push;
// the hub never polls spokes.
type Hub struct {
	cfg          config.FederationConfig
	mu           sync.Mutex
	publishMu    sync.Mutex // serialises Reset+repopulate to prevent interleaved scrape gaps
	spokes       map[string]spokeEntry
	m            *metrics.Metrics
	logger       *slog.Logger
	snapshotPath string
	firstLive    atomic.Bool // set to true on the first live publishMetrics call
}

// NewHub constructs a Hub ready to accept spoke pushes. snapshotPath enables
// LD-13 persistence; pass "" to disable snapshot writes (e.g., in tests).
func NewHub(cfg config.FederationConfig, m *metrics.Metrics, logger *slog.Logger, snapshotPath string) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		cfg:          cfg,
		spokes:       make(map[string]spokeEntry),
		m:            m,
		logger:       logger,
		snapshotPath: snapshotPath,
	}
}

// RestoreGraph populates hub metrics from a snapshot loaded at startup so the
// hub can serve stale-but-valid metrics (GraphStale=1) until the first live
// spoke push arrives (LD-13). The caller must set m.GraphStale=1 before
// invoking this; the hub clears it after the first successful push.
func (h *Hub) RestoreGraph(g discovery.Graph) {
	h.publishMetrics(g, false)
}

// Serve starts the mTLS federation server and the spoke-eviction goroutine,
// blocking until ctx is cancelled. Per LD-20, connections without a valid
// client certificate signed by the configured CA are rejected at the TLS
// handshake before any payload is read.
func (h *Hub) Serve(ctx context.Context) error {
	caCert, err := os.ReadFile(h.cfg.Hub.TLSCACert)
	if err != nil {
		return fmt.Errorf("hub: read CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return fmt.Errorf("hub: no CA certs parsed from %q", h.cfg.Hub.TLSCACert)
	}
	serverCert, err := tls.LoadX509KeyPair(h.cfg.Hub.TLSCert, h.cfg.Hub.TLSKey)
	if err != nil {
		return fmt.Errorf("hub: load server cert/key: %w", err)
	}
	tlsCfg := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/spoke/push", h.handlePush)

	srv := &http.Server{
		Addr:              h.cfg.Hub.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", h.cfg.Hub.ListenAddr)
	if err != nil {
		return fmt.Errorf("hub: listen on %s: %w", h.cfg.Hub.ListenAddr, err)
	}
	tlsLn := tls.NewListener(ln, tlsCfg)

	go h.runEviction(ctx)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	h.logger.Info("hub federation server listening", "addr", h.cfg.Hub.ListenAddr)
	if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("hub server: %w", err)
	}
	return nil
}

func (h *Hub) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload SpokePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)) // 64 MiB
	if err := dec.Decode(&payload); err != nil {
		h.logger.Warn("hub: malformed spoke payload", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if payload.SpokeID == "" {
		http.Error(w, "spoke_id required", http.StatusBadRequest)
		return
	}
	if len(payload.SpokeID) > 128 {
		http.Error(w, "spoke_id too long (max 128)", http.StatusBadRequest)
		return
	}
	for _, c := range payload.SpokeID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			http.Error(w, "spoke_id contains invalid characters (allowed: a-z A-Z 0-9 - _ .)", http.StatusBadRequest)
			return
		}
	}

	// Validate CycleAt: must be present, not in the future, and not older than
	// spoke_timeout (which would indicate a lost/replayed push).
	now := time.Now()
	if payload.CycleAt.IsZero() {
		http.Error(w, "cycle_at required", http.StatusBadRequest)
		return
	}
	if payload.CycleAt.After(now.Add(5 * time.Minute)) {
		http.Error(w, "cycle_at is too far in the future", http.StatusBadRequest)
		return
	}
	if now.Sub(payload.CycleAt) > h.cfg.SpokeTimeout {
		h.logger.Warn("hub: rejecting stale spoke payload",
			"spoke_id", payload.SpokeID,
			"cycle_at", payload.CycleAt,
			"age", now.Sub(payload.CycleAt),
			"spoke_timeout", h.cfg.SpokeTimeout,
		)
		http.Error(w, "cycle_at too stale", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	h.spokes[payload.SpokeID] = spokeEntry{payload: payload, lastSeen: now}
	combined := h.combinedGraphLocked()
	h.mu.Unlock()

	h.m.FederationSpokeUp.WithLabelValues(payload.SpokeID).Set(1)
	h.m.FederationSpokeLastPushUnix.WithLabelValues(payload.SpokeID).Set(float64(now.Unix()))
	// LD-13: clear GraphStale atomically inside publishMu on the first live push
	// so a concurrent scrape never sees fresh edges alongside GraphStale=1.
	h.publishMetrics(combined, !h.firstLive.Swap(true))
	h.writeSnapshot(combined)

	h.logger.Info("hub: spoke push accepted",
		"spoke_id", payload.SpokeID,
		"devices", len(payload.Devices),
		"edges", len(payload.Edges),
		"cycle_at", payload.CycleAt,
	)
	w.WriteHeader(http.StatusNoContent)
}

// combinedGraphLocked builds the unified discovery.Graph from all active spoke
// payloads and the configured known inter-domain links. It runs a second
// graph.Reconcile pass across the combined edge set so cross-boundary
// bidirectionality is detected at the hub level per LD-17. Caller must hold h.mu.
func (h *Hub) combinedGraphLocked() discovery.Graph {
	var allDevices []discovery.Device
	var allEdges []discovery.Edge

	for _, entry := range h.spokes {
		allDevices = append(allDevices, entry.payload.Devices...)
		for _, e := range entry.payload.Edges {
			allEdges = append(allEdges, e)
			// A bidirectional edge in the spoke's pre-reconciled graph means
			// both sides were observed within that domain. Inject the reverse
			// observation so the hub's Reconcile pass also sees both sides and
			// preserves the bidirectional status.
			if e.Direction == discovery.DirectionBidirectional {
				rev := e
				rev.SrcDevice, rev.DstDevice = e.DstDevice, e.SrcDevice
				rev.SrcPort, rev.DstPort = e.DstPort, e.SrcPort
				allEdges = append(allEdges, rev)
			}
		}
	}

	// Cross-boundary edge detection via OOS name-matching (LD-15/LD-19
	// best-effort path). For each out-of-scope observation from spoke A
	// where NeighbourHint matches another spoke B's ReportingDevice, and B
	// has a matching reverse observation, synthesise a pair of one-sided
	// edges so the hub's Reconcile pass can detect bidirectionality.
	//
	// oosIndex uses a slice value to handle multi-port links (e.g. LAG bonds
	// where the same device pair appears on multiple port combinations).
	type oosKey struct{ device, hint string }
	type oosVal struct {
		reportingPort string
		proto         string
	}
	oosIndex := make(map[oosKey][]oosVal)
	for _, entry := range h.spokes {
		for _, n := range entry.payload.OutOfScope {
			k := oosKey{normalizeDeviceName(n.ReportingDevice), normalizeDeviceName(n.NeighbourHint)}
			oosIndex[k] = append(oosIndex[k], oosVal{n.ReportingPort, n.Proto})
		}
	}

	type pairKey struct{ a, b string }
	type portPairKey struct{ aPort, bPort string }
	visited := make(map[pairKey]map[portPairKey]bool)

	var unmatchedCount int
	for k, locals := range oosIndex {
		remotes, ok := oosIndex[oosKey{k.hint, k.device}]
		if !ok {
			// Log at debug so operators can spot naming inconsistencies (e.g.
			// "Gi0/1" on one side vs "GigabitEthernet0/1" on the other) when
			// troubleshooting missing cross-domain edges.
			h.logger.Debug("hub: OOS neighbour has no reverse observation; possible naming mismatch",
				"device", k.device, "hint", k.hint)
			unmatchedCount++
			continue
		}

		a, b := k.device, k.hint
		if a > b {
			a, b = b, a
		}
		pk := pairKey{a, b}
		if visited[pk] == nil {
			visited[pk] = make(map[portPairKey]bool)
		}

		for _, local := range locals {
			for _, remote := range remotes {
				// Canonical port pair: port of the alphabetically-smaller device first,
				// so (sw-a,sw-b) and (sw-b,sw-a) iterations produce the same key.
				var aPort, bPort string
				if a == k.device {
					aPort, bPort = local.reportingPort, remote.reportingPort
				} else {
					aPort, bPort = remote.reportingPort, local.reportingPort
				}
				ppk := portPairKey{aPort, bPort}
				if visited[pk][ppk] {
					continue
				}
				visited[pk][ppk] = true

				proto := local.proto
				if proto == "" {
					proto = remote.proto
				}
				// Inject both sides so Reconcile sees len(sides) >= 2 → bidirectional.
				allEdges = appendEdgePair(allEdges,
					k.device, local.reportingPort,
					k.hint, remote.reportingPort,
					proto, "ethernet",
					discovery.ConfidenceMedium, 2,
				)
			}
		}
	}

	// Inject LD-19 known inter-domain links as authoritative overrides.
	// Rank 0 beats all protocol-observed edges so configured links always win.
	for _, link := range h.cfg.KnownInterDomainLinks {
		linkKind := link.LinkKind
		if linkKind == "" {
			linkKind = "ethernet"
		}
		allEdges = appendEdgePair(allEdges,
			link.LocalDevice, link.LocalPort,
			link.RemoteDevice, link.RemotePort,
			"configured", linkKind,
			discovery.ConfidenceHigh, 0,
		)
	}

	h.m.HubOOSUnmatchedTotal.Set(float64(unmatchedCount))

	reconciledEdges, _ := graph.Reconcile(allEdges)
	return discovery.Graph{
		Devices: allDevices,
		Edges:   reconciledEdges,
	}
}

// runEviction periodically evicts spokes that have not pushed within
// federation.spoke_timeout, per LD-18. Spoke liveness and link liveness are
// distinct failure modes; this path handles domain-level silence, not
// individual link instability.
func (h *Hub) runEviction(ctx context.Context) {
	ticker := time.NewTicker(h.cfg.SpokeTimeout / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.evictSilentSpokes()
		}
	}
}

func (h *Hub) evictSilentSpokes() {
	now := time.Now()
	h.mu.Lock()
	var evicted []string
	for id, entry := range h.spokes {
		if now.Sub(entry.lastSeen) > h.cfg.SpokeTimeout {
			delete(h.spokes, id)
			evicted = append(evicted, id)
		}
	}
	// Delete gauge series while h.mu is still held so a concurrent handlePush
	// cannot race to re-add the series before we remove it. Deletion is cleaner
	// than Set(0): the series disappears immediately on eviction rather than
	// lingering with a stale 0 value until the next scrape interval.
	for _, id := range evicted {
		h.m.FederationSpokeUp.DeleteLabelValues(id)
		h.m.FederationSpokeLastPushUnix.DeleteLabelValues(id)
	}
	h.mu.Unlock()

	for _, id := range evicted {
		h.logger.Warn("hub: spoke evicted (no push within timeout)",
			"spoke_id", id,
			"timeout", h.cfg.SpokeTimeout,
		)
	}
	if len(evicted) > 0 {
		// Re-acquire h.mu only for the graph build so the Reconcile+sort pass
		// does not block concurrent handlePush calls for its full duration.
		h.mu.Lock()
		combined := h.combinedGraphLocked()
		h.mu.Unlock()
		h.publishMetrics(combined, false)
		h.writeSnapshot(combined)
	}
}

// publishMetrics updates the shared Prometheus gauge sets with the unified
// graph. publishMu serialises concurrent Reset+repopulate sequences so a
// scrape arriving between Reset and re-population from one goroutine cannot
// interleave with another goroutine's Reset, causing label-set corruption.
// When clearStale is true, GraphStale is set to 0 at the end of the critical
// section so a scrape never observes fresh edges alongside GraphStale=1.
func (h *Hub) publishMetrics(g discovery.Graph, clearStale bool) {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()

	h.m.DeviceInfo.Reset()
	h.m.DeviceUptimeSeconds.Reset()
	for _, d := range g.Devices {
		h.m.DeviceInfo.WithLabelValues(d.ID, d.Vendor, d.Model, d.OSVersion, d.Site).Set(1)
		h.m.DeviceUptimeSeconds.WithLabelValues(d.ID).Set(d.Uptime.Seconds())
	}
	h.m.TopologyEdgeInfo.Reset()
	for _, e := range g.Edges {
		h.m.TopologyEdgeInfo.WithLabelValues(
			e.SrcDevice, e.SrcPort,
			e.DstDevice, e.DstPort,
			e.DiscoveryProto,
			e.LinkKind,
			string(e.Direction),
		).Set(1)
	}
	if clearStale {
		h.m.GraphStale.Set(0)
	}
}

// writeSnapshot persists the hub's current graph to disk (LD-13). A no-op
// when snapshotPath is empty. Hub snapshots omit credential cache and
// unconfirmed-age counters; those are spoke-side concerns.
func (h *Hub) writeSnapshot(g discovery.Graph) {
	if h.snapshotPath == "" {
		return
	}
	f := snapshot.File{
		Devices: g.Devices,
		Edges:   g.Edges,
	}
	if err := snapshot.Write(h.snapshotPath, f); err != nil {
		h.logger.Error("hub: snapshot write failed", "error", err)
		return
	}
	h.m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
}

// normalizeDeviceName lowercases s and strips everything from the first dot
// onward, converting FQDNs to bare hostnames so OOS matching is robust across
// heterogeneous deployments ("core-sw-01.corp.internal" → "core-sw-01").
func normalizeDeviceName(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// appendEdgePair appends a forward and reverse discovery.Edge to edges and
// returns the extended slice. Both edges share the same protocol, link kind,
// confidence, and precedence rank; direction is always unidirectional because
// the hub's Reconcile pass promotes to bidirectional when both sides are seen.
func appendEdgePair(edges []discovery.Edge, src, srcPort, dst, dstPort, proto, linkKind string, confidence discovery.Confidence, rank int) []discovery.Edge {
	base := discovery.Edge{
		DiscoveryProto: proto,
		Direction:      discovery.DirectionUnidirectional,
		Confidence:     confidence,
		Adjacency:      discovery.AdjacencyUnknown,
		PrecedenceRank: rank,
		LinkKind:       linkKind,
	}
	fwd := base
	fwd.SrcDevice, fwd.SrcPort = src, srcPort
	fwd.DstDevice, fwd.DstPort = dst, dstPort
	rev := base
	rev.SrcDevice, rev.SrcPort = dst, dstPort
	rev.DstDevice, rev.DstPort = src, srcPort
	return append(edges, fwd, rev)
}
