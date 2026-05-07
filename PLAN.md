# Roadmap to v1.0

## Where we are

All core features are implemented and the test suite is green with `-race`:

- Discovery: SNMP, LLDP, CDP, BGP, OSPF, FDB
- Graph: reconciliation (LD-10), scope guard (LD-11), credentials (LD-12), snapshot (LD-13), unidirectional TTL (LD-14)
- Federation: uncoordinated, spoke, hub, mTLS (LD-15–LD-21)
- Metric publication: atomic `TopologyCollector` snapshot — no scrape race, no empty windows
- CI: unit, integration, lint, helm-lint, docker, e2e (containerlab + SR Linux), release pipeline
- Deployment: Helm chart, distroless Dockerfile, PrometheusRule with 5 alerts

Overall test coverage: ~97%. The CI pipeline is wired for release on `v*` tags.

---

## Sprint 1 — 100% test coverage

**Goal:** `go test -race -count=1 ./...` exits green with 100% statement coverage across every
package that contains non-trivial runtime logic.

**Packages excluded from the 100% target** (explicitly not testable at statement level):

| Package | Reason |
|---|---|
| `internal/version` | Only ldflags-injected variables — no statements. |
| `internal/discovery` (root) | Interface and type declarations only — no statements. |

---

### Required refactor: extract `run()` from `main()`

`main()` currently calls `os.Exit` directly on any error. No statement in `main()` itself is
reachable from a test. Before adding test cases, refactor the entrypoint:

```
main()                 → calls os.Exit(run(context.Background(), os.Args[1:]))
run(ctx, args) error   → the current body of main(), returning error instead of os.Exit
```

`run()` must accept `ctx context.Context` so tests can cancel it after the first cycle.
After the refactor, `main()` is a 2-line wrapper that is intentionally excluded from coverage;
every statement in `run()` becomes reachable.

---

### cmd/topology-exporter (48.8% → 100%)

#### `run()` (extracted from `main()`)

| Test case | Branch covered |
|---|---|
| `--version` flag prints version and returns nil | early return at `--version` check |
| config file does not exist → error returned | `cfg.Load()` error path |
| config file has invalid YAML → error returned | `cfg.Load()` YAML unmarshal error |
| valid standalone config, context cancelled after first cycle | happy path; server shuts down cleanly |
| valid hub config → hub `Serve` goroutine started | hub mode initialization branch |
| spoke config with missing TLS files → error returned | `federation.NewSpoke` error path |
| HTTP listener bind fails (port already in use) → error returned | `net.Listen` error path |

#### `newLogger()`

| Test case | Branch covered |
|---|---|
| `"debug"` → `slog.LevelDebug` | debug case |
| `"warn"` → `slog.LevelWarn` | warn case |
| `"error"` → `slog.LevelError` | error case |
| unrecognized string → `slog.LevelInfo` | default case |

#### `runDiscoveryLoop()` (59.2% → 100%)

| Test case | Branch covered |
|---|---|
| snapshot load returns `ErrVersionMismatch` — loop continues with empty graph | version mismatch quarantine path |
| snapshot load returns a non-version I/O error — loop exits | fatal snapshot error |
| credential resolver build fails (invalid CIDR in config) — loop exits | resolver error path |
| context already cancelled at loop entry — no cycle runs | early ctx check |
| context cancelled mid-backoff between cycles — loop exits cleanly | tick/ctx select |
| spoke push fails (hub returns 500) but ctx is still active — counter incremented, loop continues | spoke push error, non-cancel path |
| first cycle completes — `GraphStale` set to 0 | `ready` store path |

#### `runCycle()` (60.0% → 100%)

| Test case | Branch covered |
|---|---|
| DNS lookup for a hostname target fails — device skipped | `LookupHost` error |
| DNS lookup returns empty result — device skipped | empty addresses |
| target IP outside CIDR allow-list — device skipped | CIDR rejection |
| all devices time out — `DiscoveryDevicesTotal{timeout}` incremented | all-timed-out path |
| one device fails, others succeed — mixed status counters | partial failure |
| `AgeUnconfirmed` expires an edge — `TopologyChangeTotal{removed}` incremented | edge expiry path |
| a module returns an error that is not `DeadlineExceeded` — logged, cycle continues | non-timeout module error |

#### `credentialCandidates()` (55.6% → 100%)

