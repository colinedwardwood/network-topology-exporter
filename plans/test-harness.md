# Plan: Test Harness — Tester-Deployable Stack + Grafana Cloud Dashboards

**Status:** Implemented
**Author:** Tester-onboarding initiative, 2026-05-21
**Created:** 2026-05-21
**Implemented:** 2026-05-21
**Estimate:** 3–5 engineering days across 4 PRs
**Risk:** Medium — operator-facing artifacts (deploy templates, scoped credentials); cardinality decisions baked in early are expensive to undo.

## Problem statement

The exporter is functionally complete (per the v1.x tags) but the project follows pre-1.0 stability conventions and actively wants adversarial use. Today, a prospective tester has to:

1. Read the README, build from source or pull a Docker image.
2. Author their own `config.yaml` against the schema documented in `config/example.yaml`.
3. Decide how to ship metrics + logs somewhere they can see them — there is no canonical observability target shipped with the project.
4. Construct dashboards themselves to interpret what the exporter is producing.

The friction is doing real harm: testers either don't start, or start and don't know whether what they're seeing is correct, normal, or a bug. Both outcomes block the feedback loop the project depends on.

This plan introduces a turnkey test harness so a tester can go from "git clone" to "live topology graph in Grafana Cloud" in under 15 minutes, with three curated dashboards waiting in the Grafana Cloud stack.

## Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| AC1 | A new tester running `cd deploy/test-harness && docker compose up` against a YAML stub they filled with (their lab CIDR, their SNMP community, the project-provided Grafana Cloud token) gets a running stack pushing metrics + logs to the project's Grafana Cloud instance within 5 minutes | First-tester dry-run; record wall-clock time |
| AC2 | The project-controlled Grafana Cloud folder contains three dashboards: `Test Harness — How to Use`, `Topology Graph`, `Exporter Health` | Dashboard JSON applied via terraform; verified via `grafana-cli` or UI |
| AC3 | The `Topology Graph` dashboard renders the discovered edges using the **native Grafana node graph panel** (not graphviz) | Visual inspection against a lab with ≥10 nodes |
| AC4 | Per-tester telemetry is filterable in every dashboard via a `tester_id` template variable | Verify the variable populates, filtering reduces the panel to one tester |
| AC5 | The `Exporter Health` dashboard surfaces every metric in the README metric table at least once, with the exporter-toolkit-style alert candidates from `docs/metrics.md` annotated | Cross-reference panel queries against `README.md` metric list |
| AC6 | The `How to Use` dashboard is the first dashboard a tester lands on; it contains markdown panels with copy-pasteable PromQL the tester should expect to see firing, plus a live "ARE YOU CORRECTLY CONFIGURED?" stat panel keyed off `network_topology_graph_stale` | Visual inspection from a tester role |
| AC7 | Grafana Cloud token shipped to testers is scoped write-only (metrics-publish + logs-publish), no admin or read scopes | Verify via `gcloud-cli stacks tokens describe` |
| AC8 | The test harness deploy is documented in a single tester-facing README under `deploy/test-harness/README.md` with a < 1-page quickstart | Page renders < ~80 lines |

## Technical approach

### Architecture

```
┌─ Tester's lab ─────────────────────────────────┐         ┌─ Project's GC stack ─┐
│                                                │         │                      │
│   ┌─────────────────┐    ┌──────────┐          │  HTTPS  │   ┌──────────────┐   │
│   │ topology-exporter│──→│  Alloy   │──────────┼────────→│   │   Mimir      │   │
│   │  /metrics        │    │ scrape   │          │ remote_ │   │   (metrics)  │   │
│   │  stderr JSON logs│←──┤ tail     │          │  write  │   ├──────────────┤   │
│   └─────────────────┘    │  + ship  │          │  + Loki │   │   Loki       │   │
│         ↑                 │  + label │          │   push  │   │   (logs)     │   │
│         │                 │ tester_id│          │         │   ├──────────────┤   │
│   config.yaml             └──────────┘          │         │   │  Grafana     │   │
│   (lab CIDR, SNMP                               │         │   │  (3 dashboards│  │
│    community)                                   │         │   │  in folder    │  │
│                                                 │         │   │  "Test Harness")│ │
└────────────────────────────────────────────────┘         └──────────────────────┘
```

