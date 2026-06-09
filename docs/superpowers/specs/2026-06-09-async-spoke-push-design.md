# Async spoke push (#6, Phase 1) — design

**Goal:** the discovery cycle must never block on the federation hub. A slow or
unreachable hub can today stall the spoke's discovery cycle and ultimately
evict the spoke, causing silent data loss; this change decouples the push from
the cycle.

**Issue:** #6 (Phase 1 — spoke-side only). Phase 2 (hub-side acceptance of
"stale-but-most-recent" generations) is explicitly **out of scope** and deferred
to the v2.0.0 federation batch alongside #71.

**Status:** non-breaking. The hub, the wire payload, and `Spoke.Push`'s contract
are all unchanged.

---

## Problem

`internal/app/loop.go` (~line 460) pushes the reconciled graph to the hub
**synchronously inside the discovery cycle**:

```go
if err := lc.Spoke.Push(cycleCtx, payload); err != nil && ctx.Err() == nil {
    lc.Logger.Warn("spoke push failed", "error", err)
}
```

`Spoke.Push` retries 3× with exponential backoff under a 30s client timeout, so
a slow hub can block the cycle ~37s. At `discovery.interval: 60s` with
`spoke_timeout: 3×interval`, three consecutive slow pushes evict the spoke; once
evicted, subsequent pushes carry a `cycle_at` the hub rejects as
`stale_generation`, and the spoke stays dark until the timing window realigns.
Failure mode: **silent data loss under hub-side latency**.

## Design principle (why latest-only, not a queue)

A normal Prometheus exporter is pull-based and stateless between scrapes:
"most-recent-wins" is the native model, and a missed scrape is normal. The two
push mechanisms in the ecosystem agree: **Pushgateway** holds the last pushed
value and overwrites it (no queue); `remote_write` queues only because
individual samples are irreplaceable in a continuous series. Our payload is a
**complete graph snapshot per cycle** — a newer snapshot fully supersedes an
older one, so nothing is lost by dropping an intermediate. Therefore the spoke
keeps a **latest-only mailbox**, not a FIFO of stale snapshots. This also
sidesteps the `stale_generation` rejection entirely (we never send a stale
intermediate), which is why Phase 1 fixes the bug without the Phase 2 hub change.

The headline operational signal is **freshness** (seconds since last successful
push), mirroring how operators already alert on scrape staleness — not queue
depth, which is only ever 0 or 1.

## Architecture

Extract a small, independently testable component rather than inlining (the
snapshot writer is inline, but #6 requires a test of the slow-hub behavior).

New file `internal/app/spoke_pusher.go` — a `spokePusher` with three seams:

- `Enqueue(payload federation.SpokePayload)` — non-blocking; called from the
  discovery cycle. Always leaves the freshest payload in the mailbox.
- `run(ctx context.Context)` — the consumer loop; one push at a time.
- `Shutdown(deadline time.Duration)` — drains the final payload, attempts one
  last push bounded by `deadline`, then returns.

Constructed and its `run` goroutine started in `loop.go` only when
`lc.Spoke != nil` (i.e. `federation.role: spoke`).

### Mailbox (latest-only)

A capacity-1 channel `ch chan federation.SpokePayload`. `Enqueue`:

```go
select {
case p.ch <- payload:
default:
    // Slot occupied by an un-consumed older payload: drop it (superseded) and
    // insert the newer one.
    select {
    case <-p.ch:
        p.m.FederationSpokePushDropsTotal.WithLabelValues("superseded").Inc()
    default:
    }
    select {
    case p.ch <- payload:
    default:
        // Pusher consumed the slot between the two ops; the in-flight push will
        // be followed by this payload on the next cycle. Benign.
    }
}
p.m.FederationSpokePushQueueDepth.Set(float64(len(p.ch)))
```

The narrow race (pusher consumes between drain and re-insert) at worst pushes a
one-cycle-older snapshot; the next cycle re-enqueues the current one. Acceptable
and self-correcting.

## Data flow

1. `cycle()` builds `federation.SpokePayload` (unchanged fields) and calls
   `pusher.Enqueue(payload)` instead of `lc.Spoke.Push(...)`. The cycle returns
   immediately.
2. `run` receives a payload and calls `lc.Spoke.Push(pushCtx, payload)`.
3. On success: `FederationSpokePushLastSuccessUnix.Set(now)`. On error (and
   `ctx` not cancelled): `FederationSpokePushFailuresTotal.Inc()` + rate-limited
   Warn.

## Decisions

1. **`Spoke.Push` retry/timeout unchanged.** Keep the existing 3-retry / backoff
   / 30s-timeout. Phase 1's only change is *where* it runs. Minimal blast radius;
   no contract change.
2. **Push runs under the process-lifetime context, not `cycleCtx`.** `cycleCtx`
   is cancelled when the cycle ends, which would abort an async push. The pusher
   uses the long-lived loop `ctx`. Trace propagation for #68 (spoke→hub W3C
   `traceparent`) is preserved by capturing the cycle's trace context into the
   payload at enqueue time and using it for the outbound push span (via a span
   link / stored `trace.SpanContext`), so the push still correlates to its cycle
   even though it runs after the cycle span closes.
