# v1.3 — SHIPPED

All P1–P5 items closed. 2026-05-07.

## What shipped

- **P1** — Optional TLS (`listen.tls_cert_file` / `listen.tls_key_file`) for `/metrics`; plain HTTP when unconfigured.
- **P2** — NetworkPolicy Helm template (`networkPolicy.enabled: false` default) restricting hub port 9101 to spoke pods.
- **P3** — IS-IS `SrcPort` populated from `isisISCircTable` + `ifDescr` walk; degrades gracefully when circuit table absent.
- **P4** — MPLS-TE tunnel `Metadata["mpls_te.admin_status"]` (up/down/testing/unknown) via second `mplsTunnelAdminStatus` bulk walk.
- **P5** — OTLP `schemaUrl: https://opentelemetry.io/schemas/1.21.0` on all resource payloads; `Edge.Metadata` serialised as `network.topology.*` attributes; `docs/otlp-schema.md` documents all emitted attributes.

## No open items

Next work should be tracked in a new plan.
