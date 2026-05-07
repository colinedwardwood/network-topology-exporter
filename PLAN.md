# v1.2 Remediation Plan

## Objective

Close all production-readiness gaps identified in the research-backed review (2026-05-07) and ship `v1.2.0`.

## Rules for This Plan

- Track only unresolved work (no historical "done" sections).
- Every deficiency must map to code changes, tests, and a verification command.
- No item is considered complete until its acceptance criteria are met.

---

## Release Gate

- [ ] `go test ./tests/integration/... -tags integration`
- [ ] `make e2e-image && CLAB_DOCKER=1 make test-e2e`
- [ ] Changelog updated for v1.2.0

---

## Exit Criteria (Ship Blockers for v1.2.0)

- [ ] Release gate passes in full environment.
- [ ] Changelog updated.

## Deferred (Post-v1.2)

- IS-IS feature expansion beyond current scope.
- MPLS-TE/SR-TE modeling enhancements.
- Additional OTLP payload schema/versioning work.
- TLS option for public `/metrics` endpoint.
- NetworkPolicy scaffold for hub federation port.
