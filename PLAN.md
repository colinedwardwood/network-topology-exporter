# Engineering Improvement Plan: Network Topology Exporter

## Previous work
All §2–§5 items from the original plan are shipped. See git log for details.

---

## Audit findings (2026-05-09)

Multi-angle review covering correctness, scaling, standards, and open-source
readiness. Items are ordered within each section by severity.

---

## §1 Correctness — Silent Wrong Output

These produce incorrect topology data with no error signal. Fix before any
public release.

### 1.1 FDB-LLDP deduplication does not work

**Claim in PLAN §3** ("enables `graph.Reconcile` to deduplicate LLDP+FDB edges
for the same link") is false. FDB edges always have empty `DstPort` because
FDB has no knowledge of the remote port. LLDP edges have a non-empty `DstPort`.
`graph.normalizedGroupKey` hashes all four endpoint fields; even after Phase 2
resolves `DstDevice` from MAC to sysName, the `DstPort` mismatch puts FDB and
LLDP observations into different buckets. The reconciled output contains two
`network_topology_edge_info` series for the same physical link — one from LLDP
with a DstPort, one from FDB without — instead of one merged series with the
LLDP observation winning on rank.

**Fix:** In `runCycle` (main.go), after `resolveEdgeDstDevices` resolves FDB
`DstDevice` to sysName, do a second pass to back-fill `DstPort` on FDB edges
from LLDP observations with matching endpoints. Build an index
`(SrcDevice, SrcPort, DstDevice) → DstPort` from the LLDP edge set and apply
it to any FDB edge with `DstPort == ""`. This gives `graph.Reconcile` matching
`normalizedGroupKey` values so the LLDP edge (rank 2) wins over the FDB edge
(rank 4) rather than coexisting alongside it.

**Files:** `cmd/topology-exporter/main.go`, `internal/graph/graph_test.go`.

---

### 1.2 Hub `normalizeDeviceName` silently merges devices across sites

`normalizeDeviceName` strips everything after the first `.` so
`core-sw.dc1.example.com` and `core-sw.dc2.example.com` both become
`core-sw`. In a federation where sites share hostname short-names (common in
enterprises with per-site DNS domains), the OOS-matching loop synthesises
phantom cross-domain edges between unrelated devices and the device-dedup
path in `buildCombinedGraph` silently discards the second device. A warning
is logged but the wrong topology is still published.

**Fix:** Add `FederationHubConfig.StrictDeviceNameMatching bool` (default
`false` for backwards compatibility). When true, `normalizeDeviceName` is
replaced with a case-fold-only normalisation with no domain stripping.
Document the default behaviour and the collision risk in `docs/architecture.md`
(new LD-22). The existing ambiguity warning remains regardless of mode.

**Files:** `internal/federation/hub.go`, `internal/config/config.go`,
`docs/architecture.md`.

---

### 1.3 `sanitizeLabel` truncates at byte boundary, producing invalid UTF-8

```go
if len(s) > maxLabelLen {
    return s[:maxLabelLen]   // byte slice — splits multi-byte rune
}
```

For any label value containing multi-byte UTF-8 characters (Kanji hostnames,
non-ASCII vendor strings, some Japanese/Chinese NMS integrations), truncation
at byte 128 can split a rune, producing an invalid UTF-8 suffix. Prometheus
client_golang accepts these as raw byte strings; some downstream consumers
(Grafana label matchers, Alertmanager) may reject or silently mangle them.

**Fix:** After the `strings.Map` pass, truncate at the last valid rune
boundary at or before `maxLabelLen` bytes using `utf8.ValidString` or
`strings.ToValidUTF8` + a rune-boundary walk. Cap is still 128 bytes.

**Files:** `internal/metrics/topology_collector.go`.

---

### 1.4 Uncapped sysName used as map key before sanitization

`NormaliseName` lowercases and trims whitespace but does not cap length.
A malicious SNMP agent returning a 64 KB `sysName` causes a 64 KB key to be
inserted into `ipToID`, `macToID`, and `seenDevices` before `sanitizeLabel`
caps it at the metric layer. RFC 1213 defines sysName as `SIZE(0..255)`;
enforcement happens nowhere. On 500 targets each returning a 64 KB sysName
this is 32 MB of unbounded string allocations per cycle, growing with target
count.

**Fix:** Cap `NormaliseName` return value at 256 bytes (one byte over RFC
maximum, generous for FQDN variants). Alternatively, cap in the Walk caller
after reading the sysName PDU. Document the cap in the SNMP package comment.

**Files:** `internal/discovery/snmp/snmp.go` (or `pdu.go`).

---

## §2 Open-Source / Metric Schema — Breaking Changes

All items in this section require a semver major version bump and a one-release
deprecation window where both old and new metric names are emitted
simultaneously under a `compatibility.legacy_metric_names: true` config flag.
Update `schema_test.go` and `docs/metrics.md` in the same commit as each
rename.

---

### 2.1 `network_device_*` metrics missing `topology` namespace

`network_device_info` and `network_device_uptime_seconds` break the
`network_topology_` namespace used by every other metric. A Prometheus user
joining these metrics against `network_topology_edge_info` must know the
namespace changed mid-design. Dashboard authors will file issues on day one.

**Rename:**
- `network_device_info` → `network_topology_device_info`
- `network_device_uptime_seconds` → `network_topology_device_uptime_seconds`

Update `docs/metrics.md`, `schema_test.go` (`expectedMetricNames`),
Helm chart `PrometheusRule` alert expressions, and `README.md` metric table.

**Files:** `internal/metrics/topology_collector.go`, `internal/metrics/schema_test.go`,
`docs/metrics.md`, `deploy/helm/topology-exporter/templates/prometheusrule.yaml`,
`README.md`.

---

### 2.2 Unix-timestamp gauges use non-standard `_unix` suffix

Prometheus naming conventions require `_timestamp_seconds` for Unix epoch
gauges. The `_unix` suffix is unrecognised by standard tooling (unit
inference in Grafana, `promtool check metrics` warnings).

**Rename:**
- `network_topology_federation_spoke_last_push_unix` →
  `network_topology_federation_spoke_last_push_timestamp_seconds`
- `network_topology_snapshot_last_written_unix` →
  `network_topology_snapshot_last_written_timestamp_seconds`

**Files:** `internal/metrics/metrics.go`, `internal/metrics/schema_test.go`,
`docs/metrics.md`, `deploy/helm/topology-exporter/templates/prometheusrule.yaml`.

---

### 2.3 `link_type` label on `network_topology_edge_info` should be `link_kind`

The edge label is `link_type` but the internal field is `LinkKind` and the
config vocabulary uses `link_kind`. The mismatch will confuse contributors
tracing label values to source fields. `link_type` also collides with
conventional Prometheus use of `type` as a generic dimension label.

**Rename:** `link_type` → `link_kind` on `network_topology_edge_info`.

Update the `edgeInfoDesc` descriptor, `topology_collector.go` emit call,
`schema_test.go`, `docs/metrics.md`, Helm alert rules, and `README.md`.

**Files:** `internal/metrics/topology_collector.go`, `internal/metrics/schema_test.go`,
`docs/metrics.md`.

---

## §3 Scaling — Enterprise Readiness

### 3.1 FDB VLAN community walks are serial

`walkVlanCommunityFdbs` opens and walks one SNMP session per VLAN
sequentially:

```go
for _, vlanID := range vlanIDs {
    vlanClient, _ := snmputil.Open(vp)
    walkFdbTableInto(ctx, vlanClient, vlanEntries)
    vlanClient.Conn.Close()
}
```

A Cisco IOS core switch with 100 active VLANs (the default `MaxVlans` cap)
runs 100 sequential BulkWalks. At 50 ms per walk (conservative for a loaded
core), that is 5 s of serial SNMP I/O for the FDB module alone on one device.
An environment with 30 such cores and `Parallelism=10` can have the FDB module
consume the full cycle budget and cascade-cancel LLDP, CDP, and IS-IS on all
remaining devices in the slot.

**Fix:** Within `walkVlanCommunityFdbs`, fan out VLAN walks into a bounded
goroutine pool (suggested default: 8 concurrent sessions per device, derived
from empirical testing against Cisco 6500/9000 series). Use a mutex-protected
merge into the shared `entries` map after each walk completes. Cap total
per-device VLAN walk wall time at `TimeoutPerModule` (existing config) if set.

This is I/O-bound work with no shared state contention (VLAN walks read
different OID subtrees); the parallelism is safe.

**Files:** `internal/discovery/fdb/fdb.go`.

---

### 3.2 `DiscoveryDecodeIssues` `oid` label — document and assert cardinality bound

The metric `network_topology_discovery_decode_issues_total{module, oid, reason}`
uses `OID` values from `DecodeIssue.OID`. In the current implementation these
are hardcoded table root OIDs passed by the callers of `WalkToIntMapStrict`
(e.g., `"1.3.6.1.2.1.14.10.1"` for OSPF neighbour state). The set is bounded
at compile time (~7 modules × ~4 tables × 2 reasons ≈ 56 unique label sets).

However, the metric signature admits unbounded OIDs — nothing in the type
system or call sites enforces this. A future caller passing a per-PDU OID
(e.g., the full instance OID of a failing row) would create one label set per
PDU across all targets, which at 500 targets × 1,000 anomalous PDUs each
exceeds Prometheus cardinality limits.

**Fix:** Add a CI-level assertion (similar to `cardinality_test.go`) that
enumerates all call sites of `WalkToIntMapStrict` and confirms each passes a
root OID from a known allow-list. Alternatively, add a `TableRootOID string`
field to `DecodeIssue` (distinct from a per-PDU OID) and use that as the
metric label, enforcing the constraint structurally. Document the design
constraint in the `metrics` package comment.

**Files:** `internal/metrics/metrics.go`, `internal/discovery/snmp/pdu.go`,
a new `internal/metrics/cardinality_labels_test.go` or extension to
existing `cardinality_test.go`.

---

## §4 Standards & Documentation

### 4.1 LLDP chassis subtype 7 (local) binary data produces mojibake device IDs

`decodeChassisID` falls through to `strings.TrimRight(string(raw), "\x00")`
for all subtypes other than MAC (4) and network-address (5). IEEE 802.1AB
defines subtype 7 (local) as arbitrary vendor-defined bytes — some vendors
(certain Cisco WLC firmware, some Huawei data-centre platforms) encode binary
identifiers in this field. `string(raw)` on binary data produces invalid or
garbled UTF-8 that survives `sanitizeLabel`'s printability filter as partial
garbage, resulting in device IDs that cannot be correlated with anything in
NMS systems.

**Fix:** For subtype 7 (local), detect whether `raw` is valid UTF-8 via
`utf8.Valid`. If valid, use the string form (covers text-based local subtypes
like Arista's EOS node ID). If not valid, use `hex.EncodeToString(raw)` as
the fallback. Apply the same logic to subtype 1 (chassisComponent) which can
also carry binary data from ENTITY-MIB.

**Files:** `internal/discovery/lldp/lldp.go` (`decodeChassisID`),
`internal/discovery/lldp/lldp_test.go`.

---

### 4.2 sysUpTime rolls over silently at 497 days

`TimeTicks` is a `uint32` counter of hundredths-of-a-second. It wraps to zero
at `2^32 × 10 ms = 497.1 days`. The code converts it directly:

```go
if ticks, ok := pdu.Value.(uint32); ok {
    dev.Uptime = time.Duration(ticks) * 10 * time.Millisecond
}
```

After rollover, `network_topology_device_uptime_seconds` drops to near-zero,
indistinguishable from a reboot event. Any alert on `uptime < threshold` will
fire falsely for all devices that have been up more than 497 days.

**Fix (minimal):** Document the rollover in the metric help text and in
`docs/metrics.md`. Alert authors should use `increase()` on a separate reboot
counter (from the management plane) rather than a threshold on raw uptime.

**Fix (full):** Emit `network_topology_device_sysuptime_wrap_total` as a
counter that increments when the current `ticks` reading is less than the
previous reading for the same device. Store the previous ticks value per
device in the snapshot. The wrapping counter plus the current ticks value
reconstructs the true monotonic uptime. Update snapshot schema with a
`SysUptimeTicks` map.

**Files:** `internal/discovery/snmp/snmp.go`, `internal/snapshot/snapshot.go`,
`internal/metrics/topology_collector.go` (if full fix), `docs/metrics.md`.

---

### 4.3 Silent credential profile skip gives no diagnostic

In `credentialCandidates`, when a profile's env var is empty (e.g., operator
typos `SNMP_COMMUNITY` as `SNMP_COMUNITY`), `profileToParams` returns an
error and the profile is silently skipped with no log line. If all profiles
fail this way, `walkSystemWithCredentials` returns "no usable credential
profiles for IP" with no indication of which profile failed or why. An
operator staring at a full-failure discovery cycle has no signal to distinguish
"device unreachable" from "env var typo".

**Fix:** In `credentialCandidates`, log a `Warn` when `profileToParams` fails,
including the profile name and the error string (which names the env var).
This is logged at Warn (not Error) because the profile might be intentionally
absent in some deployment modes.

**Files:** `cmd/topology-exporter/main.go` (`credentialCandidates`).

---

### 4.4 RFC 2922 citation error in `lldp.go`

```go
// TTL liveness: lldpRemTable entries are aged out by the agent per RFC 2922 §3.
```

RFC 2922 is the Physical Topology MIB. It has no TTL aging provisions for
`lldpRemTable`. The correct citation is IEEE 802.1AB-2016 §9.6.3 ("LLDP TTL
and lldpRemTable aging"). The code behaviour is correct; the citation is wrong.

**Fix:** Correct the comment. While there, audit all RFC citations in the
`lldp` package for accuracy.

**Files:** `internal/discovery/lldp/lldp.go`.

---

### 4.5 Self-contradicting comment in `snmp.go` enterprise prefix table

```go
// Ordering matters: iteration stops at the first match, so longer prefixes
// must precede any shorter prefix that is also a prefix of the longer one.
// Entries here share no prefix relationship (each enterprise number is unique),
// so order within the list does not affect correctness
```

These sentences contradict each other. The second is correct. Remove the
first sentence entirely; it introduces false requirements.

**Files:** `internal/discovery/snmp/snmp.go`.

---

### 4.6 `normalizeSysDescr` comment describes what, not why

The comment says "Extract first M.N or M.N.P version token." The function
name and the regex already communicate this. The why — that using the full
sysDescr string as a Prometheus label creates one series per patch version
across a device fleet, multiplying cardinality by the number of active
firmware versions — is not documented anywhere.

**Fix:** Replace the comment with the cardinality rationale. Mention the
fallback (first 64 raw chars) and why 64 was chosen.

**Files:** `internal/discovery/snmp/snmp.go` (`normalizeSysDescr`).

---

## Remediation order

| Priority | Item | Effort | Risk |
|---|---|---|---|
| 1 | §1.1 FDB-LLDP dedup | Medium | Low (additive change) |
| 2 | §1.2 Hub hostname collision | Low | Low (opt-in config flag) |
| 3 | §1.3 sanitizeLabel UTF-8 | Low | Low |
| 4 | §1.4 sysName length cap | Low | Low |
| 5 | §3.1 VLAN walk parallelism | Medium | Medium (concurrency) |
| 6 | §2.x Metric renames (bundle) | High | High (breaking — needs major version) |
| 7 | §3.2 Decode OID cardinality | Low | Low |
| 8 | §4.1 Subtype 7 binary | Low | Low |
| 9 | §4.2 Uptime rollover | Low–Medium | Low |
| 10 | §4.3 Credential diagnostic | Trivial | Low |
| 11 | §4.4–4.6 Docs/comments | Trivial | None |

**Version bump guidance:** §1 and §3–§4 items are non-breaking and can ship
as patch or minor releases. §2 metric renames are breaking and require a
coordinated major version bump with a deprecation shim (emit both old and new
names for one release cycle under `compatibility.legacy_metric_names: true`).
