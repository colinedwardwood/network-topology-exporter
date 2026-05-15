# Operating at Scale

The exporter exposes the full topology graph as a single Prometheus `/metrics`
response. At small to medium topologies (under ~5k edges) this is a non-issue.
As the topology grows, three things become operator-visible:

1. The response body gets larger (linear in edge + device count).
2. Render time grows roughly linearly with that.
3. Once render time approaches the scraper's `scrape_timeout`, individual
   scrapes can be killed mid-response, producing flapping gaps in your TSDB.

This document covers what you can observe, when to worry, and the three
mitigations available to you.

---

## What to observe

Three metrics matter at scale, all live since v1.3.0:

| Metric | Type | What it tells you |
|---|---|---|
| `network_topology_metrics_render_duration_seconds` | histogram | Wall time per `/metrics` scrape. Alert at p99 against your `scrape_timeout`. |
| `network_topology_metrics_payload_bytes` | histogram | Response body size per scrape. Watch the trend, not the absolute. |
| `network_topology_last_scrape_samples_total` | gauge (last value) | How many series the most recent scrape emitted. |

Recommended alert (Prometheus-compatible):

```promql
# Page when p99 render time crosses 70% of the scraper's scrape_timeout.
# Adjust the divisor to match your real scrape_timeout.
(
  histogram_quantile(
    0.99,
    sum by (le) (rate(network_topology_metrics_render_duration_seconds_bucket[10m]))
  )
  > (10 * 0.7)
)
```

The exporter also emits a one-time **startup warning** when the first
discovery cycle produces more than **5,000 edges**. This is intentionally
conservative — well below the scale ceiling on any realistic hardware — so
operators get pointed at this document before any real degradation.

---

## When to worry

At default `scrape_interval: 15s` and `scrape_timeout: 10s` (Prometheus defaults):

| Edge count | Expected render time | Action |
|---|---|---|
| < 5,000 | sub-100ms | Nothing — well under budget. |
| 5,000 – 20,000 | 100ms – 1s | Watch the histogram p99. Still well under budget. |
| 20,000 – 50,000 | 1s – 5s | Time to raise `scrape_timeout` or split via federation. |
| 50,000 – 100,000 | 5s – 10s | Mandatory: raise `scrape_timeout` to 30s+ **and** use one of the escape hatches below. |
| > 100,000 | > 10s | Push-mode OTLP only. Scrape-mode is not a fit at this size. |

These numbers are **rough**. Real-world render time depends on label
cardinality, response compression, and the scraper's network distance.
Measure your deployment with the histograms above before treating any
number here as authoritative.

---

## The three escape hatches

### 1. Raise the scrape timeout

The simplest fix and the right answer for most operators in the 5k–50k edge
range. In your Prometheus / Mimir / Agent config:

```yaml
scrape_configs:
  - job_name: network-topology-exporter
    scrape_interval: 30s     # was 15s
    scrape_timeout: 25s      # was 10s, must be < scrape_interval
    static_configs:
      - targets: [topology-exporter:9100]
```

Bigger `scrape_timeout` does not increase your storage cost — it only changes
how long a scrape is permitted to run before the scraper gives up. The
storage cost is determined by `scrape_interval`.

### 2. Federation: split the topology

Hub/spoke and uncoordinated federation are documented in
`docs/operator/federation.md`. Both naturally shard the discovery surface:

- **Hub/spoke**: each spoke discovers only its own domain (e.g. one DC,
  one campus). The hub aggregates. Spoke `/metrics` payloads are small;
  the hub's combined `/metrics` is the same size as the global topology
  but the operator can configure the hub on larger hardware.
- **Uncoordinated**: each instance discovers a partition and emits
  `network_topology_boundary_observation_info` for cross-partition edges.
  A Mimir recording rule joins them. Each instance's `/metrics` is
  smaller than the global topology by exactly the partitioning ratio.

For a global topology of 50k edges, four uncoordinated instances at 12.5k
edges each is a comfortable scrape size at default timeouts.

### 3. OTLP push instead of scrape

For very large topologies (50k+ edges) or when scrape coordination is
infeasible, switch to OTLP push:

```yaml
output:
  otlp:
    endpoint: http://otel-collector:4318
    timeout: 30s
    heartbeat_cycles: 10
```

The exporter pushes the entire graph to an OTel collector once per
discovery cycle. Backpressure becomes the collector's problem (it has
better tools for it than the Prometheus pull model), and there's no
client-side timeout to manage at the topology size.

Caveats:
- The collector you push to needs to handle the payload size. Test on
  your real collector before committing.
- Push frequency is the discovery cycle, not the scrape interval. If you
  need higher temporal resolution than your `discovery.interval`, push is
  not the answer.
- For metrics-only environments without an OTel collector (e.g. operators
  running Grafana Cloud Metrics directly), OTLP push is not currently a
  viable path.

---

## What we considered and rejected

**Chunked / paginated `/metrics`.** Investigated in `plans/metrics-chunking.md`.
The Prometheus exposition format does not admit pagination — a single
scrape is one HTTP request that must return the complete state. We could
have built a sharded endpoint family (`/metrics/edges/{shard}`) but that
puts a non-standard scrape contract on every downstream operator. The three
escape hatches above solve the same scale problem without departing from
the standard contract.

**Streaming OpenMetrics.** Allowed by spec, but scrapers still read the
entire stream in one request — it doesn't help with the timeout problem.

**Server-side push to Prometheus.** Prometheus is pull-by-design. OTLP
push to a collector that itself exposes a `/metrics` endpoint is the
sanctioned indirection.

---

## Reference: configuring the size cap

When you have one instance handling more than you want it to, the
`hub.max_graph_edges` / `hub.max_graph_devices` size budget rejects pushes
above a threshold (in hub mode). It is a relief valve, not a scaling
strategy — use it to fail loud when something has gone wrong with your
sharding, not to enable a too-large topology.

```yaml
federation:
  hub:
    max_graph_devices: 10000
    max_graph_edges: 50000
```

Updates that exceed either limit are rejected with HTTP 503 (or via the
Reconcile path, dropped) and accounted in
`network_topology_graph_updates_rejected_total`.
