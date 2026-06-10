# Native Hub HA — Design Document (#71, v2.0.0)

**Status:** design only (no implementation — v2.0.0 milestone). Adversarially reviewed (2 reviewers) before writing; their findings are folded in and noted in §11.
**Issue:** #71 (priority/high, area/federation, breaking-change). **Prerequisite:** #6 async spoke push (merged).

## 1. Goal & non-goals

**Goal:** make the federation hub survive the loss of a single instance without manual cutover, replacing the three operator-side workarounds in `docs/operator/federation.md` § "Hub high-availability patterns" with a native, opt-in flow.

**Non-goals:** geo-distribution / multi-cluster HA; making topology data strongly-consistent or real-time (it is eventually-consistent, bounded by the discovery interval); HA on non-Kubernetes orchestrators in v2.0 (the election backend is pluggable so this can come later — see §10).

**Hard constraints (from the issue):**
- Single-hub (`role: hub`, no HA) must keep working unchanged. **HA is opt-in.**
- mTLS spoke→hub auth keeps working, including on followers.
- `/metrics` must be scrapeable on every replica (see §6 for the precise, honest semantics — it is leader-authoritative, not byte-identical).

## 2. Two modes, one binary

| | Single-hub (default) | HA (opt-in: `federation.hub.ha.enabled: true`) |
|---|---|---|
| Replicas | 1 | 2+ |
| Leader election | none | k8s Lease (client-go) |
| Spoke routing | one Service → the one pod | leader-only push Service (readiness-gated) |
| Snapshot | local volume (as today, LD-13) | shared volume **optional** (warm-start only) |
| Behaviour | **byte-identical to today** | leader serves + publishes; followers stand by |

When `ha.enabled: false` the binary makes **no** k8s API calls, starts **no** elector, and behaves exactly as the current `role: hub`. The HA machinery is entirely gated behind the flag.

## 3. Architecture (HA mode)

```
                 spokes (mTLS push, unchanged hub_url → push Service)
                                   │
                    ┌──────────────▼───────────────┐
                    │  push Service (ClusterIP)     │  selects: Ready==leader
                    │  port 9101                    │  → endpoints = leader pod only
                    └──────────────┬───────────────┘
              ┌────────────────────┼────────────────────┐
        ┌─────▼─────┐        ┌─────▼─────┐         ┌──────▼────┐
        │ hub pod A │        │ hub pod B │         │ hub pod C │
        │ LEADER    │        │ follower  │         │ follower  │
        │ Ready=T   │        │ Ready=F*  │         │ Ready=F*  │
        └─────┬─────┘        └───────────┘         └───────────┘
              │ writes snapshot (optional shared vol, warm-start seed)
              │
        ┌─────▼──────────────────────────────────────────────────┐
        │ metrics Service (headless, publishNotReadyAddresses)    │  selects: all hub pods
        │ port 9100  → Prometheus ServiceMonitor scrapes ALL pods │
        └─────────────────────────────────────────────────────────┘
   k8s coordination.k8s.io/Lease  ←  client-go leaderelection (one holder)
```
*Follower readiness for the **push** Service is gated on leadership (see §5); followers stay scrapeable via the separate metrics Service.

### Core decisions
1. **Election:** `client-go/tools/leaderelection` over a `coordination.k8s.io/Lease`, behind a pluggable `LeaderElector` interface (§10 future backends).
2. **Routing:** **leadership-aware readiness + two Services** — NOT pod-label self-patching (rejected; see §11/F5). The leader reports `Ready` on a leadership-aware predicate driving the **leader-only push Service**; a separate **metrics Service** with `publishNotReadyAddresses: true` keeps every pod scrapeable.
3. **State:** **spokes are the source of truth.** No replicated mutable state. A new leader rebuilds the graph from the spokes' next push (≤ one `discovery.interval`, since #6 pushes every cycle). The shared snapshot is an **optional warm-start optimisation**, not a requirement.

## 4. Components

### 4.1 `LeaderElector` interface (pluggable)
```go
type LeaderElector interface {
    // Run blocks, runs the election, and invokes the callbacks. Returns when ctx is cancelled.
    Run(ctx context.Context, cb LeaderCallbacks) error
}
type LeaderCallbacks struct {
    OnStartedLeading func(ctx context.Context) // became leader
    OnStoppedLeading func()                    // lost/yielded leadership — step down NOW
    OnNewLeader      func(identity string)     // informational
}
```
Default impl `k8sLeaseElector` drives `leaderelection.NewLeaderElector` + `le.Run` (not `RunOrDie`, which calls `klog.Fatal` on apiserver loss — we want to surface a returned error and fail closed instead) with a `LeaseLock`. The interface keeps the hub decoupled from k8s and lets a Raft/standalone backend land later (§10).

