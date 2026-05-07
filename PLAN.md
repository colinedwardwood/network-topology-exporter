# v1.2 Remediation Plan

## Objective

Close all production-readiness gaps identified in the research-backed review (2026-05-07) and ship `v1.2.0`.

## Rules for This Plan

- Track only unresolved work (no historical "done" sections).
- Every deficiency must map to code changes, tests, and a verification command.
- No item is considered complete until its acceptance criteria are met.

---

## Deficiency Register — OPEN

### D11 - BGP / OSPF / IS-IS edges use raw IP as DstDevice

Risk:
- BGP/OSPF/IS-IS set `DstDevice` to peer IP; LLDP sets it to sysName. Same
  physical link appears as two separate edges — deduplication misses it.

Required remediation:
- After each device walk, resolve discovered peer IPs to sysName using the
  `deviceInventory` map already built in `main.go`. Apply as a post-processing
  step or thread a resolver into the protocol Walk functions.

Acceptance criteria:
- Test: device A (LLDP-visible) and device B (BGP peer only) → single canonical edge.

---

## Release Gate

- [ ] `go test ./tests/integration/... -tags integration`
- [ ] `make e2e-image && CLAB_DOCKER=1 make test-e2e`
- [ ] Changelog updated for v1.2.0

---

## Exit Criteria (Ship Blockers for v1.2.0)

- [ ] D11 closed with merged tests.
- [ ] Release gate passes in full environment.
- [ ] Changelog updated.

## Deferred (Post-v1.2)

- IS-IS feature expansion beyond current scope.
- MPLS-TE/SR-TE modeling enhancements.
- Additional OTLP payload schema/versioning work.
- TLS option for public `/metrics` endpoint.
- NetworkPolicy scaffold for hub federation port.
