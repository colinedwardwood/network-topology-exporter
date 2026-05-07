# v1.6 — SHIPPED 2026-05-07

## Goal

Preserve strict decode correctness while eliminating brittle module-level hard
failures caused by sparse/mixed malformed SNMP rows in heterogeneous fleets.
Move from binary failure semantics to thresholded, evidence-based degradation.

---

## P1 — Row-level quarantine for required tables

### Scope

Change required-table handling (`isis adjState`, `mpls operStatus`) from
"any anomaly hard-fails module" to "invalid rows are quarantined; valid rows
continue."

### Tasks

- Extend strict walk output to expose:
  - total rows,
  - valid rows,
  - invalid row count,
  - invalid ratio.
- Update IS-IS and MPLS required-table consumers to:
  - drop invalid rows,
  - keep processing valid rows.
- Keep existing decode issue emission for every anomaly category.

### Acceptance criteria

- A single malformed row no longer aborts a module run.
- Valid rows still produce edges in the same cycle.
- Invalid rows are excluded from graph output.

---

## P2 — Thresholded hard-fail policy for required signals

### Scope

Introduce explicit hard-fail thresholds so required-table failures are
deterministic and production-safe.

### Tasks

- Add policy constants/config (module-specific):
  - minimum required valid rows (for example, `min_valid_rows`),
  - maximum invalid ratio before hard fail (for example, `max_invalid_ratio`).
- Hard-fail only when threshold policy is violated.
- Add structured failure reasons:
  - `required_table_no_valid_rows`,
  - `required_table_invalid_ratio_exceeded`,
  - `required_table_walk_error`.

### Acceptance criteria

- Hard-fail outcomes are policy-driven, not incidental.
- Failure reason is unambiguous in logs/metrics.
- Behavior is stable across mixed vendor data quality.

---

## P3 — Degraded-state semantics and telemetry expansion

### Scope

Differentiate "partial required-table degradation" from "optional enrichment
degradation" to improve operator actionability.

### Tasks

- Add degraded reason codes for partial required-table quarantine (for example:
  `required_table_partial_decode`).
- Keep hard-fail counter for threshold violations.
- Add metric dimensions or companion counters for:
  - row quarantine counts by module+oid,
  - threshold-triggered hard fails by module+reason.
- Update alert guidance to distinguish:
  - noisy-but-healthy (quarantine below threshold),
  - outage-risk (threshold exceeded).

### Acceptance criteria

- Operators can distinguish degraded-but-serving vs hard-failed modules.
- Alerts can route to "investigate vendor drift" vs "incident now" classes.
- Metric cardinality remains bounded.

---

## P4 — Soak/chaos test matrix for noisy agents

### Scope

Validate resilience under intermittent malformed PDUs and mixed valid/invalid
table rows over repeated cycles.

### Tasks

- Add table-driven tests for required-table policies:
  - 1 invalid / many valid rows,
  - many invalid / few valid rows,
  - all invalid rows,
  - invalid ratio edge at threshold boundary.
- Add multi-cycle tests ensuring:
  - transient anomalies do not flap module hard-fail state,
  - sustained anomaly breaches trigger hard-fail deterministically.
- Add randomized fuzz-like row-mix tests for strict walker stats stability.

### Acceptance criteria

- Tests prove module continuity under sparse anomalies.
- Tests prove deterministic hard-fail under sustained bad data.
- No regression in existing strict decode and degraded metadata tests.

---

## P5 — Observer coupling hardening

### Scope

Reduce global-callback coupling in decode issue reporting.

### Tasks

- Replace process-global decode observer with explicit dependency injection path
  (or scoped registry object) used by discovery runtime.
- Ensure tests can run in parallel without shared global observer state.
- Keep external behavior/metrics unchanged.

### Acceptance criteria

- No process-global mutable observer dependency in normal runtime path.
- Decode issue reporting remains functionally equivalent.
- Tests remain deterministic under parallel execution.

---

## Execution order

1. **P1** row-level quarantine primitives and consumer updates.
2. **P2** thresholded hard-fail policy wiring.
3. **P3** telemetry/alert semantics split.
4. **P4** soak/chaos matrix and boundary tests.
5. **P5** observer coupling refactor.

---

## Definition of done (full remediation gate)

- [x] Required-table modules continue on partial row anomalies.
- [x] Hard-fail triggers only when explicit threshold policy is breached.
- [x] Quarantine/hard-fail reasons are separately observable in metrics/logs.
- [x] Multi-cycle noisy-agent tests validate continuity and deterministic failover.
- [x] Decode reporting path no longer depends on process-global mutable observer state.
