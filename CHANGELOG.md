# Changelog

## Unreleased — planned as v1.4.0-rc.1

Upcoming releases adopt `-rc.N` suffixes to signal that the public
surface is intentionally not frozen — see the pre-release notice in
the README. The lab-fixture-capture work previously slotted for
v1.3.1 lands under this milestone instead, renamed to
[v1.4.0-rc.1 — Lab Fixture Capture](https://github.com/colinedwardwood/network-topology-exporter/milestones).

### Security & correctness (post-merge hardening)

- **`/admin/rediscover` auth gate now requires real client authentication.** The
  endpoint previously enabled itself whenever `listen.web_config_file` was set —
  but a TLS-only web-config encrypts without authenticating the caller, so any
  HTTPS-only deployment exposed the privileged forced-walk endpoint
  unauthenticated. It now parses the web-config and enables the endpoint only
  when `basic_auth_users` is set or a client-cert-requiring `client_auth_type`
  (`RequireAnyClientCert`/`RequireAndVerifyClientCert`) is configured; otherwise
  it stays `403` (fails closed on an unreadable/unparseable config). Regression
  test `TestWebConfigHasClientAuth`.
- **`/admin/rediscover` no longer monopolises the discovery cycle.** The forced
  walk now acquires the cycle mutex **per target** instead of around the whole
  batch, so a large or slow/unreachable batch can no longer stall the regular
  discovery cycle for the full request — bounding the contiguous hold to one
  device's walk while still serialising each walk against the cycle.
- **OTLP no longer reports phantom topology.** The edge/device metrics are now
  **observable gauges** that report exactly the current graph on each
  collection. The previous synchronous gauges retained every recorded
  attribute-set under cumulative temporality, so a removed edge/device would
  linger at the OTLP receiver indefinitely. Regression test
  `TestPushGraphDropsStaleEdges`.
- Removed the dead no-op `Config.EmitDeprecationWarnings` stub (no deprecated
  keys remain after v1.5.0; a future deprecation re-introduces a real one).
- Documented the topology dashboard's **Grafana SQL Expressions** requirement
  in `dashboards/test-harness/README.md`.

### Configuration

- **#63 — Config schema parity audit.** `config/example.yaml` is now the
  authoritative schema document for all accepted YAML keys (ROADMAP v1.5.0
  freeze). The following keys were present in the struct but absent from the
  example: `listen` block (`addr`, `web_config_file`), `modules.isis.enabled`,
  `modules.mpls_te.enabled`, `credentials.profiles[].retries`,
  `credentials.profiles[].context_name`, and
  `federation.hub.min_push_interval`. All are now documented in the example
  (commented-out where optional, with defaults and valid ranges). A new test
  `TestExampleConfigLoadsCleanly` loads `config/example.yaml` under
  `KnownFields(true)` on every CI run to keep the example and the struct in
  sync. `CONTRIBUTING.md` gains a "Config schema sync" rule. Closes #63.

### Configuration (breaking)

- **#60 (breaking — config schema)** — `modules.bgp.use_v2_mib` removed. The key was deprecated in v1.4.0 and carried with a startup warning for one release. Configs that set `use_v2_mib` now fail to load with a parse error. Use `modules.bgp.disable_v2_mib` (default `false` = v2 walkers enabled; set `true` to fall back to RFC 4273-only behaviour). Closes #60.
- **#61 (breaking — config schema)** — `federation.hub.strict_device_name_matching` removed. The key was deprecated in v1.4.0 and carried with a startup warning for one release. Configs that set `strict_device_name_matching` now fail to load with a parse error. Use `federation.hub.loose_device_name_matching` (default `false` = strict matching; set `true` to enable domain-suffix stripping for single-site mixed short/FQDN deployments). Closes #61.
- **#62 (breaking — config schema)** — `listen.tls_cert_file` and `listen.tls_key_file` removed. The keys were deprecated in v1.4.0 (replaced by `listen.web_config_file`) and carried with a startup warning for one release. Configs that set either key now fail to load with a parse error. Migrate to `listen.web_config_file` pointing at a Prometheus exporter-toolkit web-config YAML (set `tls_server_config.cert_file` / `key_file` to the same paths); the toolkit adds basic_auth and mTLS support beyond what the legacy fields offered. The `srv.ListenAndServeTLS` code path is also removed from the application — all TLS is now handled by `web.ListenAndServe`. Closes #62.

### Licensing

- **Relicensed from Apache-2.0 to AGPL-3.0.** The project is now distributed
  under the GNU Affero General Public License v3.0 (`LICENSE` replaced with the
  full AGPL-3.0 text). README, `CONTRIBUTING.md`, the OCI image label
  (`org.opencontainers.image.licenses`), and the LibreNMS comparison were
  updated to match. Note the consequences of moving to a strong network-use
  copyleft: operators who modify the exporter and offer it over a network must
  make their modified source available (AGPL §13). The clean-room contribution
  rule is unchanged, but its rationale was corrected — AGPL-3.0 *is*
  GPLv3-compatible, so the policy now rests on avoiding derivative-work
  entanglement (incl. GPLv2-only sources) rather than blanket GPL
  incompatibility. References to `snmp_exporter`/`kentik` (Apache-2.0) describe
  those upstream projects and are unchanged.

### Features

- **Enriched topology Node Graph dashboard.** Reworked the "02. Network
  Topology" dashboard (`dashboards/test-harness/topology-graph.json`) so each
  node carries protocol and connectivity context drawn entirely from the
  exporter's existing `network_topology_device_info` / `edge_info` metrics:
  the node ring is split into proportional, per-discovery-protocol arcs (LLDP,
  CDP, BGP, OSPF, IS-IS, FDB, MPLS-TE, configured) that sum to 1; the secondary
  stat shows the device's link count (degree, counted across both endpoints);
  edges label the `src_port → dst_port` cabling with discovery protocol,
  link kind, and direction in the hover tooltip. Per-device protocol ratios are
  assembled with a SQL Expression (`__expr__`) that pivots a single per-protocol
  ratio query into the `arc__*` columns the Node Graph colours — so the panel
  needs no new metrics. An "About this dashboard" panel explains how to read the
  graph and what richer SNMP data could add (e.g. link speed → edge thickness).
  Note: the panel relies on Grafana **SQL Expressions**.

- **Long-running validation lab** — Added `deploy/long-running-test/`, a
  continuously-running harness. Six containerlab nodes (`spine1, spine2,
  leaf1..leaf4`) deploy once with pinned management IPs in a dedicated
  `172.30.0.0/24` subnet. An hourly mutator reconciles veth links
  between them per topology (`topo-1` chain → `topo-2` cross-link →
  `topo-3` ring → `topo-4` CLOS) without restarting containers. The
  exporter sees real edge add/remove events on the hour boundary,
  exercising the same reconciliation code path it exists to validate.
  Mutator emits structured JSON events to stdout; Alloy ships them to
  Loki labelled `tester_id=long-running-lab, job=long-running-mutator`.

### Observability

- **#98–#102 — Per-walker observability for the non-BGP discovery walkers.**
  Closes the per-walker failure-mode visibility gaps identified in the #67
  audit: the LLDP, CDP, OSPF, and FDB walkers move from SILENT to FULL coverage,
  and the IS-IS IPv6 skip and SNMP system-walk content gaps are closed.
  - **New `network_topology_walker_outcome_total{walker, outcome}` (#98)** — the
    generic, non-breaking sibling of `network_topology_bgp_walker_outcome_total`
    (which is **not** renamed). `walker ∈ {lldp, cdp, ospf, fdb}`;
    `outcome ∈ {edges, mib_unimplemented, no_neighbours, walker_drift, error}`,
    mirroring the BGP four-bucket semantics — a zero-edge result can now be told
    apart as "feature absent" (`mib_unimplemented`, do not page), "feature up,
    nothing to report" (`no_neighbours`), or "decoder broken on this firmware"
    (`walker_drift`, page-level).
  - **New decode-issue reasons on `network_topology_discovery_decode_issues_total` (#99)** —
    the same four walkers now report per-row decode rejections that were
    previously a silent `continue`/`slog.Debug`: `lldp`
    (`chassis_subtype_invalid`, `port_subtype_invalid`, `chassis_mac_bad_length`,
    `port_mac_bad_length`, `chassis_addr_malformed`), `cdp` (`index_unparseable`,
    `empty_device_id`), `ospf` (`oid_suffix_malformed`, `nbr_ip_undecodable`),
    and `fdb` (`bridge_port_index_invalid`, `ifindex_unmapped`).
  - **New FDB degraded reasons on `network_topology_discovery_degraded_total` (#100)** —
    `{module="fdb", reason="qbridge_walk_failed"}` (Q-BRIDGE walk errored and the
    B-MIB produced no usable entries; guarded against false alerts when the B-MIB
    already had entries or for clean B-MIB-only devices) and
    `{module="fdb", reason="vlan_walk_failed"}` (a per-VLAN community walk failed,
    labelled by reason only, not by VLAN id). Both emit via a new direct
    `RecordDegraded` module sink so they fire even with zero edges.
  - **New `network_topology_system_walk_anomaly_total{reason}` (#101)** — a
    low-cardinality counter (closed set `{empty_sysname, unknown_vendor}`, no
    device/IP/OID label) flagging content anomalies in an otherwise-successful
    system GET: `empty_sysname` (device ID falls back to the management IP) and
    `unknown_vendor` (sysObjectID unresolved, so the vendor BGP4-V2 walker is
    skipped — BGP still falls through to the RFC 4273 path).
  - **New IS-IS degraded reason `unsupported_ip_version` (#102)** —
    `{module="isis", reason="unsupported_ip_version"}` fires once per walk when
    IPv6 IS-IS adjacency rows are skipped (IPv4 edges unaffected). Emitted via
    the direct `RecordDegraded` sink so it fires even on an IPv6-only device with
    zero IPv4 edges, reviving the previously-dead
    `DegradedReasonUnsupportedIPVersion` constant.

  `docs/metrics.md` and `docs/operator/failure-modes.md` updated to match (the
  failure-modes coverage table now reads FULL/observable for LLDP, CDP, OSPF,
  FDB, the IS-IS IPv6 skip, and the system walk).

- **#68 — Opt-in OpenTelemetry tracing of the discovery cycle (v1.7.0 work).**
  The exporter can now emit OTel traces of its own discovery cycle. A new
  `output.otlp.traces` block (`enabled`, default `false`; `sample_rate`, default
  `0.1`, validated to `[0,1]`) turns it on. Tracing reuses the **#82 OTLP SDK
  path** — the same `TracerProvider`-style SDK, the existing
  `output.otlp.endpoint`, `output.otlp.protocol`, and transport — so it gets no
  endpoint of its own. Spans nest per cycle: `discovery.cycle` →
  `target.poll` → (`credentials.resolve`, `<module>.walk` for lldp/cdp/fdb/ospf/
  bgp/isis/mpls_te), plus `graph.reconcile`, and on a federation spoke
  `spoke.push`. The spoke injects a W3C `traceparent` into its outbound push so
  the hub's `hub.handlePush` span continues the same trace (TraceContext set as
  the global propagator). Head sampling is
  `ParentBased(TraceIDRatioBased(sample_rate))`, keeping each trace whole. When
  disabled, no provider is installed and the instrumentation resolves to the
  OTel no-op tracer at effectively zero cost. See `docs/operator/tracing.md`.

### Security

- **Fuzz coverage for SNMP parsers and index decoders.** Sixteen new
  `Fuzz*` harnesses cover every parser that touches device-controlled
  bytes: the BGP4-V2 vendor index decoders (`decodeCiscoCbgpPeer2Index`,
  `decodeAristaBgp4v2Index`, `decodeBgp4v2InstanceIndex`), the RFC 4001
  InetAddress reader (`readInetAddrAt`), the generic OID splitter
  (`splitOIDParts`), the FDB Q-BRIDGE index parser, the OSPF neighbour-
  OID parser, the MPLS-TE tunnel-suffix parser, the LLDP port-ID and
  chassis-ID decoders, and the SNMP PDU type-coercion helpers
  (`PDUString`, `PDUBytes`, `PDUInt`, `PDUIntStrict`, `PDUIPv4`). CI
  runs each target for 30 s per push and 10 min nightly via the new
  `fuzz-nightly` workflow.
  Initial pass found a real panic in `readInetAddrAt`: a negative `pos`
  argument hit `parts[pos]` with no lower bound check (Issue would
  surface only on hostile bytes since current callers always pass 0
  or 1). Fixed in the same commit; the failing input is now a
  permanent regression case at
  `internal/discovery/bgp/testdata/fuzz/FuzzReadInetAddrAt/`. Closes #70.
- **Templatized harness credentials** — Reworked
  `deploy/long-running-test/alloy-config.alloy` so all Grafana Cloud tokens
  and user IDs come from environment variables (see `.env.example`). Added
  `.gitignore` rules for SSH keys (`id_ed25519`), `.env` files, and TLS
  material (`*.pem`, `*.key`) so this class of file cannot be accidentally
  committed.
- **Signed release artefacts.** Every release (container image + Go
  binaries) is now signed with cosign keyless signing and carries a SLSA
  build-provenance attestation produced via `actions/attest-build-provenance`.
  Operators verify with `cosign verify` / `gh attestation verify`; commands
  documented in `docs/operator/security.md` § "Verifying release artefact
  provenance." Closes the supply-chain attestation gap identified in the
  May 2026 architectural review.

### Discovery

- **Nokia SR Linux LLDP-via-SNMP marked as not supported by the vendor.**
  Local reproduction (2026-05-25) against `ghcr.io/nokia/srlinux:24.7.2`
  with containerlab's default SNMP config confirmed that SR Linux 24.x
  does not implement the standard IEEE 802.1AB LLDP MIB at
  `1.0.8802.1.1.2` — every probe returns `No Such Object available on
  this agent at this OID`. The classic TIMETRA-LLDP-MIB at
  `1.3.6.1.4.1.6527.3.1.2.43` (used on SR-OS) is also absent. LLDP data
  on SR Linux is exposed via gNMI / JSON-RPC at `/system/lldp` only.
  The exporter's LLDP walker code is IEEE 802.1AB-2016 compliant; this
  is a vendor coverage gap, not a walker bug. The four LLDP tests in
  `tests/e2e/srl/` now skip with a clear explanation pointing operators
  at gNMI as the future path (v2.0.0). `TestSNMPSystemWalk` continues
  to run — the SNMP system group is exposed on SR Linux and works as
  expected. The classic SR-OS family is unaffected. `troubleshooting.md`
  § 2 gains a Nokia-fleet note describing the limitation. Closes #46.

- **Cisco IOS-XE cross-confirmation of `ciscoCbgpPeer2Spec` walker.** The
  walker was already byte-level validated against Cisco IOL 17.12.1 on
  2026-05-16 (see `TestWalkVendorCisco` in `bgp_v2_test.go`); this entry
  records cross-confirmation against real Cisco IOS-XE hardware via a
  colleague-supplied snmpwalk on 2026-05-30. Capture covered four BGP
  sessions (two IPv4, two IPv6, all in state established) across columns
  3-29 of `cbgpPeer2Table` at `1.3.6.1.4.1.9.9.187.1.2.5`. Column numbers
  (3=state, 11=remoteAs) and index encoding (`1.4.<v4>` / `2.16.<v6>`)
  matched IOL byte-for-byte — no walker drift between the IOL emulator
  and real IOS-XE. The IOS-XE scaffold test (`bgp_v2_iosxe_test.go`)
  was removed since the IOL fixture already provides CI-level regression
  protection and a near-duplicate IOS-XE fixture would add maintenance
  cost without coverage. README BGP vendor-coverage table updated to
  describe what was validated where. Closes #58.

### Operability

- **Admin endpoint for forced out-of-cycle re-discovery.** New
  `POST /admin/rediscover` on the existing listen port accepts a JSON body
  `{"targets":["10.0.0.1","10.0.0.2"]}` and triggers an immediate SNMP walk
  against the listed IPs, so an operator who has just fixed a device's SNMP
  config (community, ACL, snmpd) can confirm the fix without waiting up to a
  full `discovery.interval`. The response reports a per-target outcome
  (`success` / `timeout` / `auth_failure` / `out_of_scope` / `error`) and the
  resulting edge count. The endpoint is **privileged**: it is only exposed when
  `listen.web_config_file` configures `basic_auth`/mTLS, and returns **403**
  otherwise — unlike `/metrics`, the default no-auth ground state does not
  expose it. Targets outside `discovery.scope.cidr_allow_list` are rejected
  with HTTP 400 (no scope expansion via the admin call, LD-11). The forced
  walk is serialised against the regular discovery cycle (shared cycle mutex)
  so it cannot race or corrupt a running cycle; it reports results to the
  caller but does not itself publish into the live graph — the corrected
  device's edges land in `/metrics` on the next regular cycle. New audit metric
  `network_topology_admin_rediscovery_total{outcome}`. See
  `docs/operator/troubleshooting.md` § 4a. Closes #73.

### Documentation

- **Per-walker failure-mode coverage audit (#67).** New
  `docs/operator/failure-modes.md` — a dependency × failure-mode ×
  operator-signal matrix for every discovery walker (LLDP, CDP, OSPF, IS-IS,
  BGP, FDB, MPLS-TE, the SNMP system walk, and the ARP enrichment sub-walk),
  every claim traced to a `file:line`. Records which walkers meet the GA
  observability criterion (BGP via `bgp_walker_outcome_total`; IS-IS, MPLS-TE,
  and the system walk via the hard-fail/degraded/sub-reason split) and which
  still degrade silently (LLDP, CDP, OSPF, FDB lack a
  `mib_unimplemented`/`no_peers`/`walker_drift` outcome split and drop rows with
  bare `continue`+`slog.Debug`). Cross-referenced from `docs/architecture.md`
  and `docs/operator/troubleshooting.md`. No code changes — audit + docs only.
- **RFC 8345 YANG topology mapping (design reference for #75).** New
  `docs/operator/yang-topology.md` defines how the reconciled graph maps onto
  the RFC 8345 / RFC 8346 YANG models (Device → `node` + termination-points,
  Edge → unidirectional `link`s, exporter provenance via an `ntx-topology`
  augmentation), with a worked example and the known gaps (no router-id/prefix
  collection yet). The YANG output path + `pyang`/`yanglint` CI remain v2.0.0
  work; this is the mapping contract that implementation will follow.
- **Threat model document** — New `docs/operator/threat-model.md` with a
  STRIDE matrix scoped to the binary's role on the management plane. Names
  assets, threats, mitigations shipping today (including the new PDU fuzz
  coverage above), and the one remaining known gap tracked as a follow-up
  (#72 per-device PDU rate limit).
- **Hub HA patterns** — New section in `docs/operator/federation.md` § "Hub
  high-availability patterns" covering three workarounds operators have today
  (cold standby, active-passive LB with shared snapshot, dual independent
  stacks). Real native HA is v2.0 work; tracked at #71.
- Added a runbook at `deploy/long-running-test/README.md` covering the
  mutation schedule, credential layout, and known limitations of the lab.
- README now lists both test environments (`deploy/test-harness/` and
  `deploy/long-running-test/`).
- **Test-harness onboarding now uses the published GHCR image.**
  `deploy/test-harness/docker-compose.yml` pins
  `ghcr.io/colinedwardwood/network-topology-exporter:1.3.1-rc1` so every
  tester runs the same build, and the "00. Getting Started" dashboard
  plus `deploy/test-harness/README.md` now instruct testers to
  `docker compose pull` instead of running a local `docker build`.
  Cuts cold-start time from minutes (Go toolchain + multi-arch build)
  to seconds (image pull).
- **Removed OTLP/Tempo references from tester onboarding.** The default
  harness ships metrics + logs only; the trace-related access-policy
  scope, Tempo username, and OTLP push endpoint have been dropped from
  the Getting Started dashboard to stop confusing first-time testers
  with a path the harness doesn't actually exercise.

### Documentation

- **SECURITY.md** (closes #76) — New top-level `SECURITY.md` with: supported-version matrix (current minor + previous minor best-effort), private reporting via GitHub Security Advisories, 72 h acknowledge / 7 day status-update SLA, and coordinated disclosure and credit policy. Cross-referenced from `README.md`, `docs/operator/security.md`, and `docs/operator/threat-model.md`. Note for maintainer: Private Vulnerability Reporting must be enabled in repo **Settings → Security → Code security and analysis** before the "Report a vulnerability" button is active.

- **GitHub issue templates** (closes #77) — Three YAML-form templates in `.github/ISSUE_TEMPLATE/`: `bug.yml` (version dropdown, deploy-mode dropdown, config snippet, observed/expected, logs, repro steps), `feature.yml` (problem, proposed solution, alternatives, acceptance criteria), and `config.yml` (`blank_issues_enabled: false` + contact link to the Security Advisory form for vulnerability reports).

- **Supported-platforms matrix** (closes #80) — New `docs/supported-platforms.md` recording per-vendor/OS real-device validation status for each discovery module. Covers Cisco IOS (IOL 17.12.1), Cisco IOS-XE (cross-confirmation via #58), Arista cEOS 4.36, Nokia SR Linux 24.7.2 (LLDP-via-SNMP not supported, #46 caveat documented), Nokia SR-OS (#57 pending), and Juniper (#56 pending). Experimental walkers are clearly marked. Cross-referenced from `README.md` and `docs/standards.md`.

- **Vendor comparison matrix** (closes #81) — New `docs/comparisons/matrix.md` with rows per feature and columns for network-topology-exporter, LibreNMS, SuzieQ, Nautobot, OpenNMS, and SolarWinds NPM. Includes a note on what is needed alongside the exporter for a complete operational stack (NCM, IPSLA, alert correlation). The existing `docs/comparisons/librenms.md` deep bilateral read is linked from the matrix. Matrix linked from README "Why this exists" section.

- **Stability matrix / GA criteria** (closes #66) — New `docs/operator/stability.md` enumerating the five frozen surfaces (config schema, metric names, CLI flags, snapshot format v3, federation API), the semver contract per surface, and measurable acceptance criteria for each structural v1.0 commitment (real-device walker validation, no silent failure modes, operator runbook sufficiency). Documents the deprecation policy (≥1 minor overlap, startup WARN, CHANGELOG note). Cross-referenced from `README.md`, `ROADMAP.md`, and `CONTRIBUTING.md`.

- **Operator upgrade runbook (closes #64).** New `docs/operator/upgrades.md` with per-minor-version sections back to v1.1, covering breaking changes, backup steps (snapshot file at `snapshot.path`, credential cache embedded in snapshot), config-migration commands, and the recommended hub-first rollout order for hub/spoke fleets. Documents that there is no version negotiation in the federation protocol and that mixed-version operation during rolling upgrades is limited to one `spoke_timeout` window. Cross-referenced from `docs/operator/troubleshooting.md` and the README "Stability and security" section.

- **SLO guidance (closes #65).** New `docs/operator/slos.md` defining three SLIs with Google SRE Workbook-style multi-window multi-burn-rate alerts and copy-pasteable PromQL. SLI 1 (cycle-duration headroom: p99 < 70% of `discovery.interval` over 30 days) uses `network_topology_discovery_cycle_duration_seconds`. SLI 2 (snapshot-drop rate: zero drops over 7 days) uses `network_topology_snapshot_drops_total`. SLI 3 (federation spoke-down rate: ≤1 eviction per hub per 7 days) uses `network_topology_federation_spoke_last_push_timestamp_seconds`. All metric names verified against `docs/metrics.md` and source. Notes which SLIs are standalone-validatable (SLI 1 and 2) vs. requiring a hub/spoke deployment (SLI 3). Cross-referenced from the README "Stability and security" section and the Helm chart PrometheusRule.

### Added

- Vendor lab capture toolkit at `scripts/colleague-capture.sh` and `scripts/redact-snmp-capture.py` — lets a colleague with vendor hardware produce a self-diagnosing snmpwalk tarball in one command, then redact the result before fixture conversion. Lab dirs `lab/cisco-iosxe-bgp/` (#58), `lab/juniper-jnxbgp/` (#56), and `lab/nokia-srbgp/` (#57) all shipped with switch-side v2c + v3 prep and a thin shim into the shared wrapper.

### Internal

- **OTLP output migrated to the official OpenTelemetry Go SDK (#82).** The
  hand-rolled OTLP/HTTP+JSON encoder in `internal/output/otlp` was replaced with
  a `metric.MeterProvider` + `log.LoggerProvider` built from the OTel SDK
  (`go.opentelemetry.io/otel/sdk/metric`, `.../sdk/log`, and the
  `otlpmetric{http,grpc}` / `otlplog{http,grpc}` exporters). The SDK now owns the
  proto3 wire mapping, the schema URL, and transport-level retry/backoff; no
  `json.Marshal` of OTLP payloads remains in the binary. Config and API are
  preserved: the `Exporter.PushGraph` / `Exporter.PushChanges` behaviour, the
  emitted metric names (`network_topology_edge_info`,
  `network_topology_device_info`), the topology-change log records, and all
  existing config keys (`output.otlp.endpoint`, `timeout`, `heartbeat_cycles`)
  are unchanged — a deployment that only sets `endpoint` keeps working (proven by
  `TestNewDefaultsHTTPProtobuf`).
- **Wire-format change (OTLP): JSON → protobuf.** The pre-v1.5.0 hand-rolled
  path emitted OTLP/HTTP + JSON; the SDK emits **protobuf** (the OTLP default and
  the only encoding the OTel Go SDK exporters implement). OTLP receivers must
  accept protobuf — virtually all do. There is no JSON option, so no
  `output.otlp.encoding` key exists.
- **New OTLP config key: `output.otlp.protocol` (http|grpc), default `http`.**
  Adds OTLP/gRPC support alongside the existing HTTP transport.

First release candidate for the 1.3.1 milestone. Includes the new tester-onboarding stack and critical codebase health fixes.

### Features

- **D50 (Test Harness)** — Full tester-deployable stack added in `deploy/test-harness/`. Includes a turnkey `docker-compose.yml` with the exporter and Grafana Alloy, plus curated Grafana dashboards for topology visualization and health monitoring. Closes #45.
- **D51 (Makefile Automation)** — Added `make dashboards-apply` to synchronize JSON dashboards from the repository to Grafana Cloud via `grafana-cli`.

### Bug Fixes

- **D52 (Static Analysis)** — Resolved 16 `SA5011` possible-nil-dereference warnings across the test suite by ensuring test termination after fatal failures.
- **D53 (E2E Stability)** — Fixed `permission denied` errors in E2E tests by explicitly configuring local snapshot paths for test binaries.
- **D54 (Scope Guard)** — LLDP and CDP modules now support catch-all CIDRs (`0.0.0.0/0`) in the `cidr_allow_list`, allowing discovery of non-IP neighbors (like MAC-only containerlab nodes) when explicitly permitted by the operator.
## v1.3.0 — 2026-05-17

Post-audit hardening. 25 numbered changes (D22–D46) covering 28 closed
issues — the May 2026 adversarial review's findings plus the
adjacent-failure issues uncovered during remediation. Themes: SNMP credential zeroization, `/metrics`
authentication via the Prometheus exporter-toolkit, BGP walker structural
rewrite informed by real-device captures, partitioned status counters,
typed reject reasons and Edge enums, rate-limited chronic Warn logs, and
measured scale benchmarks. Several breaking changes — operators upgrading
from v1.2.0 should read the breaking-change entries before deploying.

### Configuration (breaking)

- **D40 (breaking — config schema)** — Two `*bool` pointer-typed config fields replaced with positive-default `bool` fields. `federation.hub.strict_device_name_matching` (`*bool`, default true via pointer-deref) is now `federation.hub.loose_device_name_matching` (`bool`, default false). `modules.bgp.use_v2_mib` (default true) is now `modules.bgp.disable_v2_mib` (default false). Default runtime behaviour is unchanged in both cases — strict matching and the v2/vendor BGP walkers remain on by default. The legacy YAML keys are still accepted with a startup deprecation warning for one minor release; remove them or migrate to the new keys before v1.5.0. Eliminates the nil-deref hazard at `cmd/topology-exporter/main.go` (which was an unconditional pointer deref) and the defensive nil-check at `internal/federation/hub.go`. Originally milestoned v1.4.0 (OTLP modernization) and landed early to ship config-schema breakage in a single batch. Closes #10.
- **D22** — `modules.arp.enabled` is now honored at runtime; the default is **false** to match every other module toggle. Existing deployments that relied on the previous "always on" behavior must set `modules.arp.enabled: true` to keep ARP-based MAC→IP enrichment. ARP enrichment is only useful when `modules.fdb.enabled: true`; the exporter logs a startup warning when FDB is enabled without ARP.
- **D23** — `federation.hub.strict_device_name_matching` now defaults to **true**. Out-of-scope neighbour matching at the hub no longer strips FQDN suffixes by default, preventing silent cross-DC hostname collisions (e.g. `core-sw.dc1` colliding with `core-sw.dc2`). The field is now pointer-typed in Go: an omitted YAML key uses the safe strict default, an explicit `false` is still honoured. Single-site deployments that previously relied on FQDN/short-form reconciliation against the same physical device must opt in via the new key — set `loose_device_name_matching: true` (see D40 below; the legacy `strict_device_name_matching` form is accepted with a deprecation warning). Addresses docs/audits/2026-05-architectural-review.md §2.3 recommendation #1.

### Discovery

- **D38 (breaking — metric label value set)** — `network_topology_bgp_walker_outcome_total{outcome}` adds a new value `walker_drift` and the existing values are now declared as named constants. Previously the vendor walker recorded `outcome=no_peers` for two operationally different cases: (a) PDUs arrived, decoded cleanly, but no peer reached `bgpStateEstablished` (BGP genuinely broken — should page), and (b) PDUs arrived but every row was rejected by the vendor's `decodeIndex` (our walker is broken on this vendor's MIB — same severity, completely different root cause). Case (b) now records `outcome=walker_drift`. Operators alerting on `rate(network_topology_bgp_walker_outcome_total{outcome="no_peers"}[5m])` and relying on it firing for all-rows-malformed must add a parallel alert on `outcome="walker_drift"`. The per-row `outcome=malformed_index` counter is unchanged. The outcome label values (`edges`, `mib_unimplemented`, `no_peers`, `malformed_index`, `walker_drift`, `error`) are now declared as package-level constants in `internal/discovery/bgp/bgp.go` to prevent typo-induced new series. Closes #26, #27.

### Security

- **D45** — `/metrics` endpoint now supports authentication and full mTLS via Prometheus exporter-toolkit. New `listen.web_config_file` field points at a [web-config YAML](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) — the same schema snmp_exporter / node_exporter / blackbox_exporter use. Three modes: server TLS only, basic_auth + server TLS, full mTLS with `client_auth_type: RequireAndVerifyClientCert` + `client_allowed_sans`. Closes a reconnaissance-disclosure vector identified by the May 2026 adversarial review: the topology graph (device IDs, vendor, OS version, full edge list) was readable by any host with network reach to the listen port, while the federation `/spoke/push` endpoint already required mTLS. The toolkit's `web.ListenAndServe` replaces the previous hand-rolled `srv.ListenAndServeTLS` path; cert reload-on-change is now automatic. The deprecated `listen.tls_cert_file` / `listen.tls_key_file` fields stay functional with a startup deprecation warning (server-TLS-only, no client auth) for one minor release; remove in v1.5.0. Operator guidance — including the recommended Grafana Cloud push-via-Alloy pattern that sidesteps inbound auth entirely — added to `docs/operator/security.md`. Default behaviour unchanged: plain HTTP on the listen address, matching the canonical Prometheus "scrape from a private network" convention. Closes #3.
- **D39** — SNMP credentials (`Community`, `AuthKey`, `PrivKey` in `internal/discovery/snmp/snmp.go::Params`) switched from `string` to `[]byte` and are now explicitly zeroed at end-of-cycle and on shutdown. Best-effort mitigation against credential leakage via core dumps, `/proc/<pid>/mem` reads, and container memory snapshots. New `Params.Zeroize()` method overwrites the underlying bytes; called via `defer` after each per-device walk and from the SIGTERM/SIGINT shutdown handler. Caveat documented honestly in new `docs/operator/security.md`: Go's GC may have copied bytes elsewhere, and `gosnmp`'s upstream API requires `string` so a copy crosses the boundary at the SNMP call site; the mitigation reduces but does not eliminate the in-memory exposure window. New section in `docs/operator/security.md` covers the threat model (core dumps, /proc, container snapshots, host-compromise forensics), the mitigations, the limitations, and operator guidance (drop `CAP_SYS_PTRACE`, `hidepid=2`, disable/encrypt core dumps, rotate credentials periodically). Closes #5.

### Internal

- **D41** — Removed the package-global `walkerOutcomeCounter` from `internal/discovery/bgp/`. The counter handle is now plumbed through a new `snmputil.WalkerMetrics` interface on `Params`, alongside the existing `Vendor`/`UseBGPV2MIB`/`MaxVlans` fields; a thin `metrics.WalkerMetricsAdapter` wraps `BGPWalkerOutcomeTotal` and satisfies the interface. Cleared a latent `t.Cleanup` race in `bgp_outcome_test.go` (tests now run with `t.Parallel()` and `-race`). Closes #18.
- **D42** — Five enum-like string fields on `internal/discovery/discovery.go::Edge` are now declared as named types with constant sets: `DiscoveryProtocol`, `LinkKind`, `Direction`, `Confidence`, `Adjacency`. Each type has `String()` and `Valid()` methods. Underlying string values are unchanged — `type DiscoveryProtocol string` JSON/YAML-marshals byte-identically, so the wire format (snapshot.json, OTLP attributes, Prometheus label values) is preserved. Internal typo protection only — a misspelled `"lldp"` at any emit site is now a compile error rather than a silent broken-reconciliation/broken-metric bug. Hub's `validateSpokePayload` deliberately left UNCHANGED in its UTF-8+length validation rather than enforcing `Valid()` membership, because operators set arbitrary `LinkKind` strings via `KnownInterDomainLinks` config (e.g. `fiber`, `100G`, MPLS-VPN labels) and tightening to enum-membership would be operator-breaking. `Valid()` is available as a helper for future call sites where strict membership is appropriate. Originally milestoned v1.4.0; landed early to clean discovery package types alongside the #18 walker-counter DI work. Closes #21.

- **D37** — Snapshot loader `validateSnapshotFields` now accumulates per-field errors via `errors.Join` instead of returning on the first failure. Capped at 100 errors per file with an "…and more errors omitted (cap=100)" sentinel so a deliberately-corrupted file cannot produce a multi-MB error message. An operator recovering from a corrupted `snapshot.json` with multiple oversized fields now sees every offender in one Load attempt instead of fixing-reloading-rinse-repeat. Per-field error message wording is unchanged; existing substring assertions in upstream callers still match. Closes #29.
- **D36 (breaking — federation reject reason)** — Hub structural-validation failures (empty/oversized/self-edge/invalid-UTF-8 of the typed Device, Edge, and OutOfScopeNeighbour fields) now route through the typed `pushRejection` JSON response with new reject reason `structural_invalid` (HTTP 400). Previously `validateSpokePayload` had a mix of `newValidationError` paths (label-key/value rejects, already typed) and plain `fmt.Errorf` paths (structural rejects), and the latter fell through a "defensive" branch in `handlePush` that mislabeled them as `invalid_label_value` in the rejection counter and returned a plain-text 400 body instead of the documented JSON envelope. The fallthrough is replaced with a `panic` that documents the invariant — `http.Server` recovers from handler panics so only the offending request fails, but a contributor who adds a new validation site that bypasses `newValidationError` now sees the defect at the first push rather than silently mislabeling rejects in production. Operators alerting on `rate(network_topology_graph_updates_rejected_total{reason="invalid_label_value"}[5m])` for structural-failure signal must add `reason="structural_invalid"` to their query. Closes #19.
- **D33** — Snapshot loader now validates per-field byte lengths after `json.Unmarshal`. A malicious or corrupted `snapshot.json` declaring a 1 GB device ID parsed successfully today and consumed RAM on reload; the threat model is filesystem-write access (limited but real for NFS-shared snapshot paths). Caps mirror the existing federation hub bounds (`maxDeviceIDBytes`, `maxPortNameBytes`, `maxLabelKeyBytes`, `maxLabelValueBytes`) and a small enum cap for `discovery_proto`/`link_kind`. Rejection error names the offending index and field. Closes #22.
- **D34** — Hub `validateSpokePayload` now iterates `Edge.Metadata` keys + values, closing a gap left by D26 / #4. The original label-injection hardening covered `Device.Labels` and the typed Edge string fields but missed the `map[string]string` metadata field next door. Edge.Metadata flows to OTLP attribute names+values (`internal/output/otlp/otlp.go:201`, with `metadataAttrPrefix`) — not Prometheus labels — so validation rules are looser than for Labels: size caps (256/4096 bytes) and control-character rejection, but NOT the Prometheus identifier shape. Production discovery code uses dotted metadata keys like `bgp.remote_as` and `mpls_te.admin_status` that would have been rejected by Prometheus's `[a-zA-Z_][a-zA-Z0-9_]*` grammar. Closes #25.
- **D35 (breaking — metric label values)** — BGP walker structural rewrite informed by real-device captures (lab/cisco-iol-bgp/captures/, lab/arista-ceos-bgp/captures/). Three changes shipped together: (1) Removed the `walker="v2_draft"` path that targeted the IETF draft OID `1.3.6.1.3.5.1.1.2`; no vendor tested implements it there. (2) Fixed `vendor_cisco` walker — column numbers (RemoteAs=11 not 13) and index decoder (Cisco encodes peer IP in the index `<addrType>.<addrLen>.<addr...>`, not in a column). The pre-fix walker dropped 100% of rows on every real Cisco device via the `malformed_index` path, silently degrading to RFC 4273 IPv4 coverage. (3) Added `vendor_arista` walker for Arista's enterprise BGP4V2 MIB at `1.3.6.1.4.1.30065.4.1.1.2` (state=13, RemoteAs=10; index `<peerInst>.<addrType>.<addrLen>.<addr...>`). Per-vendor index decoders replace the shared `decodeBgp4V2Index`. Operators alerting on `network_topology_bgp_walker_outcome_total{walker="v2_draft"}` must migrate; `walker="vendor_arista"` is the new label for Arista-fleet alerts. Juniper and Nokia walkers ship unchanged (still unverified against real devices; flagged with `verified:false` in the spec struct and tracked under #1 for v1.3.1). Closes #31.
- **D31** — Hub now caps individual spoke-supplied label keys at 256 bytes and label values at 4096 bytes *before* per-rune validation iterates the string. Closes a CPU-DoS vector: today's `http.MaxBytesReader` caps the whole push body at 16 MiB but individual fields had no separate cap, so a single 16 MiB label value forced ~4M rune iterations per validated field. mTLS scopes the attacker pool but does not prevent compute exhaustion from a misbehaving or compromised spoke. Sized against Prometheus / OpenMetrics conventions and Grafana Cloud Mimir limits, which operate well under these caps. Closes #14.
- **D26** — Hub now validates spoke-pushed label keys and values at the trust boundary, rejecting the entire push with HTTP 400 + a structured `reason` enum when validation fails. New reject reasons: `invalid_label_key`, `invalid_label_value`. Label keys must match `^[a-zA-Z_][a-zA-Z0-9_]*$` and must not start with `__` (Prometheus reserved namespace); values must not contain NUL, newline, carriage return, other C0 control characters, or DEL. Applied to Device.Labels (keys + values), Device inventory string fields (Vendor/Model/OSVersion/Site), Edge fields (SrcDevice/SrcPort/DstDevice/DstPort/DiscoveryProto/LinkKind), and OutOfScopeNeighbour fields. mTLS authenticates WHO can push; this is the WHAT enforcement. Closes a /metrics line-protocol injection vector identified in the adversarial review. Closes #4.

### Discovery

- **D24** — BGP module now surfaces IPv6 sessions via `bgp4V2PeerTable` (IETF draft form, covers Arista) with fallback to vendor-specific tables: `cbgpPeer2Table` (Cisco), `jnxBgpM2PeerTable` (Juniper), and `tBgpPeerTable` (Nokia SR-OS / Alcatel-Lucent). Walker selection: v2 draft → vendor table → RFC 4273 BGP4-MIB. RFC 4273 remains the final IPv4-only fallback for devices implementing only the original MIB. New kill-switch to revert to RFC 4273-only behaviour for operators who hit a vendor regression — set `modules.bgp.disable_v2_mib: true` (see D40 below; the legacy `use_v2_mib` form is accepted with a deprecation warning). Addresses docs/audits/2026-05-architectural-review.md §2.2 (IPv6 visibility gap).
- **D32 (breaking — metric label values)** — `network_topology_bgp_walker_outcome_total{outcome}` no longer emits `outcome="empty"`. The previous `empty` value collapsed two operationally distinct conditions; this release splits them: `outcome="mib_unimplemented"` (BulkWalk returned zero PDUs — device does not implement the table; expected on non-BGP devices, must not page) and `outcome="no_peers"` (PDUs arrived but no peer reached `bgpStateEstablished` — BGP is configured but every session is down; this is the correct "BGP broken" signal). All three walkers (v2 draft, vendor table, RFC 4273) emit the new values. Operator alerts on `rate({outcome="empty"}) > 0` must migrate to `rate({outcome="no_peers"}) > 0`; the old form would have false-positived on every non-BGP device in fleet. Closes #15.
- **D30** — LLDP, CDP, and the shared ifName/ifDescr walker now sanitise device-supplied port-name strings at discovery time: control characters stripped, length capped at 255 bytes on a rune boundary. The cap matches the underlying MIBs (IEEE 802.1AB-2016 SnmpAdminString, CISCO-CDP-MIB OCTET STRING SIZE(0..255), RFC 2863 IF-MIB DisplayString) and sits one byte under the hub's federation-push validator (`maxPortNameBytes = 256`). Prevents a single non-conforming device from silently dropping an entire spoke push due to oversized `Edge.SrcPort`/`DstPort` or `OutOfScopeNeighbour.ReportingPort`. New helper `snmputil.SanitisePortName`. Closes #13.
- **D27** — New counter `network_topology_bgp_walker_outcome_total{walker, outcome}` surfaces the BGP module's fallback chain. Three previously-silent failure paths are now observable: `outcome=malformed_index` increments when `decodeBgp4V2Index` rejects a row (and logs a debug line with the truncated suffix); `outcome=empty` when a walker returns no rows and the chain falls through; `outcome=error` when a walker errors and the chain falls through. The v2-draft error log is now `Warn` (was `Debug`) when a later walker succeeds — the previous level masked vendor MIB column drift behind a working RFC 4273 fallback. Partial mitigation for the silent-failure mode in #1: operators alerting on `rate(...{outcome="malformed_index"}[5m]) > 0` see a signal that the v2 walker is broken on their vendor before they see a topology gap. Closes #8.

### Observability

- **D46** — `docs/operator/scale.md` "When to worry" table replaced with measured benchmarks. Previous numbers (5k→100ms, 25k→1-5s, 100k→10s) were estimates extrapolated from architectural reasoning and were dramatically pessimistic — the measured render time at 50k edges is 1.33s (claimed 5s), and the 100k extrapolation is 2.6s (claimed 10s). The dominant cost is the Prometheus text encoder serialising each edge into exposition format; scaling is linear at ≈26 µs per edge on a 2015 Broadwell laptop CPU. New `scripts/run-scale-bench.sh` drives a build-tag-gated benchmark harness (`go test -tags=bench -bench=BenchmarkMetricsRender`) that synthesizes graphs of N ∈ {1k, 5k, 10k, 25k, 50k} edges and exercises the full `/metrics` render path (TopologyCollector.Collect → promhttp.HandlerFor → text-format encode). The runner stamps host specs (CPU, RAM, OS, kernel, Go version, governor, taskset pinning) into the result file so the methodology is reproducible. The `largeTopologyEdgeThreshold = 5000` startup-warning constant in `cmd/topology-exporter/main.go` was confirmed-not-revised: measured render at 5k edges is ~97ms (≈1% of the default 10s scrape budget), so the warning fires intentionally early as a documentation pointer, not an alert. Closes #2.

- **D43 (breaking — metric label values)** — Three status-shaped counters now carry a `reason` label that classifies sub-causes the prior `status="error"` / `status="failed"` collapsed:
  - `network_topology_otlp_push_total{status, reason}` — reason ∈ {`timeout`, `tls_error`, `http_4xx`, `http_5xx`, `payload_rejected`, `network`, `n/a`}
  - `network_topology_discovery_devices_total{status, reason}` — reason ∈ {`unreachable`, `auth_failed`, `timeout`, `snmp_error`, `mib_unsupported`, `dns_failed`, `outside_allow_list`, `no_credentials`, `budget_expired`, `panic`, `n/a`}
  - `network_topology_snmp_walks_total{status, reason}` — reason ∈ {`unreachable`, `auth_failed`, `snmp_error`, `mib_unsupported`, `decode_error`, `module_error`, `n/a`}

  Reason values are the underlying strings of typed constants in `internal/metrics/sub_reason.go` (mirroring D29's `RejectReason` partitioning pattern); enum sets are closed and pinned by `TestSubReasonWireValuesPinned`. `status="ok"`/`success`/`dropped`/`timeout` rows carry `reason="n/a"`. New `otlp.ClassifyPushError` returns a typed `*httpStatusError` so the push call site can branch on `errors.As` without parsing text. Migration: Prometheus treats `foo{status="error"}` and `foo{status="error", reason="timeout"}` as different series, so pre-upgrade dashboards must switch to `sum by (status)(...)` or pick a specific reason; historical data does not flow forward — the partitioned series begins at the upgrade boundary. Closes #20.

### Internal

- **D44** — Chronic Warn-log emissions now flow through a shared rate-limit helper in new package `internal/loglimit/`. Default cooldown 1h, per-call override via `WarnEvery`. Bounded by `DefaultMaxKeys=4096` with LRU eviction so a high-cardinality error pattern can't grow the seen-set unbounded; clock injection for tests. Four chronic emission sites migrated (each had been emitting up to 1440 Warns/day per device at the 60s discovery cadence): BGP v2/vendor-walker fallback warning, two FDB VLAN-community anomaly warnings, spoke push retry warning, and three NFS snapshot-write warnings. First occurrence still emits; concurrent same-key emissions resolve to one within the cooldown window; distinct keys remain independent. Invariants pinned by 10 tests in `internal/loglimit/loglimit_test.go` (run with `-race`). Operator effect: chronic vendor-MIB drift, NFS snapshot timeouts, and persistent hub-unreachable conditions now surface a single Warn per hour per (site, dimension) instead of one per cycle, freeing the Warn channel for transient signals. Closes #16.

- **D25** — New histograms `network_topology_metrics_render_duration_seconds` and `network_topology_metrics_payload_bytes` are observed on every `/metrics` scrape; alert at p99 against your scraper's `scrape_timeout`. The `/metrics` handler is wrapped to record duration + body size without buffering the response. A one-time startup warning is logged when the first discovery cycle produces more than 5,000 edges, pointing operators at the new `docs/operator/scale.md` guide which documents the three scrape-mode scale escape hatches (timeout tuning, federation, OTLP push). Addresses docs/audits/2026-05-architectural-review.md §2.4 (`/metrics` payload at 10k+ edges).
- **D28** — Two bug-fixes against the D25 instrumentation introduced last cycle. (1) `countingResponseWriter` no longer silently promotes `http.Hijacker` and `http.Pusher` from the embedded `ResponseWriter` — both now panic with a descriptive message. The wrapper is `/metrics`-only; WebSocket upgrade and HTTP/2 server push would bypass `Write()` and silently corrupt the byte counter, so loud failure on those paths beats a silently wrong histogram. The misleading "buffers the response body once" doc comment is replaced with one that describes what the code actually does (streams, counts via `Write()`). (2) The large-topology warning at `largeTopologyEdgeThreshold = 5000` no longer fires only on the first discovery cycle. It now fires on the upward threshold crossing (was at-or-below, now strictly above), rate-limited to one emission per 60 discovery cycles to keep an oscillating topology from flooding the log. A topology that grows past 5,000 edges months after deployment now produces a warning at the moment of crossing, not silently. Closes #7, #9.
- **D29 (breaking — metric shape)** — `network_topology_graph_updates_rejected_total` is now partitioned by a `reason` label. Reason values mirror the federation push-rejection enum: `size_budget_exceeded`, `invalid_label_key`, `invalid_label_value`. Existing dashboards and alerts that query the unlabeled counter must be updated to either match all reasons (e.g. `sum(rate(network_topology_graph_updates_rejected_total[5m]))`) or pick a specific reason. Migration: Prometheus treats `foo` and `foo{reason="x"}` as different series, so historical data does not flow forward automatically — the partitioned series begins at the upgrade boundary. Closes #12.

## v1.2.0 — 2026-05-07

### Security

- **D1** — Spoke `hub_url` now requires `https://`; `http://` is rejected at config load, enforcing the mTLS transport expectation.
- **D2** — Spoke push endpoint built with `net/url.ResolveReference` instead of string concatenation; handles trailing slashes and path prefixes correctly.

### Reliability

- **D3** — OTLP push goroutines bounded by a semaphore (`maxOTLPPushConcurrency = 4`); excess pushes are dropped and counted in `network_topology_otlp_push_total{status="dropped"}`.
- **D5** — OTLP push goroutines are now tracked in a dedicated `WaitGroup`; shutdown drains all in-flight pushes before the process exits.
- **D7** — OTLP HTTP push retries on 429 and 503 with exponential backoff (3 attempts, 100 ms base); `Retry-After` header honoured on 429.
- **D9** — Public metrics server gains `ReadTimeout: 30s` and `WriteTimeout: 60s`, preventing goroutine leaks from slow scrapers.
- **D10** — `BulkWalk` context cancellation is now effective mid-walk; a goroutine + `SetDeadline` interrupt ends stalled SNMP walks promptly on parent context cancellation.

### Correctness

- **D8** — New `fdb.max_vlans` config (default 100, max 4096) caps the per-device VLAN community walk; prevents timeout exhaustion on large campus IOS switches. A warning is logged when the ceiling is reached.
- **D11** — BGP/OSPF/IS-IS peer IP `DstDevice` values are resolved to canonical sysName before reconciliation using the per-cycle device inventory; cross-protocol edge deduplication with LLDP now works correctly.
- **D16** — `os_version` Prometheus label is normalised to the first `M.N[.P]` version token extracted from `sysDescr`, reducing TSDB label churn across OS patch upgrades.
- **D17** — Hub logs a warning when two spokes report different FQDNs that normalise to the same bare hostname, surfacing false-merge risk.
- **D18** — IS-IS `precedenceRank = 5`, OSPF `precedenceRank = 6` (previously both 5); tie-breaking is now deterministic and documented.
- **D21** — `enterprisePrefixes` vendor map replaced with an ordered slice; vendor lookup is now deterministic.

### Observability

- **D12** — OTLP resource now includes `service.version` and `service.instance.id` alongside `service.name`.
- **D19** — `network_topology_snapshot_last_written_timestamp_seconds` initialised to `time.Now()` at startup; prevents the `GraphStale` alert from firing on fresh pods.
- **D20** — Prometheus metric name corrected: `network_topology_discovery_devices` → `network_topology_discovery_devices_total` to match documented name and README.

### Configuration

- **D15** — `retries` (default 1) and `context_name` added to `CredentialProfile`; enables tuning for lossy management networks and Cisco VRF-aware SNMP access.

### Helm / Deployment

- **D6** — `readinessProbe` now targets `/readyz` (was `/healthz`); pods are only marked ready after the first discovery cycle completes.
- **D13** — `podSecurityContext` includes `seccompProfile: RuntimeDefault`, satisfying Kubernetes `Restricted` Pod Security Standards.
- **D14** — Optional `PodDisruptionBudget` template added (`pdb.enabled: false` by default).

---

## v1.1.0 — 2026-05-07

### Discovery protocols

- **IS-IS** — adjacency-state walk via IS-IS MIB (RFC 4444); only `up(3)` adjacencies emitted.
- **MPLS-TE** — tunnel topology via MPLS-TE-MIB (RFC 3812); only operationally `up(1)` tunnels emitted; `SrcPort` encodes tunnel index as `te-tunnel{idx}`.

### Fixes

- IS-IS adjKey extraction rewritten with tail-count split; eliminates false key collision when `adjIdx == 4` and the peer IP begins with `1.4.x.x`.
- MPLS-TE `precedenceRank` corrected from 4 to 7 (was inadvertently overriding OSPF rank 5).
- OTLP HTTP response body drained before `Close()` to enable TCP connection reuse.
- `BulkWalk` and OTLP push goroutines now use the discovery loop context rather than `context.Background()`, preventing goroutine leaks on shutdown.
- Spoke `hub_url` validates `https://` scheme at config load (D1).
- Spoke push URL constructed safely via `net/url` (D2).
- OTLP push concurrency bounded by semaphore (D3).

---

## v1.0.0 — unreleased

### Features

- **LD-09: Clean-room constraint** — GPL-sourced monitoring code may be read for behavioural specification only; the Go implementation is written from specs. Full guardrails in CONTRIBUTING.md.
- **LD-10: Source-attributed reconciliation** — every edge carries `discovery_proto`, `direction`, `link_kind`, and `precedence_rank`; protocol conflicts emit `network_topology_conflict_total` rather than being silently resolved.
- **LD-11: CIDR allow-list scope guard** — the exporter polls only IPs in `discovery.scope.cidr_allow_list`; out-of-scope neighbours emit a log line and increment `network_topology_out_of_scope_neighbours_total`.
- **LD-12: Credential management** — named profiles, per-IP and per-CIDR assignments, ordered fallback, and a token-bucket trial limiter to prevent device lockout on cold start.
- **LD-13: Snapshot persistence** — versioned JSON snapshot written atomically after every cycle; `/metrics` serves the previous graph immediately on restart with `network_topology_graph_stale=1`.
- **LD-14: Unidirectional link TTL** — links reported by only one endpoint for `discovery.unconfirmed_link_ttl_cycles` consecutive cycles are removed.
- **LD-15: Uncoordinated federation** — each instance emits `network_topology_boundary_observation_info` per OOS neighbour; a Mimir recording rule `count by(peer_a,peer_b,proto)(...) == 2` confirms cross-boundary edges without inter-instance connectivity.
- **LD-16: Hub/spoke federation** — spokes push pre-reconciled graphs to a hub after each cycle; the hub aggregates and re-reconciles across all domains, emitting unified metrics from a single scrape target.
- **LD-17: Spoke pre-reconciliation** — each spoke runs `graph.Reconcile` before pushing, keeping hub payloads small and cross-boundary detection clean.
- **LD-18: Spoke liveness vs link liveness** — `federation.spoke_timeout` governs spoke eviction independently of `discovery.unconfirmed_link_ttl_cycles`; the two failure modes have distinct signals.
- **LD-19: Known inter-domain links** — `federation.known_inter_domain_links` injects rank-0 confirmed bidirectional edges for boundary ports where automatic name-matching is unreliable.
- **LD-20: Mutual TLS on hub/spoke channel** — the hub's `/spoke/push` endpoint requires client certificates signed by an operator-controlled CA; plaintext connections are rejected at the TLS handshake.
- **LD-21: Spoke identity binding** — the hub verifies that the client certificate CN matches `federation.spoke.spoke_id` in the push payload, preventing spoke impersonation.

### Discovery protocols

SNMP SYSTEM group, LLDP (IEEE 802.1AB), CDP (CISCO-CDP-MIB), BGP4-MIB, OSPF-MIB, BRIDGE-MIB FDB.

### Deployment

Helm chart, distroless Dockerfile, PrometheusRule with five alerts (GraphStale, DiscoveryCycleSlow, TopologyConflict, FederationSpokeDown, HubGraphStale), amd64/arm64 release binaries.
