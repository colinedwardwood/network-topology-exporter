# v1.2 — SHIPPED

All deficiencies closed. Release gate passed 2026-05-07.

## Release gate evidence

- `go test -race -count=1 ./...` — PASS
- `golangci-lint run ./...` — 0 issues
- `helm lint deploy/helm/topology-exporter/` — 0 failures
- `go test ./tests/integration/... -tags integration` — PASS
- `CLAB_DOCKER=1 make test-e2e` — PASS (7 tests, OrbStack/arm64)

## Deferred (Post-v1.2)

- IS-IS feature expansion beyond current scope.
- MPLS-TE/SR-TE modeling enhancements.
- Additional OTLP payload schema/versioning work.
- TLS option for public `/metrics` endpoint.
- NetworkPolicy scaffold for hub federation port.
