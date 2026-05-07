# v1.0 Execution Plan

## Status: SHIPPED

`v1.0.0` tagged, CI green, release artifacts published to GitHub Releases and
`ghcr.io/colinedwardwood/network-topology-exporter:1.0.0` on 2026-05-07.
Companion repo `network-o11y-dev` consuming `v1.0.0`.

## Objective

Ship a stable `v1.0.0` release with predictable runtime behavior, reliable metrics, and release-grade verification.

## Workstreams

## 1) Runtime Correctness — DONE

- [x] Replace direct `os.Exit(1)` in discovery loop with controlled error/cancel flow.
- [x] Ensure all fatal startup/runtime failures terminate through the `run()` contract.
- [x] Add/adjust tests for resolver init failure and graceful shutdown behavior.

## 2) Metrics and Scrape Safety — DONE

- [x] Verify collector update path remains race-free under concurrent update/scrape.
- [x] Confirm descriptor contract stays stable across all role modes.
- [x] Add regression test coverage for empty graph and boundary observation edge cases.

## 3) Documentation Accuracy — DONE

- [x] Verify `README.md` reflects current release posture and runtime behavior.
- [x] Verify `docs/architecture.md` matches current collector/concurrency model.
- [x] Verify operator troubleshooting docs reflect real failure modes and remediation.
- [x] Ensure `CHANGELOG.md` accurately describes shipped scope.

## 4) Release Readiness — DONE

- [x] `go test -race -count=1 ./...`
- [x] `go test ./tests/integration/... -tags integration`
- [x] `golangci-lint run ./...`
- [x] `helm lint deploy/helm/topology-exporter/`
- [x] `make e2e-image && CLAB_DOCKER=1 make test-e2e`

## 5) Release Execution — DONE

- [x] Tag `v1.0.0` on `main`.
- [x] Verify CI publishes container images and release binaries.
- [x] Verify deployment repo consumes stable `v1.0.0` tag.

## Deferred (Post-v1.0)

- IS-IS topology support.
- MPLS-TE / SR-TE discovery and modeling.
- Grafana Alloy plugin output path.
