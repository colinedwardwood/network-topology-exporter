# Changelog

## Unreleased — planned as v1.3.0

The next release is committed to ship as **v1.3.0**. Inline references to
"v1.3.0" in this changelog and in code comments (e.g. `internal/federation/hub.go`,
`internal/config/config.go`, `docs/operator/scale.md`) refer to the same release.
This commitment can be revised only by renaming the milestone
[v1.3.0 — Post-Audit Hardening](https://github.com/colinedwardwood/network-topology-exporter/milestones)
and doing a project-wide string update at that time.

### Configuration (breaking)

- **D40 (breaking — config schema)** — Two `*bool` pointer-typed config fields replaced with positive-default `bool` fields. `federation.hub.strict_device_name_matching` (default true via nil-deref dance) is now `federation.hub.loose_device_name_matching` (default false). `modules.bgp.use_v2_mib` (default true) is now `modules.bgp.disable_v2_mib` (default false). Default runtime behaviour is unchanged in both cases — strict matching and the v2/vendor BGP walkers remain on by default. The legacy YAML keys are still accepted with a startup deprecation warning for one minor release; remove them or migrate to the new keys before v1.5.0. Eliminates the nil-deref hazard at `cmd/topology-exporter/main.go` (which was an unconditional pointer deref) and the defensive nil-check at `internal/federation/hub.go`. Originally milestoned v1.4.0 (OTLP modernization) and landed early to ship config-schema breakage in a single batch. Closes #10.
- **D22** — `modules.arp.enabled` is now honored at runtime; the default is **false** to match every other module toggle. Existing deployments that relied on the previous "always on" behavior must set `modules.arp.enabled: true` to keep ARP-based MAC→IP enrichment. ARP enrichment is only useful when `modules.fdb.enabled: true`; the exporter logs a startup warning when FDB is enabled without ARP.
- **D23** — `federation.hub.strict_device_name_matching` now defaults to **true**. Out-of-scope neighbour matching at the hub no longer strips FQDN suffixes by default, preventing silent cross-DC hostname collisions (e.g. `core-sw.dc1` colliding with `core-sw.dc2`). The field is now pointer-typed in Go: an omitted YAML key uses the safe strict default, an explicit `false` is still honoured. Single-site deployments that previously relied on FQDN/short-form reconciliation against the same physical device must add `strict_device_name_matching: false` to keep the pre-v1.3.0 behaviour. Addresses docs/audits/2026-05-architectural-review.md §2.3 recommendation #1.

### Discovery

- **D38 (breaking — metric label value set)** — `network_topology_bgp_walker_outcome_total{outcome}` adds a new value `walker_drift` and the existing values are now declared as named constants. Previously the vendor walker recorded `outcome=no_peers` for two operationally different cases: (a) PDUs arrived, decoded cleanly, but no peer reached `bgpStateEstablished` (BGP genuinely broken — should page), and (b) PDUs arrived but every row was rejected by the vendor's `decodeIndex` (our walker is broken on this vendor's MIB — same severity, completely different root cause). Case (b) now records `outcome=walker_drift`. Operators alerting on `rate(network_topology_bgp_walker_outcome_total{outcome="no_peers"}[5m])` and relying on it firing for all-rows-malformed must add a parallel alert on `outcome="walker_drift"`. The per-row `outcome=malformed_index` counter is unchanged. The outcome label values (`edges`, `mib_unimplemented`, `no_peers`, `malformed_index`, `walker_drift`, `error`) are now declared as package-level constants in `internal/discovery/bgp/bgp.go` to prevent typo-induced new series. Closes #26, #27.

### Security

