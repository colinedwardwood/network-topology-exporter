# Changelog

All notable changes are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0] - 2026-06-18

Initial release under the [Grafana](https://github.com/grafana) organization.

### Added

- SNMP discovery for LLDP, CDP, BGP (RFC 4273 + vendor MIBs), OSPF, FDB,
  IS-IS, and MPLS-TE, with graph reconciliation across protocols.
- Prometheus metrics at `/metrics` (~50 series) for device inventory, topology
  edges, discovery outcomes, federation health, and operational signals.
- Structured JSON log lines for topology change events and walker outcomes.
- Versioned on-disk snapshot so `/metrics` serves the previous graph on restart.
- Optional OTLP push for topology metrics, change-event logs, and (opt-in)
  discovery-cycle traces.
- Optional RFC 8345 / RFC 8346 YANG topology output at `/topology/yang`.
- Multi-instance federation: standalone, hub, spoke, and uncoordinated roles
  with mTLS spoke→hub push and opt-in hub high availability (Kubernetes lease
  leader election).
- Credential profiles (SNMP v2c/v3) with env-var indirection, trial rate
  limiting, and profile invalidation.
- Kubernetes deployment paths: Helm chart and Kustomize overlays (standalone,
  hub, spoke).
- Multi-arch container images (`linux/amd64`, `linux/arm64`) on GHCR with
  cosign keyless signing, SLSA provenance attestations, and offline release
  tarballs.
- Containerlab-based vendor labs (Cisco, Arista, Juniper, Nokia) and SR Linux
  e2e coverage.

### Security

- Supply-chain hardened CI: pinned actions, cosign-signed release artefacts,
  SPDX SBOM, and govulncheck in the release pipeline.
