# Remediation Workflow

This workflow governs how we close findings — from `/adversarial-review`, an
external audit, a production incident postmortem, or a CVE notification. It
applies to security findings *and* operational correctness findings. The
discipline is the same in both cases: triage → file an issue → minimal fix →
close with evidence → summary.

The workflow is deliberately heavier than ad-hoc fixing because the project's
security surface is mostly protocol parsing (SNMP, OTLP, LLDP, BGP MIBs) and
credential handling (SNMPv3 keys, mTLS material). Quiet failures in these
paths produce silent topology drift or credential exposure — neither of which
is caught by smoke testing in production.

---

## Step 0 — Freeze check

If `README.md` contains an `## Active Freeze` section, only **Critical** and
**High** severity findings may proceed to fix. Medium/Low items must be filed
as GitHub issues (Step 2) and left for the next release window. A freeze is
typically declared during a release-candidate cycle or in response to a
production incident on `main`.

If no freeze is active, all severities proceed normally.

---

## Step 1 — Triage findings

Read the review report or postmortem end-to-end before writing the first
issue. Classify every finding against the matrix below:

| Severity | Criteria | Action |
|---|---|---|
| **Critical** | Silent topology corruption (wrong edges, missed devices). Credential exposure (logs, snapshot, core dump). Unauthenticated state mutation on `/spoke/push`. Remote code execution via crafted SNMP/OTLP/spoke payload. | Fix this cycle. Cannot ship until closed. |
| **High** | Untrusted-input parser without bounds checking. Known CVE in a direct dependency. Denial-of-service on `/metrics` or `/spoke/push`. Auth/TLS configuration that violates BCP 195. Cardinality bomb in a hot label. | Fix this milestone. |
| **Medium** | Defense-in-depth gap (e.g. missing rate limiter where the threat is bounded by mTLS). Observability gap that would mask a Critical/High incident if it occurred. Test fixture is synthetic where a real-device capture is needed (use `needs-real-device` label). | Issue filed; fix in the next 1–2 milestones. |
| **Low** | Informational hardening. Doc inaccuracy. Cleanup that improves auditability but does not change behavior. | Issue filed; fix opportunistically. |

If severity is ambiguous between two rows, ask before filing — getting this
wrong wastes review cycles downstream.

---

## Step 2 — File one GitHub issue per finding

Before touching code, file every classified finding via `gh issue create`.
The labels and milestone we use are the ones already configured on the repo:

**Labels (always):**
- `priority/critical`, `priority/high`, or `priority/medium`. Low severity = no priority label.

**Labels (conditional):**
- `security` — for findings with an exploitation path or credential implication.
- `area/federation`, `area/discovery`, `area/metrics`, `area/snmp` — wherever the change lands.
- `bug` if existing behavior is wrong. `enhancement` if the fix adds capability the project did not have.
- `breaking-change` — if the fix changes the YAML config schema, a metric name/label, or a wire contract.
- `needs-real-device` — for any fix that touches vendor MIB parsing or protocol decoding. **This label is an exit-gate**: the issue cannot close until a real-device capture has validated the fix, not just a synthetic fixture.

**Milestone:**
- Critical/High items go on the *current* milestone (today: `v1.3.0 — Post-Audit Hardening`).
- Medium items go on `v1.4.0 — OTLP Modernization` or later depending on theme.
- Low items go on a future milestone or are left unmilestoned in the backlog.

**Issue body template:**

```markdown
## Finding
<one sentence from the review, plus file:line of the affected code>

## Risk
<one paragraph: realistic exploitation path and what state/credentials/topology accuracy is at stake. For non-security findings, what user-visible failure mode this produces.>

## Standards reference
<applicable RFC, MIB module, or vendor doc. If none applies, write "n/a — internal logic.">

## Fix
<one paragraph: the intended change and why it closes the finding>

## Verification
<the test name(s) that will assert the fix. For `needs-real-device` issues, identify which real-device capture will validate the fix.>

## Exit criteria
- [ ] Fix landed (commit SHA cited in close comment)
- [ ] Verifying test added and passing
- [ ] Standards verification step (§3) completed, where applicable
- [ ] No regression in existing tests
- [ ] For `needs-real-device`: real-device capture committed alongside the fix
```

For Critical/High items being fixed this session, you may file the issue and
immediately move to Step 3 — close at commit time per Step 5. For Medium/Low
deferred items, the filed issue **is** the deferred record; nothing else is
required until the next cycle picks it up.

---

## Step 3 — Standards verification (protocol and security changes)

If the fix touches any of the following, complete this step before writing
code. Cite the relevant standard in the PR description and confirm the change
aligns with current guidance:

