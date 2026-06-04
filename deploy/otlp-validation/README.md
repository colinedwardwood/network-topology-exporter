# OTLP validation harness

A self-contained stack for validating the exporter's **OTLP push** path
(metrics, logs, traces) end-to-end through **Grafana Alloy** to a Grafana Cloud
OTLP gateway. This is a test/validation asset — not a deployment template (for
operator deployments see `deploy/test-harness/` and `deploy/long-running-test/`).

## What it exercises

- `output.otlp` metric + log push (`internal/output/otlp`).
- `output.otlp.traces` — discovery-cycle tracing (`internal/tracing`).
- The **observable-gauge** behaviour: when a device or edge disappears, the
  exporter stops exporting its series — no stale/phantom topology at the
  receiver. (Regression-tested in `internal/output/otlp/otlp_test.go`
  `TestPushGraphDropsStaleEdges`; this harness confirms it on the wire.)

## Layout

| File | Purpose |
|---|---|
| `docker-compose.yml` | `snmpsim` (one discoverable device) + the exporter (built from this repo) + Alloy (OTLP receiver → Grafana Cloud OTLP gateway) |
| `exporter-config.yaml` | exporter config with `output.otlp` + `traces` enabled, pointed at the snmpsim target |
| `alloy-config.alloy` | Alloy: `otelcol.receiver.otlp` → `otelcol.exporter.otlphttp` to the gateway |
| `.env.example` | connection settings — copy to `.env` (gitignored) and fill in |

## Run

```bash
cp .env.example .env          # fill in GCLOUD_OTLP_ENDPOINT, GCLOUD_OTLP_USER, GCLOUD_TOKEN
docker compose up -d --build  # builds the exporter from the repo Dockerfile
```

Verify in Grafana Cloud:

- **Metrics** (Mimir): `network_topology_device_info{service_name="network-topology-exporter"}`
  — the snmpsim device (`zeus.snmplabs.com`) arriving over the OTLP path.
- **Traces** (Tempo): TraceQL `{ name = "discovery.cycle" }`.

### Staleness check (the observable-gauge fix)

```bash
docker compose stop snmpsim   # remove the device
# wait ~90s, then in Grafana Cloud confirm the series stops receiving samples:
#   count_over_time(network_topology_device_info{service_name="network-topology-exporter"}[1m]) -> 0
```

The series must drop to zero new samples — the exporter does not keep pushing a
device that no longer exists.

```bash
docker compose down
```

## Notes

- `snmpsim` serves its default `public` recording (sysName `zeus.snmplabs.com`),
  which the exporter discovers as a single device. Point `exporter-config.yaml`
  at real devices for richer topology.
- The OTLP gateway endpoint, instance ID, and token come from the environment
  (`.env`) so no stack-specific values or secrets live in the committed files.
