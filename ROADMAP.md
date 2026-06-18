# Roadmap

Forward-looking plan for the network topology exporter. Shipped work is
recorded in [CHANGELOG.md](CHANGELOG.md); open work is tracked in the
[issue tracker](https://github.com/grafana/network-topology-exporter/issues).

## Current status

**v1.0.0 shipped 2026-06-18** as the initial release under the Grafana
organization. The exporter discovers topology over SNMP (LLDP, CDP, BGP, OSPF,
FDB, IS-IS, MPLS-TE), reconciles a graph, exposes Prometheus metrics, supports
hub-spoke federation with optional HA, and optionally pushes OTLP signals and
YANG topology documents.

The five semver-stable surfaces (config schema, metric names, CLI flags,
snapshot format, federation API) are documented in
[`docs/operator/stability.md`](docs/operator/stability.md).

## Near-term themes

### Operator polish

- Expand vendor lab coverage and capture-based regression tests for BGP walkers.
- Harden the operator runbook: upgrade paths, SLO guidance, and failure-mode
  alerts per walker.
- SR Linux LLDP parity and additional platform validations.

### Federation maturity

- Async spoke push decoupled from the discovery cycle (issue #6).
- gNMI as a first-class discovery transport alongside SNMP.
- Fuller RFC 8345 / RFC 8346 YANG topology model emission.

## Out of scope (intentional)

Per [`docs/architecture.md`](docs/architecture.md), the following are not on
the roadmap:

- **OTLP trace export of monitored network state** — traces model process
  execution, not topology. The exporter's own discovery-cycle tracing is
  separate and already supported.
- **Loki direct push** — topology change events are log lines; ship them via
  Alloy, Promtail, or Fluentd.
- **NetBox writeback** — use a separate reconciliation process reading from
  Prometheus/Mimir.
- **ARP tables as a topology source** — ARP is used only for FDB MAC→IP
  resolution.
- **Paginated `/metrics`** — see [`docs/operator/scale.md`](docs/operator/scale.md)
  for scale mitigations within the Prometheus exposition contract.

## How this document is maintained

- New work goes into a GitHub issue and, when scoped, onto a milestone.
- Shipped work is recorded in `CHANGELOG.md` at release time.
- Significant scope decisions are recorded in [`docs/audits/`](docs/audits/).
