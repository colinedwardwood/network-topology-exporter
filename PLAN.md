# v1.4 — SHIPPED 2026-05-07

- **P1** — `WalkToIntMap` and `WalkIfDescr` helpers in `internal/discovery/snmp`; `WalkIfNames` refactored to share `walkIntIndexedStrings`; `OIDIfDescr` exported alongside `oidIfNameTable`. Three callers simplified: `isis.walkAdjStates` (10 → 1 line), `isis.walkCircuitIfNames` (25 → 8 lines), `mpls` admin-status block (10 → 3 lines).

---

# v1.3 — SHIPPED 2026-05-07

- **P1** — Optional TLS (`listen.tls_cert_file` / `listen.tls_key_file`) for `/metrics`.
- **P2** — NetworkPolicy Helm template restricting hub port 9101 to spoke pods.
- **P3** — IS-IS `SrcPort` from `isisISCircTable` + `ifDescr` walk; degrades gracefully.
- **P4** — MPLS-TE `Metadata["mpls_te.admin_status"]` via `mplsTunnelAdminStatus` walk.
- **P5** — OTLP schema URL on all resource payloads; `Edge.Metadata` as `network.topology.*` attributes; `docs/otlp-schema.md`.
- **simplify** — Collapsed duplicate HTTP server goroutines; `fs.Visit()` for flag override detection; removed WHAT comments; guarded circuit walk behind `len(states) > 0`; MPLS admin-status degraded mode; extracted `metaKeyAdminStatus` and `metadataAttrPrefix` constants.
