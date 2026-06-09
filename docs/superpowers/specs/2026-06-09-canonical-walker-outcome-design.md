# Canonical Walker-Outcome Layer — Design Spec (#149)

**Issue:** #149. **Process:** lighter — spec + one adversarial check + subagent-driven TDD.
**Goal:** Remove the copy-pasted walker outcome-accounting code across 6 discovery packages by lifting it into the `snmp` (`snmputil`) package once. **Pure refactor — zero metric-value or behavior change.** Every emitted `{walker, outcome}` / `{module, reason}` label stays byte-identical so existing Prometheus series and alerts are untouched.

## The duplication (verified in code)

- **`recordWalkerOutcome(p *snmputil.Params, walker, outcome string)`** — nil-safe forwarder, duplicated in `fdb.go:138`, `lldp.go:64`, `cdp.go:57`, `ospf.go:87` (all forward to `p.WalkerMetrics.RecordProtocolWalkerOutcome`) **and** `bgp.go:66` (forwards to `p.WalkerMetrics.RecordWalkerOutcome` — a **different** sink/counter).
- **`recordDegraded(p *snmputil.Params, module, reason string)`** — identical in `fdb.go:150` and `isis.go:93` (forward to `p.WalkerMetrics.RecordDegraded`).
- **Outcome constant set** `{edges, mib_unimplemented, no_neighbours, walker_drift, error}` — duplicated in `fdb.go:117`, `lldp.go:53`, `cdp.go:46`, `ospf.go:76`, each carrying a "Keep in sync" comment (the tell that the compiler should own this). BGP has a **different** vocabulary (`no_peers`, `malformed_index` instead of `no_neighbours`) and is NOT a member of this set.
- **The 4-way terminal classification switch** `edges>0 → edges; !hadPDUs → mib_unimplemented; decoded → no_neighbours; default → walker_drift` — duplicated in `fdb.go:249`, `lldp.go:155`, `cdp.go:110`, `ospf.go:122`. (The `error` outcome is emitted separately on early walk-error returns, not part of this terminal switch.)

**The trap (do not fall into it):** bgp routes to `RecordWalkerOutcome` (→ `network_topology_bgp_walker_outcome_total`); the other four route to `RecordProtocolWalkerOutcome` (→ `network_topology_walker_outcome_total`). These are deliberately separate (snmp.go:42-46) so the long-standing BGP series is never renamed. A single shared forwarder MUST preserve both routes.

## Canonical layer — new file `internal/discovery/snmp/outcome.go` (package `snmp`)

```go
package snmp

// Walker outcome label values for the generic per-protocol walker-outcome
// counter (network_topology_walker_outcome_total, issue #98), shared by the
// LLDP, CDP, OSPF, and FDB walkers. BGP uses its own vocabulary
// (network_topology_bgp_walker_outcome_total) and does not share these.
const (
	OutcomeEdges            = "edges"
	OutcomeMIBUnimplemented = "mib_unimplemented"
	OutcomeNoNeighbours     = "no_neighbours"
	OutcomeWalkerDrift      = "walker_drift"
	OutcomeError            = "error"
)

// RecordProtocolWalkerOutcome forwards a {walker, outcome} observation to the
// generic per-protocol counter via the metrics sink on Params. nil-safe: a nil
// Params or nil Params.WalkerMetrics drops the increment rather than panicking.
func RecordProtocolWalkerOutcome(p *Params, walker, outcome string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordProtocolWalkerOutcome(walker, outcome)
}

// RecordBGPWalkerOutcome forwards to the BGP-specific counter
// (network_topology_bgp_walker_outcome_total). Same nil-tolerance.
func RecordBGPWalkerOutcome(p *Params, walker, outcome string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordWalkerOutcome(walker, outcome)
}

// RecordDegraded forwards a {module, reason} observation to
// DiscoveryDegradedTotal. Same nil-tolerance. Zero-edge degraded path (#100).
func RecordDegraded(p *Params, module, reason string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordDegraded(module, reason)
}

// ClassifyNeighbourOutcome maps a neighbour-walk result to its terminal outcome
// label, shared by the LLDP/CDP/OSPF/FDB walkers. edgeCount is the edges built;
// hadPDUs is whether the MIB returned any rows; decoded is whether rows decoded
// cleanly even if they produced no edge. (Walk-error early returns emit
// OutcomeError directly and never reach here.)
func ClassifyNeighbourOutcome(edgeCount int, hadPDUs, decoded bool) string {
	switch {
	case edgeCount > 0:
		return OutcomeEdges
	case !hadPDUs:
		return OutcomeMIBUnimplemented
	case decoded:
		return OutcomeNoNeighbours
	default:
		return OutcomeWalkerDrift
	}
}
```

## Per-package changes (mechanical; metric values unchanged)