### 4.2 Leadership state + the accept/publish gate
A single `atomic.Bool isLeader`, flipped by the callbacks. The hub:
- **Push handler:** `handlePush` short-circuits with **`503 Service Unavailable`** at the very top (before `validateSpokePayload`/`publishIfWinner`) when `!isLeader`. 503 is retryable in `spoke.go` (verified: 5xx/429 retried, 4xx fatal) — so a spoke that hits a momentary follower retries onto the leader. The mTLS listener + CN-binding stay active on followers (the handshake completes; the handler returns early — no half-open state).
- **Publish:** the discovery/reconcile→publish path only runs on the leader. (In hub mode the hub does no local discovery; "publish" = applying a winning spoke push via `publishIfWinner`, already leader-gated by the handler.)

### 4.3 Step-down (demoted leader)
`OnStoppedLeading` must, atomically and fast: set `isLeader=false`; **close idle keep-alive push connections / send `Connection: close`** so a spoke pinned to the demoted leader re-resolves to the new one (adversarial F4 — k8s Service changes don't tear existing connections); stop writing the snapshot. It does NOT exit the process (it stays a warm follower, ready to be re-elected).

### 4.4 Optional shared-snapshot warm-start + fencing
If a shared snapshot volume is configured, a freshly-elected leader `Load`s it (existing one-shot `RestoreGraph`) to serve stale-but-valid `/metrics` (`GraphStale=1`) immediately, before the first push rebuilds it. Without a shared volume, the new leader cold-starts (`GraphStale=1`, empty) and is fresh within one interval.

**Fencing (adversarial F1/F2 — client-go leaderelection provides NO fencing):** because a frozen-then-resumed old leader can briefly still believe it's leader, two pods can write the shared snapshot concurrently. Atomic `tmp→fsync→rename` (verified in `snapshot.go`) prevents **corruption**, but not a stale overwrite, and `publishGen` is in-process-only (resets on restart) so it gives **zero cross-pod ordering**. To make single-writer *correct* (not just corruption-free), add a **fence token** to `snapshot.File`: `{holder string, leaseEpoch uint64}` where `leaseEpoch` derives from the Lease's monotonic `leaderTransitions`/acquire epoch (server-assigned, monotonic across pods). A writer reads the current file's `leaseEpoch`; if its own is lower, it **refuses the write**. This makes a resumed stale leader's write detectable and rejected. *(Decision: implement the fence token. If the team prefers minimal change, the fallback is to drop the "single-writer" claim and document "last-writer-wins, corruption-free, self-heals within one interval" — both are acceptable; the doc must not assert single-writer without the token.)*

## 5. Readiness model (two predicates)
Reusing one `IsReady` for both roles is wrong (adversarial F6/MINOR-6): a follower never accepts a push, so `firstLive` is never set → it would be `NotReady` forever.
- **Leader readiness** (gates the **push** Service): `isLeader && firstLive` (has live data).
- **Follower liveness/scrapeability** (gates the **metrics** Service via `publishNotReadyAddresses: true`, so readiness doesn't remove it): a follower is "serving" if it loaded a non-empty snapshot recently (or is cold with `GraphStale=1`).
- `/readyz` returns leader-ready; a new `/healthz`-style liveness stays green on followers so k8s doesn't restart them.

## 6. `/metrics` semantics (honest)
The leader's `/metrics` is **authoritative**. Followers serve either a warm snapshot (lagged by the snapshot write+reload cadence) or cold `GraphStale=1`. So `/metrics` is **leader-authoritative, not byte-identical** across pods (adversarial MINOR-5). Operators scrape all pods via the metrics Service; dashboards should prefer the leader (or treat `GraphStale=1` followers as standby). The doc + dashboards will state this explicitly rather than promise identity the architecture can't give.

## 7. Failover sequence & recovery-time (honest, two numbers)
1. Leader dies → Lease not renewed → expires after `leaseDuration` → a follower's acquire loop CASes the Lease (`+retryPeriod`). **~`leaseDuration`+`retryPeriod`.**
2. New leader flips `isLeader`, becomes `Ready` → EndpointSlice controller + kube-proxy repoint the push Service. **Seconds (1–10s, cluster-dependent) — this term was missing from the first design.**
3. Spokes' pushes reach the new leader. **Caveat (adversarial MAJOR-3):** `spoke.go` retries only 3× (1s,2s backoff) then waits for its **next discovery cycle**. So a push in flight during the flip may be lost and replayed up to one **full interval** later. → **Mitigation: increase spoke push `maxAttempts`/backoff so a push survives the flip window**, and document the two recovery numbers.

**Recovery-to-serving** (warm or `GraphStale=1` `/metrics` available): ~seconds (lease + flip). **Recovery-to-fresh** (live reconciled graph): ≤ flip + one `discovery.interval`. Zero data loss (spokes replay).

## 8. Split-brain & failure modes
- **One leader:** the Lease is a CAS against the API server — ≤1 holder; no quorum, so 2 replicas suffice and an even split cannot deadlock (verified).
- **Dual-leader window:** bounded, transient; harmless for push processing (the demoted leader 503s once it observes loss; the brief pre-observation window can double-accept, but each leader has its own local graph and dedup `(spoke_id, cycle_at)`); harmless for the snapshot given atomic-rename + the §4.4 fence token.
- **API-server outage:** neither pod can renew/acquire → both step down → **zero leaders (fail-closed)**, not split-brain. Spokes get 503 and retry; recovery on apiserver return. (Documented trade: HA election availability is bounded by kube-apiserver availability.)

## 9. Config surface
```yaml
federation:
  hub:
    ha:
      enabled: false                 # opt-in; default = today's single-hub
      lease_name: topology-exporter-hub
      lease_namespace: ""            # defaults to the pod's namespace (downward API)
      lease_duration: 15s
      renew_deadline: 10s            # must be < lease_duration
      retry_period: 2s
    snapshot:
      shared: false                  # true ⇒ warm-start from a shared volume (optional)
```
Validation: `renew_deadline < lease_duration`; `enabled` requires in-cluster k8s config (fail with a clear error, not a panic, if not in k8s).

## 10. Deployment changes (Helm + Kustomize)
- **RBAC:** a namespaced `Role` granting `coordination.k8s.io/leases` `{get,create,update}` + `RoleBinding` to the hub SA. **No pod-patch needed** (readiness-gate, not label-patch). Flip `automountServiceAccountToken: true` **only when `ha.enabled`**.
- **Services:** split into (a) **push Service** (`port 9101`, selector includes a leader-readiness gate), (b) **metrics Service** (headless, `publishNotReadyAddresses: true`, `port 9100`) for the ServiceMonitor. Single-hub keeps one Service.
- **PVC:** shared snapshot is **opt-in**; when used it needs `ReadWriteMany` (NFS/CephFS/EFS/Filestore) — **most default StorageClasses are RWO**, so the chart must guard `replicaCount>1 + RWO + shared snapshot` with a clear failure/notes (adversarial F4). Default HA path uses **no shared volume** (cold-start), sidestepping RWX entirely.
- `replicaCount` ≥ 2 for HA; values toggles for all of the above.

## 11. Dependency decision (client-go)
`client-go/tools/leaderelection` requires the clientset (it needs `coordination/v1` Lease access), pulling `k8s.io/api` + `apimachinery` + transitive deps (~several MB). This dents the project's lean `go.mod`. **Decision:** accept `client-go` for the default elector — reimplementing the renew/acquire/clock-skew state machine by hand is the bug-prone part client-go exists to handle. The `LeaderElector` interface isolates it; a future build-tag could keep the non-HA binary lean if footprint becomes a concern (noted, not done now).

## 12. Adversarial-review provenance (what was caught & resolved)
- **"Single-writer" was false** (no fencing in leaderelection) → §4.4 fence token / honest restatement.
- **Follower live-tail of a lossy snapshot was mis-architected** → followers are cold standby; shared volume optional warm-start only; leader-authoritative `/metrics` (§6).
- **Label-patch routing needed pod-patch RBAC + was fragile** → readiness-gate + two Services (§3.2/§10).
- **Recovery-time bound was optimistic** → two honest numbers + spoke retry tuning (§7).
- **Keep-alive pins spoke to demoted leader** → `Connection: close` on step-down (§4.3).
- **RWX is an operational landmine** → made optional; default HA uses no shared volume (§10).
- **client-go dep cost** → explicit decision (§11).
- **Verified safe:** atomic snapshot write (no corruption), 2-replica lease (no quorum deadlock), apiserver-outage fails closed, mTLS-on-follower has no half-open state, spoke needs no code change (routing-only).

## 13. Testing (acceptance)
1. **Failover mid-cycle:** kill the leader during a push cycle; assert a follower acquires the lease, becomes Ready, and the first successful publish lands within a bounded time (lease + flip + one push). Envtest/kind-based or a faked `LeaderElector`.
2. **No split-brain under partition:** partition a follower; assert never two concurrent Lease holders and the isolated follower 503s pushes (never publishes).
3. Single-hub regression: `ha.enabled:false` behaves exactly as today (no k8s calls, local snapshot).
4. Fence-token: a stale-epoch write is refused.

## 14. Out of scope / future
- Non-k8s election backend (Raft/standalone) via the `LeaderElector` interface.
- Active re-push (hub→spoke "please re-push") to shrink recovery-to-fresh — deferred; it inverts the LD-16 push-only invariant, so passive rebuild is the v2.0 choice.
- Build-tag to strip client-go from non-HA binaries.

## 15. Operator-doc change (acceptance item)
Replace `docs/operator/federation.md` § "Hub high-availability patterns" (the three workarounds) with the native HA flow: enable `ha`, set `replicaCount≥2`, RBAC, the two Services, optional shared snapshot, and the recovery-time expectations from §7.