- **D39** — SNMP credentials (`Community`, `AuthKey`, `PrivKey` in `internal/discovery/snmp/snmp.go::Params`) switched from `string` to `[]byte` and are now explicitly zeroed at end-of-cycle and on shutdown. Best-effort mitigation against credential leakage via core dumps, `/proc/<pid>/mem` reads, and container memory snapshots. New `Params.Zeroize()` method overwrites the underlying bytes; called via `defer` after each per-device walk and from the SIGTERM/SIGINT shutdown handler. Caveat documented honestly in new `docs/operator/security.md`: Go's GC may have copied bytes elsewhere, and `gosnmp`'s upstream API requires `string` so a copy crosses the boundary at the SNMP call site; the mitigation reduces but does not eliminate the in-memory exposure window. New section in `docs/operator/security.md` covers the threat model (core dumps, /proc, container snapshots, host-compromise forensics), the mitigations, the limitations, and operator guidance (drop `CAP_SYS_PTRACE`, `hidepid=2`, disable/encrypt core dumps, rotate credentials periodically). Closes #5.

### Internal

- **D41** — Removed package-global `walkerOutcomeCounter atomic.Pointer[prometheus.CounterVec]` from `internal/discovery/bgp/`. The counter handle is now plumbed via a new `snmputil.WalkerMetrics` interface field on `Params`, mirroring how `Vendor`, `UseBGPV2MIB`, and `MaxVlans` are already passed. A thin `metrics.WalkerMetricsAdapter` wraps `BGPWalkerOutcomeTotal` and satisfies the interface. Three benefits: (1) `internal/discovery/bgp/` is now free of package-level mutable state — confirmed via `grep -E "^var " internal/discovery/bgp/*.go`; (2) tests added `t.Parallel()` to every case in `bgp_outcome_test.go` and pass under `go test -race`, fixing the latent cleanup-race the previous `SetWalkerOutcomeCounter`/`t.Cleanup` pattern carried; (3) the pattern composes — a future protocol module wanting the same observability follows the same plumbing instead of inventing a parallel package-global. Originally milestoned v1.4.0; landed early to clean BGP package state alongside the #21 typed-enums work that also touches `internal/discovery/`. Closes #18.
- **D42** — Five enum-like string fields on `internal/discovery/discovery.go::Edge` are now declared as named types with constant sets: `DiscoveryProtocol`, `LinkKind`, `Direction`, `Confidence`, `Adjacency`. Each type has `String()` and `Valid()` methods. Underlying string values are unchanged — `type DiscoveryProtocol string` JSON/YAML-marshals byte-identically, so the wire format (snapshot.json, OTLP attributes, Prometheus label values) is preserved. Internal typo protection only — a misspelled `"lldp"` at any emit site is now a compile error rather than a silent broken-reconciliation/broken-metric bug. Hub's `validateSpokePayload` deliberately left UNCHANGED in its UTF-8+length validation rather than enforcing `Valid()` membership, because operators set arbitrary `LinkKind` strings via `KnownInterDomainLinks` config (e.g. `fiber`, `100G`, MPLS-VPN labels) and tightening to enum-membership would be operator-breaking. `Valid()` is available as a helper for future call sites where strict membership is appropriate. Originally milestoned v1.4.0; landed early to clean discovery package types alongside the #18 walker-counter DI work. Closes #21.

