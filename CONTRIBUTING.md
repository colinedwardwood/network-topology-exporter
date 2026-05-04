# Contributing

Thanks for your interest in `network-topology-exporter`. This document covers two things:

1. The standard contribution flow (issues, branches, PRs, tests).
2. The **clean-room development commitments** that are binding on every contributor. These are not optional.

## Clean-room development commitments

`network-topology-exporter` is licensed under Apache 2.0 and targets feature parity with the discovery surface of mature GPL-licensed projects (LibreNMS, Netdisco, NAV, OpenNMS, Observium, Cacti). Apache 2.0 cannot legally accept GPL contributions; line-by-line translation of GPL source into Go creates derivative-work exposure. The exporter is therefore developed clean-room: GPL source may be read to extract behavioural specifications, but the implementation is written from those specs against public protocol standards.

By contributing to this repository you agree to the following:

### 1. Source-for-spec, never source-for-code

You **may** read GPL-licensed network-monitoring source (LibreNMS, Netdisco, NAV, OpenNMS, Observium, Cacti, …) for the limited purpose of understanding how an existing tool behaves so that behaviour can be re-expressed as a specification under `docs/research/` in the companion [`network-o11y-dev`](https://github.com/colinedwardwood/network-o11y-dev) repo (or in this repo's `docs/`).

You **do not** then implement the Go code by translating the GPL source line-by-line. The Go code is written *from the specification*, not from the source. The legal protection is the abstraction-filtration-comparison test US courts apply to software copyright: ideas, algorithms, and behaviours are not copyrightable; specific expression (code structure, naming, organisation, comment phrasing, file layout) is. Specs extract the former; the implementation discards the latter.

### 2. Spec-extraction guardrails

When you read GPL source to extract a spec, do not in the spec or the eventual implementation:

- reuse function, method, type, variable, or constant names from the source,
- reproduce the source file's structural organisation (function order, helper grouping, control-flow shape),
- paraphrase comments inline,
- copy literal table data such as vendor-OID mappings or SNMP profile YAML — these accumulated as community work in the source project and have their own attribution requirements; pull them from the upstream MIBs and the IANA Enterprise Numbers registry instead.

If a PR introduces material that you derived from GPL source, the PR description must:

- name the source repo, file path, and commit SHA you read,
- link the spec under `docs/research/` (or in `network-o11y-dev/docs/research/`) that records the behavioural extraction,
- confirm in writing that no code expression was copied or paraphrased.

If you have not read any GPL network-monitoring source while preparing the PR, say so. The default disclosure is "no GPL sources consulted."

### 3. Citation discipline

Every discovery module (LLDP, CDP, BGP, OSPF, ARP, FDB, etc.) cites its source RFC, IEEE specification, or vendor-published MIB in a top-of-file comment. If a module's behaviour cannot be cited to a public specification, the module does not ship. Where the module also implements behaviour synthesised from a `docs/research/` spec, cite the spec too.

Example header:

```go
// Package lldp implements LLDP neighbor discovery against LLDP-MIB.
//
// Specification sources (public protocol specs only):
//   - IEEE 802.1AB-2016 — Station and Media Access Control Connectivity Discovery
//   - RFC 4957 — LLDP MIB
//   - LLDP-MIB.mib (IETF, public)
//
// Behavioural decisions sourced from the LD-10 reconciliation spec
// (network-o11y-dev/docs/research/topology-reconciliation-prior-art.md).
//
// SNMP walk: 1.0.8802.1.1.2.1.4 (lldpRemoteSystemsData)
package lldp
```

### 4. Pattern provenance

When you borrow a structural pattern from a permissive-licensed project (e.g., the YAML-driven module pattern from `prometheus/snmp_exporter`), note it in code comments with the upstream license. This is not legally required for permissive licenses but it makes the development trail explicit and removes ambiguity in future audits.

Example:

```go
// The module-config schema below is patterned after prometheus/snmp_exporter
// (Apache 2.0). No code is copied; only the YAML shape is borrowed.
```

### 5. Specification sources we reference

The exporter's discovery surface is sourced from these public specifications, all of which are independent of LibreNMS et al.:

- **RFC 4957** (LLDP-MIB), **IEEE 802.1AB** (LLDP)
- **RFC 4188** (BRIDGE-MIB), **RFC 2863** (IF-MIB), **RFC 4293** (IP-MIB), **RFC 4292** (IP-FORWARD-MIB)
- **RFC 4750** (OSPF-MIB), **RFC 1657** (BGP4-MIB)
- **CISCO-CDP-MIB** (vendor-published)
- Vendor-specific OSPF / BGP / EIGRP MIBs (vendor-published)
- **IANA Enterprise Numbers** for vendor identification

If a MIB you want to use isn't in this list, propose it in a PR description with a public source link.

## Standard contribution flow

1. Open an issue describing the change before sending a large PR.
2. Branch from `main`, name your branch `<type>/<short-description>` (e.g. `feat/ospf-mib-discovery`).
3. Commit messages follow conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).
4. Run `make lint test` before pushing.
5. Open a PR with a clear description of *what* changed and *why*. Reference the public specification source(s) per the citation discipline above.
6. CI must pass and one maintainer must review before merge.

## Code style

- `gofmt` / `goimports` — enforced in CI.
- `golangci-lint` with the config in `.golangci.yml`.
- Tests live next to the code they test. Use `internal/discovery/<module>/<module>_test.go`.
- Integration tests under `tests/integration/` use containerlab or simulated SNMP targets.

## License

By contributing you agree your contributions are licensed under the Apache License 2.0, the same license as the project.
