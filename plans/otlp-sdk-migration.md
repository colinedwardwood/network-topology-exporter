# Plan: OTLP SDK Migration

**Status:** Proposed
**Author:** ARCHITECTURAL_REVIEW.md remediation
**Created:** 2026-05-14
**Estimate:** 3–5 engineering days
**Risk:** High — replaces a deliberate "no SDK" architectural stance

## Problem statement

`internal/output/otlp/` (424 lines + 930 lines of tests) is a hand-rolled OTLP/HTTP+JSON exporter. The package comment is explicit: *"No otel SDK is required."* This is LD-09 ("source-for-spec, never source-for-code") applied to telemetry output, and it has real benefits:

- Tight dependency graph and small binary
- Inspectable wire format (JSON, debuggable with `curl` and `jq`)
- No lock-in to SDK version drift

The architectural review (§2.1) flags three real costs:

1. No OTLP/gRPC — high-throughput collectors prefer binary gRPC.
2. No OTLP/HTTP+protobuf — collectors that disable JSON reject our pushes.
3. Hardcoded schema URL (`https://opentelemetry.io/schemas/1.21.0` at `otlp.go:167`). Manually tracking spec evolution is fragile.

This plan migrates the OTLP output to the official Go SDK (`go.opentelemetry.io/otel/sdk/metric`, `…/log`, plus `…/exporters/otlp/otlpmetric/otlpmetricgrpc` + `otlpmetrichttp` + corresponding log exporters). The hand-rolled implementation is replaced wholesale.

## Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| AC1 | Exporter emits OTLP via gRPC when configured with a `grpc://` endpoint | Integration test: push to a collector built from `otelcol-contrib`; assert receipt on `otelcol.receiver.otlp{grpc}` |
| AC2 | Exporter emits OTLP via HTTP+protobuf when configured with an `http://` or `https://` endpoint and `protocol: http/protobuf` | Same as AC1 against `otelcol.receiver.otlp{http}` with protobuf content type |
| AC3 | Backward-compatible YAML keys: `output.otlp.endpoint`, `output.otlp.timeout`, `output.otlp.instance_id` continue to work | Config-load test with the example.yaml federation profile |
| AC4 | All existing metric names, labels, and resource attributes are preserved (no observable change for downstream consumers using JSON path today) | Diff-test against the JSON path's `otlp_test.go` golden outputs translated to OTLP protobuf — same data, different envelope |
| AC5 | Schema URL is sourced from the SDK's semconv constants, not a string literal in our code | `grep -rn '1\.21\.0\|opentelemetry.io/schemas' internal/output/otlp/` returns zero matches |
| AC6 | Push retry, timeout, concurrency bound, and shutdown drain semantics match the current behaviour (D3, D5, D7, D9 — see CHANGELOG) | Unit tests for the wrapper assert `429`/`503` retry, `Retry-After` honour, concurrency semaphore, WaitGroup drain |
| AC7 | Binary size grows by no more than 20 MB (sanity ceiling — actual budget TBD after first build) | `go build -ldflags='-s -w' ./cmd/topology-exporter` size comparison before/after |
| AC8 | `network_topology_otlp_push_total{status="dropped"}` semantics preserved when the concurrency semaphore is full | Existing test in `otlp_test.go` ports cleanly to the new wrapper |

## Technical approach

### Package selection

| Concern | Hand-rolled today | SDK package |
|---|---|---|
| Metrics SDK | hand-rolled JSON structs | `go.opentelemetry.io/otel/sdk/metric` |
| Logs SDK | hand-rolled JSON structs (change events) | `go.opentelemetry.io/otel/sdk/log` |
| Resource | hand-rolled | `go.opentelemetry.io/otel/sdk/resource` |
| Semantic conventions | `serviceName`, hardcoded schema URL | `go.opentelemetry.io/otel/semconv/v1.26.0` (or current) |
| OTLP/HTTP+protobuf | n/a | `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp` + `…/otlplog/otlploghttp` |
| OTLP/gRPC | n/a | `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc` + `…/otlplog/otlploggrpc` |
| OTLP/HTTP+JSON | hand-rolled | **Dropped in phase 3.** SDK does not emit JSON; maintaining a second codec doubles the test matrix without operator-visible benefit. |

### Architecture

```
graph.Edges / graph.Devices ──┐
                              ├──> Exporter (this package)
change events (events.Event) ─┘     │
                                    ├──> MeterProvider ──> otlpmetric{grpc|http} exporter
                                    └──> LoggerProvider ──> otlplog{grpc|http} exporter
```

The Exporter type stays as the public surface, but internally:

1. `New(cfg Config) (*Exporter, error)` constructs a `MeterProvider`, `LoggerProvider`, and the chosen exporter (grpc or httpproto based on `cfg.Protocol`).
2. `PushGraph(ctx, g)` walks edges/devices and calls `Int64Gauge.Record(...)` on cached meter instruments.
3. `PushEvents(ctx, evs)` calls `Logger.Emit(...)` for each change event.
4. `Shutdown(ctx)` calls the SDK's `Shutdown` which drains in-flight batches.

### Config surface (proposed)

```yaml
output:
  otlp:
    endpoint: grpc://alloy:4317        # NEW: scheme determines protocol
    # endpoint: https://alloy:4318      # http/protobuf, with explicit protocol below
    protocol: grpc                      # NEW: grpc | http/protobuf
    timeout: 10s
    instance_id: spoke-dc-a
    insecure: false                     # NEW: skip TLS verification (dev only)
    headers:                            # NEW: e.g. tenant routing
      X-Scope-OrgID: my-tenant
    heartbeat_cycles: 10                # unchanged
```