Alloy sits in the middle deliberately: it gives us scrape, log-tailing, retry/buffer, and `external_labels` injection (`tester_id`) in one process with one config file. The exporter's built-in OTLP push path is left intact for production direct-to-receiver use cases but is not used by the test harness.

### File layout

```
deploy/
  test-harness/
    docker-compose.yml          # 2 services: topology-exporter, alloy
    config.yaml.example         # tester fills in their lab CIDR + community
    alloy/
      config.alloy              # scrape /metrics, tail logs, ship to GC
      config.alloy.example      # tester fills in token + tester_id
    README.md                   # ≤80 lines, quickstart
dashboards/
  test-harness/
    how-to-use.json             # markdown panels + onboarding stat panel
    topology-graph.json         # node graph panel + neighbour table
    exporter-health.json        # metric coverage + alert candidates
terraform/
  test-harness/
    main.tf                     # grafana_dashboard resources for the three above
    folder.tf                   # the "Test Harness" folder
    variables.tf                # gc stack URL, service account token
    README.md                   # how to apply (project-maintainer ops, not tester ops)
plans/
  test-harness.md               # this file
```

### Telemetry shape

The tester's Alloy adds these `external_labels` to every metric and log line:

- `tester_id` — short string, tester picks (e.g. their Grafana corp username). Filterable everywhere.
- `lab_id` — optional, tester can use this if they run more than one lab.
- `stack_version` — derived from the running exporter image tag (`v1.4.0-rc.1` etc).
- `alloy_version` — populated from Alloy's own version so test reports cite the exact agent that produced them.

### Token model

Testers are Grafana employees and cut their own Alloy write tokens against the project stack (`networko11ydev.grafana.net`). Maintainer-side, a separate **provisioning** token (at `~/Code/grafana/network-o11y-demo/grafana-cloud-api.token`, never committed) drives dashboard CRUD via `grafana-cli`.

### Dashboard provisioning