| Test case | Branch covered |
|---|---|
| no profiles configured, community string set — returns single v2c candidate | legacy single-community path |
| per-IP assignment matches — that profile is first in list | per-IP assignment |
| per-CIDR assignment matches — that profile is first in list | per-CIDR assignment |
| resolver returns error for a profile entry — error propagated | profileToParams error |

#### `walkSystemWithCredentials()` (66.7% → 100%)

| Test case | Branch covered |
|---|---|
| candidates slice is empty — returns `(Device{}, ErrNoCredentials)` | empty candidates early return |
| all candidates return `context.Canceled` — returns context error immediately | Canceled propagation |
| one candidate times out, next succeeds — success returned | allTimedOut=false path |
| all candidates time out — returns timeout error | allTimedOut=true path |

---

### internal/config (95.2% → 100%)

All missing branches are error paths in `Load` and normalization helpers:

| Test case | Branch covered |
|---|---|
| `Load("")` (empty path) — returns read error | `ReadFile` error |
| `Load` with syntactically invalid YAML — returns unmarshal error | YAML parse error |
| `Load` with semantically invalid config (e.g., no CIDR) — returns validation error | `validate` error |
| `normalizeAuthProtocol("MD5")` — returns error (broken MD5) | MD5 error branch |
| `normalizeAuthProtocol("unknown")` — returns error | unknown auth protocol |
| `normalizePrivProtocol("DES")` — returns error (broken DES) | DES error branch |
| `normalizePrivProtocol("unknown")` — returns error | unknown priv protocol |
| `validateFederation` with `role: spoke` but no `hub_url` — returns error | spoke missing hub_url |
| `validateFederation` with `role: spoke` but missing TLS triplet — returns error | spoke missing TLS |
| `validateFederation` with `role: hub` but missing TLS triplet — returns error | hub missing TLS |
| `validateFederation` with `role: hub` and `spoke_timeout < 2×interval` — returns error | spoke_timeout too small |
| `validateFederation` with invalid role string — returns error | unknown role |

---

### internal/credentials (94.5% → 100%)

| Test case | Branch covered |
|---|---|
| `New()` with an invalid CIDR in an assignment — returns parse error | CIDR parse error in New |
| `newTokenBucket(0)` or negative rate — rate is clamped to 1 | rate < 1 clamp |
| `refillLocked` called twice in rapid succession with no time advance — tokens unchanged | elapsed ≤ 0 |
| `refillLocked` called with elapsed < 1/rate — add ≤ 0, tokens unchanged | add ≤ 0 |

---

### internal/snapshot (68.8% → 100%)

All missing branches are I/O error paths. Use `os.TempDir()` + `chmod 000` on parent directory or
a custom `WriteFn` injection to force failures.

| Test case | Branch covered |
|---|---|
| `Load` on a file whose version field is wrong — file is quarantined | `ErrVersionMismatch` quarantine |
| `quarantine`: `.bad` file already exists — suffix increments until a free name is found | `ErrExist` loop-continue |
| `quarantine`: `OpenFile` fails — returns error | OpenFile error |
| `quarantine`: `Write` fails after open — temp write error path | write error + cleanup |
| `quarantine`: `Close` fails — error returned | Close error |
| `quarantine`: final `os.Remove` of original fails — error returned | Remove error |
| `Write`: `json.Marshal` fails (inject un-marshalable type via writeFn) — returns error | Marshal error |
| `Write`: `MkdirAll` fails (parent dir is a file, not a dir) — returns error | MkdirAll error |
| `Write`: `CreateTemp` fails (dir is read-only) — returns error | CreateTemp error |
| `Write`: `tmp.Write` fails — temp write error | Write to tmp error |
| `Write`: `tmp.Sync` fails — fsync error | Sync error |
| `Write`: `tmp.Close` fails — close error | Close error |
| `Write`: `os.Rename` fails — rename error | Rename error |

Note: the `snapshot` package already uses an injectable `writeFn` for some paths. Extend the same
pattern to allow test-injected failures for `MkdirAll`, `CreateTemp`, and `os.Rename`.

---

### internal/metrics (77.4% → 100%)

| Test case | Branch covered |
|---|---|
| `TopologyCollector.Collect` with `emitBoundaryObs=true` and graph with OutOfScope entries | boundary obs emission path |
| `TopologyCollector.Collect` with `emitBoundaryObs=true`, `canonicalPair` where `a > b` | `canonicalPair` swap branch |
| `TopologyCollector.Describe` with `emitBoundaryObs=true` — all 5 descriptors returned | Describe with boundary obs |
| `TopologyCollector.Collect` with empty graph (nil Devices, nil Edges) — oosCount=0, no panics | empty snapshot safety |
| Concurrent `Update` + `Collect` from 10 goroutines — race detector clean | concurrency correctness |

