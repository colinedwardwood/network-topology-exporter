package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/colinedwardwood/network-topology-exporter/internal/config"
	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	"github.com/colinedwardwood/network-topology-exporter/internal/graph"
	"github.com/colinedwardwood/network-topology-exporter/internal/metrics"
	"github.com/colinedwardwood/network-topology-exporter/internal/snapshot"
)

const (
	maxDevicesPerPush = 10_000
	maxEdgesPerPush   = 50_000

	maxDeviceIDBytes = 256
	maxPortNameBytes = 256
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
	cfg                  config.FederationConfig
	mu                   sync.Mutex
	spokes               map[string]spokeEntry
	m                    *metrics.Metrics
	logger               *slog.Logger
	snapshotPath         string
	snapshotCh           chan discovery.Graph
	firstLive            atomic.Bool // set to true on the first live publishMetrics call
	snapshotWriteFn      func(string, snapshot.File) error
	snapshotWriteTimeout time.Duration
	publishGen           atomic.Uint64
	lastPublishedGen     atomic.Uint64
}

// NewHub constructs a Hub ready to accept spoke pushes. snapshotPath enables
// LD-13 persistence; pass "" to disable snapshot writes (e.g., in tests).
func NewHub(cfg config.FederationConfig, m *metrics.Metrics, logger *slog.Logger, snapshotPath string) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hub{
		cfg:                  cfg,
		spokes:               make(map[string]spokeEntry),
		m:                    m,
		logger:               logger,
		snapshotPath:         snapshotPath,
		snapshotWriteFn:      snapshot.Write,
		snapshotWriteTimeout: 30 * time.Second,
	}
	if snapshotPath != "" {
		h.snapshotCh = make(chan discovery.Graph, 1)
	}
	return h
}

