# Changelog

## Unreleased

### Configuration (breaking)

- **D22** — `modules.arp.enabled` is now honored at runtime; the default is **false** to match every other module toggle. Existing deployments that relied on the previous "always on" behavior must set `modules.arp.enabled: true` to keep ARP-based MAC→IP enrichment. ARP enrichment is only useful when `modules.fdb.enabled: true`; the exporter logs a startup warning when FDB is enabled without ARP.
- **D23** — `federation.hub.strict_device_name_matching` now defaults to **true**. Out-of-scope neighbour matching at the hub no longer strips FQDN suffixes by default, preventing silent cross-DC hostname collisions (e.g. `core-sw.dc1` colliding with `core-sw.dc2`). The field is now pointer-typed in Go: an omitted YAML key uses the safe strict default, an explicit `false` is still honoured. Single-site deployments that previously relied on FQDN/short-form reconciliation against the same physical device must add `strict_device_name_matching: false` to keep the pre-v1.3.0 behaviour. Addresses ARCHITECTURAL_REVIEW.md §2.3 recommendation #1.

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
