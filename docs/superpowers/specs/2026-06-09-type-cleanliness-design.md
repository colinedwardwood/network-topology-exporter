# Config & Boundary Type Cleanliness — Design Spec (#152)

**Issue:** #152. **Process:** lighter — spec + one adversarial check + subagent-driven TDD.
**Status correction:** the originally-claimed `LinkKind` "latent bug" is a FALSE POSITIVE — the federation hub already defaults an empty `link_kind` to `ethernet` at the sole consumption site (`hub.go:880-882`), and validation never rejects it. So #152 is **pure cleanup, no behavior change.** Every YAML wire value and every validation error string stays byte-identical.

Two parts (B + C), one branch (`refactor/152-type-cleanliness`), one PR. Sequential TDD tasks (they touch disjoint files).

---

## Part A — DROPPED (do not implement)

The original plan relocated the `InterDomainLink.LinkKind` `""`→`"ethernet"` default from the consumer (`hub.go:881-882`) into `applyDefaults`. The adversarial check found this is **not** a stray default: `config_test.go:1257 TestFederationKnownLinkOptionalLinkKind` *explicitly asserts* `LinkKind == ""` after `Load()` with the comment "default applied by hub at injection time" — i.e. keeping the loaded-config field empty and defaulting at the single hub consumer is a **deliberate, tested design**. Relocating it would change the post-`Load` config state, fight that test, and deliver zero functional change (the hub output is identical either way). That is exactly the kind of "fixing" a non-problem we declined for #150. **No change to LinkKind handling.** (#152 is therefore Parts B + C only.)

---

## Part B — type the four stringly-typed config enums

Promote four `string` fields to named types with constants + a `Validate()` method. **Wire-value identity is mandatory** (these are YAML contract values): the underlying string constants stay byte-identical, YAML unmarshals into a named string type unchanged, and the existing validation error strings are preserved verbatim.

