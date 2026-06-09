# Hub Atomic Spoke-Commit + Size-Before-Generation — Design Spec

**Issue:** #147
**Status:** design, adversarially reviewed (2 reviewers) + revised; pending third-party spec review.
**Scope:** the **push path only** (`handlePush` + the publish primitive). Eviction reconciliation and hub.go file decomposition are explicitly out of scope (tracked by #147 push-only decision and #151 respectively).

---

## Goal

Make a spoke's registration in `h.spokes` **commit atomically with winning publication**, and stop an oversize graph from **consuming a generation slot**. Both are latent-correctness defects in `internal/federation/hub.go`.

## The two defects (today)

1. **Speculative write + rollback (`handlePush`, hub.go:571-655).** Under `h.mu` the handler writes `h.spokes[id] = entry` at :596 *before* the graph is validated, snapshots, releases the lock, builds the combined graph, and calls `tryPublishMetrics`. On rejection it re-acquires `h.mu` and rolls the entry back (:633-639). Between the speculative write and the rollback, a concurrent push for a *different* spoke can observe the unvalidated entry via `spokesSnapshot()` and fold it into a graph that publishes. The comment at :565-570 claims this is deferred; the code does not actually defer it.
2. **Generation burned on size-reject (`tryPublishMetrics`, hub.go:1019-1043).** The CAS advances `lastPublishedGen` at :1025 *before* the size-budget check at :1028. A rejected oversize graph still consumes the generation, so a concurrent valid lower-generation graph is then dropped as "stale" with nothing published — and the spoke that built it receives a spurious `409`.

## Non-goals

- Reconciling `evictSilentSpokes`' eager-delete-then-publish ordering (benign, self-healing; out of scope per the #147 push-only decision).
- Splitting hub.go (#151).
- Changing the wire contract beyond the documented 409→204 correction below.

---

## Architecture

One publish primitive owns the generation gate, the size gate, and the state commit, and runs **entirely under `h.mu`**. The expensive `buildCombinedGraph` (the LD-17 Reconcile pass) stays **outside** the lock. Only the cheap publish bundle (a few map/gauge writes + an atomic pointer swap) holds `mu`.

### New type

```go
// acceptedPush carries the spoke state to commit atomically with a winning
// publication. entry.lastSeen is the accept time used for the liveness gauge.
// A nil *acceptedPush (e.g. from eviction) publishes the graph without
// registering any spoke or touching liveness metrics.
type acceptedPush struct {
	id    string
	entry spokeEntry
}
```

### New publish primitive (replaces `tryPublishMetrics`)

```go
// publishIfWinner runs ENTIRELY under h.mu. Order is load-bearing:
//   1. stale-generation check  → reject; generation untouched
//   2. size-budget check       → reject; generation UNTOUCHED  (fixes defect #2)
//   3. win: advance generation, and if accepted != nil commit the spoke entry,
//      its liveness gauges, and first-live/GraphStale — atomically with
//      Topology.Update (fixes defect #1, including the eviction-race reverse).
//
// Removing the old CAS loop is safe: publishGen.Add(1) is performed under h.mu
// at the single call site, so two callers can never hold equal generations and
// the generation is strictly monotonic.
func (h *Hub) publishIfWinner(gen uint64, g discovery.Graph, unmatched int, accepted *acceptedPush) (bool, metrics.RejectReason) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if gen <= h.lastPublishedGen {
		return false, rejectReasonStaleGeneration
	}

	maxEdges := h.cfg.Hub.MaxGraphEdges
	maxDevices := h.cfg.Hub.MaxGraphDevices
	if (maxEdges > 0 && len(g.Edges) > maxEdges) || (maxDevices > 0 && len(g.Devices) > maxDevices) {
		h.logger.Warn("graph update rejected: exceeds size budget",
			"edges", len(g.Edges), "max_edges", maxEdges,
			"devices", len(g.Devices), "max_devices", maxDevices)
		h.m.GraphUpdatesRejectedTotal.WithLabelValues(string(rejectReasonSizeBudgetExceeded)).Inc()
		return false, rejectReasonSizeBudgetExceeded // generation NOT advanced
	}

	h.lastPublishedGen = gen // plain uint64, guarded by h.mu

	if accepted != nil {
		h.spokes[accepted.id] = accepted.entry
		h.m.FederationSpokeUp.WithLabelValues(accepted.id).Set(1)
		h.m.FederationSpokeLastPushUnix.WithLabelValues(accepted.id).Set(float64(accepted.entry.lastSeen.Unix()))
		if !h.firstLive.Load() {
			h.m.GraphStale.Set(0)
			h.firstLive.Store(true)
		}
	}

	h.m.HubOOSUnmatchedTotal.Set(float64(unmatched))
	h.m.Topology.Update(g)
	return true, ""
}
```

**Why the liveness writes move inside the lock (critical, from adversarial review).** If `FederationSpokeUp`/`LastPushUnix`/`firstLive` are set *after* `publishIfWinner` returns (as today, hub.go:611-614), a concurrent `evictSilentSpokes` can run between the locked commit and the outside-the-lock gauge set: it deletes the just-committed entry (hub.go:943) and its gauge series (:952), then the push's `SpokeUp.Set(1)` resurrects a gauge for a spoke absent from both `h.spokes` and the published graph — the #147 inconsistency, reversed. Folding all liveness writes into the same locked region as the `h.spokes` write closes this.

### Struct field change

`lastPublishedGen` changes from `atomic.Uint64` to a plain `uint64` (guarded by `h.mu`). It is read nowhere outside the publish primitive (verified by grep), so no other access needs updating. `publishGen` stays `atomic.Uint64` (still `.Add(1)` under `mu`; monotonic either way). `firstLive` stays `atomic.Bool` because `IsReady()` (hub.go:1049) reads it without `mu`; it is now `.Store(true)`-d inside the locked region, which is safe.

### `handlePush` flow (rewritten)

```go
// ... transport decode, validation, freshness checks unchanged ...

h.mu.Lock()
prevEntry, hadPrev := h.spokes[payload.SpokeID] // for min-push-interval only
if hadPrev && h.cfg.Hub.MinPushInterval > 0 {
	// ... existing rate-limit check, unchanged; unlock + 429 on reject ...
}
entry := spokeEntry{payload: payload, lastSeen: now}
candidate := h.spokesSnapshot() // copy of h.spokes WITHOUT the new entry
candidate[payload.SpokeID] = entry // added to the COPY only
gen := h.publishGen.Add(1)
h.mu.Unlock()
// h.spokes is NOT mutated here.

combined, unmatchedCount := h.buildCombinedGraph(candidate) // outside the lock

published, rejectReason := h.publishIfWinner(gen, combined, unmatchedCount, &acceptedPush{id: payload.SpokeID, entry: entry})
if published {
	h.writeSnapshotAsync(combined)
	h.logger.Info("hub: spoke push accepted", /* ... */)
	w.WriteHeader(http.StatusNoContent)
	return
}

// Rejected: nothing was committed, so there is nothing to roll back.
h.logger.Warn("hub: spoke push rejected — combined graph not applied", /* ... reject_reason ... */)
writePushRejection(w, statusForRejectReason(rejectReason), rejectReason, map[string]any{ /* ... */ })
```

`prevEntry`/`hadPrev` are retained only for the min-push-interval check. The rollback branch (current :627-639) and the misleading comment (:565-570) are deleted. Gauge/`firstLive` writes are no longer here — they live in `publishIfWinner`.

### Unchanged call sites

- **`evictSilentSpokes` (hub.go:937-973):** still deletes eagerly under `mu`, then `publishIfWinner(gen, combined, unmatched, nil)`. `accepted=nil` → publishes the graph, registers no spoke, touches no liveness metric (matches today's `tryPublishMetrics(..., clearStale=false)`).
- **`RestoreGraph` (hub.go:126-130):** **must NOT** be routed through `publishIfWinner`. It continues to call `publishMetrics(g, false)` directly so it does not consume generation 1 (restore is pre-first-push). Confirmed in-scope-to-preserve.
- **`combinedGraphLocked` (hub.go:714):** test helper, unchanged.

---

## Behavior: corrected, not merely preserved

This is a **behavior-correcting** change, not behavior-preserving. One observable difference:

- **409 → 204 (intended correction).** Today a valid push can receive `409 stale_generation` because a *non-winning oversize push burned the generation* — contrary to `docs/operator/federation.md`, which documents 409 as "a concurrent **newer** push advanced the generation." After this change the generation is only advanced by a graph that actually publishes, so that push correctly returns `204`. **Action:** update `docs/operator/federation.md` (the 409 description) and `CHANGELOG.md` to record the fix.

Genuinely preserved (verified against current code):

- **Final `h.spokes` state on a rejected push is identical** to today's rollback, in both branches: `hadPrev=true` → entry unchanged (today restores `prevEntry`; new code never overwrote it); `hadPrev=false` → entry absent (today deletes; new code never wrote it).
- **`lastSeen` semantics unchanged** — only an accepted push updates `lastSeen`, so the min-push-interval baseline remains "last *accepted* push," exactly as the rollback produced today.
- **HTTP status mapping (413/409/400/429) unchanged**; the spoke's retry policy (all 4xx-except-429 are fatal-this-cycle) is unaffected.

Minor, acknowledged deltas:

- **Publish locking profile (F5).** `Topology.Update` now runs under `h.mu` rather than lock-free. Verified safe: `Topology.Update` is a single `atomic.Pointer.Store`, gauge `Set`s use client_golang's own mutex, and the Prometheus scrape path reads only `snap.Load()` — none re-enter `h.mu`, so there is **no deadlock**, only bounded contention on a cheap critical section.
- **Rate-limit edge (F7).** Two near-simultaneous same-spoke pushes no longer see each other's *in-flight* `lastSeen` (because there is no speculative write), so a second push that today might `429` may now proceed to generation arbitration. Guarded by mTLS-CN binding and unique-generation arbitration; impact negligible. Acknowledged, not a regression worth preserving.

---

## Testing (all `-race` where concurrent)

New / changed tests in `internal/federation/hub_test.go`:

1. **Defect #2, deterministic, at the primitive (headline regression).** Call `publishIfWinner(gen=5, oversizeGraph, 0, nil)` → expect `(false, size_budget_exceeded)` **and** `h.lastPublishedGen == 0` (unchanged). Then `publishIfWinner(gen=4, validGraph, 0, nil)` → expect `(true, "")` and the graph published. This **fails against today's code** (today gen=5 is committed, so gen=4 is rejected stale) and passes after the fix.
2. **Defect #2, at the HTTP handler.** Drive `handlePush` such that a push whose generation would have been collaterally burned returns **204, not 409** (orchestrated via a seam/barrier or sequenced oversize-then-valid pushes). Asserts the customer-visible manifestation.
3. **Defect #1, deterministic.** A push whose publish is forced to lose (lower generation) or is oversize-rejected leaves `h.spokes` unchanged and `FederationSpokeUp{id}` unset — proving `h.spokes` is never speculatively written. (Extends the existing `TestHubHandlePushRejectedGraphDoesNotMarkSpokeUp` / `...RollsBackPreviousEntry` to the new no-write model.)
4. **Defect #1, real-path concurrency (`-race`).** Fire concurrent real `handlePush` requests (via `httptest`) for different spokes — replacing/augmenting `TestHubConcurrentPushAndEviction`, which currently bypasses `handlePush` by poking `h.spokes` directly. Run with `-race`.
5. **Eviction-race regression (`-race`).** Concurrent `handlePush` accept + `evictSilentSpokes` for the same spoke must never leave `FederationSpokeUp{id}=1` while the spoke is absent from `h.spokes` and the published graph (proves the F1 fold-into-lock fix).
6. **Preservation.** Accepted push still sets both liveness gauges + `firstLive` + returns 204 + writes snapshot; a stale-generation push commits nothing and returns 409.
7. **Migrate the three existing direct call sites** of `tryPublishMetrics` (`hub_test.go` TestHubOOSUnmatchedMetricIncrementsOnMiss, TestTryPublishMetricsRejectsOversizedGraphEdges, TestTryPublishMetricsRejectsOversizedGraphDevices) to `publishIfWinner(..., nil)`. **Keep** the two oversize tests — they pin the reject-increments-counter + generation-untouched semantics — and add the `lastPublishedGen == 0` assertion to them.

All existing federation tests must stay green; `go test ./... -race`, `gofmt`, and `golangci-lint run` clean per the project gate. No Co-Authored-By / AI-attribution trailers.

---

## Files touched

- `internal/federation/hub.go` — new `acceptedPush`; `publishIfWinner` replacing `tryPublishMetrics`; rewritten `handlePush` (no speculative write, no rollback); `lastPublishedGen` → plain `uint64`; eviction call updated to pass `nil`; delete the :565-570 comment.
- `internal/federation/hub_test.go` — tests 1-7 above.
- `docs/operator/federation.md` — correct the 409/stale_generation description.
- `CHANGELOG.md` — record the fix (409→204 correction + atomic commit + no burned generation on oversize).
