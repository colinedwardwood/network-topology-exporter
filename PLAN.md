# Engineering Improvement Plan: Network Topology Exporter

The exporter itself is release-ready. All planned engineering items for the
core have landed; remaining work tracks operational tooling around the
exporter.

## Active work

| Item | Status | Notes |
| ---- | ------ | ----- |
| Test harness (single-shot) | Implemented (2026-05-21) | See [`plans/test-harness.md`](plans/test-harness.md) and [`deploy/test-harness/`](deploy/test-harness/). |
| Long-running validation lab (hourly topology mutation) | Implemented (2026-05-22) | See [`deploy/long-running-test/`](deploy/long-running-test/). Mutator rotates `topo-1` (chain) → `topo-2` (cross-link) → `topo-3` (ring) → `topo-4` (CLOS). |
| Curated dashboard set | In progress | 9 dashboards under [`dashboards/test-harness/`](dashboards/test-harness/). 4 are synced with Grafana Cloud stack `networko11ydev`; 5 are local-only WIP. |
