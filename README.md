# network-topology-exporter

A standalone, Apache 2.0 Go exporter that discovers network topology over SNMP, LLDP, CDP, BGP, OSPF, FDB, IS-IS, and MPLS-TE, and emits four signals:

- **Prometheus metrics** for device inventory and topology edges, scraped via `/metrics`.
- **Structured log lines** (JSON to stderr) for topology change events and operational state.
- **A versioned JSON snapshot** on disk so `/metrics` serves the previous graph immediately on restart.
- **OTLP push** (optional) — topology edges and devices as OTLP metrics, change events as OTLP log records, delivered directly to any OTLP-capable receiver.

By default there is no bespoke control-plane API and the exporter does not push to external systems. Anything that needs the topology graph queries Prometheus / Mimir. Topology change events are log lines — ship them to Loki, Elasticsearch, or any log aggregator using the collector agent already in your stack (Promtail, Alloy, Fluentd). Enable `output.otlp` to additionally push topology state directly to an OTLP endpoint.

## Why this exists

The Prometheus / Grafana stack already covers storage, query, alerting, and visualisation. What it doesn't cover is a network-discovery surface that produces topology data in formats that stack already understands. Think `snmp_exporter` for the topology problem: a Grafana panel renders the network graph by querying Mimir directly, with no bespoke graph endpoint sitting in front of the data.

## Status

**Release candidate.** SNMP / LLDP / CDP / BGP / OSPF / FDB / IS-IS / MPLS-TE discovery, graph reconciliation, credential management, snapshot persistence, multi-instance federation, and optional OTLP push are all implemented and covered by unit, integration, and end-to-end tests.

## Quickstart

```bash
go build -o bin/topology-exporter ./cmd/topology-exporter
./bin/topology-exporter --config.file=config/example.yaml
curl http://localhost:9100/metrics   # topology metrics
curl http://localhost:9100/readyz    # 503 during startup, 200 after first cycle
curl http://localhost:9100/healthz   # last cycle timestamp and error count
```

Or via Docker:

```bash
docker build -t network-topology-exporter:dev .
docker run --rm -p 9100:9100 \
  -v $PWD/config/example.yaml:/etc/topology-exporter/config.yaml:ro \
  network-topology-exporter:dev
```

## Emitted signals

### Prometheus metrics

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `network_device_info` | gauge (always 1) | `device_id`, `vendor`, `model`, `os_version`, `site` | One series per discovered device. |
| `network_device_uptime_seconds` | gauge | `device_id` | Per-device uptime from sysUpTime. |
| `network_topology_edge_info` | gauge (always 1) | `src_device`, `src_port`, `dst_device`, `dst_port`, `discovery_proto`, `link_type`, `direction` | One series per discovered edge. |
| `network_topology_change_total` | counter | `change_kind` (added\|removed\|updated), `discovery_proto` | Topology mutations between cycles. Use `increase()` not `rate()`. |
| `network_topology_out_of_scope_neighbours_total` | gauge | (none) | Count of LLDP/CDP neighbours outside the CIDR allow-list this cycle. Detail in log lines. |
| `network_topology_graph_stale` | gauge (0/1) | (none) | 1 while serving the startup snapshot; 0 after first live cycle. |
| `network_topology_snapshot_last_written_unix` | gauge | (none) | Wall-clock time of most recent snapshot write. |
| `network_topology_snapshot_loaded_devices_total` | gauge | (none) | Device count loaded from snapshot at startup. |
| `network_topology_discovery_devices_total` | gauge | `status` (success\|failed\|timeout) | Per-cycle device-discovery outcome. |
| `network_topology_discovery_cycle_duration_seconds` | histogram | (none) | End-to-end discovery cycle wall time. |
| `network_topology_discovery_module_duration_seconds` | histogram | `module` | Per-module wall time within a cycle. |
| `network_topology_snmp_walks_total` | counter | `status` (ok\|timeout\|error) | SNMP walk outcomes, aggregated across all devices. |
| `network_topology_credential_trials_total` | counter | `status` (ok\|failed) | Credential trial attempts under the LD-12 rate limiter. |
| `network_topology_conflict_total` | counter | `conflict_type` | Source disagreements detected during reconciliation (port_name_mismatch, neighbour_disagreement, direction_asymmetry, documented_vs_observed). |
| `network_topology_federation_spoke_up` | gauge (0/1) | `spoke_id` | Hub mode only. 1 while spoke has pushed within `spoke_timeout`; 0 after eviction. |
| `network_topology_federation_spoke_last_push_unix` | gauge | `spoke_id` | Hub mode only. Wall-clock time of most recent push from each spoke. |
| `network_topology_federation_spoke_push_failures_total` | counter | (none) | Spoke mode only. Incremented each time a push exhausts all retries. |
| `network_topology_hub_oos_unmatched_total` | gauge | (none) | Hub mode only. OOS neighbour hints that could not be matched to any known device name. |
| `network_topology_otlp_push_total` | counter | `status` (ok\|error) | Incremented after each OTLP push attempt. Only present when `output.otlp.enabled: true`. |