3. **Bounded shutdown drain.** On `ctx.Done()`, `loop.go` calls
   `pusher.Shutdown(SpokePushDrainTimeout)`; the pusher attempts one final push
   of the latest payload bounded by the deadline, then exits. New const
   `SpokePushDrainTimeout = 5 * time.Second` (well under the typical 30s k8s
   `terminationGracePeriodSeconds`). A `sync.WaitGroup` makes shutdown
   deterministic, mirroring the snapshot writer's `snapWg`.

## Error handling

`run` is wrapped in `recoverGoroutine("spoke_pusher", lc.Logger, lc.M)` — the
same one-shot recovery the snapshot writer uses (`loop.go:251`). If the pusher
panics and exits, subsequent `Enqueue` calls fill the slot once and then count
`superseded` drops, so the failure stays observable via the drop counter and the
freshness gauge going stale.

## Metrics (`internal/metrics/metrics.go`)

| Metric | Type | Purpose |
|---|---|---|
| `network_topology_federation_spoke_push_last_success_unix` | Gauge | **Headline freshness.** Alert on `time() - it > N`. |
| `network_topology_federation_spoke_push_drops_total{reason}` | CounterVec | `reason="superseded"` (newer payload replaced it) / `reason="shutdown"` (dropped at exit). |
| `network_topology_federation_spoke_push_queue_depth` | Gauge | 0–1. Included per #6 acceptance; secondary to freshness. |
| `network_topology_federation_spoke_push_failures_total` | Counter | **Existing** — reused for push errors. |

## Testing (`internal/app/spoke_pusher_test.go`)

A fake `Spoke` whose `Push` blocks for a controllable duration:

- **Cycle never blocks:** with a 60s-blocking push, assert `Enqueue` returns
  immediately and multiple enqueues proceed while the first push is in flight
  (the issue's required test).
- **Most-recent-wins:** enqueue payloads A, B, C while a push is blocked; on
  unblock, assert the next pushed payload is the newest queued, and
  `drops_total{reason="superseded"}` counted the dropped intermediates.
- **Freshness gauge:** assert `last_success_unix` advances only on a successful
  push.
- **Bounded shutdown:** with a blocking push, assert `Shutdown(5s)` returns
  within the deadline and counts a `shutdown` drop if the final push can't
  complete.
- **No goroutine leak:** `goleak` (already used in the package) confirms `run`
  exits after `Shutdown`/`ctx` cancel.

## Files

- `internal/app/spoke_pusher.go` — new `spokePusher` component.
- `internal/app/loop.go` — construct + start the pusher when `Spoke != nil`;
  replace the synchronous `Spoke.Push` call with `pusher.Enqueue`; call
  `pusher.Shutdown` on `ctx.Done()`; add `SpokePushDrainTimeout` const.
- `internal/metrics/metrics.go` — add the three new metrics + registration.
- `internal/app/spoke_pusher_test.go` — new tests.

## Rollout / compatibility

Non-breaking. No config changes (the pusher is always used in spoke mode; there
is no opt-out — the old synchronous behavior was a defect). The hub, the wire
payload, and `Spoke.Push` are untouched. CHANGELOG entry under "Fixed".
