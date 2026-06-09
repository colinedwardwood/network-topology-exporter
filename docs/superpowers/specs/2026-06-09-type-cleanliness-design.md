# Config & Boundary Type Cleanliness — Design Spec (#152)

**Issue:** #152. **Process:** lighter — spec + one adversarial check + subagent-driven TDD.
**Status correction:** the originally-claimed `LinkKind` "latent bug" is a FALSE POSITIVE — the federation hub already defaults an empty `link_kind` to `ethernet` at the sole consumption site (`hub.go:880-882`), and validation never rejects it. So #152 is **pure cleanup, no behavior change.** Every YAML wire value and every validation error string stays byte-identical.

Three independent parts, one branch (`refactor/152-type-cleanliness`), one PR. Sequential TDD tasks (B and C touch disjoint files; do not parallelize subagents on the same package).

---

## Part A — relocate the `InterDomainLink.LinkKind` default to `applyDefaults`

**Not a bug fix** — a single-source-of-defaults consistency cleanup. Today the default lives at the consumer (`hub.go:881-882`: `if linkKind == "" { linkKind = LinkKindEthernet }`). Other config defaults all live in `applyDefaults`. 

**Change:** in `applyDefaults` (config.go), add a loop that sets the default eagerly:
```go
for i := range c.Federation.KnownInterDomainLinks {
	if c.Federation.KnownInterDomainLinks[i].LinkKind == "" {
		c.Federation.KnownInterDomainLinks[i].LinkKind = string(discovery.LinkKindEthernet)
	}
}
```
(Check whether config.go already imports `internal/discovery`; if not, use the literal `"ethernet"` with a comment rather than add an import just for one constant — match the file's existing style. The field stays `string`.)

**Keep the hub guard at `hub.go:881-882` as defense-in-depth** (it's cheap and `applyDefaults` running before hub use is a Load() invariant, not a hub-local one). Update the `InterDomainLink.LinkKind` doc comment to note the default is applied at load.

**Test:** a config-load test that a `KnownInterDomainLinks` entry with omitted `link_kind` has `LinkKind == "ethernet"` after `Load`/`applyDefaults`.

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
- **Consumer sites** (small blast radius — update comparisons to typed constants):
  - `internal/app/app.go` — Role checks (e.g. `cfg.Federation.Role == "hub"` → `== config.RoleHub`) and OTLP Protocol use.
  - `internal/tracing/tracing.go`, `internal/output/otlp/otlp.go` — Protocol use (these likely pass `string(protocol)` to SDK constructors; convert at the boundary with `string(...)`).
  - `internal/app/probe.go` — `CredentialProfile.Type` use.
  - Anywhere a typed value crosses into a `string`-typed API (gosnmp, OTLP SDK), convert explicitly with `string(...)` at the call site.

**Tests:** existing config validation tests must pass unchanged (error strings identical). Add a small `Valid()` table test per type. Existing example-config load tests are the wire-value regression gate.

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
- **Preserve EXACTLY:** the sem-full drop path + `OTLPPushTotal{dropped, n/a}` increment; the `wg.Add(1)` before goroutine + `defer wg.Done()`; `recoverGoroutine("otlp_push", …)` ordering (runs last); the background-ctx + `OTLPPushTimeout`; the `ok`/`error`+`ClassifyPushError` metric. The defer ORDER (recover registered first → runs last, after sem release + wg.Done) is load-bearing for the concurrency cap + drain correctness on panic.
- `app.go`: construct one `otlpPublisher` (enabled with exp+sem+wg when OTLP configured, else a no-op publisher); the shutdown drain at app.go:440 (`otlpWg.Wait()`) becomes `pub.drain()` (the publisher owns the wg).
- Update tests that set `OtlpExp/OtlpSem/OtlpWg` to construct an `otlpPublisher` instead. `goleak` tests for the OTLP push/drain must stay green (the drain semantics are unchanged).

**Tests:** existing OTLP push tests (drop-on-full, success/error classification, shutdown drain, goleak) must pass with the publisher; add a no-op-publisher test asserting `push` is a no-op when `exp == nil` (no goroutine, no metric).

---

## Cross-cutting
- No Co-Authored-By / AI-attribution trailers; author colinedwardwood.
- `go test ./... -race`, `gofmt`, `golangci-lint run` clean.
- CHANGELOG `### Internal` entry: typed config enums + LoopConfig OTLP-publisher collapse + LinkKind default relocated to applyDefaults (no behavior change).
- **No behavior change anywhere:** YAML values, validation errors, metric labels, and OTLP concurrency/drain semantics all identical.

## Files touched
- `internal/config/config.go` (+ maybe `config/enums.go`) — Part A defaults loop, Part B types/Validate/field changes/validation.
- `internal/app/app.go`, `internal/app/loop.go`, new `internal/app/otlp_publisher.go` — Part C.
- `internal/tracing/tracing.go`, `internal/output/otlp/otlp.go`, `internal/app/probe.go` — Part B consumer sites.
- `internal/federation/hub.go` — Part A doc note (guard kept).
- Tests in config/app packages; `CHANGELOG.md`.