`grafana-cli` (Grafana's observability-as-code CLI), not terraform. See https://grafana.com/docs/grafana/latest/as-code/observability-as-code/grafana-cli/.

Dashboard JSON lives in `dashboards/test-harness/*.json`. A `make dashboards-apply` target wraps `grafana-cli` against the stack URL + provisioning token, writing the JSON files into a folder named `Test Harness` on the stack. Testers never run this — only the maintainer does, when a dashboard changes. No terraform state to manage.

### Dashboard contents

**`how-to-use.json`** — markdown panels with the README quickstart copy-pasteable; a top stat panel "ARE YOU CORRECTLY CONFIGURED?" keyed off `count(network_topology_graph_stale == 0)` and filtered by `tester_id`; links to the other two dashboards.

**`topology-graph.json`** — primary panel is the native Grafana node graph. Edges query: `network_topology_edge_info` with a `transform` step that renames labels to the panel's expected `id`/`source`/`target`/`mainStat`/`secondaryStat` columns. Nodes query: `network_topology_device_info`. Secondary panels: a flat edge table (one row per `src_device`/`dst_device`), a "stale links" panel (edges with `direction="unidirectional"` for >3 cycles), and a "discovery proto breakdown" pie.

**`exporter-health.json`** — covers every metric in the README table:
- `network_topology_discovery_cycle_duration_seconds` histogram → p50/p95/p99 over time.
- `network_topology_discovery_devices_total{status}` → stack chart of success/failed/timeout.
- `network_topology_snmp_walks_total{status}` → same.
- `network_topology_credential_trials_total{status}` → same.
- `network_topology_conflict_total{conflict_type}` → counter over time.
- `network_topology_snapshot_drops_total{reason}` → counter (recent addition per #42).
- `network_topology_change_total{change_kind,discovery_proto}` → stacked area, `increase()` over $__interval.
- `network_topology_out_of_scope_neighbours_total` → time series.
- Alert-candidate annotations on each panel referencing `docs/metrics.md`.

## PR sequence

| PR | Scope | Estimate | Risk |
|---|---|---|---|
| **PR 1 (this plan)** | `plans/test-harness.md` design doc | < 0.5 day | Low |
| **PR 2** | `deploy/test-harness/` — compose + Alloy config + tester-facing README | ~1 day | Medium — operator-facing |
| **PR 3** | `dashboards/test-harness/*.json` — three dashboards as JSON | ~1–2 days | Medium — JSON authoring tedious, panel choice critical |
| **PR 4** | `make dashboards-apply` target driving `grafana-cli` against the project stack | ~0.25 day | Low — no terraform state |

Dependencies: PR 3 can be authored in parallel with PR 2; PR 4 depends on PR 3.

## Resolved scope decisions

These are this plan's recommendations; the user has chance to override at review time.

| # | Decision | Rationale |
|---|---|---|
| D1 | **Tester deploy footprint = docker-compose**, not helm | Lowest friction for ad-hoc testers; helm chart is a v2 follow-up if k8s shops ask |
| D2 | **Alloy in the middle**, not exporter OTLP-push direct | Unified scrape + log-tail + retry + buffering; one agent the tester installs and configures |
| D3 | **Per-tester Alloy tokens — tester self-serves** | Testers are Grafana employees; they cut their own Alloy tokens against the project stack. No maintainer-side token distribution. |
| D4 | **Dashboard provisioning via `grafana-cli` (observability-as-code), NOT terraform** | One CLI, no provider/state machinery; ships dashboards from a git-tracked folder. Docs: https://grafana.com/docs/grafana/latest/as-code/observability-as-code/grafana-cli/ |
| D5 | **`How to Use` dashboard = markdown panels + one live "configured?" stat panel** | Discoverable on landing; no separate doc site needed |
| D6 | **`tester_id` + `alloy_version` labels injected by Alloy `external_labels`** | `tester_id` for per-tester filtering. `alloy_version` recorded so test reports cite the exact agent version that produced them. |
| D7 | **Native node graph panel, NOT graphviz** | Designed exactly for nodes+edges data; clickable detail panels; no DOT conversion step |
| D8 | **Target GC stack: `networko11ydev.grafana.net` (instance 1544961)** | Maintainer-controlled; provisioning token lives at `~/Code/grafana/network-o11y-demo/grafana-cloud-api.token` and never enters the repo |

## Risks and mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Tester reports a "bug" that's actually a misconfigured lab on their side | Wasted maintainer triage | The `How to Use` dashboard's "configured?" stat panel is the first triage step ("does your lab show green here?") |
| Node graph panel chokes on very dense topologies (>500 edges) | Tester sees an unusable graph and assumes the exporter is broken | Document a "shows the top N most-active edges" override and a fallback to the flat edge table |
| Dashboard JSON drift if hand-edited in the UI | `make dashboards-apply` overwrites tester edits, or stale repo JSON propagates | Document: changes happen in the JSON file + `make dashboards-apply`, never directly in the UI. Add a CI check that the deployed JSON matches the repo JSON. |
| Tester's Alloy version not recorded with bug reports | Hard-to-reproduce telemetry shape differences | `alloy_version` external label baked into every series/log; bug reports cite it. |

## Out of scope

- A web UI for tester registration. V1 testers DM the maintainer for a token.
- Multi-cloud support. The harness targets a single Grafana Cloud stack the maintainer controls. Self-hosted Grafana is supported by editing the terraform variables but not documented as a primary path in V1.
- Tracing (OTLP traces). The exporter doesn't emit traces today; if it grows to, this plan covers it via Alloy's OTLP receiver without code change.
- A separate "production deployment" template. The test harness is explicitly for testing — production users follow the existing operator docs.
- Synthetic load generators against the exporter. Out of scope; a real lab is the test.
- Auto-provisioning the tester's lab (SNMP credentials, devices, etc.). The tester owns their lab; we own the dashboards.

## Open questions / blockers

All resolved.

- GC stack: **`networko11ydev.grafana.net`** (instance `1544961`).
- Tester onboarding: tester cuts their own Alloy token — no registration form.
- Provisioning state: handled by `grafana-cli`; no terraform state to track.

## Sign-off

- [x] GC stack identified (D8).
- [x] Decisions D1–D8 reviewed.
- [x] PR 2 (Compose + Alloy) implemented.
- [x] PR 3 (Dashboards) implemented.
- [x] PR 4 (Makefile target) implemented.
