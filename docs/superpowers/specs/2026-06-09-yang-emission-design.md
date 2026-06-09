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
- **Method:** only `GET`/`HEAD`; others → `405`.

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
  (verified: each cycle allocates a fresh `Graph`), so pointer identity is a sound
  cache key and `atomic.Pointer.Load()` is race-free.
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
   node; `link-id` unique across links. `link-id` is derived from the full tuple
   `<src-node>:<src-tp>-<dst-node>:<dst-tp>` plus a direction discriminator on the
   reverse link of a bidirectional edge (per the mapping doc). A
   `TestRenderLinkIDsUnique` asserts no collision on an adversarial graph
   (parallel edges, multiple protocols between the same pair).
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

## Validation (CI) [AR/C3]

- Vendor the import closure under `yang/`: `ietf-network`, `ietf-network-topology`,
  `ietf-inet-types`, `ietf-yang-types`, `ietf-l3-unicast-topology`, plus the new
  `ntx-topology.yang` augmentation (enums mirroring `DiscoveryProtocol`/`LinkKind`/
  `Confidence`/`Adjacency`; node leaves vendor/model/os-version/site). Offline,
  reproducible — not fetched at build time.
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
  `Ready`; `404`/not-registered when disabled; `405` on POST; cache hit returns
  identical bytes and does not re-render (assert via a counting `GraphSource`).
- yanglint validation in CI as above.

## Files

- `internal/output/yang/{types.go,render.go,handler.go,render_test.go,handler_test.go}` (new)
- `internal/metrics/topology_collector.go` — add `CurrentGraph() *discovery.Graph`
- `internal/app/app.go` — register the endpoint when enabled
- `internal/config/config.go` + `deploy/helm/topology-exporter/values.schema.json` + `config/example.yaml` — the `yang` block
- `yang/*.yang` (vendored modules + `ntx-topology.yang`)
- `.github/workflows/` — `yang-validate` job (or a job in an existing workflow)
- `docs/operator/yang-topology.md` — resolve the two `TBD`s (augmentation namespace URI; flip status planned→implemented; add the `GET /topology/yang` usage)
- `CHANGELOG.md` — Features entry

## Rollout / compatibility

Non-breaking and **off by default** (`output.yang.enabled: false`). No change to
`/metrics`, OTLP, snapshot, or the discovery loop beyond the read-only
`CurrentGraph()` accessor. The endpoint is additive.