`protocol` defaults to `grpc` when the endpoint scheme is `grpc://`, otherwise `http/protobuf`. Maintain a one-release deprecation window for the legacy `http://…:4318/v1/metrics` form (treat as `http/protobuf` and warn).

### Metric instrument mapping

Today's hand-rolled gauges map cleanly to SDK `Int64ObservableGauge`. The hub/edge/device counts are inherently snapshot-shaped, so the observable-gauge callback pattern is the right fit: register one callback per metric family, evaluate it on the SDK's collection interval (which we drive by calling `meterProvider.ForceFlush(ctx)` from `PushGraph`).

| Current metric | SDK instrument | Notes |
|---|---|---|
| `network_topology_edges` (gauge per edge) | `Int64ObservableGauge` | One series per `(src_device, src_port, dst_device, dst_port, proto, …)` tuple via attributes |
| `network_topology_devices` (gauge per device) | `Int64ObservableGauge` | Same attribute pattern |
| change events (log records) | `Logger.Emit` | Severity 9 (info) / 13 (warn) preserved via `log.SeverityInfo` / `log.SeverityWarn` |

The current per-edge attribute set must be replicated exactly. Drift breaks downstream queries.

### Test strategy

The 930-line `otlp_test.go` currently asserts JSON shape. With SDK migration, the wire format is owned by the SDK — testing it means testing the SDK, which we shouldn't.

**New strategy:**

1. **Unit tests** — assert against the Exporter's *intent*: which instruments were recorded, with which attributes, in which order. Use the SDK's `metricdata` package + a `manual.NewReader` to snapshot what would have gone on the wire.
2. **Integration test** — spin up `otelcol-contrib` as a container, push to it, scrape its `prometheus` exporter, assert metric presence and label values end-to-end. Gated by a `OTEL_INTEGRATION=1` env var so CI can skip when the container isn't available.
3. **Retry/concurrency tests** — keep these as today (test the wrapper, not the SDK).
4. **Schema URL test** — assert that the resource emitted by the SDK carries a non-empty schema URL sourced from semconv. Remove the literal `https://opentelemetry.io/schemas/1.21.0` assertion.

### Migration mechanics

1. **Phase 1 (one PR):** Add the SDK as a feature flag (`output.otlp.use_sdk: false` by default). Run both paths in parallel in tests. Land.
2. **Phase 2 (one PR):** Flip the default to `use_sdk: true`. Deprecation warning in startup logs if the flag is set explicitly. Land.
3. **Phase 3 (one PR, one minor version later):** Delete the hand-rolled implementation. Land as `v1.4.0`.

This gives operators a release where they can `output.otlp.use_sdk: false` to revert without redeploying a different binary.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Binary size growth exceeds budget (AC7) | Measure on first build; if over, consider keeping otlpmetrichttp only and dropping gRPC (most collectors accept HTTP+protobuf) |
| Dependency CVE surface grows (otel + grpc + protobuf) | Add `govulncheck` to CI if not already present; accept this as a known cost |
| SDK semantic conventions drift from the version we pick | Pin `semconv` import path explicitly to a versioned subpath (e.g. `semconv/v1.26.0`); bump deliberately, never auto |
| The hand-rolled JSON path is used by an unknown number of operators | Two-release deprecation window; flag-gated rollout (phase 1) before hard cutover |
| Change-event log records change shape under the SDK | Snapshot the current JSON change-event output; diff against SDK-emitted protobuf decoded back to the same logical fields; PR-block on any difference not in a documented "format change" allow-list |

## Resolved scope decisions

1. **JSON path: dropped.** The hand-rolled OTLP/HTTP+JSON exporter is removed in phase 3 alongside the rest of the legacy implementation. Operators on the JSON path migrate to HTTP+protobuf during the deprecation window (phase 1 → phase 2). Rationale: maintaining two output codecs doubles the test matrix; protobuf is universally accepted by collectors.
2. **gRPC: in scope.** Ship both OTLP/gRPC and OTLP/HTTP+protobuf in phase 1. `protocol: grpc` is selected by `grpc://` endpoint scheme; `protocol: http/protobuf` by `http://` or `https://`. The binary-size growth (AC7) is accepted as a known cost.
3. **Tenant headers / auth.** In scope. Adding `headers:` map to the config (per the "Config surface" example) is part of phase 1 — the SDK exposes header injection cleanly via `WithHeaders`, so the marginal cost is small and it unblocks Grafana Cloud Metrics tenant routing.

## Out of scope

- Traces. The exporter does not emit spans today; adding traces is a separate initiative.
- OTLP push for the hub's reconciled graph (currently only the standalone/spoke path pushes). The architectural review treats this as a separate consideration.
- Replacing the change-event Prometheus metrics with OTel logs — only the OTLP output path is migrating; the `/metrics` scrape path stays as is.

## Sign-off

Implementation cannot start until:
- [x] JSON path: drop in phase 3 (resolved)
- [x] gRPC + HTTP+protobuf both shipped (resolved)
- [x] Headers/auth: in scope for phase 1 (resolved)
- [ ] User signs off on the two-release deprecation window in "Migration mechanics"
- [ ] User assigns the work (3–5 engineering days)
