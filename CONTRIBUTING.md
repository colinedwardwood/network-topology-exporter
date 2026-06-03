# Contributing

Thanks for your interest. This document covers the standard contribution flow and the clean-room development rules. The clean-room rules are binding.

## Clean-room development

The exporter is licensed under the GNU Affero General Public License v3.0 (AGPL-3.0) and targets feature parity with the discovery surface of mature copyleft monitoring projects (LibreNMS, Netdisco, NAV, OpenNMS, Observium, Cacti). Some of those projects are GPLv2-only and remain license-incompatible with AGPL-3.0, and in every case line-by-line translation of third-party source into Go creates derivative-work exposure. The project's working rule: such source may be read to extract behavioural specifications, but the implementation is written from the spec, not from the source.

By contributing you agree to the following four rules.

### 1. Source-for-spec, never source-for-code

You may read GPL-licensed network-monitoring source (LibreNMS, Netdisco, NAV, OpenNMS, Observium, Cacti, …) for the limited purpose of understanding how an existing tool behaves so that behaviour can be re-expressed as a specification under `docs/research/` in this repo or the companion [`network-o11y-dev`](https://github.com/colinedwardwood/network-o11y-dev) repo. The Go code is then written from the specification, not from the source.

The legal protection here is the abstraction-filtration-comparison test US courts apply to software copyright: ideas, algorithms, and behaviours are not copyrightable; specific expression (code structure, naming, organisation, comment phrasing, file layout) is. Specs extract the former and discard the latter.

### 2. Spec-extraction guardrails

When you read GPL source to extract a spec, don't in the spec or the eventual implementation reuse function / method / type / variable / constant names from the source, reproduce the source file's structural organisation, paraphrase comments inline, or copy literal table data such as vendor-OID mappings or SNMP profile YAML. The vendor-OID tables accumulated as community work in the source project and carry their own attribution baggage; pull them from the upstream MIBs and the IANA Enterprise Numbers registry instead.

If a PR introduces material derived from GPL source, the PR description names the source repo, file path, and commit SHA you read; links the spec under `docs/research/` (in either repo) that records the behavioural extraction; and confirms in writing that no code expression was copied or paraphrased. Default disclosure is "no GPL sources consulted."

### 3. Citation discipline

Every discovery module (LLDP, CDP, BGP, OSPF, ARP, FDB, etc.) cites its source RFC, IEEE specification, or vendor-published MIB in a top-of-file comment. If a module's behaviour can't be cited to a public specification, the module doesn't ship. Where a module's behaviour also draws from a `docs/research/` spec, cite the spec too.

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

When you borrow a structural pattern from a permissive-licensed project (the YAML-driven module pattern from `prometheus/snmp_exporter`, for example), note it in code comments with the upstream license. Not legally required for permissive licenses, but it keeps the development trail explicit:

```go
// The module-config schema below is patterned after prometheus/snmp_exporter
// (Apache 2.0). No code is copied; only the YAML shape is borrowed.
```

### Specification sources

The exporter's discovery surface is sourced from these public specifications, all independent of LibreNMS et al.:

**Protocol standards (MIBs and wire protocols):**

- **IEEE 802.1AB-2016** — LLDP. Defines lldpRemTable (OID 1.0.8802.1.1.2.1.4.1) and the LldpPortIdSubtype encoding table (Table 8-3). Primary L2 neighbour discovery source.
- **RFC 4957** — LLDP-MIB (SNMP surface for LLDP)
- **RFC 4188** — BRIDGE-MIB v2 (dot1dTpFdbTable, dot1dBasePortTable). Foundation for FDB-based L2 topology.
- **RFC 2863** — IF-MIB (ifTable, ifXTable). Required for bridge-port→ifIndex→interface-name resolution.
- **RFC 4293** — IP-MIB (ipNetToPhysicalTable / ARP table). Used as IP→MAC helper inside the FDB module only; not an independent edge source.
- **RFC 4292** — IP-FORWARD-MIB (ipCidrRouteTable). Reference for routing table structure; L3 topology derived from routing tables is explicitly out of scope — routing entries encode forwarding policy, not physical adjacency.
- **RFC 4750** — OSPF Version 2 MIB (ospfNbrTable)
- **RFC 1657** — BGP4-MIB (bgpPeerTable)
- **RFC 3418** — SNMPv2-MIB SYSTEM group (sysDescr, sysObjectID, sysUpTime, sysName). Obsoletes RFC 1907.
- **RFC 8345** — A YANG Data Model for Network Topologies. The conceptual reference for the Device/Edge graph model (nodes/links/termination-points).
- **CISCO-CDP-MIB** (vendor-published) — cdpCacheTable
- **IANA Enterprise Numbers** (https://www.iana.org/assignments/enterprise-numbers) — vendor identification from sysObjectID prefix

**Academic references (algorithm and design decisions):**

- Lowekamp, O'Hallaron, Gross — "Topology Discovery for Large Ethernet Networks", ACM SIGCOMM 2001. Foundational proof that bridged Ethernet topology can be inferred from standard Bridge MIBs. https://dl.acm.org/doi/10.1145/383059.383078
- Bejerano, Breitbart, Garofalakis, Rastogi — "Physical Topology Discovery for Large Multisubnet Networks", IEEE INFOCOM 2003. First complete multi-subnet L2 algorithm; source for the direct/indirect adjacency classification and the principle that ARP is a resolution helper, not an edge source. https://ieeexplore.ieee.org/document/1208686
- Breitbart et al. — "The NetInventory System", IEEE/ACM ToN 2004. System paper; source for the multi-source reconciliation and conflict-surfacing approach in LD-10. https://dl.acm.org/doi/abs/10.1109/TNET.2004.828963
- Donnet, Friedman — "Internet Topology Discovery: A Survey", IEEE COMST 2007. Context for BGP/OSPF as L3 logical adjacency, not physical proximity. https://hal.science/hal-01151820
- "Discovering Neighbor Devices in Computer Network", arXiv:1709.02209, 2017. Validates the BFS discovery algorithm used by the discovery loop. https://arxiv.org/pdf/1709.02209

**Prior art (pattern references, permissive-licensed):**

- **prometheus/snmp_exporter** (Apache 2.0) — scalar-GET vs table-BulkWalk split; module/auth separation; LldpPortId subtype dispatch pattern. No code copied. https://github.com/prometheus/snmp_exporter
- **kentik/snmp-profiles** (Apache 2.0) — LLDP polling interval (3600s for neighbour data); device profile YAML structure. No code copied. https://github.com/kentik/snmp-profiles
- **github.com/gosnmp/gosnmp** (BSD-2-Clause) — SNMP client library used by this project. Both snmp_exporter and ktranslate converge on this library.

If a MIB you want to use isn't in this list, propose it in a PR description with a public source link.

## Standard contribution flow

Open an issue describing the change before sending a large PR. Branch from `main` with a `<type>/<short-description>` name (`feat/ospf-mib-discovery`). Commit messages follow conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`). Run `make lint test` before pushing. Open the PR with a clear description of what changed and why, and reference the public specification source per the citation discipline above. CI must pass and one maintainer reviews before merge.

Changes to config keys, metric names and label sets, CLI flags, the snapshot schema, or the federation API are **breaking changes** — see [`docs/operator/stability.md`](docs/operator/stability.md) for the full contract and deprecation policy. Breaking changes require a changelog entry labelled `(breaking)` and, at GA, a major-version bump.

## Code style

`gofmt` / `goimports` and `golangci-lint` (config in `.golangci.yml`) are enforced in CI. Tests live next to the code they test as `internal/discovery/<module>/<module>_test.go`. Integration tests under `tests/integration/` use containerlab or simulated SNMP targets.

## License

By contributing you agree your contributions are licensed under the GNU Affero General Public License v3.0 (AGPL-3.0), the same license as the project.
