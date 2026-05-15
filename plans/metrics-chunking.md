# Plan: `/metrics` Partial Emission / Chunking for Large Topologies

**Status:** Proposed — likely outcome is **wontfix + documented scale ceiling**
**Author:** docs/audits/2026-05-architectural-review.md remediation
**Created:** 2026-05-14
**Estimate:** 1 day to validate the wontfix path, 3–4 days for the partial implementation if pursued
**Risk:** Low for wontfix path; medium for implementation (departs from standard Prometheus scrape semantics)

## Problem statement

`/metrics` emits the entire topology graph in a single Prometheus exposition response. At 10k+ edges, the payload reaches multiple megabytes, which can exceed scraper timeouts (default Prometheus `scrape_timeout` is 10s).

The architectural review (§2.4) proposes "a chunked or paginated metrics output mode for massive topologies to preserve scrape reliability."

This plan investigates whether this is achievable within Prometheus scrape semantics, recommends an outcome, and details the implementation if pursued.

## TL;DR recommendation

**Recommend wontfix with a documented scale ceiling and three operator-facing mitigations** (longer scrape timeout, federation hub split, OTLP push path). The "chunked /metrics" framing in the review is a non-starter — Prometheus's exposition format and scrape contract do not admit pagination. The cost of departing from the standard contract is higher than the cost of operating within it.

If you disagree with that recommendation, this plan also covers the implementation path.

## Background — why "paginated /metrics" is hard

The Prometheus exposition format (text or OpenMetrics) is a **single response** describing the **complete** state of all exposed metrics at the moment of scrape. The scrape contract:

1. One request → one response → one snapshot.
2. The scraper has no protocol-level way to ask for "page 2".
3. Splitting into multiple endpoints (`/metrics/shard1`, `/metrics/shard2`) creates a coordination problem: which shards exist? How does the scraper enumerate them? Service discovery is the only standard answer, and SD targets are stable per-scrape — they cannot be dynamically discovered mid-scrape.

The OpenMetrics specification does discuss streaming but does not specify pagination — a scraper that supports streaming still reads the whole stream in one request.

Real-world large-cardinality exporters (kube-state-metrics, node_exporter on busy hosts, the cAdvisor metrics path) handle this not by paginating but by:

- Splitting into multiple **independently-scraped exporter instances** (sharded by domain)
- Raising `scrape_timeout` to match the expected response time
- Moving to a **push** model (OTLP, remote_write from exporters with internal aggregation)

## What this exporter already provides

Three existing properties make the "wontfix + document" path defensible:

1. **OTLP push output exists** (`internal/output/otlp/`). For large topologies, operators can disable scrape entirely and push to a collector that handles backpressure properly.
2. **Federation hub/spoke** sharding is already supported. A 10k-edge global topology can run as 5× spokes with 2k edges each and a hub that exposes the combined view — or, in uncoordinated mode, the per-instance metric is naturally sharded with the join performed by Mimir's recording rule.
3. **Graph size admission control** (`max_graph_edges`, `max_graph_devices` per `internal/config/config.go:89-92`) rejects payloads above a configurable cap. This is the relief valve, not the solution.

## Wontfix path (recommended)

### Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| AC1 | Document the per-scrape scale ceiling — the edge count at which a default-config scrape exceeds 10s on reference hardware | Benchmark `/metrics` response time at edge counts 1k, 5k, 10k, 25k, 50k; publish the curve in `docs/operator/scale.md` |
| AC2 | Document the three escape hatches: scrape timeout tuning, hub/spoke or uncoordinated federation, OTLP push | Update `docs/operator/cardinality.md` or new `docs/operator/scale.md` |
| AC3 | Add a `network_topology_metrics_payload_bytes` and `network_topology_metrics_render_duration_seconds` pair so operators can observe how close they are to the scrape budget | New histograms in `internal/metrics/` |
| AC4 | A warn-level log at startup when `len(edges) > 5000` recommends the operator review the scale doc | Log emission test |

### Implementation outline

1. **Benchmark** — write a one-off harness in `cmd/topology-exporter/` (under a `_bench` build tag) that synthesizes graphs of N edges and serves them. Hit it with `curl --max-time` at varying N. Record the inflection point where p95 response time crosses 1s, 5s, 10s.
2. **Metrics instrumentation** — wrap the existing Prometheus collector's render path with a histogram-recording shim. Both bytes-out and duration are useful.
3. **Documentation** — new doc `docs/operator/scale.md` with the benchmark table, the three escape hatches with config examples, and a decision tree (10k edges? Tune timeout. 50k? Federate. 100k+? Push.).
4. **Startup warning** — single emission per process lifetime, gated on observed edge count after the first discovery cycle.

