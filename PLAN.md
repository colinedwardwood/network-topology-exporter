# v1.1 Execution Plan

## Status: READY TO TAG

All 12 workstreams complete. Full test suite (`go test -race -count=1 ./...`) green.
`golangci-lint` and `helm lint` remain to run before tagging.

`v1.0.0` shipped 2026-05-07. IS-IS, MPLS-TE, and OTLP output landed post-tag.
Adversarial review findings fully remediated in commit `10c1920` (2026-05-07).

---

## Workstream 1 — IS-IS adjKey Bug — DONE

- [x] Replaced `strings.LastIndex` with tail-count split in `internal/discovery/isis/isis.go`.
- [x] Unit tests: normal case (adjIdx=1, IP 10.0.0.1) and ambiguous case (adjIdx=4, IP 1.4.5.6).

---

## Workstream 2 — OTLP TCP Connection Churn — DONE

- [x] `io.Copy(io.Discard, resp.Body)` before `Close()` in `post()`.
- [x] Connection-reuse test added in `otlp_test.go`.

---

## Workstream 3 — OTLP Write Amplification — DONE

- [x] `HeartbeatCycles int` added to `OTLPOutputConfig` (default 10).
- [x] Cycle counter in `runDiscoveryLoop`; `PushGraph` only on changes or heartbeat.
- [x] `PushChanges` fires only when `len(changes) > 0`.

---

## Workstream 4 — OTLP Context Leak on Shutdown — DONE

- [x] OTLP goroutines use loop `ctx` instead of `context.Background()`.

---

## Workstream 5 — MPLS-TE Precedence Rank — DONE

- [x] `precedenceRank` changed from 4 to 7 in `internal/discovery/mpls/mpls.go`.
- [x] Rank-ladder comment added to constants block.

---

## Workstream 6 — Shared pduIP Helper — DONE

- [x] `PDUIPv4` added to `internal/discovery/snmp/pdu.go`.
- [x] `pduIP` removed from `bgp.go` and `isis.go`; callers updated.

---

## Workstream 7 — OTLP Quality Fixes — DONE

- [x] `serviceRes` package-level var replaces `serviceResource()` function.
- [x] WHAT comments removed from `PushGraph`.
- [x] All `// ──` section dividers removed.

---

## Workstream 8 — MPLS-TE Quality Fixes — DONE

- [x] `DstPort: ""` removed from edge literal.
- [x] `parts[0]` validated with `strconv.Atoi`; SrcPort uses `%d` format.

---

## Workstream 9 — OTLP Push Health Metric — DONE

- [x] `network_topology_otlp_push_total{status}` counter added to `Metrics`.
- [x] Incremented on every `PushGraph` and `PushChanges` outcome.
- [x] Documented in README metrics table.

---

## Workstream 10 — OTLP Endpoint Validation / SSRF — DONE

- [x] `validateOTLPEndpoint` added to `config.go`; enforces `http`/`https` scheme and non-empty host.
- [x] Tests: `file://`, `ftp://`, empty string all fail; `http://` and `https://` pass.

---

## Workstream 11 — runDiscoveryLoop Refactor — DONE

- [x] `loopConfig` struct defined; `runDiscoveryLoop` reduced to 2 params.
- [x] All 7 test call sites updated.

---

## Workstream 12 — Documentation — DONE

- [x] IS-IS and MPLS-TE module sections added to `README.md`.
- [x] OTLP output section with Alloy snippet added to `README.md`.
- [x] `docs/metrics.md` updated with new modules and `network_topology_otlp_push_total`.
- [x] `docs/operator/troubleshooting.md` updated with OTLP scrape-collision guidance.

---

## Release Gate — v1.1.0

- [x] `go test -race -count=1 ./...` — green
- [ ] `golangci-lint run ./...`
- [ ] `helm lint deploy/helm/topology-exporter/`
- [ ] Tag `v1.1.0`, verify CI publishes container image and release binaries.
- [ ] Update companion repo `network-o11y-dev` to `tag: v1.1.0`.