Define (in config.go or a new `config/enums.go`):
```go
type Role string
const (
	RoleStandalone    Role = "standalone"
	RoleUncoordinated Role = "uncoordinated"
	RoleSpoke         Role = "spoke"
	RoleHub           Role = "hub"
)
func (r Role) Valid() bool { switch r { case RoleStandalone, RoleUncoordinated, RoleSpoke, RoleHub: return true }; return false }

type OTLPProtocol string
const ( OTLPProtocolHTTP OTLPProtocol = "http"; OTLPProtocolGRPC OTLPProtocol = "grpc" )
func (p OTLPProtocol) Valid() bool { return p == OTLPProtocolHTTP || p == OTLPProtocolGRPC }

type SNMPVersion string
const ( SNMPVersionV2c SNMPVersion = "v2c"; SNMPVersionV3 SNMPVersion = "v3" )
func (v SNMPVersion) Valid() bool { return v == SNMPVersionV2c || v == SNMPVersionV3 }

type ProfileType string
const ( ProfileTypeSNMPv2c ProfileType = "snmp_v2c"; ProfileTypeSNMPv3 ProfileType = "snmp_v3" )
func (t ProfileType) Valid() bool { return t == ProfileTypeSNMPv2c || t == ProfileTypeSNMPv3 }
```
- Change the struct fields: `FederationConfig.Role Role` (yaml unchanged), `OTLPOutputConfig.Protocol OTLPProtocol`, `ModuleSNMP.Version SNMPVersion`, `CredentialProfile.Type ProfileType`.
- The existing `ProfileTypeSNMPv2c`/`ProfileTypeSNMPv3` are currently UNTYPED string consts (config.go:387-390) — retype them to `ProfileType` (callers using them in `switch p.Type` still compile since `p.Type` is now `ProfileType`).
- `applyDefaults`: the `== ""` / assignment lines stay but compare/assign typed values — e.g. `if c.Modules.SNMP.Version == "" { c.Modules.SNMP.Version = SNMPVersionV2c }` (untyped `""` compares fine; assign the typed const). Same for Role/Protocol.
- Validation: keep the existing error strings EXACTLY. Either keep the switches (now over typed values — `case RoleStandalone, RoleUncoordinated:` etc.) or call `.Valid()`; the error messages (`"federation.role must be standalone, uncoordinated, spoke, or hub; got %q"`, `"modules.snmp.version must be v2c or v3, got %q"`, `"output.otlp.protocol must be http or grpc, got %q"`, `"profile %q has unknown type %q"`) must remain byte-identical (the `%q` of a named string type prints the same as the bare string).
- **Consumer sites** (small blast radius — verified exhaustively by the adversarial check):
  - `internal/app/app.go` — Role comparisons against untyped string literals (`== "uncoordinated"/"spoke"/"hub"`) still compile unchanged; the OTLP Protocol is passed to `tracing.Protocol(...)` / `otlp.Protocol(...)`, which are themselves named string types — `tracing.Protocol(cfg.Output.OTLP.Protocol)` is a single valid conversion, **no inner `string(...)` needed** (don't add spurious casts).
  - `internal/app/probe.go` — the `switch p.Type` compiles once `ProfileType` consts are retyped.
  - `internal/tracing/tracing.go`, `internal/output/otlp/otlp.go` — no change needed (they receive their own `Protocol` named type).
  - **`internal/app/watchdog_test.go:39` (REQUIRED — the one missed call site that breaks the build):** it assigns a `string` *variable* (`tc.role`) to `Federation.Role`. Once `Role` is a named type this is a compile error. Fix: change the test table field from `role string` to `role config.Role` (the case literals stay untyped constants and assign fine).

**Tests:** existing config validation tests pass unchanged — verified they assert only `err != nil`, never the exact string, and no path marshals these config structs to JSON/YAML (so a named type changes no wire output). Add a small `Valid()` table test per type. Existing example-config load tests are the wire-value regression gate.

---

## Part C — collapse `LoopConfig`'s three nilable OTLP fields into one publisher

Today `LoopConfig` carries `OtlpExp *otlp.Exporter`, `OtlpSem chan struct{}`, `OtlpWg *sync.WaitGroup` (loop.go:63-65), all nil when OTLP is disabled, producing scattered `if lc.Otlp* != nil` guards in `OtlpPush` (loop.go:95-122) and the publish path (loop.go:398-416). The three are always all-set or all-nil (app.go:188, 355-365 wire them together only when OTLP is enabled).

**Change:** introduce one always-non-nil publisher (app package, e.g. `otlp_publisher.go`):
```go
// otlpPublisher owns the OTLP exporter plus the concurrency cap and in-flight
// tracking for async pushes. A publisher whose exp is nil is a no-op (OTLP
// disabled), so callers never nil-check.
type otlpPublisher struct {
	exp    *otlp.Exporter // nil ⇒ disabled, push() is a no-op
	sem    chan struct{}
	wg     *sync.WaitGroup
	logger *slog.Logger
	m      *metrics.Metrics
}

// push runs fn asynchronously under the concurrency cap, tracked for shutdown
// drain, with panic recovery and #20 error classification. No-op when disabled.
func (p *otlpPublisher) push(fn func(context.Context) error, warnMsg string) { /* the current OtlpPush body, minus the nil-checks (sem/wg are non-nil iff exp != nil) */ }

// drain blocks until in-flight pushes finish (shutdown). No-op when disabled.
func (p *otlpPublisher) drain() { if p.wg != nil { p.wg.Wait() } }
```
- Replace the three `LoopConfig` fields with one `Otlp *otlpPublisher` (always non-nil; constructed disabled in tests/standalone).
- `OtlpPush` becomes a thin `lc.Otlp.push(...)` (or move callers directly to `lc.Otlp.push`). The publish path (loop.go ~398-416) calls `lc.Otlp.push(func(ctx) error { return lc.Otlp.exp.PushChanges(ctx, ch) }, …)` etc. — but since `push` no-ops when disabled, the outer `if lc.OtlpExp != nil` guards are removed. (Keep the `len(changes)>0 || heartbeat` gate — that's logic, not a nil-check.)
- **CRITICAL invariant the collapse depends on (from the adversarial check):** the "no-op when `exp==nil`, otherwise sem+wg non-nil" model means the **enabled publisher MUST allocate `sem` AND `wg` together as a unit** (and the disabled publisher leaves all three nil and `push` early-returns on `exp==nil` BEFORE touching sem/wg). This is true in production wiring, but the current TESTS deliberately set partial combinations against `OtlpPush` directly with `exp==nil` (e.g. `loop_test.go:51`+`main_test.go:1819` set sem-only; `loop_test.go:90`+`main_test.go:1846` set wg-only, sem nil). If `push` drops the sem/wg nil-checks, those fixtures would send on a nil channel (deadlock) / `Done()` a nil wg (panic). **Resolution:** rewrite those four tests to construct an *enabled* `otlpPublisher` (exp+sem+wg together) — which is what they were really exercising (the cap/drain behavior). `push` keeps exactly ONE guard: `if p.exp == nil { return }` at the top; everything after assumes sem+wg are set.
- **Preserve EXACTLY:** the sem-full drop path + `OTLPPushTotal{dropped, n/a}` increment; the `wg.Add(1)` before goroutine + `defer wg.Done()`; `recoverGoroutine("otlp_push", …)` ordering (runs last); the background-ctx + `OTLPPushTimeout`; the `ok`/`error`+`ClassifyPushError` metric. The defer ORDER (recover registered first → runs last, after sem release + wg.Done) is load-bearing for the concurrency cap + drain correctness on panic.
- `app.go`: **declare `pub *otlpPublisher` BEFORE the role switch as a no-op (`&otlpPublisher{}`)**, and assign the enabled one (exp+sem+wg) inside the OTLP-enabled branch — so hub mode (which never enables OTLP) has a non-nil no-op `pub` and the shared shutdown-drain at app.go:440 (`otlpWg.Wait()` → `pub.drain()`) never nil-panics. `drain()` no-ops when `wg==nil`.
- `goleak` tests for the OTLP push/drain must stay green (the drain semantics are unchanged).

**Tests:** existing OTLP push tests (drop-on-full, success/error classification, shutdown drain, goleak) must pass with the publisher; add a no-op-publisher test asserting `push` is a no-op when `exp == nil` (no goroutine, no metric).

---

## Cross-cutting
- No Co-Authored-By / AI-attribution trailers; author colinedwardwood.
- `go test ./... -race`, `gofmt`, `golangci-lint run` clean.
- CHANGELOG `### Internal` entry: typed config enums + LoopConfig OTLP-publisher collapse (no behavior change). (Part A dropped — no LinkKind change.)
- **No behavior change anywhere:** YAML values, validation errors, metric labels, and OTLP concurrency/drain semantics all identical. (Verified: no config struct is marshaled to JSON/YAML, and validation tests assert only `err != nil`.)

## Files touched
- `internal/config/config.go` (+ maybe `config/enums.go`) — Part B types/Validate/field changes/validation switches over typed values.
- `internal/app/app.go`, `internal/app/loop.go`, new `internal/app/otlp_publisher.go` — Part C.
- `internal/app/probe.go` — Part B `CredentialProfile.Type` consumer.
- `internal/app/watchdog_test.go` — Part B: type the test's `role` field (REQUIRED for compilation).
- Tests in config/app packages (incl. rewriting the four `OtlpPush` tests to the enabled-publisher form); `CHANGELOG.md`.
- NOT touched: `internal/federation/hub.go`, `internal/tracing/tracing.go`, `internal/output/otlp/otlp.go` (Part A dropped; the named-type conversions at the tracing/otlp boundary need no edits).
