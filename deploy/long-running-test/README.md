# Long-running test lab

A continuously-running validation environment for the topology exporter. A
single mutator container swaps the underlying containerlab topology every UTC
hour, exercising add/remove/swap operations against a live exporter shipping
to Grafana Cloud.

## Stack

| Container             | Image                          | Purpose                                                  |
| --------------------- | ------------------------------ | -------------------------------------------------------- |
| `long-running-exporter` | `network-topology-exporter:latest` | Discovers the lab, exposes `/metrics`                 |
| `long-running-alloy`    | `grafana/alloy:latest`         | Scrapes the exporter and ships logs/metrics/traces to Grafana Cloud |
| `long-running-mutator`  | `alpine:latest` + `clab`       | Swaps the lab topology every UTC hour                    |

## Mutation schedule

`mutate.sh` selects a topology file based on `UTC hour mod 4`:

| `UTC hour % 4` | File          | Topology                                                |
| -------------- | ------------- | ------------------------------------------------------- |
| `0`            | `topo-1.yml`  | spine + 4 leaves, star (chain)                          |
| `1`            | `topo-2.yml`  | star + 2 leaf cross-link, leaf4 daisy-chained           |
| `2`            | `topo-3.yml`  | 5-node ring                                             |
| `3`            | `topo-4.yml`  | **CLOS** — 2 spines × 3 leaves (canonical fabric)      |

On the hour boundary, the mutator runs
`containerlab deploy -t topologies/topo-N.yml --reconfigure`, which fully
tears down the running lab and rebuilds it. The exporter sees a ~20–40s
window of SNMP timeouts during each swap.

The exporter target list in `config.yaml` is the **union** of all addresses
across topologies; devices not present in the current topology will fail
discovery and age out via the standard stale-edge eviction path.

## Prerequisites

- Docker + Docker Compose v2
- Containerlab installed on the host (the mutator pulls `ghcr.io/srl-labs/clab`
  at runtime to avoid a host-side dependency)
- A Grafana Cloud Access Policy token with write scopes for metrics, logs, traces
- A built `network-topology-exporter:latest` image on the host
- A built `nte-testnode:latest` image (used by every containerlab node)

## Bring-up

```bash
# 1) Populate credentials (see .env.example)
cp .env.example .env
$EDITOR .env

# 2) Pre-create the clab network so compose can attach to it
sudo containerlab deploy -t topologies/topo-4.yml --reconfigure

# 3) Start the stack
docker compose --env-file .env up -d

# 4) Tail logs
docker compose logs -f
```

## Operator notes

- **All Alloy credentials come from `.env`**. Never edit `alloy-config.alloy`
  to inline a token. The `.env` file is gitignored; `.env.example` is the
  on-repo template.
- **The mutator runs `--privileged --network host --pid host`** because that
  is what containerlab needs to manage Linux netns. Treat the host as
  root-controlled.
- **`exporter-data` is a named volume**. The snapshot persists across
  topology swaps; the exporter will surface stale devices for the first
  cycle after a mutation until eviction catches up. To start clean,
  `docker compose down -v`.
- **Telemetry labels**: all series carry `tester_id=long-running-lab` and
  `env=e2e` so the test data can be filtered out of dashboards that share
  the same Grafana stack.
- **No alerting is wired up.** If `containerlab deploy` fails or the cycle
  budget is exhausted, you will only see it on the test-harness dashboards.

## Known limitations

- `deploy.sh` is currently pinned to `macbookpro-2015` and rsyncs the
  working tree (including `id_ed25519`) to the remote. Use only on a host
  you control.
- The mutator has no retry/backoff. A failed `clab deploy` retries on the
  next minute with the same parameters.
- All four topologies share the lab name `nte-dynamic-1`; `clab inspect`
  reflects the *currently deployed* topology regardless of which YAML you
  point at.
