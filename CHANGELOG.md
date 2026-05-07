# Changelog

## v1.0.0 — unreleased

### Features

- **LD-09: Clean-room constraint** — GPL-sourced monitoring code may be read for behavioural specification only; the Go implementation is written from specs. Full guardrails in CONTRIBUTING.md.
- **LD-10: Source-attributed reconciliation** — every edge carries `discovery_proto`, `direction`, `link_type`, and `precedence_rank`; protocol conflicts emit `network_topology_conflict_total` rather than being silently resolved.
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
