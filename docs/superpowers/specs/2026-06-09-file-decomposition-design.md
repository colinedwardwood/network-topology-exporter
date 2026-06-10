# File Decomposition: config.go + fdb.go — Design Spec (#151)

**Issue:** #151. **Process:** lighter — spec + subagent-driven implementation + spec/quality review (no separate adversarial design pass: this is pure declaration-movement within a package; the Go compiler resolves all references and the unchanged test suite is the behavior gate). **Zero behavior change — declaration moves only, no logic edits.**

## Goal
Bring two oversized files under the 1000-line maintainability boundary by splitting them along responsibility, with **no code changes other than moving declarations between files in the same package** (and fixing imports). Same package names, same exported/unexported surface, same tests.

---

## Part 1 — `internal/config/config.go` (1105 lines, post-#152) → split

All new files are `package config` in `internal/config/`. Move declarations verbatim (doc comments travel with their decl). The package-level doc comment stays atop `config.go`.

- **`types.go`** — every struct/type definition and its directly-attached small accessor methods: `Config`, `ListenConfig`, `OutputConfig`, `YANGOutputConfig`, `OTLPOutputConfig`, `OTLPTracesConfig`, `FederationConfig`, `FederationHubConfig`, `FederationSpokeConfig`, `InterDomainLink`, `DiscoveryConfig` (+ its `LivenessMaxStaleCyclesValue` method), `DiscoverySNMPConfig`, `SessionPoolConfig`, `TargetOverride`, `ScopeConfig`, `FDBConfig`, `BGPConfig`, `ModulesConfig`, `ModuleToggle`, `ModuleSNMP`, `CredentialsConfig`, `CredentialProfile` (+ the `ProfileType` consts already live in `enums.go` — leave them there), `CredentialAssignment`, `SnapshotConfig`, `TargetConfig`. (~lines 21-438.)
- **`defaults.go`** — `applyDefaults` (~457-539) and any default literals/consts it introduces.
- **`validate.go`** — the whole validation family: `validateListen`, `validate`, `validateHTTPEndpoint`, `validateHTTPSEndpoint`, `validateScope`, `validateCredentials`, `normalizeAuthProtocol`, `normalizePrivProtocol`, `validateTargets`, `validateFederation`, `validateFDB`, `validateTargetOverrides`, `cidrContainsAny` (~540-924, 1054-1098).
- **`target_override.go`** — the LD-overrides resolver: `targetOverride`, `targetOverrideResolver`, `buildTargetOverrideResolver`, `ModulesForIP`, `BuildTargetOverrideResolver`, and `moduleGloballyEnabled` (~951-1052).
- **`config.go`** (remaining) — the package doc comment + `Load` (the entry point that calls `applyDefaults`+`validate`+`BuildTargetOverrideResolver`).

Target: each file well under 1000 (types.go ~400, validate.go ~450, the rest small). No `_test.go` changes (same package, same symbols).

---

## Part 2 — `internal/discovery/fdb/fdb.go` (705 lines) → extract the VLAN-community sub-engine

Move the self-contained IOS VLAN-community-string FDB sub-engine to a new **`internal/discovery/fdb/vlan_community.go`** (`package fdb`). It is a cohesive unit (concurrent, semaphore-bounded, panic-recovering per-VLAN walker) with nothing structurally in common with the standard B-MIB/Q-BRIDGE/STP decoders.

Move (verbatim): `walkVlanCommunityFdbs`, `discoverVlanIDs`, `parseQBridgeIndex`, the `maxVlanConcurrency` const, and any helper/seam used **only** by that path (e.g. a `walkQBridgeFdbTableFn`-style test seam if it's VLAN-community-specific — read the file and move what's exclusively VLAN-community; leave shared helpers like `BulkWalk`/`TrimOIDPrefix` callers that the standard path also uses in fdb.go). Leave `fdb.go` as "decode the standard tables → classify edges" (~450-590 lines).

If a symbol is used by BOTH the standard path and the VLAN-community path, leave it in `fdb.go` (don't move shared code). The implementer must read the actual call graph before moving — only move what's exclusively the VLAN-community engine.

---

## Verification (the gate — no behavior change)
- **The diff must contain ONLY moved declarations + import-list adjustments — NO logic edits.** A reviewer should be able to confirm each moved block is byte-identical to its original (modulo whitespace/import grouping). Call out any line that isn't a pure move.
- `go build ./...` clean; `go test ./... -race` PASS with **unchanged test files**; `gofmt -l` empty; `golangci-lint run` 0 issues.
- `wc -l internal/config/*.go internal/discovery/fdb/fdb.go` — config.go and fdb.go both under ~600; no new file over 1000.
- No exported/unexported symbol added, removed, or renamed (the API is identical).

## Cross-cutting
- One branch (`refactor/151-decompose-files`), one PR closing #151. No Co-Authored-By / AI-attribution trailers; author colinedwardwood.
- CHANGELOG `### Internal` entry: split config.go and fdb.go along responsibility (no behavior change).
- Do them as two commits (config split, fdb split) for reviewability.

## Files
- New: `internal/config/types.go`, `internal/config/defaults.go`, `internal/config/validate.go`, `internal/config/target_override.go`, `internal/discovery/fdb/vlan_community.go`.
- Modified (shrunk): `internal/config/config.go`, `internal/discovery/fdb/fdb.go`, `CHANGELOG.md`.
