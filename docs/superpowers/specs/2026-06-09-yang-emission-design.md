# RFC 8345 YANG topology emission (#75) — implementation design

**Goal:** expose the reconciled topology as RFC 8345 / RFC 8346 YANG-JSON at a pull
endpoint, behind `output.yang.enabled: false`, validated in CI with `yanglint`.

**Source of truth for the data mapping:** the already-committed, reviewed
`docs/operator/yang-topology.md` (PR #93). This spec does **not** restate the
mapping; it honors that contract verbatim and covers what that doc does not:
delivery, renderer architecture, correctness guarantees, validation, security,
config, and files. Where this spec and the mapping doc could drift, the mapping
doc wins (and we update it to resolve its two open `TBD`s — the augmentation
namespace URI, and "planned" → "implemented").

This design was revised after an adversarial review (2026-06-09) that caught the
original draft contradicting the committed contract. Decisions taken from that
review are marked **[AR]**.

## Scope (confirmed)

- Honor the committed contract: one `network` with
  `network-types: { ietf-l3-unicast-topology:l3-unicast-topology: {} }` (RFC 8346
  marker); `l3-node-attributes` (router-id/prefixes) **omitted** — documented gap,
  not collected. This is 8345 structure + the 8346 type declaration, not "8345
  only" and not "full 8346".
- `Device → node`; incident ports → `termination-point`; `Edge → link` with
  **bidirectional → two links**, unidirectional → one. Rich metadata in the
  `ntx-topology` augmentation (plain `enumeration` leaves).
- **Out of scope:** populating L3 attributes (no router-id/prefix collection),
  any push/file delivery, multiple `network` instances, RESTCONF datastore
  semantics.

## Delivery

A pull HTTP endpoint **`GET /topology/yang`** on the existing mux/port (default
`:9100`), registered only when `output.yang.enabled`. It renders the **current**
reconciled graph on demand.

- **Trust level = same as `/metrics`** (confirmed). The endpoint exposes no data
  `/metrics` doesn't already expose (device IDs, vendor/OS, ports, edges); it
  inherits whatever web-auth the mux carries. The operator-facing doc states
  plainly that this is the full topology as one structured document, so operators
  who treat `/metrics` as sensitive treat this the same. No separate auth gate.
- **`503` before readiness [AR/M1]:** key the not-ready response on the existing
  `Ready *atomic.Bool` (the `/readyz` gate), **not** a nil-graph check — the
  collector stores a non-nil *empty* graph at init (`topology_collector.go`
  `c.snap.Store(&empty)`), so a nil check would serve `200` with an empty
  topology before the first cycle.
- `Content-Type: application/yang-data+json` (RFC 8040 media type). Documented as
  a RESTCONF-flavored convenience read, **not** a conformant RESTCONF datastore.
- **Method [AR2/I2]:** `GET` and `HEAD` only. `net/http` does not synthesize HEAD
  for a `HandlerFunc`, so handle it explicitly: set `Content-Type` and
  `Content-Length` (from the cached/rendered bytes) and write **no body** for HEAD.
  Any other method → `405` with an `Allow: GET, HEAD` header (RFC 7231 §6.5.5).

## Architecture

New package `internal/output/yang`, split for testability:

- `types.go` — Go structs modeling the RFC 8345/8346 JSON with `encoding/json`
  tags carrying the **RFC 7951** module-qualified member names (e.g.
  `ietf-network-topology:link`, `ntx-topology:discovery-protocol`). **[AR/C3]**
  Rendering via typed structs + `encoding/json`, never string concatenation —
  this removes the namespace-prefix bug class at the source.
- `render.go` — `Render(g *discovery.Graph, cfg Config) ([]byte, error)`: pure,
  no I/O. Builds nodes, termination-points, links from the graph per the mapping
  contract. Deterministic output (sort nodes by node-id, tps by tp-id, links by
  link-id) so byte output is stable for golden tests and cache keys.
- `handler.go` — `Handler(src GraphSource, ready *atomic.Bool, cfg Config) http.HandlerFunc`.
  `GraphSource` is a one-method interface `CurrentGraph() *discovery.Graph`
  satisfied by the `TopologyCollector` (add that accessor; it already holds
  `snap atomic.Pointer[discovery.Graph]`). **[AR/I3]** The handler caches the last
  rendered bytes keyed on the snapshot pointer identity — re-render only when
  `CurrentGraph()` returns a different pointer — so repeated GETs of an unchanged
  graph are O(1). The published graph is immutable after the cycle swaps it in
  (verified: each cycle allocates a fresh `Graph` via `Update(g)` storing `&g`), so
  pointer identity is a sound cache key.
  **[AR2/I1] Cache concurrency:** the handler's cache is itself shared mutable state
  read/written by concurrent GETs, so it must NOT be plain struct fields (data race).
  Hold it as a single `atomic.Pointer[renderCache]` where
  `renderCache struct { key *discovery.Graph; bytes []byte }` is immutable once
  built. On GET: `c := cache.Load()`; if `c == nil || c.key != CurrentGraph()`,
  render and `cache.Store(&renderCache{key, bytes})`. Two concurrent GETs racing a
  fresh pointer may both render — harmless, since `Render` is pure and deterministic
  (identical bytes) and the store is last-writer-wins. A `go test -race` test with
  concurrent GETs during a graph swap is required.
- `internal/app/app.go` — construct the handler with the collector as
  `GraphSource` and the loop's `Ready` bool; `mux.HandleFunc("/topology/yang", …)`
  only when `cfg.Output.YANG.Enabled`.

## Correctness guarantees the validator will NOT give us [AR/C1,C2]

RFC 8345's leafrefs (`source-node`, `source-tp`, `dest-node`, `dest-tp`) are
`require-instance false`, and its keys (`node-id`/`tp-id`/`link-id`/`network-id`)
are pattern-free `inet:uri`. So `yanglint` will accept dangling references and
arbitrary key strings. The renderer must therefore enforce, with unit tests:

1. **Termination-point completeness:** every `source-tp`/`dest-tp` a link
   references is declared as a `termination-point` under its node. Build the tp
   set per node from *all* incident edges first, then emit links.
2. **Node completeness:** every `source-node`/`dest-node` exists as a `node`.
   Edges may reference a device not present in `Graph.Devices` (the slices are
   populated independently); synthesize a minimal `node` for any such referenced
   device so no link dangles.
3. **Key uniqueness:** `node-id` unique across nodes; `tp-id` unique within a
   node; `link-id` unique across links. `link-id` is computed from the link's
   **own** endpoints — `<source-node>:<source-tp>-<dest-node>:<dest-tp>` — so the
   forward and reverse links of a bidirectional edge differ naturally (forward
   `leaf1:Gi0/1-spine1:Gi0/2`, reverse `spine1:Gi0/2-leaf1:Gi0/1`, matching the
   committed doc's worked example — no separate discriminator needed). For the
   residual collision cases (parallel edges between the same endpoint pair from
   different protocols, or a self-loop with equal tps), append a stable
   discriminator `#<discovery-proto>` and, if still colliding, `#<n>`. A
   `TestRenderLinkIDsUnique` asserts no collision on an adversarial graph
   (parallel multi-protocol edges between the same pair; self-loop).
4. **Empty/degenerate inputs:** `source-tp`/`dest-tp` are *optional* leafs in
   RFC 8345, so when `SrcPort`/`DstPort` is empty the link **omits** the
   `source-tp`/`dest-tp` (and no empty tp is created) — cleaner and valid, vs.
   inventing a sentinel key. A device with no incident edges → a `node` with no
   tps and no links; an empty graph → a valid empty `network` (with the
   `network-types` marker still present).

## Config

```go
// internal/config/config.go — under OutputConfig
type OutputConfig struct {
    OTLP OTLPOutputConfig `yaml:"otlp"`
    YANG YANGOutputConfig `yaml:"yang"`
}
type YANGOutputConfig struct {
    Enabled   bool   `yaml:"enabled"`    // default false
    NetworkID string `yaml:"network_id"` // network-id; default "network-topology-exporter"
}
```
`values.schema.json` (Helm) and `config/example.yaml` updated to match.

## The `ntx-topology` augmentation module [AR2/C1]

Concretely defined (the committed mapping doc left these `TBD`):

- **namespace:** `https://github.com/colinedwardwood/network-topology-exporter/yang/ntx-topology`
- **prefix:** `ntx` · **revision:** `2026-06-09`
- **imports:** `ietf-network` (`nw`), `ietf-network-topology` (`nt`)
- **augment targets** (absolute schema-tree paths, each step prefixed by the module
  that *defines* it — `nt:link` is contributed to `nw:network` by an
  `ietf-network-topology` augment, which is legal to further-augment):
  - `/nw:networks/nw:network/nt:link` → leaves `discovery-protocol`, `link-kind`,
    `confidence`, `adjacency`
  - `/nw:networks/nw:network/nw:node` → leaves `vendor`, `model`, `os-version`, `site`
- **enum leaves use plain `enumeration`** whose `enum` names are **byte-identical to
  the Go wire constants** so the renderer can emit `string(e.DiscoveryProto)` directly:
  - `discovery-protocol`: `lldp cdp bgp ospf fdb isis mpls_te configured` (note the
    underscore in `mpls_te` — matches `discovery.DiscoveryProtocolMPLSTE`)
  - `link-kind`: `ethernet mpls-te ip logical` (note the hyphen in `mpls-te`)
  - `confidence`: `high medium low`
  - `adjacency`: `direct indirect unknown`
  - node leaves (`vendor`/`model`/`os-version`/`site`) are `type string`.
- **Parity test (required):** a Go test asserts every constant returned valid by
  `DiscoveryProtocol.Valid()` / `LinkKind.Valid()` / `Confidence.Valid()` /
  `Adjacency.Valid()` (`internal/discovery/discovery.go:125-249`) has a matching
  `enum` in the module, so the schema and the wire values can never drift (a new
  protocol constant without a matching enum fails CI, not production).

## Validation (CI) [AR/C3]

- Vendor the **complete** import closure under `yang/` **[AR2/Fatal]**:
  `ietf-network`, `ietf-network-topology`, `ietf-inet-types`, `ietf-yang-types`,
  `ietf-l3-unicast-topology`, **`ietf-routing-types` (RFC 8294 — imported by
  `ietf-l3-unicast-topology`; without it `yanglint` cannot load the schema and the
  whole job fails)**, plus the new `ntx-topology.yang` augmentation. `ietf-routing-types`'s
  own closure (`ietf-yang-types`, `ietf-inet-types`) is already covered, so that one
  module completes the set. Offline, reproducible — not fetched at build time.
  The plan must `yanglint`-load all modules with no instance doc as a first step to
  prove the closure resolves before any instance validation.
- New workflow job `yang-validate`: install `yanglint`; run a small generator
  (`go test`-driven or `go run`) that calls `Render` on an **adversarial fixture
  graph** — empty port, edge whose endpoint device is absent from `Devices`, a
  device with no edges, a unicode device ID, parallel multi-protocol edges — and
  writes the doc; then `yanglint` the modules + the instance doc, failing on any
  diagnostic. Validating a hand-picked happy path would be theatre; the fixture is
  built to exercise the shapes production actually emits.
- Pin the `yanglint`/`libyang` install to a version (no `@latest`), consistent
  with #136.

## Testing

- `render_test.go`: golden-file for the worked example from the mapping doc
  (the leaf1↔spine1 bidirectional case → two links); structural tests for the four
  correctness guarantees above; determinism (render twice → identical bytes).
- `handler_test.go` (`httptest`): `200` + valid JSON when ready; `503` before
  `Ready`; `404`/not-registered when disabled; `405` + `Allow: GET, HEAD` on POST;
  `HEAD` returns headers (incl. `Content-Length`) and no body; cache hit returns
  identical bytes and does not re-render (assert via a counting `GraphSource`).
- `handler_test.go` `-race`: concurrent GETs while the `GraphSource` swaps the
  graph pointer — must be clean under `-race` **[AR2/I1]**.
- `ntx_parity_test.go`: every `Valid()` constant of `DiscoveryProtocol`/`LinkKind`/
  `Confidence`/`Adjacency` has a matching `enum` in `ntx-topology.yang` **[AR2/C1]**.
- yanglint: first load all vendored modules with no instance (proves the import
  closure resolves, incl. `ietf-routing-types`), then validate the adversarial
  instance doc.

## Files

- `internal/output/yang/{types.go,render.go,handler.go,render_test.go,handler_test.go,ntx_parity_test.go}` (new)
- `internal/metrics/topology_collector.go` — add `CurrentGraph() *discovery.Graph`
- `internal/app/app.go` — register the endpoint when enabled
- `internal/config/config.go` + `deploy/helm/topology-exporter/values.schema.json` + `config/example.yaml` — the `yang` block
- `yang/` — vendored `ietf-network`, `ietf-network-topology`, `ietf-inet-types`,
  `ietf-yang-types`, `ietf-l3-unicast-topology`, `ietf-routing-types`, + new `ntx-topology.yang`
- `.github/workflows/` — `yang-validate` job (or a job in an existing workflow)
- `docs/operator/yang-topology.md` — resolve the two `TBD`s (augmentation namespace URI; flip status planned→implemented; add the `GET /topology/yang` usage)
- `CHANGELOG.md` — Features entry

## Rollout / compatibility

Non-breaking and **off by default** (`output.yang.enabled: false`). No change to
`/metrics`, OTLP, snapshot, or the discovery loop beyond the read-only
`CurrentGraph()` accessor. The endpoint is additive.
