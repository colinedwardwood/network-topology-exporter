# SNMP-Core Decode Correctness — Design Spec (#148)

**Issue:** #148. **Process:** lighter — spec + one adversarial check + subagent-driven TDD (per-task spec/quality reviews).
**Scope:** three independent correctness defects in `internal/discovery/snmp`, surfaced by the thermo-nuclear review. One branch (`fix/148-snmp-decode-correctness`), one PR, three TDD tasks. No behavior change for valid in-range data; the changes only affect malformed/overflow inputs and a test-only API.

---

## Fix 1 — `PDUIntStrict` silently truncates and reports success

**File:** `internal/discovery/snmp/pdu.go:304-320`.

**Problem.** `PDUIntStrict` converts `int64`/`uint64` (and `uint`) to `int` with an unconditional `int(v)` and returns `ok=true`. A `uint64 > math.MaxInt64` becomes negative; the `//nolint:gosec` papers over exactly the truncation the `*Strict` variant exists to prevent. Because `WalkToIntMapStrict` (pdu.go:119) feeds `EvaluateRequiredTablePolicy`, a required-table gate can **pass on corrupt data** counted as a valid row. Callers: `WalkToIntMapStrict` (pdu.go:119), `PDUInt` (pdu.go:295), `snmp.go:524` (uptime ticks) — all already treat `ok=false` as "skip / decode failure", so returning `ok=false` on overflow is safe for every caller.

**Change.** Range-check the wide/unsigned cases against `math.MinInt`/`math.MaxInt`; return `(0, false)` when out of range. `int`/`int32` always fit `int` and stay unguarded. Remove the `//nolint:gosec` (the conversion is now guarded). Add `import "math"`.

```go
func PDUIntStrict(pdu g.SnmpPDU) (int, bool) {
	switch v := pdu.Value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		if v < math.MinInt || v > math.MaxInt {
			return 0, false
		}
		return int(v), true
	case uint:
		if uint64(v) > math.MaxInt {
			return 0, false
		}
		return int(v), true
	case uint32:
		if uint64(v) > math.MaxInt { // always false on 64-bit; correct on 32-bit
			return 0, false
		}
		return int(v), true
	case uint64:
		if v > math.MaxInt {
			return 0, false
		}
		return int(v), true
	}
	return 0, false
}
```

**Tests** (extend `TestPDUIntStrict`, snmp_test.go:89): add cases — `uint64(math.MaxInt64)+1` → `(0,false)`; `uint64(math.MaxUint64)` → `(0,false)`; `int64(math.MaxInt64)` → `(MaxInt64, true)` on 64-bit; a normal `uint64(64512)` → `(64512,true)`. Confirm `WalkToIntMapStrict` counts an overflow row as a `DecodeFailure` (excluded from the map) via a small walk test with an out-of-range Counter64 PDU.

---

## Fix 2 — `EvaluateRequiredTablePolicy` `(degraded, hardFailReason)` foot-gun

**File:** `internal/discovery/snmp/pdu.go:166-177`. **Call sites:** `mpls.go:50` + `mpls.go:63` (evaluates the *same* `operStats` **twice**, extracting one half each), `isis.go:105`.

**Problem.** Two of three returns are `(false, "<reason>")` — a hard-fail returns `degraded=false`, so the two outputs are not independent and a caller checking only `degraded` (mpls.go:63 discards the reason) silently treats a hard-failed table as fine. mpls.go calls the function twice on identical stats to pull each half, inviting drift.

**Change.** Return a single three-state verdict; eliminate the impossible `(false, reason)` shape.

```go
// TableOutcome is the three-state result of EvaluateRequiredTablePolicy.
type TableOutcome int

const (
	TableOK       TableOutcome = iota // usable, no invalid rows
	TableDegraded                     // usable, but some rows failed to decode
	TableHardFail                     // failed a required-table floor; see Reason
)

// TableVerdict is the result of EvaluateRequiredTablePolicy. Reason is set only
// when Outcome == TableHardFail.
type TableVerdict struct {
	Outcome TableOutcome
	Reason  string
}

func (v TableVerdict) IsHardFail() bool { return v.Outcome == TableHardFail }
func (v TableVerdict) IsDegraded() bool { return v.Outcome == TableDegraded }

func EvaluateRequiredTablePolicy(stats IntMapDecodeStats, policy RequiredTablePolicy) TableVerdict {
	if stats.ValidRows < policy.MinValidRows {
		return TableVerdict{Outcome: TableHardFail, Reason: "required_table_no_valid_rows"}
	}
	if policy.MaxInvalidRatio >= 0 && stats.InvalidRatio > policy.MaxInvalidRatio {
		return TableVerdict{Outcome: TableHardFail, Reason: "required_table_invalid_ratio_exceeded"}
	}
	if stats.InvalidRows > 0 {
		return TableVerdict{Outcome: TableDegraded}
	}
	return TableVerdict{Outcome: TableOK}
}
```

**Call-site updates** (behavior identical):
- `mpls.go` — evaluate `operStats` **once**:
  ```go
  operVerdict := snmputil.EvaluateRequiredTablePolicy(operStats, snmputil.RequiredTablePolicy{MinValidRows: requiredMinValidRows, MaxInvalidRatio: requiredMaxInvalidRatio})
  if operVerdict.IsHardFail() {
      return nil, nil, &discovery.PolicyError{Module: "mpls_te", Reason: operVerdict.Reason, Err: fmt.Errorf("operStatus stats: valid=%d total=%d invalid=%d ratio=%.3f", operStats.ValidRows, operStats.TotalRows, operStats.InvalidRows, operStats.InvalidRatio)}
  }
  // ... later, reuse operVerdict:
  if operVerdict.IsDegraded() {
      degradedReasons = append(degradedReasons, discovery.DegradedReasonRequiredTablePartialDecode)
  }
  ```
