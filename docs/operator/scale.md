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
conservative — measured render time at 5k is ~100ms, roughly 1% of the
default scrape budget — so operators get pointed at this document long
before any real degradation. Treat the warning as a documentation
pointer, not an alert.

---

## When to worry

At default `scrape_interval: 15s` and `scrape_timeout: 10s` (Prometheus defaults):

| Edge count | Render time (measured median) | Payload | Action |
|---|---|---|---|
| 1,000 | 26 ms | 400 KB | Nothing — well under budget. |
| 5,000 | 97 ms | 1.9 MB | Nothing — still well under budget. |
| 10,000 | 130 ms | 3.9 MB | Nothing — watch the histogram p99 over time. |
| 25,000 | 508 ms | 9.7 MB | Watch p99. Still well within budget. |
| 50,000 | 1.33 s | 19.3 MB | Watch p99. Consider federation if growth continues. |
| 100,000 (extrapolated) | ~2.6 s | ~38 MB | Raise `scrape_timeout` to 30s; plan federation. |
| 200,000 (extrapolated) | ~5.2 s | ~76 MB | Mandatory: raise `scrape_timeout` AND federate. |
| > 400,000 (extrapolated) | > 10 s | > 150 MB | Push-mode OTLP only. Scrape-mode is not a fit at this size. |

Linear scaling holds across the measured range at ≈26 µs per edge on
the reference hardware. The bottleneck is the Prometheus text encoder
serialising each edge into exposition format, not the in-memory edge
table; the same code path runs regardless of how the edges arrived
(local SNMP discovery, snapshot reload, or hub-side federation
aggregation).

**Reference hardware (when reproducing the numbers):**