This path takes ~1 day. It satisfies the spirit of the architectural review (operators have a path forward at scale) without departing from standard Prometheus scrape semantics.

## Implementation path (if wontfix is rejected)

### Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| BC1 | A new endpoint family `/metrics/edges/{shard}` exposes a deterministic subset of edges per `{shard}` value | Integration test scrapes all shards and asserts the union equals the standard `/metrics` output |
| BC2 | Shard count is configurable (`metrics.shard_count`, default 1 = no sharding) | Config-load test |
| BC3 | Shard membership is **stable** — an edge's shard does not change across scrapes unless the edge itself changes | Test: same graph, two scrapes, edge-to-shard mapping is identical |
| BC4 | A `/metrics/manifest` endpoint enumerates the active shards so operators can write SD configs | JSON shape contract |
| BC5 | Standard `/metrics` continues to work — sharding is opt-in | Existing tests stay green |

### Technical approach for the implementation path

**Sharding key.** Hash `(SrcDevice, SrcPort, DstDevice, DstPort)` via xxhash, mod `shard_count`. Each shard is a separate `/metrics/edges/{i}` endpoint. Stability falls out of the hash being a pure function of edge identity.

**The hard problem: device-level metrics.** Devices are smaller in number (10k devices is rare even when 50k edges is common), so they stay on `/metrics`. The shard endpoints emit edges only. This sidesteps the question of how to shard a `_total` counter across endpoints (you can't — it'd violate the increment-monotonic contract).

**Scrape config.** Operators have to enumerate the shards explicitly in their Prometheus config:

```yaml
scrape_configs:
  - job_name: topology-exporter-edges
    metrics_path: /metrics/edges/0
    # … repeat 0..shard_count-1 as separate jobs, or use file_sd_configs
```

Or run the manifest endpoint as the SD source. This is workable but uglier than the current single-endpoint contract.

**Render path.** Each shard endpoint iterates the full edge list once and emits only the edges whose shard hash matches the path parameter. No precomputation needed; the render time per shard is `(1/N)` of the full render plus a constant for the prelude.

### Risks specific to the implementation path

| Risk | Mitigation |
|---|---|
| Operators forget to update their Prometheus scrape config when changing `shard_count`, silently losing edges | Manifest endpoint + a `network_topology_shard_unscraped` metric that increments when a shard's last scrape was longer ago than 2× the configured scrape interval — but this requires the shard to know its own scrape interval, which is fragile |
| Federation hub combines spoke metrics — if spokes shard differently, the hub's reconciliation passes don't compose | Disallow sharding when `federation.role == hub`; document this clearly |
| Counter-typed metrics (currently device-level only) cannot be sharded — see "device-level metrics" above; an operator who later adds an edge-keyed counter has to know not to put it in a sharded endpoint | Sharding only ever covers gauges; introduce a code-review rule and a runtime check |

## Open questions for the user

1. **Take the wontfix path?** Strongly recommended. The implementation path adds operational complexity (manifest endpoint, SD config, shard-aware troubleshooting) for a problem that already has three working escape hatches.
2. **What scale are real deployments at today?** If nobody is over 5k edges, the benchmark + doc path is plenty. If there is a known 50k-edge deployment, that changes the priority.
3. **Is OTLP push acceptable as the recommended path at scale?** That assumes the OTLP collector at the receiving end can handle backpressure properly, which depends on the operator's collector deployment. May be a non-answer for operators on Grafana Cloud Metrics-only.

## Out of scope

- OpenMetrics streaming — Prometheus supports it but scrapers still pull the entire stream in one request; doesn't solve the timeout problem.
- Replacing the Prometheus output entirely with OTLP. The exporter currently emphasizes Prometheus as the primary output; that is a strategic choice this plan does not relitigate.
- Server-side push (server-initiated streams). Prometheus pull model is by design.

## Sign-off

If the wontfix path is chosen:
- [ ] User confirms wontfix is acceptable
- [ ] User confirms target benchmark edge counts (default proposal: 1k / 5k / 10k / 25k / 50k)

If the implementation path is chosen:
- [ ] User confirms acceptable to add an explicitly opt-in non-standard scrape contract
- [ ] User confirms federation hub sharding is out of scope (sharding is incompatible with hub combine)
