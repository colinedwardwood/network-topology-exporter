# Architecture

`network-topology-exporter` is a single Go binary modelled on the [Prometheus exporter pattern](https://prometheus.io/docs/instrumenting/writing_exporters/). It runs a periodic discovery cycle and exposes the results through two industry-standard signals: Prometheus metrics and structured JSON log lines to stderr.

## Design principles

The exporter emits standard signals only. No bespoke control-plane API, no proprietary RPCs. Anything that needs the topology graph queries Prometheus / Mimir; anything that needs a change feed ships the log stream to Loki (via Promtail / Alloy — not via this binary). Discovery is modular: each protocol lives in its own package under `internal/discovery/`, modules emit common `Device` and `Edge` value types, and the exporter coordinates while the metrics layer translates to Prometheus series.

**Output contract:** Prometheus metrics on `/metrics`; structured JSON log lines to stderr. The exporter does not push to external systems. Loki, NetBox, and OTLP trace export are explicitly out of scope — the coupling belongs in the shipping agent, not here.

Two policies are binding on contributors:

- **Source-for-spec, never source-for-code (LD-09).** GPL-licensed monitoring source may be read for behavioural extraction into specifications only. The Go implementation is written from the specifications. Full guardrails in [`CONTRIBUTING.md`](../CONTRIBUTING.md).
- **Source-attributed reconciliation (LD-10).** Every emitted edge carries `discovery_proto`, `direction`, `link_type`, and `precedence_rank`. Conflicts between sources emit a separate counter rather than being silently resolved. The reconciliation model is informed by Breitbart et al., "The NetInventory System" (IEEE/ACM ToN, 2004), which demonstrated that ranking multiple protocol sources for the same physical link — and surfacing disagreements rather than silently arbitrating — is the correct approach for heterogeneous network environments.

Four operational commitments:

- **Polling scope is bounded by an allow-list (LD-11).** The exporter polls only IPs in `discovery.scope.cidr_allow_list`. Out-of-scope neighbours emit a log line and increment `network_topology_out_of_scope_neighbours_total` but are never polled. The iterative BFS discovery pattern — seed device → query neighbour tables → enqueue new neighbours — is documented in "Discovering Neighbor Devices in Computer Network" (arXiv:1709.02209, 2017); the allow-list is the hard boundary that keeps BFS expansion safe in production.
- **Credential management is a first-class subsystem (LD-12).** Named profiles, per-IP and per-CIDR assignments, ordered fallback, and a token-bucket trial limiter. Per-device winning profiles are cached in the snapshot. The lockout risk on cold start (many auth failures in rapid succession triggering device-level SNMP lockout policies) informed the rate limiter design. Operator runbook: [`docs/operator/cold-start-credentials.md`](operator/cold-start-credentials.md).
- **The graph survives restart (LD-13).** A versioned JSON snapshot is written atomically after every successful cycle. On startup the exporter serves the previous edge set immediately with `network_topology_graph_stale=1` until the first live cycle completes.
- **Unidirectional links have a lifecycle (LD-14).** A link reported by only one endpoint for `discovery.unconfirmed_link_ttl_cycles` consecutive cycles (default 3) is removed. This directly addresses the partial-topology problem identified in "Improved algorithm for network topology discovery based on STP" (IEEE, 2011): bridges do not retain FDB entries for traffic not recently seen, so one-sided link reports are normal and should not be treated as permanent topology facts.

## Module layout

```
.
├── cmd/topology-exporter/main.go     # entrypoint, HTTP server, discovery loop
├── internal/
│   ├── config/                       # YAML config schema + validation (LD-11 scope guard)
│   ├── version/                      # build metadata (ldflags-injected)
│   ├── metrics/                      # every Prometheus metric the exporter emits
│   ├── events/                       # structured log lines for topology change events
│   ├── credentials/                  # LD-12 named profiles, resolver, trial limiter, cache
│   ├── snapshot/                     # LD-13 versioned JSON persistence (atomic write)
│   ├── graph/                        # LD-10 reconciliation + LD-14 unconfirmed-link lifecycle
│   └── discovery/
│       ├── discovery.go              # shared Device / Edge / OutOfScopeNeighbour types + interfaces
│       ├── snmp/                     # SYSTEM group walk (RFC 1907)
│       ├── lldp/                     # IEEE 802.1AB / RFC 4957
│       ├── cdp/                      # CISCO-CDP-MIB
│       ├── bgp/                      # RFC 1657 BGP4-MIB (v0.2+)
│       ├── ospf/                     # RFC 4750 OSPF-MIB (v0.2+)
│       ├── arp/                      # not a Prober — see package comment
│       └── fdb/                      # RFC 4188 BRIDGE-MIB dot1dTpFdbTable
├── config/example.yaml               # documented configuration schema
└── docs/operator/                    # runbooks
```

## Discovery cycle

```
startup:
  snapshot.Load(path) → graph + credential cache (LD-13)
  network_topology_graph_stale = 1
  serve /metrics from the loaded graph

for each IP in cidr_allow_list, in parallel up to parallelism:
  profile = credentials.CachedProfile(ip) ?? Resolve(ip)
  credentials.AcquireTrial(ctx)        ── token-bucket (LD-12)
  device = snmp.Walk(ip, profile)      ── populates DeviceInfo
  credentials.RecordSuccess(device.ID, profile)
  for each enabled module (lldp, cdp, bgp, ospf, fdb):
    result = module.Probe(ip)
    for each out-of-scope neighbour: log line + counter (LD-11)

graph.Reconcile(edges) → ranked edges per LD-10 precedence ladder
graph.AgeUnconfirmed(edges, ages, ttl) → expired keys (LD-14)
graph.Diff(previous, current) → TopologyChangeTotal increments
                               + structured log lines per change (events.Logger)
store.Replace(graph)
snapshot.Write(graph + credential cache + ages) — atomic (LD-13)
network_topology_graph_stale = 0
observe network_topology_discovery_cycle_duration_seconds
```

Change events appear on two surfaces. The counter (`network_topology_change_total`) is what alerts fire on. The log line carries the full before/after edge record so the operator can answer "which edge changed?" without joining metric series in their head.

## Concurrency

The discovery scheduler runs one cycle at a time. Inside a cycle, a bounded worker pool of `discovery.parallelism` goroutines (default 20) probes devices concurrently, each bounded by `discovery.timeout_per_device`. If a cycle overruns `discovery.interval`, the next cycle starts immediately — no queuing. `network_topology_discovery_cycle_duration_seconds` is the operator's signal that the interval needs to grow.

The credential trial limiter is shared across the worker pool so the global trial rate stays at `credentials.trial_rate_per_second` regardless of pool size. The graph store and credential cache each use a single `sync.RWMutex`: writers (cycle finaliser, success recorders) hold the write lock briefly; readers (the metrics collector on every scrape) take an RLock for the duration of one scrape.