Full label reference, cardinality budget, and recommended alerts: [`docs/metrics.md`](docs/metrics.md).

No metric uses a raw IP address or free-form text as a label value.

### Log lines

All operational output goes to structured JSON on stderr. Topology change events include before/after edge records:

```json
{"time":"...","level":"WARN","msg":"topology change","change_kind":"removed","before_src_device":"core-sw-01","before_src_port":"Gi0/1","before_dst_device":"access-sw-03","before_dst_port":"Gi0/24","before_proto":"lldp","before_direction":"bidirectional"}
```

Ship these to Loki with the `{job="topology-exporter"}` label using the collector already in your stack.

## Configuration

See [`config/example.yaml`](config/example.yaml) for the full schema. Four operational commitments:

- **Polling scope is bounded by a CIDR allow-list.** Anything outside is recorded as `network_topology_out_of_scope_neighbours_total` (and a log line) and never polled. Defined in `discovery.scope.cidr_allow_list`.
- **Credentials are named profiles with rate-limited trial.** Per-IP and per-CIDR assignments resolve before the fallback list; the global trial rate is bounded so cold start doesn't lock devices out. Cold-start runbook: [`docs/operator/cold-start-credentials.md`](docs/operator/cold-start-credentials.md).
- **The graph survives restarts.** A versioned JSON snapshot reloads on startup so `/metrics` serves the previous edge set immediately with `network_topology_graph_stale=1` until the first live cycle completes.
- **Unidirectional links expire.** A link reported by only one endpoint for three consecutive cycles is removed and emits a `network_topology_change_total{change_kind="removed"}` increment.

## Discovery modules

Each module is independently enabled and runs within the same cycle. Edges from multiple sources covering the same physical link are reconciled by precedence — the highest-precedence source wins; the rest increment `network_topology_conflict_total` if they disagree.

| Module | Protocol / MIB | Precedence | YAML key | Notes |
|--------|---------------|------------|----------|-------|
| LLDP | LLDP-MIB (IEEE 802.1AB) | 10 | `modules.lldp.enabled` | Standard; preferred source when available. |
| CDP | CISCO-CDP-MIB | 9 | `modules.cdp.enabled` | Cisco-only; lower precedence than LLDP. |
| OSPF | OSPF-MIB (RFC 1850) | 8 | `modules.ospf.enabled` | |
| IS-IS | ISIS-MIB (RFC 4444) | 8 | `modules.isis.enabled` | See below. |
| MPLS-TE | MPLS-TE-STD-MIB (RFC 3812) | 7 | `modules.mpls_te.enabled` | See below. |
| BGP | BGP4-MIB (RFC 4273) | 6 | `modules.bgp.enabled` | |
| FDB | BRIDGE-MIB (RFC 4188) | 5 | `modules.fdb.enabled` | Layer-2 only; noisy on large networks. |

### IS-IS (RFC 4444)

Walks `isisISAdjState` and `isisISAdjIPAddrTable`. Only adjacencies in state `up(3)` produce edges. `DstDevice` is the neighbour IP from `isisISAdjIPAddrTable`.

```yaml
modules:
  isis:
    enabled: true
```

### MPLS-TE (RFC 3812)

Walks `mplsTunnelOperStatus`. `SrcPort` is formatted as `te-tunnel<idx>` where `idx` is the tunnel index from the MIB. `DstDevice` is the egress LSR IP.

