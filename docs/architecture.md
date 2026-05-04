# Architecture

`network-topology-exporter` is a single Go binary modelled on the [Prometheus exporter pattern](https://prometheus.io/docs/instrumenting/writing_exporters/). It runs a periodic discovery cycle and exposes the results through three industry-standard signals — Prometheus metrics, Loki push events, and OpenTelemetry traces — plus an optional NetBox writeback integration.

## Design principles

1. **Standard signals only.** No bespoke control-plane API, no proprietary RPCs, no project-specific JSON schemas the consumer has to learn. Anything that needs the topology graph queries Prometheus / Mimir; anything that needs a change feed reads Loki; anything that needs cycle profiling consumes the OTLP traces.
2. **Modular discovery.** Each protocol is an independent package under `internal/discovery/`. Modules emit common `Device` and `Edge` value types; the exporter coordinates and the metrics layer translates to Prometheus series.
3. **Clean-room source-for-spec rule per LD-09.** GPL-licensed monitoring source may be read for behavioural extraction into specifications under `docs/research/` (or in the parent `network-o11y-dev` repo); the Go implementation is then written from those specifications, never from the source. See [`CONTRIBUTING.md`](../CONTRIBUTING.md) for the full guardrails.
4. **Pattern provenance.** Where structural patterns are borrowed from permissive-licensed projects (e.g. `prometheus/snmp_exporter`'s YAML module schema), the borrowing is noted in code comments with the upstream license.
5. **Source-attributed reconciliation per LD-10.** Every emitted edge carries `discovery_proto`, `direction`, `confidence`, `link_type`, and `precedence_rank` labels; conflicts between sources are emitted (not silently resolved) via a separate `network_topology_conflict_total` counter. The full precedence ladder lives in [`network-o11y-dev/docs/ARCHITECTURE.md`](https://github.com/colinedwardwood/network-o11y-dev/blob/main/docs/ARCHITECTURE.md) §LD-10; this repo implements it in `internal/graph/`.

## Module layout

```
.
├── cmd/topology-exporter/main.go     # entrypoint, HTTP server, discovery loop
├── internal/
│   ├── config/                       # YAML config schema + validation
│   ├── version/                      # build metadata (ldflags-injected)
│   ├── metrics/                      # every Prometheus metric the exporter emits
│   ├── events/                       # Loki push for topology change events
│   ├── graph/                        # graph diff / change detection between cycles
│   ├── netbox/                       # optional NetBox REST writeback (integration, not a signal)
│   └── discovery/
│       ├── discovery.go              # shared Device / Edge value types
│       ├── snmp/                     # SYSTEM group walk (RFC 1907)
│       ├── lldp/                     # IEEE 802.1AB / RFC 4957
│       ├── cdp/                      # CISCO-CDP-MIB
│       ├── bgp/                      # RFC 1657 BGP4-MIB
│       ├── ospf/                     # RFC 4750 OSPF-MIB
│       ├── arp/                      # RFC 4293 IP-MIB ipNetToPhysicalTable
│       └── fdb/                      # RFC 4188 BRIDGE-MIB dot1dTpFdbTable
├── config/example.yaml               # documented configuration schema
├── deploy/
│   ├── docker/                       # Dockerfile lives at repo root for build context simplicity
│   └── helm/topology-exporter/       # standalone Helm chart for direct deployment
└── tests/integration/                # containerlab + simulated SNMP target rig
```

## Discovery cycle

```
┌─────────────────────────────────────────────────────────────────────┐
│  for each target in config.targets, in parallel up to parallelism:  │
│    span = otel.Start("discover device")                             │
│      device = snmp.Walk(target)            ── populates DeviceInfo  │
│      for each enabled module (lldp, cdp, bgp, ospf, arp, fdb):      │
│        edges = module.Walk(target)                                  │
│        emit edges as TopologyEdgeInfo series                        │
│      span.End()                                                     │
│  graph.Reconcile(edges) → ranked edges per LD-10 precedence ladder  │
│  graph.Diff(previous, current) → TopologyChangeTotal counter +      │
│                                  TopologyConflictTotal counter +    │
│                                  Loki push events                   │
│  observe cycle_duration_seconds                                     │
│  optional: netbox.Reconcile(devices) — integration, not a signal    │
└─────────────────────────────────────────────────────────────────────┘
```

Every metric emitted by the cycle is described in `README.md` under "Emitted signals", with cardinality budget in `docs/metrics.md` (Day 4 / Day 5 deliverable).

## Why this isn't a Grafana app plugin

The discovery surface needs to walk SNMP at one-minute intervals against thousands of devices. A backend Go binary with one socket per worker is the right shape; trying to cram that into a Grafana plugin would put networking work inside Grafana's process and force the plugin into a polling-from-the-browser model that doesn't survive an air-gapped deployment. The visualization (Graphviz panel reading `network_device_info` and `network_topology_edge_info` from Mimir) goes in the parent project's dashboards instead.

## Why Loki for change events instead of "just a metric"

Topology changes are *events with a payload* (the before/after edge record) — not just rate-able counters. Counter increments work for "how many changes happened in the last hour"; the JSON event in Loki gives the operator the actual `src_device → dst_device` that flipped. Both surfaces ship: counter for alerting, Loki document for human investigation.
