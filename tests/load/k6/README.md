# Load testing with k6

[Grafana k6](https://k6.io/) load tests for the exporter's HTTP surface. The
exporter's steady-state cost is Prometheus scraping `/metrics` and rendering the
topology collector at the current graph's cardinality, so that is what these
scripts exercise.

## Scripts

| Script | What it measures |
|---|---|
| `scrape_metrics.js` | Concurrent `/metrics` scrapes — p95/p99 render latency and error rate under VU load, the way a busy or federated Prometheus would scrape it. |

## Run

Against any running exporter:

```bash
k6 run tests/load/k6/scrape_metrics.js
# tune load and target:
TARGET=http://host:9100 VUS=50 DURATION=2m k6 run tests/load/k6/scrape_metrics.js
```

Thresholds (the test fails if breached): `<1%` failed scrapes, `/metrics` p95
`< 500 ms`, p99 `< 1500 ms`. Adjust to match your Prometheus `scrape_timeout`.

### Realistic cardinality

A freshly started process renders an almost-empty `/metrics`. For a meaningful
measurement, point `TARGET` at an instance that has discovered a large topology:

- the `deploy/long-running-test/` lab (rotating multi-topology fixture), or
- an instance started from a large snapshot (`snapshot.path`), or
- a production/staging instance behind a management interface.

### Streaming to Grafana Cloud k6

```bash
k6 cloud tests/load/k6/scrape_metrics.js
```

## CI

`.github/workflows/k6-load.yml` runs `scrape_metrics.js` against a freshly built
exporter as a **best-effort** smoke (it won't block PRs — a fresh instance has
little cardinality, so it only proves `/metrics` serves cleanly under
concurrency). Use the local/large-topology runs above for real capacity work.