| Change touches | Cite + verify |
|---|---|
| SNMPv3 auth/priv key handling | RFC 3414 (USM), RFC 3826 (AES) — key derivation correctness; zero-on-shutdown semantics |
| SNMP table parsing (any module under `internal/discovery/`) | The relevant base RFC (e.g. RFC 4188 for BRIDGE-MIB, RFC 4750 for OSPF, RFC 4273 for BGP) **plus** the vendor MIB module if applicable. `needs-real-device` issue required. |
| BGP4-V2 / vendor BGP tables | `draft-ietf-idr-bgp4-mibv2` + per-vendor MIB; verify column numbers in `internal/discovery/bgp/bgp_vendor.go` against a real `snmpwalk` |
| LLDP parsing | IEEE 802.1AB-2016 — chassis ID / port ID subtype semantics |
| TLS configuration (federation, future `/metrics` auth) | BCP 195 / RFC 9325 (TLS best practices). Minimum TLS 1.2, prefer 1.3. Document cipher selection. |
| Federation push contract | RFC 6805 (H-PCE) framing — confirm the change doesn't violate the parent-PCE / child-PCE semantics the project's docs cite |
| OpenMetrics emission (new metric, label, or exposition tweak) | Prometheus exposition format + OpenMetrics spec — line-protocol safety, escape rules for label values |
| Log content (anything that interpolates external strings) | RFC 5424 — control-char escape for spoke-supplied or device-supplied data |
| OTLP output | OpenTelemetry semantic conventions for the relevant signal version. Pin the schema URL constant deliberately. |

For changes that don't touch any of the above, write "n/a — internal logic"
in the issue's *Standards reference* field and skip this step.

---

## Step 4 — Fix each item (minimal, targeted)

For each Critical/High issue in priority order:

1. **Read the affected file(s) first.** Includes the call sites, not just the
   declaration. Understand who depends on the current behavior before changing
   it.
2. **Implement the minimal fix.** A remediation is not a refactor. Change only
   what is required to close the specific finding. Do not rename, restructure,
   or "improve while we're here" — those go in a separate commit on a
   separate branch.
3. **Run the quality gates.** All must pass before commit:
   ```sh
   go build ./...
   go test ./...
   go vet ./...
   gofmt -l .   # output must be empty
   ```
4. **Commit immediately** with a Conventional Commits prefix matching the
   change:
   - `fix(security):` for security findings
   - `fix(area):` for behavioral bugs (e.g. `fix(federation):`)
   - `feat(area):` if the fix adds new capability (e.g. an auth surface)
   The commit body must name the vulnerability class or failure mode (not just
   the symptom) and reference the issue number with `Closes #N`.
5. **Do not batch unrelated fixes** into one commit. One issue, one commit.

For Medium/Low items being addressed in a later cycle, no work happens here —
the issue stays open and milestoned.

---

## Step 5 — Close the issue with evidence

After each fix is committed, close the issue. The close comment must contain
at minimum:

- The commit SHA (full or abbreviated, but the SHA — not just "merged")
- The verifying test name with package path
- The file:line of the change (a commit SHA alone is insufficient; reviewers
  need to find what changed without running `git show`)

```bash
gh issue close <N> -c "Fixed in <SHA>. Verifying test: internal/<package>/<file>_test.go::<TestName>. Affected: <path/to/file.go>:<line>."
```

For `needs-real-device` issues, also cite which fixture (and its provenance —
which vendor, which OS version) validated the fix.

---

## Step 6 — Summary report

When all in-scope items are resolved, produce a short report. This becomes
the evidence record for the milestone or release gate:

```
Fixed (Critical/High):
  #<N> <title>: <one line on what changed and why it closes the finding>
  #<N> ...

Deferred (Medium/Low):
  #<N> <title>: filed for <milestone>

Residual risk:
  <anything intentionally not fixed and the reason — e.g. "real-device fixture
  capture for Nokia SR-OS still outstanding per #1; v2 walker remains
  experimental until that lands">
```

The residual-risk paragraph is required even when it says "none." Operators
and future maintainers need to see what was *not* in scope as clearly as what
was.

---

## Operating notes

- **Adversarial-review cadence.** Run `/adversarial-review` before every minor
  release. The output feeds this workflow as Step 1 input.
- **Real-device fixture debt** is tracked by the `needs-real-device` label.
  Any issue carrying that label cannot close on synthetic tests alone — this
  rule exists because the May 2026 audit found that BGP4-V2 vendor walker
  column numbers had been shipped against only synthetic fixtures.
- **Standards drift.** RFCs get superseded; vendor MIBs change between major
  OS releases. When a fix cites a standard, the cite should reference the
  *current* version, not the version that happened to be top-of-mind. If a
  newer revision exists and the project is still tracking an older one, file
  a follow-up issue rather than silently expanding scope.
- **Documents that don't exist yet.** This workflow assumes `README.md` for
  the active-freeze hook in Step 0. There is no `CLAUDE.md` or `AGENTS.md` in
  this repo — don't reference them.
