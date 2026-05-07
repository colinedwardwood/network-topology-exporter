# v1.5 — SHIPPED 2026-05-07

Adversarial-review remediation completed end-to-end:

- **R1 — Strict decode primitives**
  - Added `PDUIntStrict` and `WalkToIntMapStrict` to eliminate silent fallback behavior.
  - Decode anomalies are now explicitly counted and observable.

- **R2 — Explicit discovery contracts**
  - Implemented hard-fail semantics for required signals:
    - IS-IS `adjState`
    - MPLS-TE `operStatus`
  - Implemented degraded semantics for optional enrichment:
    - IS-IS `SrcPort` mapping
    - MPLS-TE `adminStatus`
  - Added degraded metadata markers on emitted edges.

- **R3 — Observability**
  - Added metrics:
    - `network_topology_discovery_decode_issues_total`
    - `network_topology_discovery_degraded_total`
    - `network_topology_discovery_hard_fail_total`
  - Wired metrics into discovery runtime and documented alert guidance.

- **R4 — Negative-path tests**
  - Added strict decode tests for SNMP utilities.
  - Added hard-fail/degraded behavior tests for IS-IS and MPLS-TE walkers.

- **R5 — Maintainability cleanup**
  - Replaced oversized package prose in `isis` and `mpls` with concise invariant comments.
  - Normalized `snmp` package header to the same invariant-focused style.
  - Centralized degraded metadata keys in `internal/discovery` and removed duplicate literals.
  - Added/updated architecture documentation for discovery contracts.

---

## Done criteria (all met)

- [x] No silent int decode fallback in discovery-critical paths.
- [x] Hard-fail/degraded contracts implemented and documented per module input.
- [x] Decode/degraded/failure telemetry exposed with bounded cardinality.
- [x] Negative-path tests cover malformed type/suffix/mixed-table scenarios.
- [x] `isis` and `mpls` tests validate degraded vs failed behavior.
- [x] Comment cleanup completed with concise invariant-focused documentation.
- [x] Shared degraded metadata keys use a single source-of-truth constant set.