// RestoreGraph populates hub metrics from a snapshot loaded at startup so the
// hub can serve stale-but-valid metrics (GraphStale=1) until the first live
// spoke push arrives (LD-13). The caller must set m.GraphStale=1 before
// invoking this; the hub clears it after the first successful push.
func (h *Hub) RestoreGraph(g discovery.Graph) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
		MinVersion:   tls.VersionTLS13,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/spoke/push", h.handlePush)

	srv := &http.Server{
		Addr:              h.cfg.Hub.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	ln, err := net.Listen("tcp", h.cfg.Hub.ListenAddr)
	if err != nil {
		return fmt.Errorf("hub: listen on %s: %w", h.cfg.Hub.ListenAddr, err)
	}
	tlsLn := tls.NewListener(ln, tlsCfg)

	go h.runEviction(ctx)
	if h.snapshotCh != nil {
		go h.runSnapshotWriter(ctx)
	}

	go func() { //nolint:gosec
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

// validateSpokePayload checks semantic invariants that the JSON decoder and size
// guards cannot catch: empty/duplicate/overlong/non-UTF-8 device IDs, required
// edge fields, self-edges, and overlong/non-UTF-8 port names.
func validateSpokePayload(p SpokePayload) error {
	seen := make(map[string]bool, len(p.Devices))
	for i, d := range p.Devices {
		if d.ID == "" {
			return fmt.Errorf("device[%d]: device_id is empty", i)
		}
		if len(d.ID) > maxDeviceIDBytes {
			return fmt.Errorf("device[%d]: device_id exceeds %d bytes", i, maxDeviceIDBytes)
		}
		if !utf8.ValidString(d.ID) {
			return fmt.Errorf("device[%d]: device_id is not valid UTF-8", i)
		}
		if seen[d.ID] {
			return fmt.Errorf("device[%d]: duplicate device_id %q", i, d.ID)
		}
		seen[d.ID] = true
	}
	for i, e := range p.Edges {
		if e.SrcDevice == "" || e.SrcPort == "" || e.DstDevice == "" {
			return fmt.Errorf("edge[%d]: src_device, src_port, and dst_device are required", i)
		}
		if e.SrcDevice == e.DstDevice {
			return fmt.Errorf("edge[%d]: self-edge (src_device == dst_device == %q)", i, e.SrcDevice)
		}
		for _, f := range []struct{ name, val string }{
			{"src_device", e.SrcDevice}, {"src_port", e.SrcPort},
			{"dst_device", e.DstDevice}, {"dst_port", e.DstPort},
		} {
			if len(f.val) > maxPortNameBytes {
				return fmt.Errorf("edge[%d]: %s exceeds %d bytes", i, f.name, maxPortNameBytes)
			}
			if !utf8.ValidString(f.val) {
				return fmt.Errorf("edge[%d]: %s is not valid UTF-8", i, f.name)
			}
		}
	}
	return nil
}

func (h *Hub) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var payload SpokePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)) // 16 MiB
	if err := dec.Decode(&payload); err != nil {
		h.logger.Warn("hub: malformed spoke payload", "error", err)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large (max 16 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(payload.Devices) > maxDevicesPerPush || len(payload.Edges) > maxEdgesPerPush {
		http.Error(w, fmt.Sprintf("payload exceeds limits (max %d devices, %d edges)", maxDevicesPerPush, maxEdgesPerPush), http.StatusRequestEntityTooLarge)
		return
	}
	if err := validateSpokePayload(payload); err != nil {
		h.logger.Warn("hub: spoke payload failed semantic validation",
			"spoke_id", payload.SpokeID, "error", err)
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
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
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			http.Error(w, "spoke_id contains invalid characters (allowed: a-z A-Z 0-9 - _ .)", http.StatusBadRequest)
			return
		}
	}

	// LD-21: bind spoke_id to the presenting mTLS client certificate's Common
	// Name so a spoke holding a valid cert cannot overwrite another spoke's
	// topology data. r.TLS is nil in unit tests (httptest has no TLS); in
	// production ClientAuth: RequireAndVerifyClientCert guarantees at least one
	// peer certificate is present.
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		certCN := r.TLS.PeerCertificates[0].Subject.CommonName
		if certCN != payload.SpokeID {
			h.logger.Warn("hub: spoke_id/cert CN mismatch — rejecting push",
				"spoke_id", payload.SpokeID,
				"cert_cn", certCN,
			)
			http.Error(w, fmt.Sprintf("spoke_id %q does not match client certificate CN %q", payload.SpokeID, certCN), http.StatusForbidden)
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
	spokes := h.spokesSnapshot()
	gen := h.publishGen.Add(1)
	h.m.FederationSpokeUp.WithLabelValues(payload.SpokeID).Set(1)
	h.m.FederationSpokeLastPushUnix.WithLabelValues(payload.SpokeID).Set(float64(now.Unix()))
	h.mu.Unlock()
	combined, unmatchedCount := h.buildCombinedGraph(spokes)

	// LD-13: clear GraphStale atomically inside publishMu on the first live push
	// so a concurrent scrape never sees fresh edges alongside GraphStale=1.
	// Only advance firstLive after tryPublishMetrics confirms the graph was
	// actually published; the size-budget guard can reject the graph and return
	// false, which would otherwise leave firstLive=true with no Topology update.
	wasFirst := !h.firstLive.Load()
	published := h.tryPublishMetrics(gen, combined, wasFirst, unmatchedCount)
	if published && wasFirst {
		h.firstLive.Store(true)
	}
	if published {
		h.writeSnapshotAsync(combined)
	}

	h.logger.Info("hub: spoke push accepted",
		"spoke_id", payload.SpokeID,
		"devices", len(payload.Devices),
		"edges", len(payload.Edges),
		"cycle_at", payload.CycleAt,
	)
	w.WriteHeader(http.StatusNoContent)
}

// spokesSnapshot returns a shallow copy of h.spokes. Caller must hold h.mu.
func (h *Hub) spokesSnapshot() map[string]spokeEntry {
	s := make(map[string]spokeEntry, len(h.spokes))
	for k, v := range h.spokes {
		s[k] = v
	}
	return s
}

// combinedGraphLocked is a convenience wrapper for tests that call it while
// already holding h.mu. Production paths use spokesSnapshot + buildCombinedGraph
// to move Reconcile outside the critical section.
func (h *Hub) combinedGraphLocked() discovery.Graph {
	g, _ := h.buildCombinedGraph(h.spokes)
	return g
}

// buildCombinedGraph constructs the unified discovery.Graph from the supplied
// spoke snapshot and the configured known inter-domain links. It runs a second
// graph.Reconcile pass so cross-boundary bidirectionality is detected at the
// hub level per LD-17. Does NOT require h.mu — call spokesSnapshot() under the
// lock first, then release h.mu before calling this function.
// The second return value is the count of unmatched OOS observations; callers
// should only publish this via HubOOSUnmatchedTotal after confirming the build
// wins the CAS in tryPublishMetrics.
func (h *Hub) buildCombinedGraph(spokes map[string]spokeEntry) (discovery.Graph, int) {
	var allDevices []discovery.Device
	seenDevices := make(map[string]bool)
	var allEdges []discovery.Edge

	spokeKeys := make([]string, 0, len(spokes))
	for k := range spokes {
		spokeKeys = append(spokeKeys, k)
	}
	sort.Strings(spokeKeys)

	for _, sk := range spokeKeys {
		entry := spokes[sk]
		for _, dev := range entry.payload.Devices {
			if !seenDevices[dev.ID] {
				seenDevices[dev.ID] = true
				allDevices = append(allDevices, dev)
			} else {
				h.logger.Warn("hub: duplicate device_id across spokes; using first alphabetically",
					"device_id", dev.ID, "spoke_id", sk)
			}
		}
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

	// Detection pass: build canonical→[]raw maps to warn when distinct raw names
	// collide on the same canonical name (e.g. "core-sw-01.dc1" and
	// "core-sw-01.dc2" both normalise to "core-sw-01"). Logged at most once per
	// canonical name per merge cycle via the warnedNames guard.
	rawNamesForCanonical := make(map[string]map[string]struct{})
	for _, entry := range spokes {
		for _, n := range entry.payload.OutOfScope {
			for _, raw := range []string{n.ReportingDevice, n.NeighbourHint} {
				canon := h.canonicalizeDeviceName(raw)
				if rawNamesForCanonical[canon] == nil {
					rawNamesForCanonical[canon] = make(map[string]struct{})
				}
				rawNamesForCanonical[canon][raw] = struct{}{}
			}
		}
	}
	warnedNames := make(map[string]bool)
	for canon, rawSet := range rawNamesForCanonical {
		if len(rawSet) > 1 {
			if !warnedNames[canon] {
				warnedNames[canon] = true
				rawList := make([]string, 0, len(rawSet))
				for r := range rawSet {
					rawList = append(rawList, r)
				}
				h.logger.Warn("hub: ambiguous device name normalisation — multiple raw names map to the same canonical name; check for FQDN collisions",
					"canonical", canon,
					"raw_names", rawList,
				)
			}
		}
	}

	for _, entry := range spokes {
		for _, n := range entry.payload.OutOfScope {
			k := oosKey{h.canonicalizeDeviceName(n.ReportingDevice), h.canonicalizeDeviceName(n.NeighbourHint)}
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

	reconciledEdges, _ := graph.Reconcile(allEdges)
	return discovery.Graph{
		Devices: allDevices,
		Edges:   reconciledEdges,
	}, unmatchedCount
}

// runEviction periodically evicts spokes that have not pushed within
// federation.spoke_timeout, per LD-18. Spoke liveness and link liveness are
// distinct failure modes; this path handles domain-level silence, not
// individual link instability.
func (h *Hub) runEviction(ctx context.Context) {
	if h.cfg.SpokeTimeout <= 0 {
		return // eviction disabled; avoids time.NewTicker(0) panic in tests
	}
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
		h.mu.Lock()
		spokes := h.spokesSnapshot()
		gen := h.publishGen.Add(1)
		h.mu.Unlock()
		combined, unmatchedCount := h.buildCombinedGraph(spokes)
		if h.tryPublishMetrics(gen, combined, false, unmatchedCount) {
			h.writeSnapshotAsync(combined)
		}
	}
}

// publishMetrics atomically swaps the Topology collector snapshot and, on the
// first live push, clears GraphStale. The atomic pointer swap in
// TopologyCollector.Update means concurrent spoke pushes cannot produce an
// interleaved or empty scrape window.
func (h *Hub) publishMetrics(g discovery.Graph, clearStale bool) {
	h.m.Topology.Update(g)
	if clearStale {
		h.m.GraphStale.Set(0)
	}
}

// tryPublishMetrics publishes g only when gen is strictly greater than the last
// published generation, preventing a slow concurrent goroutine from overwriting
// a newer combined graph with an older snapshot. It uses a CAS loop so that two
// concurrent callers with equal gen do not both publish.
// unmatchedCount is only written to HubOOSUnmatchedTotal when the CAS succeeds,
// so the metric always reflects the winning build rather than a discarded one.
// Returns true only when Topology.Update is actually called (i.e. the graph was
// accepted and published). Returns false when the CAS lost the race or the
// size-budget guard rejected the graph.
func (h *Hub) tryPublishMetrics(gen uint64, g discovery.Graph, clearStale bool, unmatchedCount int) bool {
	for {
		last := h.lastPublishedGen.Load()
		if gen <= last {
			return false
		}
		if h.lastPublishedGen.CompareAndSwap(last, gen) {
			maxEdges := h.cfg.Hub.MaxGraphEdges
			maxDevices := h.cfg.Hub.MaxGraphDevices
			if (maxEdges > 0 && len(g.Edges) > maxEdges) || (maxDevices > 0 && len(g.Devices) > maxDevices) {
				h.logger.Warn("graph update rejected: exceeds size budget",
					"edges", len(g.Edges), "max_edges", maxEdges,
					"devices", len(g.Devices), "max_devices", maxDevices)
				h.m.GraphUpdatesRejectedTotal.Inc()
				return false
			}
			h.m.HubOOSUnmatchedTotal.Set(float64(unmatchedCount))
			h.m.Topology.Update(g)
			if clearStale {
				h.m.GraphStale.Set(0)
			}
			return true
		}
	}
}

// IsReady reports whether the hub has received at least one live spoke push.
// Use this as the readiness signal for Kubernetes readiness probes: the hub
// can serve /metrics from the startup snapshot immediately, but it is only
// "ready" once at least one spoke has confirmed its topology.
func (h *Hub) IsReady() bool {
	return h.firstLive.Load()
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
	if err := h.snapshotWriteFn(h.snapshotPath, f); err != nil {
		h.logger.Error("hub: snapshot write failed", "error", err)
		return
	}
	h.m.SnapshotLastWrittenUnix.Set(float64(time.Now().Unix()))
}

// runSnapshotWriter is the single bounded snapshot writer goroutine for the
// hub. It drains h.snapshotCh one graph at a time, so an NFS stall cannot
// accumulate goroutines across spoke pushes.
func (h *Hub) runSnapshotWriter(ctx context.Context) {
	var writeDone chan struct{} // non-nil while a write goroutine is in flight
	for {
		select {
		case <-ctx.Done():
			return
		case g := <-h.snapshotCh:
			// Collect result from any previously timed-out write that has now finished.
			if writeDone != nil {
				select {
				case <-writeDone:
					writeDone = nil
				default:
					h.logger.Warn("hub: snapshot write still in flight; dropping snapshot (NFS stall?)")
					continue
				}
			}
			writeDone = make(chan struct{}, 1)
			go func(g discovery.Graph, done chan struct{}) {
				h.writeSnapshot(g)
				close(done)
			}(g, writeDone)
			select {
			case <-writeDone:
				writeDone = nil
			case <-time.After(h.snapshotWriteTimeout):
				h.logger.Warn("hub: snapshot write timed out (NFS stall?)", "timeout", h.snapshotWriteTimeout)
				// writeDone goroutine still running; next iteration will detect this.
			case <-ctx.Done():
				if writeDone != nil {
					<-writeDone
				}
				return
			}
		}
	}
}

// writeSnapshotAsync enqueues g for writing by the bounded runSnapshotWriter
// goroutine. If the channel is full (previous write still in flight), the new
// snapshot is dropped rather than spawning an additional goroutine.
func (h *Hub) writeSnapshotAsync(g discovery.Graph) {
	if h.snapshotCh == nil {
		return
	}
	select {
	case h.snapshotCh <- g:
	default:
		h.logger.Warn("hub: snapshot write queue full; dropping (NFS stall?)")
	}
}

// normalizeDeviceName lowercases s and strips the domain suffix (everything after
// the first dot). This allows bare hostnames and FQDNs to match, but will
// incorrectly merge devices that share a hostname across different domains.
// A warning is emitted when this ambiguity is detected at merge time.
func normalizeDeviceName(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// canonicalizeDeviceName returns the canonical form of a device name for OOS
// neighbour matching. When StrictDeviceNameMatching is enabled, only
// case-folding is applied (preserving domain suffixes so "core-sw.dc1" and
// "core-sw.dc2" remain distinct). Otherwise it delegates to normalizeDeviceName
// which strips domain suffixes.
func (h *Hub) canonicalizeDeviceName(s string) string {
	if h.cfg.Hub.StrictDeviceNameMatching {
		return strings.ToLower(s)
	}
	return normalizeDeviceName(s)
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
