# v1.0 Execution Plan

## Objective

Ship a stable `v1.0.0` release with predictable runtime behavior, reliable metrics, and release-grade verification.

## Scope

- In scope: release blockers, correctness fixes, validation, docs accuracy for shipped behavior.
- Out of scope: new discovery protocols, major architecture rewrites, non-critical feature expansion.

## Current Decisions

- Keep `TopologyCollector` as the topology metric publication model.
- Keep hub/spoke and uncoordinated federation modes as the v1 release surface.
- Prioritize runtime correctness and operability over adding new functionality.

## Open Gaps

- ~~`runDiscoveryLoop` still contains a direct `os.Exit(1)` path on credential resolver init failure.~~ Fixed.
- `PLAN.md` previously mixed historical status with active work, reducing plan clarity. Resolved.
- No explicit release checklist owner/date tracking in-repo. Accepted for v1.0.

## Workstreams

## 1) Runtime Correctness

- [x] Replace direct `os.Exit(1)` in discovery loop with controlled error/cancel flow.
- [x] Ensure all fatal startup/runtime failures terminate through the `run()` contract.
- [x] Add/adjust tests for resolver init failure and graceful shutdown behavior.

## 2) Metrics and Scrape Safety

- [x] Verify collector update path remains race-free under concurrent update/scrape.
- [x] Confirm descriptor contract stays stable across all role modes.
- [x] Add regression test coverage for empty graph and boundary observation edge cases.

## 3) Documentation Accuracy

- [x] Verify `README.md` reflects current release posture and runtime behavior.
- [x] Verify `docs/architecture.md` matches current collector/concurrency model.
- [x] Verify operator troubleshooting docs reflect real failure modes and remediation.
- [x] Ensure `CHANGELOG.md` accurately describes shipped scope.

## 4) Release Readiness

- [x] `go test -race -count=1 ./...`
- [x] `go test ./tests/integration/... -tags integration`
- [x] `golangci-lint run ./...`
- [x] `helm lint deploy/helm/topology-exporter/`
- [x] `make e2e-image && CLAB_DOCKER=1 make test-e2e`

## 5) Release Execution

- [ ] Tag `v1.0.0` on `main`.
- [ ] Verify CI publishes container images and release binaries.
- [ ] Verify deployment repo consumes stable `v1.0.0` tag.

## Exit Criteria

- No known fatal runtime correctness blockers remain.
- Test, lint, integration, and e2e checks pass.
- Docs and changelog reflect actual shipped behavior.
- Release artifacts are published and deployable.

## Deferred (Post-v1.0)

- IS-IS topology support.
- MPLS-TE / SR-TE discovery and modeling.
- Grafana Alloy plugin output path.
