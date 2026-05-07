# v1.3 Work Plan

## Open Items (priority order)

### P1 — TLS for public /metrics endpoint

Risk: Plaintext metric scraping on shared management networks leaks device
inventory (hostnames, vendors, uptime) to any observer on the subnet.

Remediation:
- Add optional `--web.tls-config` flag following the `prometheus/exporter-toolkit`
  pattern (or inline TLS config in the YAML config file).
- Document that without TLS the metrics port should sit behind an mTLS-capable
  reverse proxy.

Acceptance criteria:
- `listen.tls_cert_file` + `listen.tls_key_file` in config enables HTTPS on the
  metrics port.
- Plain HTTP still works when TLS is not configured (no behaviour change for
  existing deployments).

---

### P2 — NetworkPolicy scaffold for hub federation port

Risk: Hub port 9101 has no network policy — non-spoke pods in the same namespace
can reach it without restriction.

Remediation:
- Add `templates/networkpolicy.yaml` to the Helm chart.
- Default: deny all ingress to port 9101 except from pods with a configurable
  label selector (e.g. `topology-exporter/role: spoke`).
- `networkPolicy.enabled: false` default so existing deployments are unaffected.

Acceptance criteria:
- `helm template --set networkPolicy.enabled=true` renders a valid NetworkPolicy.
- Spoke pods with the correct label can reach the hub; others cannot.

---

### P3 — IS-IS topology expansion

Current state: IS-IS walker emits adjacency edges (L2/L3 peer IP) but does not
extract interface names, metric/cost values, or area membership.

Remediation:
- Walk `isisISAdjTable` for interface index and map to `isisCircTable` for
  interface name (`ifDescr` via `ifIndex`).
- Populate `SrcPort` with the IS-IS circuit interface name (currently empty).
- Optionally emit IS-IS metric as an edge attribute for path-cost visualisation.

Acceptance criteria:
- IS-IS edges carry `SrcPort` matching the interface name on the reporting device.
- Existing IS-IS adjacency filtering (up-only, IPv4-only) unchanged.

---

### P4 — MPLS-TE/SR-TE modeling enhancements

Current state: MPLS-TE walker emits tunnel edges with `te-tunnel{idx}` as
`SrcPort` but no bandwidth, affinity, or explicit-route attributes.

Remediation:
- Walk `mplsTunnelTable` columns for `AdminStatus`, `Bandwidth`,
  `PrimaryInstance`, and `PathInUse`.
- Walk `mplsTunnelHopTable` for explicit route hops and attach as edge metadata.
- Consider SR-TE policy table (CISCO-MPLS-TE-STD-MIB or vendor extension) if
  target devices support it.

Acceptance criteria:
- Tunnel edges include bandwidth and admin-status attributes in OTLP payload.
- Existing up-only filter and `te-tunnel{idx}` port encoding unchanged.

---

### P5 — OTLP payload schema/versioning

Current state: OTLP payload structure is undocumented and unversioned; breaking
changes to edge/device attribute names have no migration path.

Remediation:
- Define a stable attribute name registry (e.g. `network.topology.edge.*`
  following OTel semantic convention naming).
- Add `schema_url` to the OTLP resource following OTel spec.
- Document the schema in `docs/otlp-schema.md` with a versioning policy.

Acceptance criteria:
- `schema_url` present in all OTLP resource objects.
- `docs/otlp-schema.md` documents all emitted attribute names and their types.
- Attribute names are stable across v1.3 → v1.4.