| Component | Value |
|---|---|
| CPU | Intel Core i7-5557U @ 3.10 GHz (Broadwell, 2× core, 4× HT) |
| Memory | 16 GB DDR3 |
| OS | Ubuntu 6.8.0-111-generic (kernel 6.8, x86_64) |
| Go | go1.25.0 linux/amd64 (toolchain auto-promoted from go.mod's 1.24) |
| Governor | `performance` on all cores |
| Pinning | `taskset -c 0` (single-core, isolates variance) |
| Run shape | `go test -tags=bench -bench=BenchmarkMetricsRender -benchtime=10s -count=5`, report median |

Reproduce with `scripts/run-scale-bench.sh` (the runner stamps the host
specs at the top of its result file).

These numbers are measured medians on one machine. Real-world render
time depends on:

- **Hardware vintage** — a modern data-centre Xeon/EPYC or Apple/ARM core
  is typically 1.5–3× faster per core than this 2015 Broadwell laptop
  CPU. Use these numbers as an upper bound on production-grade hardware,
  not a target.
- **Label cardinality** — edges with rich `Edge.Metadata` (BGP per-peer
  attributes, MPLS-TE admin status, etc.) take longer to encode than
  bare LLDP edges. The synthetic graph used here is the bare-LLDP case.
- **Response compression** — Prometheus and Grafana Alloy both accept
  `Content-Encoding: gzip`. The exporter's renderer does not compress;
  if your scrape goes through a reverse proxy that does, the wire bytes
  are typically ~5–10× smaller than the numbers above.
- **Scraper network distance** — `scrape_timeout` covers the full
  request including the body transfer. A 19 MB scrape across a
  bandwidth-constrained link can dominate the render time entirely.

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

## SNMP session lifecycle and conntrack pressure

Each discovery cycle opens a **fresh UDP SNMP session per (target × enabled module)** and closes it when the walk finishes. With every module turned on, that's roughly nine sessions per target per cycle:

- 1 SYSTEM walk (sysName / sysObjectID / sysDescr)
- 1 each for LLDP, CDP, OSPF, IS-IS, BGP, MPLS-TE
- 1 for FDB, plus 1 more per VLAN when the FDB module walks Cisco IOS VLAN-community indices

At 10k targets on a 60s discovery interval with all modules enabled, that's on the order of **1,500 short-lived UDP socket open/close operations per second** from the exporter, plus matching activity on any conntrack-tracking firewall in path. FDB-heavy Cisco fleets add more.

### Why it's done this way

`gosnmp`'s `*GoSNMP` struct is not goroutine-safe (see `internal/discovery/snmp/snmp.go:142`), so a single session cannot be shared across concurrent goroutines. Within a target's per-device goroutine, however, modules run **sequentially**, so one session can safely be reused across that target's modules. By default each module still opens its own fresh session for the duration of its walk (byte-identical, lowest-memory behaviour); the optional session pool below reuses one session per target instead.

### When it matters

The kernel cost is modest in isolation — UDP has no TIME_WAIT, so socket-table churn is the dominant overhead, not state-table accumulation. It becomes load-bearing in three deployment shapes:

- **Stateful firewall in path.** Conntrack tracks UDP "connections" by 5-tuple with its own timeout (typically 30s on Linux). At ~1,500 new flows/sec the table can fill faster than entries time out; if `nf_conntrack_max` is at its default (≈262 144 on modern kernels) you have headroom, but heavily-tuned-down hosts hit the ceiling and silently drop packets.
- **Large fleets with FDB enabled.** FDB's per-VLAN walks on Cisco IOS multiply the per-cycle session count. A 10k-target fleet with 64 active VLANs on average adds tens of thousands of extra UDP flows per cycle.
- **NAT'd outbound paths.** Source-NAT devices with limited port pools (cloud NAT gateways, small CGN deployments) can exhaust 5-tuples under sustained churn.

### Diagnosing it

Symptoms surface on the firewall or NAT device, not the exporter. Look for:

- Linux: `cat /proc/sys/net/netfilter/nf_conntrack_count` climbing under exporter load; `dmesg | grep -i conntrack` showing "table full, dropping packet" entries
- `nf_conntrack_udp_timeout` set lower than the SNMP timeout (`modules.snmp.timeout`, default 5s) — entries time out mid-walk, packet drops manifest as SNMP timeouts on the exporter side
- Cloud NAT gateways logging port exhaustion or connection-rate-limit hits

The exporter itself will report these as ordinary SNMP timeouts in `network_topology_snmp_timeout_total`. The firewall is where the root cause lives.

### Operator mitigations available today

In rough order of leverage:

0. **Enable the per-target SNMP session pool** (`discovery.snmp.session_pool.enabled: true`, issue #83). Default off. When on, each target reuses one SNMP session across its modules and across cycles instead of opening a fresh one per (target × module), collapsing the per-cycle new-flow count from ~9 per target to ~1 per target — an ≥80% reduction in socket/conntrack churn. Bounded at one session per (target × credential profile) at ~50 KB each, so memory scales with fleet size, not module count. Observe `network_topology_snmp_session_pool_size`, `..._hits_total`, `..._misses_total`, and `..._evictions_total{reason}`. **Credential-retention tradeoff:** a pooled session holds its own copy of the credential inside gosnmp until the session is evicted; the per-cycle credential zeroization cannot reach that copy. Idle eviction (`discovery.snmp.session_pool.max_idle`, default 5 × `discovery.interval`), shutdown, and credential-rotation invalidation all close the session and clear that copy. Rotate credentials with this in mind — see [security.md](security.md).
1. **Raise `nf_conntrack_max`** on Linux firewalls and the exporter host (`sysctl -w net.netfilter.nf_conntrack_max=1048576`). Cheapest, has no downside if the host has the memory (~300 bytes per entry).
2. **Increase `discovery.interval`.** Doubling the cycle time halves the steady-state new-flow rate. Trades freshness for kernel headroom.
3. **Lower `discovery.parallelism`.** Flattens the burst so peak new-flow rate drops, even if total flows per cycle stay the same. Cycle time grows.
4. **Disable FDB unless you need it.** FDB's per-VLAN walks are by far the biggest contributor on Cisco IOS gear.
5. **Set `modules.snmp.timeout` ≤ `nf_conntrack_udp_timeout`** so SNMP gives up before conntrack does, surfacing failures as exporter-side timeouts rather than mysterious packet drops.

### Session pooling (shipped, opt-in)

Pooling SNMP sessions per target across cycles (the [#33](https://github.com/colinedwardwood/network-topology-exporter/issues/33) follow-up, implemented in [#83](https://github.com/colinedwardwood/network-topology-exporter/issues/83)) is now available behind `discovery.snmp.session_pool.enabled` (default off — see mitigation 0 above). Reuse is safe because a target's modules run sequentially, so one session is only ever touched by one walk at a time; the pool itself is mutex-protected for the cross-target case. Credential rotation is handled by closing and evicting a profile's sessions on `InvalidateProfile`, and dead sessions are evicted on the next unhealthy return or after `max_idle`. The per-target memory cost is bounded at one session per (target × credential profile), ~50 KB each — not per module.

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
