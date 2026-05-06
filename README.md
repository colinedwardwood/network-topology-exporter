# network-topology-exporter

A standalone, Apache 2.0 Go exporter that discovers network topology over SNMP, LLDP, CDP, BGP, OSPF, and FDB, and emits only two signals:

- **Prometheus metrics** for device inventory and topology edges, scraped via `/metrics`.
- **Structured log lines** (JSON to stderr) for topology change events and operational state.

There is no bespoke control-plane API. Anything that needs the topology graph queries Prometheus / Mimir. Topology change events are log lines — ship them to Loki, Elasticsearch, or any log aggregator using the collector agent already in your stack (Promtail, Alloy, Fluentd). The exporter does not push to external systems.

## Why this exists

The Prometheus / Grafana stack already covers storage, query, alerting, and visualisation. What it doesn't cover is a network-discovery surface that produces topology data in formats that stack already understands. Think `snmp_exporter` for the topology problem: a Grafana panel renders the network graph by querying Mimir directly, with no bespoke graph endpoint sitting in front of the data.

## Status

**Pre-v1, in active development.** SNMP / LLDP / CDP / BGP / OSPF / FDB discovery, graph reconciliation, credential management, snapshot persistence, and multi-instance federation are all implemented.

## Quickstart

```bash
go build -o bin/topology-exporter ./cmd/topology-exporter
./bin/topology-exporter --config.file=config/example.yaml
curl http://localhost:9100/metrics
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

## Federation

A single instance covers one contiguous CIDR range. Links that cross a boundary between ranges are visible to each side independently but neither produces a confirmed bidirectional edge because that requires both endpoint observations in a single reconcile call. Three modes bridge this:

| Mode | How it works |
|---|---|
| `uncoordinated` | Each instance emits `network_topology_boundary_observation_info` per OOS neighbour. A Mimir recording rule `count by(peer_a,peer_b,proto)(...) == 2` fires when both sides report — no inter-instance coordination required. |
| `spoke` | Instances push their reconciled graph to a hub after each cycle. The hub aggregates, re-reconciles across all domains, and emits unified metrics. Requires mTLS. |
| `hub` | Pure aggregator — no local SNMP discovery. Receives spoke pushes on a separate listener (default `:9101`). |

Federation runbook (mTLS setup, tuning, troubleshooting): [`docs/operator/federation.md`](docs/operator/federation.md).

## Architecture

See [`docs/architecture.md`](docs/architecture.md) for the module layout and discovery cycle. Clean-room development rules: [`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Related

- [`network-o11y-dev`](https://github.com/colinedwardwood/network-o11y-dev) — provisioning, dashboards, plugins, and packaging that consume this exporter.
