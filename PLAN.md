# v1.5 — OPEN (adversarial-review remediation)

## Goal

Eliminate silent SNMP decode corruption, formalize discovery failure semantics,
add observability for degraded telemetry, and close negative-test/maintainability
gaps so production behavior is explicit and auditable.

---

## R1 — Typed decode with explicit failures (Fatal flaw closure)

### Scope

Replace lossy integer decode behavior (`PDUInt` fallback-to-zero in map walkers)
with explicit success/failure signaling so malformed or type-mismatched PDUs
cannot silently mutate topology quality.

### Tasks

- Introduce strict helper(s) in `internal/discovery/snmp`:
  - `PDUIntStrict(pdu) (int, bool)` (or `(int, error)`).
  - `WalkToIntMapStrict(...)` returning:
    - parsed map,
    - decode failure count and samples,
    - hard walk error.
- Keep existing best-effort helper only as a compatibility shim (short-lived),
  then migrate call sites and remove shim in same milestone if low blast radius.
- Update `isis.walkAdjStates`, `isis.walkCircuitIfNames`, and `mpls.Walk` to
  consume strict results and branch explicitly on decode anomalies.

### Acceptance criteria

- No integer map walker path relies on implicit `0` for non-int PDU types.
- Decode anomalies are explicit in code and cannot be mistaken for valid data.
- Existing behavior for valid integer PDUs remains unchanged.

---

## R2 — Discovery consistency contracts (hard-fail vs degraded mode)

### Scope

Define and implement module-level rules for required vs optional SNMP signals so
operators can predict when discovery aborts vs degrades.

### Tasks

- Add a small contract table in code/docs for each walker input:
  - IS-IS adjacency state: required (hard-fail).
  - IS-IS circuit/ifDescr for `SrcPort`: optional (degraded).
  - MPLS tunnel oper status: required (hard-fail).
  - MPLS tunnel admin status metadata: optional (degraded).
- Encode contract handling in control flow with clear error paths and structured
  degraded-state markers.
- Add per-edge/per-scrape metadata flags for degraded conditions (for example:
  `network.topology.degraded=true`, `network.topology.degraded_reason=...`).

### Acceptance criteria

- Every SNMP table dependency is classified and implemented per contract.
- Degraded vs failed outcomes are deterministic and documented.
- No "silent best effort" paths remain for required signals.

---

## R3 — Observability and SLO hooks for telemetry quality

### Scope

Expose parse/degrade/failure signals as metrics/logs so silent quality collapse
is detectable by dashboards/alerts.

### Tasks

- Add counters/gauges (names TBD by repo conventions), such as:
  - decode failures by module+OID,
  - degraded discoveries by module+reason,
  - hard-fail discoveries by module+stage.
- Add bounded structured logs with representative samples for decode failures
  (without high-cardinality explosion).
- Document Prometheus alert suggestions:
  - non-zero decode failures sustained over N minutes,
  - degraded ratio above threshold,
  - hard-fail spikes.

### Acceptance criteria

- A single scrape/run can answer: valid, degraded, or failed and why.
- Alerts can be configured without code changes.
- Observability overhead is bounded (no unbounded label/cardinality growth).

---

## R4 — Negative-path and cross-vendor test expansion

### Scope

Add rigorous tests for malformed/mixed SNMP PDUs to verify strict decode and
contract behavior under real failure modes.

### Tasks

- Extend `internal/discovery/snmp/snmp_test.go` with:
  - non-int type in integer column (string/octet/object id),
  - mixed valid+invalid PDUs in same walk,
  - malformed OID suffix handling,
  - large/sparse table behavior sanity checks.
- Add module tests in `isis` and `mpls` validating:
  - required table hard-fail behavior,
  - optional table degraded behavior,
  - emitted degraded metadata correctness.
- Ensure tests assert observability side effects where practical.

### Acceptance criteria

- New tests fail against previous lossy behavior and pass after remediation.
- Positive + negative paths both covered for each new strict/degraded branch.
- Test names/readability make failure mode intent obvious.

---

## R5 — Maintainability cleanup (comment-density + readability)

### Scope

Reduce low-signal narrative comment blocks and replace with concise, durable
comments adjacent to invariants enforced by code/tests.

### Tasks

- Trim oversized package prose blocks in `isis` and `mpls` to concise protocol
  context + invariants that are not obvious from code.
- Move long-form rationale to docs when needed; keep code comments short and
  tied to specific non-obvious logic.
- Add/refresh doc section for discovery contracts and degraded semantics.

### Acceptance criteria

- Comments in hot-path files are concise and implementation-aligned.
- Invariants are enforced by tests, not only prose.
- Future refactors require minimal prose churn.

---

## Execution order

1. **R1** strict decode primitives + call-site migration.
2. **R2** explicit contracts in code paths.
3. **R3** metrics/logging/alert docs.
4. **R4** negative-path tests and regression locks.
5. **R5** maintainability/doc cleanup after behavior is finalized.

---

## Definition of done (full remediation gate)

- [ ] No silent int decode fallback in discovery-critical paths.
- [ ] Hard-fail/degraded contracts implemented and documented per module input.
- [ ] Decode/degraded/failure telemetry exposed with bounded cardinality.
- [ ] Negative-path tests cover malformed type/suffix/mixed-table scenarios.
- [ ] `isis` and `mpls` tests validate degraded vs failed behavior.
- [ ] Comment cleanup completed with concise invariant-focused documentation.