---

### internal/federation (84.2% → 100%)

#### hub.go

`hub.Serve()` error paths are unit-testable by providing bad config values (nonexistent cert files,
invalid port). The happy-path `Serve` is already covered by `tests/integration/federation_test.go`.

| Test case | Branch covered |
|---|---|
| `Serve` with nonexistent CA cert file — returns error | ReadFile error |
| `Serve` with a CA file that contains no valid PEM — returns error | AppendCertsFromPEM false |
| `Serve` with invalid cert/key pair — returns error | LoadX509KeyPair error |
| `Serve` with listen address already in use — returns error | Listen error |
| `IsReady()` before any push — returns false | firstLive=false |
| `IsReady()` after a push is accepted — returns true | firstLive=true (already tested but ensure explicit assertion) |
| `handlePush` with method GET — 405 | non-POST method |
| `handlePush` with malformed JSON body — 400 | JSON decode error |
| `handlePush` with device count over `maxDevicesPerPush` — 413 | device limit |
| `handlePush` with edge count over `maxEdgesPerPush` — 413 | edge limit |
| `handlePush` with empty `spoke_id` — 400 | empty spoke_id |
| `handlePush` with spoke_id longer than 128 chars — 400 | spoke_id too long |
| `handlePush` with spoke_id containing `!` — 400 | invalid char |
| `handlePush` with r.TLS set and cert CN ≠ spoke_id — 403 | CN mismatch (LD-21) |
| `handlePush` with zero `cycle_at` — 400 | zero CycleAt |
| `handlePush` with `cycle_at` 10 minutes in the future — 400 | future CycleAt |
| `handlePush` with `cycle_at` older than `spoke_timeout` — 400 | stale CycleAt |
| `buildCombinedGraph` with `KnownInterDomainLinks` — link injected at rank 0 | LD-19 known links path |

#### spoke.go

| Test case | Branch covered |
|---|---|
| `NewSpoke` with nonexistent CA cert — returns error | ReadFile error (no TLS config) |
| `NewSpoke` with CA file containing no valid PEM — returns error | AppendCertsFromPEM false |
| `NewSpoke` with invalid cert/key pair — returns error | LoadX509KeyPair error |
| `Push` with a payload that fails json.Marshal (inject via test wrapper) | Marshal error path |
| `post` returns non-204 status — returns error | non-204 response |

---

### internal/graph (93.9% → 100%)

| Test case | Branch covered |
|---|---|
| `Reconcile(nil)` — returns empty slice, no panic | empty input |
| `Diff(nil, nil)` — returns empty slice, no panic | both nil |
| `Diff` where an edge exists in `before` but not `after` — returns `Removed` change | beforeMap remainder |
| `compareEdgeKey` where `a.SrcDevice < b.SrcDevice` — returns -1 | SrcDevice less |
| `compareEdgeKey` where `a.SrcDevice > b.SrcDevice` — returns +1 | SrcDevice greater |
| `compareEdgeKey` where SrcDevice equal, `a.SrcPort < b.SrcPort` — returns -1 | SrcPort comparison |
| `compareEdgeKey` through all six fields, all equal — returns 0 | default return |

---

### internal/discovery/snmp (79.7% → 100%)

`internal/snmptest` provides the mock agent; use it for all these tests.

#### snmp.go

| Test case | Branch covered |
|---|---|
| `Open` on an unreachable address — returns connect error | Open error |
| `BulkWalk` with context already cancelled — returns ctx error immediately | pre-call ctx check |
| `BulkWalk` with agent that returns error — falls back to `WalkAll` and succeeds | BulkWalk→WalkAll fallback |
| `Walk` on a device that returns no sysName PDU — device returned with empty Name | missing sysName |
| `Walk` on a device where `Open` fails — returns error | Walk→Open error |

#### pdu.go

| Test case | Branch covered |
|---|---|
| `WalkIfNames` against a mock agent that returns ifName entries | happy path |
| `WalkIfNames` with mock agent returning BulkWalk error — returns error | BulkWalk error |
| `WalkIfNames` with a PDU whose OID suffix is non-numeric — entry skipped | Atoi error skip |
| `ParseCIDRs([]string{"10.0.0.0/8", "not-a-cidr"})` — invalid entry silently skipped | parse error skip |