- **D37** — Snapshot loader `validateSnapshotFields` now accumulates per-field errors via `errors.Join` instead of returning on the first failure. Capped at 100 errors per file with an "…and more errors omitted (cap=100)" sentinel so a deliberately-corrupted file cannot produce a multi-MB error message. An operator recovering from a corrupted `snapshot.json` with multiple oversized fields now sees every offender in one Load attempt instead of fixing-reloading-rinse-repeat. Per-field error message wording is unchanged; existing substring assertions in upstream callers still match. Closes #29.
- **D36 (breaking — federation reject reason)** — Hub structural-validation failures (empty/oversized/self-edge/invalid-UTF-8 of the typed Device, Edge, and OutOfScopeNeighbour fields) now route through the typed `pushRejection` JSON response with new reject reason `structural_invalid` (HTTP 400). Previously `validateSpokePayload` had a mix of `newValidationError` paths (label-key/value rejects, already typed) and plain `fmt.Errorf` paths (structural rejects), and the latter fell through a "defensive" branch in `handlePush` that mislabeled them as `invalid_label_value` in the rejection counter and returned a plain-text 400 body instead of the documented JSON envelope. The fallthrough is replaced with a `panic` that documents the invariant — `http.Server` recovers from handler panics so only the offending request fails, but a contributor who adds a new validation site that bypasses `newValidationError` now sees the defect at the first push rather than silently mislabeling rejects in production. Operators alerting on `rate(network_topology_federation_spoke_rejects_total{reason="invalid_label_value"}[5m])` for structural-failure signal must add `reason="structural_invalid"` to their query. Closes #19.
- **D33** — Snapshot loader now validates per-field byte lengths after `json.Unmarshal`. A malicious or corrupted `snapshot.json` declaring a 1 GB device ID parsed successfully today and consumed RAM on reload; the threat model is filesystem-write access (limited but real for NFS-shared snapshot paths). Caps mirror the existing federation hub bounds (`maxDeviceIDBytes`, `maxPortNameBytes`, `maxLabelKeyBytes`, `maxLabelValueBytes`) and a small enum cap for `discovery_proto`/`link_kind`. Rejection error names the offending index and field. Closes #22.
- **D34** — Hub `validateSpokePayload` now iterates `Edge.Metadata` keys + values, closing a gap left by D26 / #4. The original label-injection hardening covered `Device.Labels` and the typed Edge string fields but missed the `map[string]string` metadata field next door. Edge.Metadata flows to OTLP attribute names+values (`internal/output/otlp/otlp.go:201`, with `metadataAttrPrefix`) — not Prometheus labels — so validation rules are looser than for Labels: size caps (256/4096 bytes) and control-character rejection, but NOT the Prometheus identifier shape. Production discovery code uses dotted metadata keys like `bgp.remote_as` and `mpls_te.admin_status` that would have been rejected by Prometheus's `[a-zA-Z_][a-zA-Z0-9_]*` grammar. Closes #25.
- **D35 (breaking — metric label values)** — BGP walker structural rewrite informed by real-device captures (lab/cisco-iol-bgp/captures/, lab/arista-ceos-bgp/captures/). Three changes shipped together: (1) Removed the `walker="v2_draft"` path that targeted the IETF draft OID `1.3.6.1.3.5.1.1.2`; no vendor tested implements it there. (2) Fixed `vendor_cisco` walker — column numbers (RemoteAs=11 not 13) and index decoder (Cisco encodes peer IP in the index `<addrType>.<addrLen>.<addr...>`, not in a column). The pre-fix walker dropped 100% of rows on every real Cisco device via the `malformed_index` path, silently degrading to RFC 4273 IPv4 coverage. (3) Added `vendor_arista` walker for Arista's enterprise BGP4V2 MIB at `1.3.6.1.4.1.30065.4.1.1.2` (state=13, RemoteAs=10; index `<peerInst>.<addrType>.<addrLen>.<addr...>`). Per-vendor index decoders replace the shared `decodeBgp4V2Index`. Operators alerting on `network_topology_bgp_walker_outcome_total{walker="v2_draft"}` must migrate; `walker="vendor_arista"` is the new label for Arista-fleet alerts. Juniper and Nokia walkers ship unchanged (still unverified against real devices; flagged with `verified:false` in the spec struct and tracked under #1 for v1.3.1). Closes #31.
- **D31** — Hub now caps individual spoke-supplied label keys at 256 bytes and label values at 4096 bytes *before* per-rune validation iterates the string. Closes a CPU-DoS vector: today's `http.MaxBytesReader` caps the whole push body at 16 MiB but individual fields had no separate cap, so a single 16 MiB label value forced ~4M rune iterations per validated field. mTLS scopes the attacker pool but does not prevent compute exhaustion from a misbehaving or compromised spoke. Sized against Prometheus / OpenMetrics conventions and Grafana Cloud Mimir limits, which operate well under these caps. Closes #14.
- **D26** — Hub now validates spoke-pushed label keys and values at the trust boundary, rejecting the entire push with HTTP 400 + a structured `reason` enum when validation fails. New reject reasons: `invalid_label_key`, `invalid_label_value`. Label keys must match `^[a-zA-Z_][a-zA-Z0-9_]*$` and must not start with `__` (Prometheus reserved namespace); values must not contain NUL, newline, carriage return, other C0 control characters, or DEL. Applied to Device.Labels (keys + values), Device inventory string fields (Vendor/Model/OSVersion/Site), Edge fields (SrcDevice/SrcPort/DstDevice/DstPort/DiscoveryProto/LinkKind), and OutOfScopeNeighbour fields. mTLS authenticates WHO can push; this is the WHAT enforcement. Closes a /metrics line-protocol injection vector identified in the adversarial review. Closes #4.