- `isis.go:105` —
  ```go
  verdict := snmputil.EvaluateRequiredTablePolicy(stats, snmputil.RequiredTablePolicy{MinValidRows: requiredMinValidRows, MaxInvalidRatio: requiredMaxInvalidRatio})
  if verdict.IsHardFail() {
      return nil, false, &discovery.PolicyError{Module: "isis", Reason: verdict.Reason, Err: fmt.Errorf("adjState stats: valid=%d total=%d invalid=%d ratio=%.3f", stats.ValidRows, stats.TotalRows, stats.InvalidRows, stats.InvalidRatio)}
  }
  return states, verdict.IsDegraded(), nil
  ```

**Tests.** Migrate `TestEvaluateRequiredTablePolicy` (snmp_test.go:261) to assert `verdict.Outcome` (Pass/Degraded/HardFail) + `verdict.Reason` instead of the `(degraded, hardFailReason)` pair. Keep the same input cases (no-valid-rows → HardFail+reason; ratio-exceeded → HardFail+reason; invalid>0 → Degraded; clean → OK). mpls/isis package tests should be unaffected (PolicyError shape unchanged); run them to confirm.

---

## Fix 3 — delete the divergent test-only `Checkout` acquisition path

**File:** `internal/discovery/snmp/pool.go:234-263` (`Checkout`). **Callers:** `pool_test.go` only.

**Problem.** `Checkout` is a second acquisition state machine that lacks every safety property `acquire` (pool.go:135) has: no `closed` check (resurrects the map after `Close`, pooling sessions the now-stopped evictor never sweeps), no defensive in-use guard, and it **unconditionally overwrites** `pl.sessions[key]` (pool.go:258), orphaning a live `*GoSNMP` without closing it. Its doc admits it exists only "so tests drive it directly." Two acquisition state machines where the test-only one violates the pool's invariants is a trap.

**Change.** **Delete `Checkout`.** Tests acquire through the real `acquire` state machine via a small package-internal test helper. **Keep `Return`** — it is correct *disposal* (it verifies `e.session == s`, handles the unhealthy close+evict), not a divergent acquisition path, and it backs `TestSessionPoolReturnUnhealthyEvicts`'s coverage of connection-error eviction. (Decision recorded for the adversarial check: only the acquisition duplicate is the trap; disposal stays.)

Add to `pool_test.go`:
```go
// checkout drives the real acquire() state machine for tests, returning the
// session and its release func. Replaces the deleted Checkout method so tests
// exercise exactly one acquisition path.
func checkout(t *testing.T, pl *SessionPool, p Params) (*g.GoSNMP, func()) {
	t.Helper()
	s, release, err := pl.acquire(p)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return s, release
}
```

**Test migration** (`pool_test.go`):
- Replace `s, err := pl.Checkout(p)` + error check with `s, release := checkout(t, pl, p)`.
- Replace healthy `pl.Return(p, s, true)` with `release()`.
- `TestSessionPoolReturnUnhealthyEvicts` keeps `pl.Return(p, s, false)` for the unhealthy path (the session was acquired via `checkout`; `Return` finds it by key and evicts). It must NOT also call `release()`.
- `TestSessionPoolConcurrentDistinctKeys`: each iteration `s, release := checkout(t, pl, p); release()`.
- Reuse/hit-miss assertions (e.g. `TestSessionPoolCheckoutReuseSameKey`) hold: `acquire` reuses the same pooled session on the second call after `release()`, producing miss=1, hit=1 — identical to the old expectations.

---

## Cross-cutting

- Per fix, a focused TDD task: T1 = `PDUIntStrict`, T2 = `EvaluateRequiredTablePolicy` + 2 call sites, T3 = delete `Checkout` + test migration. Each: red → green → `go test ./... -race` + `gofmt` + `golangci-lint run` clean → commit. (Independent fixes; do not parallelize subagents on the same package — sequential to avoid file conflicts in pdu.go/snmp_test.go.)
- No Co-Authored-By / AI-attribution trailers; commit author colinedwardwood.
- CHANGELOG: one Fixed entry summarizing all three (decode overflow now rejected; required-table verdict is a single three-state result; removed the divergent test-only pool-checkout path).
- No production behavior change for valid data; the only externally visible effect is that genuinely out-of-range integer PDUs are now counted as decode failures (excluded from maps, reflected in existing `invalid_type` decode-issue metrics) rather than silently mistruncated.

## Files touched
- `internal/discovery/snmp/pdu.go` — `PDUIntStrict` range checks (+`math` import); `TableOutcome`/`TableVerdict` + `EvaluateRequiredTablePolicy` return.
- `internal/discovery/snmp/pool.go` — delete `Checkout`.
- `internal/discovery/mpls/mpls.go`, `internal/discovery/isis/isis.go` — verdict call sites.
- `internal/discovery/snmp/snmp_test.go` — `TestPDUIntStrict` overflow cases, `TestEvaluateRequiredTablePolicy` migration, `WalkToIntMapStrict` overflow-row test.
- `internal/discovery/snmp/pool_test.go` — `checkout` helper + migrate off `Checkout`.
- `CHANGELOG.md`.
