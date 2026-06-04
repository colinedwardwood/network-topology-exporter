# Test-harness dashboards

Curated Grafana dashboards for the test harnesses (`deploy/test-harness/`,
`deploy/long-running-test/`). Import them into a Grafana stack that scrapes the
exporter's `/metrics` (and, optionally, has the topology logs in Loki).

| File | Dashboard | Reads |
|---|---|---|
| `getting-started.json` | 00. Getting Started | onboarding / health basics |
| `harness-health.json` | 01. Harness Health | exporter operational metrics |
| `topology-graph.json` | 02. Network Topology | `network_topology_device_info`, `network_topology_edge_info` |
| `topology-schedule.json` | 03. Topology Schedule | change events / cycle timing |

## Requirements

> [!IMPORTANT]
> **`topology-graph.json` requires Grafana [SQL Expressions](https://grafana.com/docs/grafana/latest/panels-visualizations/query-transform-data/expression-queries/).**
> The Node Graph panel builds its node frame with a server-side SQL expression
> (`datasource: __expr__`, `type: sql`) that pivots per-protocol arc ratios into
> the `arc__*` columns the panel colours, and joins in the degree stat. On a
> Grafana stack where SQL Expressions are unavailable or disabled, that panel
> renders **no data** (the other panels are unaffected).
>
> SQL Expressions are available in Grafana Cloud and in recent OSS/Enterprise
> releases; on self-managed Grafana confirm the feature is enabled before
> relying on the topology panel. There is currently no transform-only fallback —
> the proportional, per-protocol coloured ring is not achievable through the
> standard transform pipeline (the arc segments cannot be coloured without the
> SQL-expression frame).

The dashboards use the `$datasource` (Prometheus) and `$tester_id` template
variables; select your stack's Prometheus datasource and a `tester_id` present
in the data after import.