### Discovery

- **D24** — BGP module now surfaces IPv6 sessions via `bgp4V2PeerTable` (IETF draft form, covers Arista) with fallback to vendor-specific tables: `cbgpPeer2Table` (Cisco), `jnxBgpM2PeerTable` (Juniper), and `tBgpPeerTable` (Nokia SR-OS / Alcatel-Lucent). Walker selection: v2 draft → vendor table → RFC 4273 BGP4-MIB. RFC 4273 remains the final IPv4-only fallback for devices implementing only the original MIB. New kill-switch `modules.bgp.use_v2_mib` (pointer-typed, default **true**) reverts to RFC 4273-only behaviour for operators who hit a vendor regression. Addresses docs/audits/2026-05-architectural-review.md §2.2 (IPv6 visibility gap).
- **D32 (breaking — metric label values)** — `network_topology_bgp_walker_outcome_total{outcome}` no longer emits `outcome="empty"`. The previous `empty` value collapsed two operationally distinct conditions; this release splits them: `outcome="mib_unimplemented"` (BulkWalk returned zero PDUs — device does not implement the table; expected on non-BGP devices, must not page) and `outcome="no_peers"` (PDUs arrived but no peer reached `bgpStateEstablished` — BGP is configured but every session is down; this is the correct "BGP broken" signal). All three walkers (v2 draft, vendor table, RFC 4273) emit the new values. Operator alerts on `rate({outcome="empty"}) > 0` must migrate to `rate({outcome="no_peers"}) > 0`; the old form would have false-positived on every non-BGP device in fleet. Closes #15.
- **D30** — LLDP, CDP, and the shared ifName/ifDescr walker now sanitise device-supplied port-name strings at discovery time: control characters stripped, length capped at 255 bytes on a rune boundary. The cap matches the underlying MIBs (IEEE 802.1AB-2016 SnmpAdminString, CISCO-CDP-MIB OCTET STRING SIZE(0..255), RFC 2863 IF-MIB DisplayString) and sits one byte under the hub's federation-push validator (`maxPortNameBytes = 256`). Prevents a single non-conforming device from silently dropping an entire spoke push due to oversized `Edge.SrcPort`/`DstPort` or `OutOfScopeNeighbour.ReportingPort`. New helper `snmputil.SanitisePortName`. Closes #13.
- **D27** — New counter `network_topology_bgp_walker_outcome_total{walker, outcome}` surfaces the BGP module's fallback chain. Three previously-silent failure paths are now observable: `outcome=malformed_index` increments when `decodeBgp4V2Index` rejects a row (and logs a debug line with the truncated suffix); `outcome=empty` when a walker returns no rows and the chain falls through; `outcome=error` when a walker errors and the chain falls through. The v2-draft error log is now `Warn` (was `Debug`) when a later walker succeeds — the previous level masked vendor MIB column drift behind a working RFC 4273 fallback. Partial mitigation for the silent-failure mode in #1: operators alerting on `rate(...{outcome="malformed_index"}[5m]) > 0` see a signal that the v2 walker is broken on their vendor before they see a topology gap. Closes #8.

### Observability

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