---

### internal/discovery/lldp (79.0% → 100%)

| Test case | Branch covered |
|---|---|
| `Walk` with mock agent that returns BulkWalk error on loc-ports table — Walk returns error | walkLocPorts error |
| `Walk` with mock agent that returns BulkWalk error on rem-entries table — Walk returns error | walkRemEntries error |
| `walkLocPorts`: PDU with non-numeric port-number OID suffix — entry skipped | Atoi error |
| `walkRemEntries`: PDU with malformed OID components — entry skipped | SplitOIDComponent error |
| `resolveLocalPort`: port number not in locPorts map — returns `strconv.Itoa(portNum)` | portNum fallback |
| `resolveRemDevice`: sysName PDU missing — falls back to `decodeChassisID` result | sysName empty fallback |
| `decodePortID`: empty raw bytes — returns empty string | len(raw)==0 |
| `decodePortID`: subtype not MAC/network-address — returns hex.EncodeToString | default switch |
| `decodeChassisID`: empty raw bytes — returns empty string | len(raw)==0 |
| `decodeChassisID`: subtype not MAC/network-address — returns hex.EncodeToString | default switch |
| `extractChassisIP`: subtype is not 1 or 2 — returns nil | unknown subtype |
| `extractChassisIP`: IPv4 raw length ≠ 5 — returns nil | bad IPv4 length |
| `extractChassisIP`: IPv6 raw length ≠ 17 — returns nil | bad IPv6 length |
| `fmtNetAddr`: `extractChassisIP` returns nil — falls back to hex encoding | nil IP fallback |

---

### internal/discovery/cdp (86.4% → 100%)

| Test case | Branch covered |
|---|---|
| `Walk` with mock agent returning Open error — Walk returns error | Open error |
| `Walk` with mock agent returning WalkIfNames error — Walk returns error | WalkIfNames error |
| `Walk` with mock agent returning walkCacheTable error — Walk returns error | walkCacheTable error |
| `walkCacheTable`: PDU with malformed OID suffix — entry skipped | OID parse error |
| `walkCacheTable`: entry with empty device-id or port — edge not produced | empty field skip |

---

### internal/discovery/bgp (82.0% → 100%)

| Test case | Branch covered |
|---|---|
| `Walk` with mock agent returning Open error — Walk returns error | Open error |
| `Walk` with mock agent returning walkBgpPeerTable error — Walk returns error | walkBgpPeerTable error |
| `pduIP` with a string-type PDU — returns parsed IP | string branch |
| `pduIP` with an IPAddress-type PDU — returns bytes-decoded IP | IPAddress branch |
| `buildEdges`: peer IP outside allowedNets — edge not produced | OOS peer skip |

---

### internal/discovery/ospf (85.2% → 100%)

| Test case | Branch covered |
|---|---|
| `Walk` with mock agent returning Open error — Walk returns error | Open error |
| `Walk` with mock agent returning walkOspfNbrTable error — Walk returns error | walkOspfNbrTable error |
| `parseNbrOID`: OID with fewer components than expected — returns error | short OID |
| `parseNbrOID`: OID with non-numeric IP component — returns error | Atoi error |
| `buildEdges`: neighbour in state other than Full/TwoWay — edge not produced | state filter |
| `buildEdges`: neighbour IP outside allowedNets — edge not produced | OOS neighbour |

---

### internal/discovery/fdb (84.4% → 100%)

| Test case | Branch covered |
|---|---|
| `Walk` with mock agent returning Open error — Walk returns error | Open error |
| `walkFdbTableInto`: PDU with malformed OID suffix — entry skipped | OID parse error |
| `walkQBridgeFdbTable`: BulkWalk error — returns error | Q-Bridge BulkWalk error |
| `discoverVlanIDs`: BulkWalk returns no entries — returns empty map | empty VLAN table |
| `discoverVlanIDs`: BulkWalk error — returns error | VLAN BulkWalk error |
| `walkVlanCommunityFdbs`: community BulkWalk error — logged, continues | VLAN community error |
| `walkBasePortTable`: BulkWalk error — returns error | base port BulkWalk error |
| `walkBasePortTable`: PDU with malformed bridge port number — entry skipped | Atoi error |
| `walkStpPortStates`: BulkWalk error — returns error | STP BulkWalk error |
| `walkStpPortStates`: port in non-forwarding state — port excluded from result | non-forwarding filter |

