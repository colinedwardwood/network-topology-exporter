# v1.1 Execution Plan

## Status: IN PROGRESS

`v1.0.0` shipped 2026-05-07. IS-IS, MPLS-TE, and OTLP output landed post-tag.
Adversarial review identified two NO-GO bugs in the new code. v1.1 remediates all
findings before the next stable release.

## Objective

Remediate all deficiencies found by adversarial review of the IS-IS, MPLS-TE, and
OTLP modules. Stabilise the three new features to production-grade quality, then
tag v1.1.0.

---

## Workstream 1 — IS-IS adjKey Bug (P0 — NO-GO)

**Finding**: `strings.LastIndex(suffix, ".1.4.")` finds the *last* occurrence of the
marker substring. When adjIdx happens to equal 4 and the neighbour IP starts with
`1.4.`, the substring appears twice; LastIndex returns the wrong boundary, the
derived adjKey does not match any state-map entry, and the adjacency is silently
dropped. Silent edge loss under a specific but plausible combination of index values
and addressing.

**Fix**: Replace the LastIndex heuristic with a tail-count split. The IPv4 address
region is always the last 4 dot-separated components; addrLen ("4") is the component
before them; addrType ("1") is the component before that. Split on ".", count from
the right, and reconstruct adjKey from components `[0 : len-6]`.

- [ ] Replace `strings.LastIndex(suffix, ".1.4.")` in `internal/discovery/isis/isis.go`
      with a tail-count approach: `parts := strings.Split(suffix, ".")`, then
      `adjKey = strings.Join(parts[:len(parts)-6], ".")` after validating
      `len(parts) >= 6 && parts[len-6] == "1" && parts[len-5] == "4"`.
- [ ] Add a unit test covering adjIdx=4 with a neighbour IP of 1.4.x.x (the
      previously ambiguous case) to confirm correct adjKey extraction.
- [ ] Add a unit test for the normal case (adjIdx != 4) to confirm no regression.

**Exit criterion**: Both test cases pass; `go test -run TestWalkAdjIPAddrs ./internal/discovery/isis/...` green.

---

## Workstream 2 — OTLP TCP Connection Churn (P0 — NO-GO)

**Finding**: `internal/output/otlp/otlp.go` `post()` defers `resp.Body.Close()` but
never drains the body first. The Go HTTP client docs require the body to be fully
consumed before Close() to allow the underlying TCP connection to be returned to
the pool. With a short-lived push every scrape cycle, this opens a new TCP
connection every call — connection exhaustion under load.

**Fix**: Add `_, _ = io.Copy(io.Discard, resp.Body)` immediately before the deferred
close, or inline it before returning in the success path.

- [ ] Add `io` to the import block in `internal/output/otlp/otlp.go`.
- [ ] In `post()`, drain the body before closing:
      `_, _ = io.Copy(io.Discard, resp.Body)` then `resp.Body.Close()` (remove defer).
- [ ] Add a unit test using `httptest.Server` that asserts the same TCP connection
      is reused across two sequential `post()` calls (check server-side
      `r.TLS == nil && r.Header.Get("Connection") != "close"`; or count
      net.Conn.Close calls via a custom listener).

**Exit criterion**: `go test ./internal/output/otlp/...` green; connection-reuse test passes.

---

## Workstream 3 — OTLP Write Amplification (P0 — NO-GO)

**Finding**: `runDiscoveryLoop` fires both `PushGraph` and `PushChanges` on every
discovery cycle regardless of whether the graph changed. Full graph re-push every
scrape interval is write amplification — the receiver gets identical data on stable
topologies. On a 60-second scrape interval with 500 edges, this is ~1 MB/min of
constant traffic for a quiescent network.

**Fix**: Push the full graph only when `len(changes) > 0` OR once every N cycles
(heartbeat). Introduce `OTLPOutputConfig.HeartbeatCycles int` (default 10) so
operators can tune the heartbeat without touching scrape interval. `PushGraph` fires
on change or when the heartbeat counter expires; `PushChanges` fires only when
`len(changes) > 0`.

- [ ] Add `HeartbeatCycles int` to `OTLPOutputConfig` in `internal/config/config.go`
      with a default of 10.
