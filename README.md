# network-topology-exporter

A standalone, Apache 2.0 Go exporter that discovers network topology over SNMP, LLDP, CDP, BGP, OSPF, ARP, and FDB, and emits **only industry-standard observability signals**:

- **Prometheus metrics** for device inventory and topology edges (scraped via `/metrics`).
- **Loki push events** for topology change events.
- **OpenTelemetry traces** for individual discovery cycles.
- An **optional NetBox sink** for IPAM/DCIM writeback.

There is no bespoke control-plane API. Anything that needs the topology graph queries Prometheus / Mimir; anything that needs a change feed reads Loki.

## Why this exists

The Prometheus / Grafana / OpenTelemetry stack already covers storage, query, alerting, and visualization. What it doesn't cover is a network-discovery surface that produces topology data in formats that stack already understands. This project fills that gap — think `snmp_exporter` for the topology problem. A Grafana panel renders the network graph by querying Mimir directly; there is no bespoke graph endpoint anywhere.

## Status

**Pre-v1, in active development.** Tracking the parent project's 5-day v1 plan. v0.1.0 ships the SNMP / LLDP / CDP discovery surface with Prometheus metric emission. BGP / OSPF / ARP / FDB land in v0.2.0.

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
| `network_device_info` | gauge (always 1) | `device_id`, `vendor`, `model`, `os_version`, `site`, `parent_device` | One series per discovered device. The label set is the inventory record. |
| `network_device_uptime_seconds` | gauge | `device_id` | Per-device uptime from the SYSTEM group (sysUpTime). |
| `network_topology_edge_info` | gauge (always 1) | `src_device`, `src_port`, `dst_device`, `dst_port`, `discovery_proto` (lldp\|cdp\|bgp\|ospf\|arp\|fdb), `link_type` | One series per discovered edge. |
| `network_topology_edge_utilization_ratio` | gauge | same as `_info` | Optional, joined with IF-MIB rates when both ends are known. |
| `network_topology_change_total` | counter | `change_kind`, `discovery_proto` | Counts topology mutations between discovery cycles (added / removed / changed). |
| `topology_discovery_devices_total` | gauge | `status` (success\|failed\|timeout) | Per-cycle device-discovery outcome. |
| `topology_discovery_cycle_duration_seconds` | histogram | (none) | End-to-end discovery cycle wall time. |
| `topology_discovery_module_duration_seconds` | histogram | `module` | Per-module wall time. |

Cardinality budget and full reference: `docs/metrics.md`.

### Loki events

Topology change events are pushed to Loki at `/loki/api/v1/push` with labels `{job="topology-exporter", change_kind=..., discovery_proto=...}`. Body is a JSON document with the full edge record before/after.

### OpenTelemetry traces

Each discovery cycle is a root span; per-device discovery is a child span; per-module operation is a grandchild. OTLP gRPC export only; the endpoint is configured under `otel.endpoint` in the config file.

### NetBox sink (optional)

When `netbox.enabled=true`, discovered devices are reconciled with NetBox via the REST API. This is an integration, not a signal — disabled by default.

## Configuration

See `config/example.yaml` for the full schema.

## Architecture

See `docs/architecture.md` for the module layout and the discovery cycle. The clean-room development commitments live in [`CONTRIBUTING.md`](CONTRIBUTING.md) and are binding on contributors.

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Related

- [`network-o11y-dev`](https://github.com/colinedwardwood/network-o11y-dev) — provisioning + dashboards + plugins + packaging that consume this exporter.