**fdb / lldp / cdp / ospf** (each):
- Delete the local `recordWalkerOutcome` func and the local `outcome*` constant block (keep the `walkerXxx` label const and any package-specific consts e.g. fdb's `reasonQBridgeWalkFailed`).
- Replace `recordWalkerOutcome(&p, walkerX, outcomeError)` early-return calls → `snmputil.RecordProtocolWalkerOutcome(&p, walkerX, snmputil.OutcomeError)`.
- Replace the 4-way terminal switch with one call:
  `snmputil.RecordProtocolWalkerOutcome(&p, walkerX, snmputil.ClassifyNeighbourOutcome(len(edges), hadPDUs, decoded))`
  — preserving each walker's existing `hadPDUs`/`decoded` variables. (Confirm each walker's switch is exactly the 4-way shape above; if a walker has an extra arm or different comment-only nuance, keep its behavior — only the bucket strings must match.)

**fdb additionally:** delete local `recordDegraded`; replace calls with `snmputil.RecordDegraded(&p, "fdb", reason)`.
**isis:** delete local `recordDegraded`; replace calls with `snmputil.RecordDegraded(&p, module, reason)`.

**bgp:**
- Delete the local `recordWalkerOutcome` func; replace ALL its calls with `snmputil.RecordBGPWalkerOutcome(...)` (routes to the SAME `RecordWalkerOutcome` BGP counter). **Call sites are in BOTH `bgp.go` AND `bgp_vendor.go`** (the adversarial check found seven in `bgp_vendor.go` at ~:320,387,393,395,397,403,405). Mind the receiver: bgp.go passes `&p`, but `bgp_vendor.go` already has a `*snmputil.Params` (`p`) at its call sites — match each existing argument exactly (don't add/drop an `&`).
- BGP's own outcome constants (`outcomeEdges`, `outcomeMIBUnimplemented`, `outcomeNoPeers`, `outcomeMalformedIndex`, `outcomeWalkerDrift`, `outcomeError`) — **leave these local to bgp** (used by `bgp_vendor.go`'s bespoke classification). BGP's vocabulary differs (`no_peers`/`malformed_index`) and merging only the shared subset would split bgp's constants across two homes for little gain and more risk on a recently-touched, hardware-validated walker. (Verified by the adversarial check: the shared string values are coincidental, not a maintained invariant — leaving them is the lower-risk call.)

## Tests

- New `internal/discovery/snmp/outcome_test.go`: a table test for `ClassifyNeighbourOutcome` covering all four buckets (edges>0; !hadPDUs; decoded-no-edge; drift), and a nil-safety test that `RecordProtocolWalkerOutcome`/`RecordBGPWalkerOutcome`/`RecordDegraded` no-op on nil `Params` and nil `WalkerMetrics` (and forward to the correct sink method with a fake `WalkerMetrics` that records calls — assert the protocol vs BGP routing).
- **Regression gate — the emitted label VALUES are unchanged** (byte-identical), so the per-walker outcome tests' *assertions* stay the same. But two mechanical, non-semantic edits are required because the tests reference soon-to-be-deleted unexported identifiers (caught by the adversarial check):
  - The four non-bgp outcome tests (`fdb_outcome_test.go`, `lldp_outcome_test.go`, `cdp_outcome_test.go`, `ospf_outcome_test.go`) reference the local `outcomeEdges`/`outcomeMIBUnimplemented`/`outcomeNoNeighbours`/`outcomeWalkerDrift`/`outcomeError` consts → repoint to `snmputil.OutcomeEdges` etc. (the `walkerFDB`/`walkerLLDP`/… label consts are kept locally, so those references are fine).
  - `bgp_outcome_test.go`'s `TestRecordWalkerOutcomeNilSafe` calls the unexported `recordWalkerOutcome` → either retarget it to `snmputil.RecordBGPWalkerOutcome` or delete it (nil-safety is now covered canonically in `outcome_test.go`). Prefer retarget to keep bgp-specific coverage.
- `go test ./... -race`, `gofmt`, `golangci-lint run` clean.
- **fdb note:** the fdb terminal switch feeds from `hadFdbPDUs` (locally renamed), not `hadPDUs` — write `snmputil.ClassifyNeighbourOutcome(len(edges), hadFdbPDUs, decoded)` for fdb.

## Cross-cutting
- One branch (`refactor/149-canonical-walker-outcome`), one PR closing #149. No Co-Authored-By / AI-attribution trailers; author colinedwardwood.
- CHANGELOG: a brief "Changed/Internal" entry (no user-facing behavior change) noting the dedup. (If CHANGELOG has no internal/changed section, add under a suitable existing heading; this is not a Fixed/Added item.)
- Net effect: ~5 funcs + 4 constant blocks + 4 switches collapse to one `outcome.go`; the "Keep in sync" comments disappear because the compiler now owns the invariant.

## Files touched
- `internal/discovery/snmp/outcome.go` (new), `internal/discovery/snmp/outcome_test.go` (new)
- `internal/discovery/{fdb,lldp,cdp,ospf}/*.go` — delete local funcs/consts, call snmputil; repoint the four `*_outcome_test.go` const references to `snmputil.Outcome*`
- `internal/discovery/isis/isis.go` — RecordDegraded
- `internal/discovery/bgp/bgp.go` **and `internal/discovery/bgp/bgp_vendor.go`** — RecordBGPWalkerOutcome (forwarder only; keep bgp's local consts); retarget `bgp_outcome_test.go`'s nil-safe test
- `CHANGELOG.md`
