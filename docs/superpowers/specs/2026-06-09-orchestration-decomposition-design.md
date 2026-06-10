# Orchestration Decomposition + Walk-Path Dedup — Design Spec (#153)

**Issue:** #153. **Process:** full treatment — brainstorm → 2 adversarial design reviews (done) → spec → plan → subagent-driven TDD with per-task reviews.
**Scope decision (post-adversarial):** **PURE REFACTOR — zero behavior change for BOTH the cycle and `/admin/rediscover`.** The adversarial review established that "closing rediscover's instrumentation gap" is mostly a *regression* (feeding the cycle's per-cycle, source-unlabeled metric series would corrupt `rate(...)` dashboards) or *intentional isolation* (the #72 rate limiter is deliberately skipped so the diagnostic endpoint stays fast; adding it would also lengthen the `CycleMu` hold). So we dedup the duplicated module-walk **structure** via a helper *parameterized on instrumentation* — cycle passes full sinks, rediscover passes its current minimal set — making future drift structurally visible while preserving each caller's exact behavior. **Target 4 (bgp decoder/edge-builder dedup) is dropped** (assessed clarity-reducing, #150-style).

## Load-bearing constraints from the adversarial reviews (the spec exists to honor these)

**C1 — params/credential lifetime boundary (reviewer 1, the #1 condition).** `params` is built by the credential walk, mutated for module config (Vendor/MaxVlans/Pool/CredentialProfile/UseBGPV2MIB/WalkerMetrics/WarnLimiter/PanicReporter), consumed by **both** the module loop **and** the ARP step, and zeroized by a `defer params.Zeroize()` that must outlive both. Therefore `walkModules` **MUST NOT** build `params` or own its `Zeroize`. It **receives** the already-mutated `params`, returns only `(edges, oos, perModuleStatus)`; `params`-build + `defer Zeroize` + ARP stay in `probeTarget`. (Get this wrong → #83 session-reuse regression or use-after-zeroize in ARP.)

**C2 — instrumentation is parameterized, not owned (reviewer 2).** `walkModules` does NOT unconditionally emit per-module metrics or spans. Per-module span creation, `DiscoveryModuleDuration`/`SNMPWalksTotal`/`DiscoveryHardFailTotal`/`DiscoveryDegradedTotal` emission, and the walker-outcome sink (`params.WalkerMetrics`) are supplied by the caller. Cycle supplies them all → byte-identical. Rediscover supplies none (nil metrics, nil tracer, `params.WalkerMetrics` unset) → byte-identical to today's instrumentation-free `walkOne`. **Rediscover must NOT start emitting `walker_outcome_total`/decode/module-duration metrics or spans, and must NOT install the #72 rate limiter.**

**C3 — output-only helper preserves the read-only invariant (reviewer 2).** `walkModules` returns `(edges, oos, perModuleStatus)` and takes no graph/ages/snapshot/`prevGraph` handle, so rediscover (which calls it) can never mutate the published graph. Graph mutation stays exclusively in `RunCycle`'s tail.

**C4 — span parenting + ctx (reviewer 1).** Per-module spans descend from the per-*target* span. `walkModules` receives the per-device `ctx` (already descended from `targetCtx`) as its `ctx` arg; when its caller supplies a tracer it starts per-module child spans from that ctx. Rediscover passes a ctx with no span → no orphaning (and no spans created, per C2).

**C5 — keep the #74 per-IP override inside the loop (reviewer 1).** `enabledModules(cfg)` returns the module list gated by `cfg.Modules.*.Enabled` only. The per-IP `cfg.ModulesForIP(ip)` intersection (`overrideMatched && !allowedMods[proto]`) stays **inside** `walkModules` (it needs `ip`). Do not pre-filter it away.

**C6 — dep completeness (reviewer 1).** `walkModules` needs `cfg` (whole — for `TimeoutPerModule`, `ModulesForIP`, FDB/BGP knobs already baked into `params`), `logger`, `allowedNets`, `dev`, the per-device `ctx`, the mutated `params`, and an optional instrumentation set. It does NOT capture `mu`/`results`/`okCount`/`failByReason`/`allARPMACs` (all caller-side aggregation).

**C7 — T3 panic-recover stays caller-side (reviewer 1).** A `recover()` only catches its own goroutine. The probe goroutine's `defer recover` and the `sem`/cycle-budget admission `select` and the `mu`-guarded `results`/`okCount`/`failByReason` append stay in the `RunCycle` closure; `probeTarget` returns a small `(probeResult, ok, failReason)` the caller aggregates.

**C8 — T4 caller owns prevGraph/ages reassignment (reviewer 1).** `publish(...)` must not internally reassign `prevGraph` before the caller diffs. Keep `Diff(prevGraph, newGraph)` → … → `prevGraph = newGraph` ordering; the size-budget early return's `ages = newAges` side effect must fire on BOTH the reject and happy paths. `publish` receives `prevGraph`/`ages` and returns the new values (or takes pointers) so the caller owns the reassignment on both paths.

**C9 — ARP merge ordering (reviewer 1).** `allARPMACs` is merged under `mu` at probe time (first-wins by goroutine scheduling). `probeTarget` returns its per-device `arpMACToIP`; the caller merges under `mu`. Preserve merge-at-collection (do not move it to the sorted aggregation pass — that would change which IP wins a MAC conflict).

## New file: `internal/app/device_walk.go`
- `enabledModules(cfg *config.Config) []Module` — the single 7-element list (kills the duplicated slice; the one genuine "can't drift" win).
- `type moduleInstrumentation struct { metrics *metrics.Metrics; tracer trace.Tracer; logger *slog.Logger }` (or equivalent) — any field nil ⇒ that instrumentation is skipped. (The walker-outcome sink rides on `params.WalkerMetrics`, set by the caller, not here.)
- `func walkModules(ctx context.Context, cfg *config.Config, dev discovery.Device, ip net.IP, params snmputil.Params, allowedNets []*net.IPNet, inst moduleInstrumentation) (edges []discovery.Edge, oos []discovery.OutOfScopeNeighbour, perModuleStatus map[string]moduleStatus)` — the shared loop: `enabledModules` → per-IP #74 intersection → per-module (optional span) → `mod.Walk(ctx, params, dev.ID, allowedNets)` → outcome classify (error/degraded/ok) → (optional) per-module metrics → accumulate. Output-only.

## Tasks (sequential TDD; each behavior-identical)
- **T1** — add `enabledModules` + `walkModules`; refactor `RunCycle`'s probe closure to call it with FULL instrumentation. Behavior-identical for cycle (regression gate: existing cycle tests).
- **T2** — route `rediscover.walkOne` through `walkModules` with MINIMAL instrumentation (nil metrics/tracer, `params.WalkerMetrics` unset). Delete rediscover's duplicated module slice + loop. **Behavior-identical** — rediscover emits no new metrics/spans, no rate limiter. Add a test asserting `/admin/rediscover` still emits NO `walker_outcome_total` increments (pins the no-regression). Document the intentional instrumentation difference in a comment on `walkOne`/`walkModules`.
- **T3** — extract `probeTarget(...)` for the rest of the closure (DNS, allow-list, system-walk, params-build + `defer Zeroize`, ARP, returning `probeResult`); `RunCycle` keeps the goroutine's `defer recover`, `sem`/budget admission, and `mu`-guarded aggregation (C7), reading as walk→synthesize→reconcile→age.
- **T4** — extract `loop.go`'s publish sequence into `publish(...)` honoring C8 (caller owns prevGraph/ages reassignment; size-budget early-return `ages=newAges` on both paths).

## Testing
- Regression gate: all existing `internal/app` cycle + rediscover + goleak + `-race` tests pass unchanged.
- T1: a `-race` test that cycle still produces identical edges/metrics (existing tests cover this).
- T2: assert `walkOne` through the shared helper yields the same `(outcome, edgeCount)` as before AND emits zero `network_topology_walker_outcome_total` / module-duration increments (the no-regression pin). A `-race` test that cycle + rediscover sharing `walkModules` under `CycleMu` is race-clean.
- T4: unit-test `publish()` ordering (diff-before-reassign; ages advanced on size-reject).

## Cross-cutting
- One branch (`refactor/153-orchestration-dedup`), one PR closing #153. No Co-Authored-By / AI-attribution trailers; author colinedwardwood.
- CHANGELOG `### Internal`: decompose RunCycle (probeTarget + shared walkModules), dedup the cycle/rediscover module list+loop, extract loop.publish() — **no behavior change**; the rediscover instrumentation differences are intentional and now explicit.
- Files: new `internal/app/device_walk.go`; modified `internal/app/cycle.go`, `internal/app/rediscover.go`, `internal/app/loop.go`, `CHANGELOG.md`. (No `NewRediscoverer` signature change needed — rediscover passes minimal instrumentation it can build from its existing `m`/`logger`, or nil.)

## Out of scope
- Closing rediscover's instrumentation gap (metrics/spans/rate-limiter) — declined as regression/intentional.
- Target 4 (bgp decoder/edge-builder dedup) — declined as clarity-reducing.
