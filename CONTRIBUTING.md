# Contributing

Thanks for your interest in `network-topology-exporter`. This document covers two things:

1. The standard contribution flow (issues, branches, PRs, tests).
2. The **clean-room development commitments** that are binding on every contributor. These are not optional.

## Clean-room development commitments

`network-topology-exporter` is licensed under Apache 2.0 and targets feature parity with the discovery surface of mature GPL-licensed projects (LibreNMS, Observium, Cacti). Apache 2.0 cannot legally accept GPL contributions; "look at the source for inspiration" creates derivative-work exposure. The exporter is therefore developed clean-room against public protocol standards.

By contributing to this repository you agree to the following:

### 1. Source-code firewall

You **do not read** LibreNMS, Observium, Cacti, or any GPL-licensed network-monitoring source for any purpose, including "just to see how they do it," while contributing to this project. Capability comparison uses *operator-facing documentation and feature lists only*, which are factual and non-copyrightable. Architectural pattern borrowing is restricted to permissive-licensed projects (Apache 2.0, BSD, MIT, ISC) — primarily `prometheus/snmp_exporter`, `kentik/ktranslate`, `gosnmp/gosnmp`, and `openconfig/ygot`.

If you have read GPL-licensed network-monitoring source code in the past 12 months, please disclose it in your first PR. We can discuss scope and recency on a case-by-case basis. The point of the firewall is to keep authorship traceable to public protocol specs, not to gatekeep — but recency matters.

### 2. Citation discipline

Every discovery module (LLDP, CDP, BGP, OSPF, ARP, FDB, etc.) cites its source RFC, IEEE specification, or vendor-published MIB in a top-of-file comment. If a module's behavior cannot be cited to a public specification, the module does not ship.

Example header:

```go
// Package lldp implements LLDP neighbor discovery against LLDP-MIB.
//
// Specification sources (public, no vendor source code consulted):
//   - IEEE 802.1AB-2016 — Station and Media Access Control Connectivity Discovery
//   - RFC 4957 — LLDP MIB
//   - LLDP-MIB.mib (IETF, public)
//
// SNMP walk: 1.0.8802.1.1.2.1.4 (lldpRemoteSystemsData)
package lldp
```

### 3. Pattern provenance

When you borrow a structural pattern from a permissive-licensed project (e.g., the YAML-driven module pattern from `prometheus/snmp_exporter`), note it in code comments with the upstream license. This is not legally required for permissive licenses but it makes the development trail explicit and removes ambiguity in future audits.

Example:

```go
// The module-config schema below is patterned after prometheus/snmp_exporter
// (Apache 2.0). No code is copied; only the YAML shape is borrowed.
```

### 4. Specification sources we reference

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
