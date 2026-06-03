# Tracing the discovery cycle

The exporter can emit OpenTelemetry traces of its **own discovery cycle** so you
can see, per cycle, how long each target poll took, which credential profile
won, how each protocol walk fared, and — in federation — how a spoke push lands
on the hub. This is operational self-observability of the exporter, not of the
network it discovers.

Tracing is **opt-in and disabled by default**. It is an additive signal layered
on the same OpenTelemetry Go SDK that already powers the OTLP metrics and logs
output (issue #82): when enabled, spans are exported over the **same**
`output.otlp.endpoint`, `output.otlp.protocol`, and transport — tracing does not
get its own endpoint or auth.

## Enabling

```yaml
output:
  otlp:
    enabled: true
    endpoint: "http://alloy:4318"   # shared by metrics, logs, AND traces
    protocol: http                  # http (default) or grpc
    traces:
      enabled: true
      sample_rate: 0.1              # head sampling ratio, [0, 1]; default 0.1
```

`output.otlp.enabled` controls the metrics/logs push. Tracing keys off
`output.otlp.traces.enabled` and reuses `endpoint`/`protocol` regardless — so
you can run traces alongside the metrics/logs push. The receiver must be an OTLP
trace receiver (Grafana Tempo, a Grafana Alloy `otelcol.receiver.otlp`, or an
OpenTelemetry Collector feeding Tempo/Jaeger). The wire encoding is protobuf
(the only encoding the OTel Go SDK exporters implement).

When `traces.enabled` is `false` (the default), no `TracerProvider` is
installed; the instrumentation calls resolve to the OpenTelemetry no-op tracer
and emit nothing, at effectively zero cost.

## Sampling

Sampling is **head-based**: `ParentBased(TraceIDRatioBased(sample_rate))`.

- The root `discovery.cycle` span is sampled with probability `sample_rate`.
- Every child span (`target.poll`, `<module>.walk`, `credentials.resolve`,
  `graph.reconcile`, `spoke.push`) inherits the parent's decision. A sampled
  cycle keeps all of its children; an unsampled cycle drops them all. This keeps
  each trace whole rather than half-sampling its spans.
- A spoke push that is sampled propagates its decision to the hub via the W3C
  `traceparent` header, so `hub.handlePush` joins the same sampled trace.

Guidance:

- **Debugging / low cycle rate:** `sample_rate: 1.0` traces every cycle. Because
  the exporter runs one cycle per `discovery.interval` (default 60s), a fleet of
  a few hundred targets at 1.0 is still a very low span rate.
- **Production / large fleets:** keep `sample_rate` small (the `0.1` default, or
  lower) to bound trace volume and receiver cost. The per-cycle metrics already
  give you aggregate timing; traces are for drill-down.
- `sample_rate: 0.0` is honoured (sample nothing) — useful to wire up the export
  path without yet emitting traces.

## Spans emitted

Spans nest via context so a single cycle forms one trace tree:

```
discovery.cycle
├── target.poll                (one per target)
│   ├── credentials.resolve
│   ├── lldp.walk
│   ├── cdp.walk
│   ├── fdb.walk
│   ├── ospf.walk
│   ├── bgp.walk
│   ├── isis.walk
│   └── mpls_te.walk
├── graph.reconcile
└── spoke.push                 (federation spoke role only)
        │  … W3C traceparent over HTTP …
        └── hub.handlePush      (recorded on the hub process)
```

| Span | Emitted | Key attributes |
| --- | --- | --- |
| `discovery.cycle` | once per cycle (root) | `cycle.number`, `cycle.start_time`, `cycle.device_count`, `cycle.edge_count`, `cycle.device_errors` |
| `target.poll` | once per target | `target.ip`, `credential.profile`, `target.latency_seconds` |
| `credentials.resolve` | per target, during credential trial | `credential.candidates`, `credential.trials`, `credential.limiter_wait_seconds`, `credential.winning_profile` |
| `<module>.walk` | per enabled module per target — `lldp.walk`, `cdp.walk`, `fdb.walk`, `ospf.walk`, `bgp.walk`, `isis.walk`, `mpls_te.walk` | `walk.pdu_count`, `walk.outcome` (`ok` / `degraded` / `failed`) |
| `graph.reconcile` | once per cycle | `reconcile.input_edges`, `reconcile.output_edges`, `reconcile.edges.<proto>` (per-source input counts) |
| `spoke.push` | per cycle, spoke role only | `spoke.id`, `spoke.devices`, `spoke.edges` |
| `hub.handlePush` | per accepted push, hub process | `spoke.id` |

Failures set the span status to `Error` and attach the error via
`span.RecordError` (e.g. DNS resolution failure or credential resolve failure on
`target.poll`, a module walk error on `<module>.walk`).

> The module span name uses the module's protocol identifier, so the MPLS module
> emits `mpls_te.walk` (matching the `mpls_te` Prometheus label), not `mpls.walk`.

## Federation: trace context across the spoke → hub push

The exporter sets the global propagator to **W3C TraceContext** (+ Baggage). On
a federation **spoke**, the `spoke.push` span's context is injected as a
`traceparent` HTTP header on the outbound push to the hub. On the **hub**, the
`/spoke/push` handler extracts that header and starts `hub.handlePush` as a child
of `spoke.push`, so both spans share one trace ID. To see the full cross-process
trace, enable tracing (`output.otlp.traces.enabled: true`) on **both** the spoke
and the hub, pointed at the same Tempo/Jaeger back end.

If the spoke is tracing but the hub is not (or vice versa), the link is simply
not recorded on the side that has tracing off; nothing breaks.

## Verifying against a live receiver

The in-memory unit tests cover span names, nesting, attributes, the
disabled-is-a-no-op guarantee, and the spoke→hub `traceparent` round-trip. To
confirm spans actually land on a live Tempo/Jaeger receiver, run the
env-guarded integration test:

```
TRACING_INTEGRATION_ENDPOINT=http://localhost:4318 \
TRACING_INTEGRATION_PROTOCOL=http \
go test ./internal/tracing/ -run TestTracingIntegration -v
```

then look for the `integration.smoke` / `integration.child` trace in your back
end. Repeat with `TRACING_INTEGRATION_PROTOCOL=grpc` against `:4317`.