- [ ] Track a cycle counter in `runDiscoveryLoop`; push full graph only when
      `len(changes) > 0 || cycle%heartbeat == 0`.
- [ ] `PushChanges` goroutine fires only when `len(changes) > 0`.
- [ ] Add a test asserting `PushGraph` is not called on cycle 2 when graph is stable
      and heartbeat is > 1.

**Exit criterion**: Unit test confirms no push on a stable-graph cycle outside the heartbeat window.

---

## Workstream 4 — OTLP Context Leak on Shutdown (P0 — NO-GO)

**Finding**: OTLP goroutines in `runDiscoveryLoop` use `context.Background()` rather
than the loop's `ctx`. After the loop receives cancellation, inflight pushes continue
running until their 10-second HTTP timeout fires — prolonging shutdown and masking
the shutdown signal.

- [ ] Thread `ctx` into OTLP goroutines in `runDiscoveryLoop` instead of
      `context.Background()`.
- [ ] Verify `TestRunDiscoveryLoopShutdown` (or equivalent) confirms the loop exits
      promptly on cancel without waiting for in-flight pushes.

**Exit criterion**: Shutdown test exits within 200ms after context cancel.

---

## Workstream 5 — MPLS-TE Precedence Rank (P1)

**Finding**: `internal/discovery/mpls/mpls.go` sets `precedenceRank = 4`. The
existing OSPF module uses rank 5. Rank 4 is numerically lower, meaning MPLS-TE
edges *override* OSPF edges in the graph merge — backwards. MPLS-TE tunnels are
overlays over the physical layer; OSPF (IGP) should have higher precedence.

**Fix**: Change `precedenceRank` to 7, placing MPLS-TE below BGP (6) and OSPF (5).

- [ ] Change `precedenceRank = 4` to `precedenceRank = 7` in
      `internal/discovery/mpls/mpls.go`.
- [ ] Add a comment in the constants block documenting the precedence ladder:
      `// Rank ladder: LLDP=1, CDP=2, OSPF=5, BGP=6, MPLS-TE=7, IS-IS=8`.
- [ ] Verify no graph merge test relies on the old value of 4.

**Exit criterion**: `go test ./internal/discovery/mpls/...` and `./internal/graph/...` green.

---

## Workstream 6 — Shared pduIP Helper (P2)

**Finding**: `pduIP` is defined identically in `internal/discovery/isis/isis.go` and
`internal/discovery/bgp/bgp.go`. Any future bug fix must be applied twice.

- [ ] Add `PDUIPv4(pdu gsnmp.SnmpPDU) net.IP` to
      `internal/discovery/snmp/snmputil.go` with the shared implementation.
- [ ] Delete `pduIP` from `internal/discovery/isis/isis.go` and
      `internal/discovery/bgp/bgp.go`; update both callers to `snmputil.PDUIPv4`.
- [ ] Confirm no other package defines the same helper.

**Exit criterion**: `grep -r 'func pduIP' .` returns no matches; all tests pass.

---

## Workstream 7 — OTLP Quality Fixes (P2)

Several quality issues in `internal/output/otlp/otlp.go`:

**7a — serviceResource allocation**

`serviceResource()` allocates a new `resource{}` struct on every `PushGraph` and
`PushChanges` call. It is a constant value.

- [ ] Replace `serviceResource()` calls with a package-level `var serviceRes = resource{...}`.
- [ ] Delete the `serviceResource()` function.

**7b — Remove WHAT comments**

Comments that narrate what the code does rather than why:
`// Build edge data points.` and `// Build device data points.` add no information.

- [ ] Delete both WHAT comments from `PushGraph`.
- [ ] Review remaining inline comments; delete any that describe the obvious operation.

**7c — Remove section dividers**

The seven `// ──` section-divider comments are aesthetic noise in a ~300-line file.

- [ ] Delete all `// ──` divider comment lines.

**Exit criterion**: `golangci-lint run ./internal/output/otlp/...` clean.

---

## Workstream 8 — MPLS-TE Quality Fixes (P2)

**8a — Remove explicit zero DstPort**

