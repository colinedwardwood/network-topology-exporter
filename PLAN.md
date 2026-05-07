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
- Run integration and e2e gates in a full environment before tagging.

Acceptance criteria:
- All release gate commands pass with captured evidence.

## Work Plan

### Workstream 4 - Release Gate Completion (D4)

- [ ] `go test ./tests/integration/... -tags integration`
- [ ] `make e2e-image && CLAB_DOCKER=1 make test-e2e`

## Exit Criteria (Ship Blockers)

- [ ] D4 release gates fully pass (integration + e2e).
- [ ] Changelog/docs updated to reflect D1-D3 enforcement and behavior changes.

## Deferred (Post-v1.1)

- IS-IS feature expansion beyond current scope.
- MPLS-TE/SR-TE modeling enhancements.
- Additional OTLP payload schema/versioning work.
