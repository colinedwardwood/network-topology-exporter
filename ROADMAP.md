# Roadmap

This document is forward-looking. For shipped work see [CHANGELOG.md](CHANGELOG.md);
for current open work see the [issue tracker](https://github.com/colinedwardwood/network-topology-exporter/issues)
and [milestones](https://github.com/colinedwardwood/network-topology-exporter/milestones).

## Current status

Despite the existing `v1.0.0`–`v1.3.0` tags, the project follows **pre-1.0 stability
conventions** — see the warning banner in [README.md](README.md). Upcoming
releases use the `-rc.N` suffix until the surfaces below are frozen.

Functionally the exporter is complete: SNMP / LLDP / CDP / BGP / OSPF / FDB /
IS-IS / MPLS-TE discovery, graph reconciliation, credential management,
snapshot persistence, multi-instance federation, and optional OTLP push are
all implemented and covered by unit, integration, and end-to-end tests. The
path to a real **v1.0 GA** is therefore about hardening, not feature work.

## Definition of "done" (v1.0 GA criteria)

The README pre-release banner names five surfaces that may break between
minor releases. v1.0 GA is the point at which each of them carries a
semver-significant stability promise:

| Surface | Stability promise at v1.0 |
|---|---|
| **Config schema** | YAML keys in `config/example.yaml` are part of the contract; renames and removals require a major-version bump and one minor release of deprecation overlap |
| **Metric names** | Every series in [`docs/metrics.md`](docs/metrics.md) is part of the contract; the same deprecation rule applies |
| **CLI flags** | The flags documented in `--help` and `README.md` are part of the contract |
| **Snapshot format** | The on-disk `snapshot.json` schema is versioned and migration-compatible across minor versions |
| **Federation API** | `/spoke/push` payload schema and TLS contract are part of the contract |

In addition:

- **Every discovery walker is real-device validated.** Synthetic-only test
  coverage is permitted during development but is not a v1.0 surface.
- **No silent failure modes.** Every external dependency the exporter relies
  on (per-vendor MIB shape, per-protocol assumption) has either a hard-fail
  contract or a `network_topology_*_outcome_total` counter labelled with the
  specific failure shape — operators can alert on degradation rather than
  discovering it through dashboard absence.
- **Operator runbook is sufficient for a new operator to deploy, alert on,
  and troubleshoot a production fleet** without reading source.
- **CI is green on `main`** across all jobs (golangci-lint, govulncheck,
  build + unit, integration, containerlab e2e, SR Linux e2e, helm lint).

## Release plan

### `v1.4.0-rc.1` — Lab Fixture Capture *(in progress)*

[Milestone](https://github.com/colinedwardwood/network-topology-exporter/milestone/4).
Closes the real-device validation gap inherited from v1.3.0. Until this
milestone closes, the Juniper and Nokia BGP4-V2 walkers ship marked
**experimental** and operators are directed to set `disable_v2_mib: true` on
those fleets.

- #56 — Juniper `jnxBgpM2PeerTable` captures
- #57 — Nokia `tBgpPeerTable` captures
- #58 — Cisco IOS-XE cross-validation (scaffold exists; gated `t.Skip`)
- #59 — Refactor BGP4-V2 tests to load capture files directly
- #46 — SR Linux LLDP walker returns zero rows (only chronic-red CI signal)

The long-running validation lab (`deploy/long-running-test/`) and the test
harness onboarding work already live on this milestone — see the CHANGELOG
Unreleased section.

### `v1.5.0` — Config schema freeze

Remove the deprecated keys carried for one minor release of overlap (done):

- `modules.bgp.use_v2_mib` (replaced by `disable_v2_mib` in v1.3.0) — removed
- `federation.hub.strict_device_name_matching` (replaced by
  `loose_device_name_matching` in v1.3.0) — removed
- `listen.tls_cert_file` / `listen.tls_key_file` (replaced by
  `listen.web_config_file` in v1.3.0) — removed

Audit `config/example.yaml` against `internal/config/config.go` so the
example file is the authoritative schema document. Declare the config schema
frozen against further additive change without bump.

### `v1.6.0` — Operator readiness

The runbook gaps that block a new operator from running this in production
without reading source:

- **Upgrade runbook** (`docs/operator/upgrades.md`) — per-minor-version
  section listing breaking changes, recommended rollout sequence, and the
  state to back up before upgrade.
- **SLO guidance** (`docs/operator/slos.md`) — three SLIs an operator
  should track (cycle-duration headroom, snapshot-drop rate,
  federation-spoke-down rate); recommended burn-rate alerts.
- **Stability matrix** (`docs/operator/stability.md`) — single-page authoritative restatement of the v1.0
  GA criteria above, with the actual frozen surfaces enumerated.
- **Failure-mode coverage audit** — per walker, document what's hard-fail vs
  what surfaces via a `*_outcome_total` counter, and what the operator
  alert should look like.

### `v1.7.0` — Self-observability

The exporter's own observability story is metrics + structured
logs, plus the two additions below that close the diagnostic gap operators
hit during slow cycles, federation-debug sessions, and memory-pressure
incidents. Both have shipped into the codebase:

- **OpenTelemetry tracing of the discovery cycle (#68 — done)** — per-cycle
  root span, per-target child spans, per-module child spans,
  credential-resolution spans, federation-push spans linked across hub/spoke
  via W3C trace context. Opt-in via `output.otlp.traces.enabled`; reuses the
  existing OTLP endpoint and auth. Probabilistic sampling via
  `output.otlp.traces.sample_rate` (default 0.1). Note: this traces the
  *exporter's own execution* — exporting traces of monitored network state
  remains out of scope.
- **pprof admin endpoint (#69 — done)** — `net/http/pprof` on an
  operator-configured separate listen port (`listen.debug_listen_addr`, off
  by default). Gives operators the standard
  `/debug/pprof/{profile,heap,goroutine,allocs,mutex,block}` endpoints for
  on-demand diagnosis without committing to continuous profiling.

Continuous profiling (Pyroscope / Parca) is intentionally *not* in scope
for v1.7. The pprof endpoint covers ~90% of operator diagnostic needs at a
fraction of the operational cost. If continuous profiling is later
justified, the pprof endpoint is the foundation it builds on (additive,
not refactor).

### `v2.0.0` — Streaming telemetry & federation maturity

[Milestone](https://github.com/colinedwardwood/network-topology-exporter/milestone/3).
Major-version work that touches core architecture:

- #6 — Decouple spoke push from the discovery cycle (async push queue).
  Breaking by design: inverts the push-completion-implies-cycle-completion
  invariant the current hub rejection semantics assume.
- gNMI as a first-class discovery transport alongside SNMP (the path
  `internal/discovery/bgp/bgp.go:36-39` already points at).
- Full RFC 8345 / RFC 8346 YANG topology model emission.

### `v1.0.0` — flip the banner

After v1.7 has shipped and a full release cycle has shaken out, retag the
latest pre-1.0 release as `v1.0.0` and remove the README pre-release
warning. The fact that this is a retag of an already-shipping release —
not a new build with new code — is itself the strongest stability signal
the project can give.

## Out of scope (intentional, not deferred)

Per [`docs/architecture.md`](docs/architecture.md), the following are
intentionally not on the roadmap. They are not "TODO at some point"; they
are "the wrong shape for this binary":

- **OTLP trace export of monitored network state.** Traces are a
  process-execution model, not a topology model. The exporter's *own*
  tracing (v1.7) is a different question.
- **Loki direct push.** Topology change events are log lines; ship them
  via the agent already in your stack (Promtail, Alloy, Fluentd).
- **NetBox writeback.** The correct pattern is a separate reconciliation
  process reading from Prometheus/Mimir; writeback would force this binary
  to own NetBox auth, partial-write handling, and idempotency.
- **ARP tables as a topology source.** ARP records L3 reachability, not
  physical adjacency. ARP is used internally as a MAC→IP resolution helper
  for FDB stitching only.
- **Paginated `/metrics`.** The Prometheus exposition format does not admit
  pagination; the three escape hatches in
  [`docs/operator/scale.md`](docs/operator/scale.md) (raise scrape timeout,
  shard via federation, push via OTLP) solve the same scale problem within
  the standard contract.

## How this document is maintained

- New work goes into a GitHub issue and (when scoped) onto a milestone.
- Shipped work moves out of the CHANGELOG `Unreleased` section at release
  time. This document is updated when a milestone closes, when a release
  ships, or when the "done" criteria above change.
- Significant scope decisions are recorded in
  [`docs/audits/`](docs/audits/) and referenced from the relevant release
  section above.