```yaml
modules:
  mpls_te:
    enabled: true
```

## OTLP output

The exporter can push topology state directly to any OTLP-compatible receiver (Grafana Alloy, OpenTelemetry Collector, Grafana Cloud OTLP endpoint, etc.). This is additive — Prometheus `/metrics` continues to work unchanged.

```yaml
output:
  otlp:
    enabled: true
    endpoint: "http://alloy:4318"   # must be http:// or https://
    timeout: 10s                    # per-push deadline; default 10s
    heartbeat_cycles: 10            # push full graph every N cycles even if no changes; default 10
```

**What gets pushed:**

| Signal | OTLP path | Content |
|--------|-----------|---------|
| Full graph | `POST /v1/metrics` | `network_topology_edge` and `network_topology_device` gauge metrics, one data point per edge/device. Sent every cycle when there are changes, and unconditionally every `heartbeat_cycles` cycles. |
| Change events | `POST /v1/logs` | OTLP log records — severity INFO for added/updated edges, WARN for removed edges. Mirrors the JSON log lines already written to stderr. |

**Heartbeat semantics:** If no topology change is detected, the exporter skips the `/v1/metrics` push to avoid redundant data. Every `heartbeat_cycles` cycles (default: every 10th cycle) the full graph is pushed unconditionally so downstream receivers can detect a stale or silently-dropped connection.

**Self-monitoring metric:** `network_topology_otlp_push_total{status="ok|error"}` — a counter incremented after each push attempt, scraped via the normal `/metrics` endpoint.

### Receiving OTLP in Grafana Alloy

```alloy
otelcol.receiver.otlp "topology" {
  grpc { endpoint = "0.0.0.0:4317" }
  http { endpoint = "0.0.0.0:4318" }

  output {
    metrics = [otelcol.exporter.prometheus.topology.input]
    logs    = [otelcol.exporter.loki.topology.input]
  }
}

otelcol.exporter.prometheus "topology" {
  forward_to = [prometheus.remote_write.mimir.receiver]
}

otelcol.exporter.loki "topology" {
  forward_to = [loki.write.default.receiver]
}
```

Set `output.otlp.endpoint: "http://<alloy-host>:4318"` in the exporter config to target the HTTP receiver above.

## Federation

A single instance covers one contiguous CIDR range. Links that cross a boundary between ranges are visible to each side independently but neither produces a confirmed bidirectional edge because that requires both endpoint observations in a single reconcile call. Three modes bridge this:

| Mode | How it works |
|---|---|
| `uncoordinated` | Each instance emits `network_topology_boundary_observation_info` per OOS neighbour. A Mimir recording rule `count by(peer_a,peer_b,proto)(...) == 2` fires when both sides report — no inter-instance coordination required. |
| `spoke` | Instances push their reconciled graph to a hub after each cycle. The hub aggregates, re-reconciles across all domains, and emits unified metrics. Requires mTLS. |
| `hub` | Pure aggregator — no local SNMP discovery. Receives spoke pushes on a separate listener (default `:9101`). |

Federation runbook (mTLS setup, tuning, troubleshooting): [`docs/operator/federation.md`](docs/operator/federation.md).

Operator troubleshooting (no edges, SNMP timeouts, credential failures, slow cycles, snapshot issues): [`docs/operator/troubleshooting.md`](docs/operator/troubleshooting.md).

## Development

```bash
make test               # unit tests (no external deps)
make test-integration   # integration tests (in-process SNMP agents + federation mTLS)
make lint               # golangci-lint
make build              # binary → bin/topology-exporter
make docker             # container image
```

End-to-end tests require Docker and [containerlab](https://containerlab.dev/install/):

```bash
make e2e-image          # build the lightweight Alpine test node image (once)
make test-e2e           # e2e tests: lldp + snmp + binary exporter + federation spoke/hub
make test-e2e-srl       # e2e tests against Nokia SR Linux (x86-64, pulls srlinux:24.7.2)
```

## Architecture

See [`docs/architecture.md`](docs/architecture.md) for the module layout and discovery cycle. Clean-room development rules: [`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Related

- [`network-o11y-dev`](https://github.com/colinedwardwood/network-o11y-dev) — provisioning, dashboards, plugins, and packaging that consume this exporter.