---

### internal/snmptest (0% → 100%)

`snmptest` is a test helper that shows 0% because no `_test.go` lives in the package itself.
Its functions are exercised transitively through discovery tests, but statement-level coverage
requires an in-package test.

Add `internal/snmptest/agent_test.go`:

| Test case | Branch covered |
|---|---|
| `Start` with a fixture that has one OID entry, send GET → correct value returned | Start + handleGet |
| `Start` with fixture, send GETNEXT → next OID value returned | handleGetNext |
| `Start` with fixture, send GETBULK for multiple OIDs → all returned | handleBulk |
| `Start` with empty fixture, send GET for nonexistent OID → noSuchObject | exactLookup miss |
| `Start` with empty fixture, send GETNEXT → endOfMibView | nextLookup end |
| `ParseAddr("")` — returns error | ParseAddr error path |
| `ParseAddr("127.0.0.1:0")` — returns resolved address | ParseAddr success |
| `StartMultiCommunity` with two communities, wrong community rejected | serveMulti community check |
| `oidLess`: OID a < b (different lengths) — returns true | shorter OID |
| `oidLess`: identical OIDs — returns false | equal OID |
| `nextComponent`: empty string — returns error | empty input |
| `sendReply`: unrecognized PDU type does not panic | unknown PDU type |

---

## Sprint 2 — Documentation accuracy pass

The federated topology refactor changed how metrics are published. Several docs still reference
the old Reset+repopulate model.

- `docs/architecture.md` — Update the "Concurrency" section: the RWMutex description no longer
  applies to topology metrics; replace with the atomic-pointer-swap model.
- `docs/operator/federation.md` — Verify all config field names match the current schema.
- `docs/operator/troubleshooting.md` — Add a section on "Topology metrics absent from /metrics"
  covering the empty-graph startup window and snapshot restore.
- `README.md` status line — change "Pre-v1, in active development" to "Release candidate" once
  Sprint 1 is complete.
- Add `CHANGELOG.md` with a v1.0.0 entry summarising LD-09 through LD-21.

---

## Sprint 3 — v1.0 release

**Gating criteria** (all must be green before tagging v1.0.0):

- [ ] `go test -race -count=1 ./...` exits green with 100% coverage
- [ ] `go test ./tests/integration/... -tags integration` clean
- [ ] `golangci-lint run ./...` zero findings
- [ ] `helm lint deploy/helm/topology-exporter/` clean
- [ ] E2E tests pass locally (`make e2e-image && CLAB_DOCKER=1 make test-e2e`)
- [ ] `docs/architecture.md` concurrency section updated
- [ ] `CHANGELOG.md` present

**Release steps:**

1. Tag `v1.0.0` on main.
2. CI release job pushes `ghcr.io/<repo>:1.0.0`, `ghcr.io/<repo>:1.0`, and creates the GitHub
   release with amd64/arm64 binaries.
3. Update companion repo (`network-o11y-dev`) to reference the stable image tag.

---

## Deferred (post-v1.0)

- **IS-IS topology** — `internal/discovery/isis/` using ISIS-MIB (RFC 4444).
- **MPLS-TE / SR-TE** — SR-Policy API or MPLS-TE-MIB.
- **Grafana Alloy plugin** — OTEL resource attributes output for teams without a Prometheus
  scrape path.
- **NetBox writeback** — permanently out of scope (see `docs/architecture.md`).
- **ARP as a topology source** — permanently out of scope (same section).

---

## Coverage baseline (2026-05-07, post-Sprint-1)

| Package | Coverage | Notes |
|---|---|---|
| `events` | 100.0% | |
| `config` | 100.0% | |
| `credentials` | 100.0% | |
| `graph` | 100.0% | |
| `metrics` | 100.0% | |
| `snapshot` | 100.0% | |
| `discovery/bgp` | 100.0% | |
| `discovery/fdb` | 100.0% | |
| `discovery/ospf` | 100.0% | |
| `federation` | 99.2% | 1 unreachable TLS error branch |
| `discovery/snmp` | 99.3% | Defensive OID guard (gosnmp BER guarantee) |
| `snmptest` | 97.9% | 2 defensive branches in sendReply |
| `discovery/cdp` | 98.3% | Defensive OID guard |
| `discovery/lldp` | 96.8% | Defensive OID guard |
| `cmd/topology-exporter` | 87.4% | `main()` excluded by design (calls os.Exit) |
