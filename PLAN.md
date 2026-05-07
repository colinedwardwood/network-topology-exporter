# v1.1 Remediation Plan

## Objective

Close all currently identified production-readiness gaps from adversarial reviews and ship `v1.1.0` with explicit security, correctness, and release-gate evidence.

## Rules for This Plan

- Track only unresolved work (no historical "done" sections).
- Every deficiency must map to code changes, tests, and a verification command.
- No item is considered complete until its acceptance criteria are met.

## Deficiency Register

### D4 - Release gate incomplete

Risk:
- Lint/Helm/release-path regressions can ship despite green unit tests.

Required remediation:
- Run and record all required pre-tag checks.
- Block tag creation until all gates pass.

Acceptance criteria:
- All release gate commands pass in CI or locally with captured evidence.

## Work Plan

## Workstream 4 - Release Gate Completion (D4)

- [x] `go test -race -count=1 ./...` — PASS (2026-05-07)
- [x] `golangci-lint run ./...` — 0 issues (2026-05-07)
- [x] `helm lint deploy/helm/topology-exporter/` — 0 failures (2026-05-07)
- [ ] `go test ./tests/integration/... -tags integration`
- [ ] `make e2e-image && CLAB_DOCKER=1 make test-e2e`

## Verification Matrix

- Security:
  - [x] Spoke config rejects insecure transport. (D1 — `validateHTTPSEndpoint`)
  - [x] Spoke push endpoint path generation is deterministic. (D2 — `buildSpokeURL`)
- Correctness:
  - [x] Existing federation and OTLP behavior remains backward-compatible where intended.
- Performance:
  - [x] OTLP push path remains bounded under churn. (D3 — semaphore + dropped counter)
- Operability:
  - [x] New failure modes are observable via metrics/logs and documented.

## Exit Criteria (Ship Blockers)

- [x] D1-D3 closed with merged tests.
- [ ] D4 release gates fully pass (integration + e2e pending environment).
- [ ] No open HIGH severity adversarial findings.
- [ ] Release gate commands pass.
- [ ] Changelog/docs updated to reflect enforcement and behavior changes.

## Deferred (Post-v1.1)

- IS-IS feature expansion beyond current scope.
- MPLS-TE/SR-TE modeling enhancements.
- Additional OTLP payload schema/versioning work.