`DstPort: "",` in the edge literal is an explicit zero value for a string field — remove it.

- [ ] Delete `DstPort: "",` from the edge literal in `internal/discovery/mpls/mpls.go`.

**8b — Validate tunnel index before formatting SrcPort**

`parts[0]` is a raw OID component string used directly in `fmt.Sprintf("te-tunnel%s", parts[0])`.
A non-numeric OID component (malformed MIB response) would produce garbage port labels.

- [ ] Parse `parts[0]` with `strconv.Atoi`; skip the entry if it fails to parse.
- [ ] Use the parsed integer in `fmt.Sprintf("te-tunnel%d", tunnelIdx)`.

**Exit criterion**: `go test ./internal/discovery/mpls/...` green; malformed-index test case added.

---

## Workstream 9 — OTLP Push Health Metric (P2)

**Finding**: There is no metric tracking OTLP push outcomes. Operators have no
visibility into push failures until the topology stops updating in Grafana.

- [ ] Add `network_topology_otlp_push_total` counter with label `status` (`ok`, `error`)
      to the existing Prometheus metrics set.
- [ ] Increment on every `PushGraph` and `PushChanges` call in `runDiscoveryLoop`.
- [ ] Document the metric in `README.md` under the metrics reference table.

**Exit criterion**: Metric present in `/metrics` output; scrape test asserts the counter increments.

---

## Workstream 10 — OTLP Endpoint Validation / SSRF (P2)

**Finding**: `OTLPOutputConfig.Endpoint` is used directly in `http.NewRequest` with
no scheme or host validation. A misconfigured or adversarially set `endpoint` value
could direct push traffic to internal network services (SSRF).

- [ ] In `config.Validate()` (or a new `OTLPOutputConfig.Validate()` method), check
      that `Endpoint` parses as a valid URL, that the scheme is `http` or `https`,
      and that the host is non-empty.
- [ ] Return an error at startup if validation fails; do not silently ignore a bad
      endpoint.
- [ ] Add a test asserting that `file://`, `ftp://`, and empty-string endpoints all
      fail validation.

**Exit criterion**: `go test ./internal/config/...` green; bad-endpoint test cases pass.

---

## Workstream 11 — runDiscoveryLoop Refactor (P2)

**Finding**: `runDiscoveryLoop` now takes 9 parameters. Adding any future option
requires touching every call site and every test stub.

- [ ] Define a `loopConfig` struct in `cmd/topology-exporter/main.go` containing all
      current parameters except `ctx`.
- [ ] Change `runDiscoveryLoop(ctx context.Context, cfg loopConfig)`.
- [ ] Update all call sites (main + tests).

**Exit criterion**: `go build ./...` and `go test ./...` clean; function signature has ≤ 2 params.

---

## Workstream 12 — Documentation (P2)

- [ ] Add IS-IS module section to `README.md`: required MIB (RFC 4444), SNMP config
      snippet, `modules.isis.enabled: true`, what edges are produced.
- [ ] Add MPLS-TE module section to `README.md`: required MIB (RFC 3812), SNMP
      config snippet, `modules.mpls_te.enabled: true`, SrcPort format
      (`te-tunnel<idx>`), precedence rank relative to OSPF/BGP.
- [ ] Add OTLP output section to `README.md`: config block, heartbeat semantics,
      Grafana Alloy receiver snippet, `network_topology_otlp_push_total` metric.
- [ ] Document OTLP scrape-and-push collision in `docs/operations.md`: if
      `push_interval ≈ scrape_interval`, a slow push goroutine can overlap with the
      next scrape. Operator guidance: set `otlp.timeout` < `scrape_interval/2`.

**Exit criterion**: All three new modules have README entries; operations doc updated.

---

## Release Gate — v1.1.0

All workstreams above complete. Then:

- [ ] `go test -race -count=1 ./...`
- [ ] `golangci-lint run ./...`
- [ ] `helm lint deploy/helm/topology-exporter/`
- [ ] Tag `v1.1.0`, verify CI publishes container image and release binaries.
- [ ] Update companion repo `network-o11y-dev` to `tag: v1.1.0`.
