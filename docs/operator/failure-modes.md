# Per-Walker Failure-Mode Coverage

This document audits every discovery walker against the GA observability
criterion (ROADMAP):

> every external dependency the exporter relies on has either a **hard-fail
> contract** or a `network_topology_*_outcome_total`-style counter labelled
> with the **specific failure shape**.

For each walker it lists, per external dependency: what happens on failure
(hard error that aborts the walk / silent skip / partial result / panic),
and the operator-visible signal (which metric, with the exact label values, or
which log line and at what level).

Every claim is traceable to a `file:line`. Where a failure mode is ambiguous
it is marked **AMBIGUOUS** rather than asserted.

Audit basis: issue #67. Code state: branch `docs/walker-failure-modes`.

## How walker returns become operator signals

Every walker has the signature
`Walk(ctx, params, localDevice, allowedNets) ([]Edge, []OutOfScopeNeighbour, error)`
(`internal/app/probe.go:245`). The orchestrator in `RunCycle`
(`internal/app/cycle.go`) is what translates a walker's *return value* into
metrics — the walkers themselves emit almost nothing directly. The four
shared, walker-agnostic signals are:

| Walker return | Orchestrator signal | Site |
|---|---|---|
| `err != nil` (any) | `network_topology_discovery_hard_fail_total{module=<proto>, reason=<r>}` +1; `network_topology_module_last_status{module=<proto>}=2`; `network_topology_snmp_walks_total{status="error"|"timeout"}` +1 | `cycle.go:276-301` |
| `err != nil` AND `errors.As(&PolicyError)` | `reason` label = `PolicyError.Reason` (e.g. `required_table_no_valid_rows`, `required_table_invalid_ratio_exceeded`); else `reason="module_walk_error"` | `cycle.go:282-287` |
| edges carry `degraded=true` metadata | `network_topology_discovery_degraded_total{module=<proto>, reason=<r>}` +1; `module_last_status=1` | `cycle.go:304-312` |
| clean return, ≥0 edges | `module_last_status=0`; `snmp_walks_total{status="ok"}` +1 | `cycle.go:303,313-317` |
| goroutine panic | `network_topology_discovery_devices_total{status="failed", reason="panic"}`; `logger.Error("per-device probe panicked")` | `cycle.go:133-138` |

A fifth signal is decode-anomaly accounting, available **only** to walkers that
route table reads through `snmputil.WalkToIntMapStrict` with a non-empty
`module` argument: those increment
`network_topology_discovery_decode_issues_total{module, oid, reason}` and
`network_topology_discovery_quarantined_rows_total{...}` via the context
reporter wired at `cycle.go:214-217`. Reasons are `invalid_type` (PDU not an
integer) and `invalid_oid` (prefix trim failed) — `snmp/pdu.go:115-132`.

**Key consequence for this audit:** a walker that drops a row with a bare
`continue` (no `WalkToIntMapStrict`, no degraded metadata, at most a
`slog.Debug`) produces **no metric** for that drop. The original #67 audit
listed many such drops as **silent**; the #98–#102 follow-up work (below) closed
them. Where this document still cites a drop as silent it is called out
explicitly.

> **Update (#98–#102):** the four formerly-silent walkers (LLDP, CDP, OSPF, FDB)
> gained a generic per-walker outcome counter
> (`network_topology_walker_outcome_total{walker, outcome}`), per-row decode
> rejections now increment `network_topology_discovery_decode_issues_total`, FDB
> and IS-IS gained new `network_topology_discovery_degraded_total` reasons, and
> the SNMP system walk gained `network_topology_system_walk_anomaly_total`. The
> rows and prose below have been updated to reflect the new signals; the
> coverage summary at the foot of this document now reads **FULL/observable**
> for these walkers.

BGP was the first walker to emit a *per-walker, failure-shape-labelled* counter,
via `network_topology_bgp_walker_outcome_total{walker, outcome}`
(`metrics/metrics.go`, emitted through `recordWalkerOutcome`, `bgp/bgp.go`).
Since #98 the non-BGP walkers (lldp, cdp, ospf, fdb) emit the generic sibling
`network_topology_walker_outcome_total{walker, outcome}` with the same
four-bucket semantics (`edges`, `mib_unimplemented`, `no_neighbours`,
`walker_drift`, `error`). The BGP counter was **not** renamed — the new generic
counter is additive and non-breaking, and the two coexist.

---

## LLDP — `internal/discovery/lldp/lldp.go`

Walks `lldpLocPortTable` (`oidLocPortTable` 1.0.8802.1.1.2.1.3.7) and
`lldpRemTable` (`oidRemTable` ...4.1) via `snmputil.BulkWalk`. As of #98/#99 the
walker emits `walker_outcome_total{walker="lldp", ...}` and reports per-row
decode rejections via `decode_issues_total{module="lldp", oid="1.0.8802.1.1.2.1.4.1", reason=...}`.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="lldp", reason="module_walk_error"}`; `module_last_status=2`; `walker_outcome_total{walker="lldp", outcome="error"}` (#98) — `lldp.go` |
| `lldpLocPortTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above (incl. `outcome="error"`) — `lldp.go` |
| `lldpRemTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above (incl. `outcome="error"`) — `lldp.go` |
| `lldpRemTable` BulkWalk | **zero PDUs (MIB unimplemented)** | clean return, **0 edges** | `walker_outcome_total{walker="lldp", outcome="mib_unimplemented"}` (#98) — expected on non-LLDP devices, **must not page**. `module_last_status=0` — `lldp.go` |
| `lldpRemChassisIdSubtype` out of range 1–7 | row rejected | `continue` | `decode_issues_total{module="lldp", oid="1.0.8802.1.1.2.1.4.1", reason="chassis_subtype_invalid"}` (#99) + `slog.Debug` — `lldp.go` |
| `lldpRemPortIdSubtype` out of range 1–7 | row rejected | `continue` | `decode_issues_total{module="lldp", oid="1.0.8802.1.1.2.1.4.1", reason="port_subtype_invalid"}` (#99) + `slog.Debug` — `lldp.go` |
| MAC chassis/port ID wrong length | row rejected | `continue` | `decode_issues_total{module="lldp", oid="1.0.8802.1.1.2.1.4.1", reason="chassis_mac_bad_length"\|"port_mac_bad_length"}` (#99) + `slog.Debug` — `lldp.go` |
| malformed network-address chassis ID | row rejected | `continue` | `decode_issues_total{module="lldp", oid="1.0.8802.1.1.2.1.4.1", reason="chassis_addr_malformed"}` (#99) + `slog.Debug` — `lldp.go` |
| every `lldpRemTable` row rejected (above reasons) | no edges | `continue` per row | `walker_outcome_total{walker="lldp", outcome="walker_drift"}` (#98) — MIB implemented but our decoder matches no row; page-level — `lldp.go` |
| ≥1 row decodes but no usable edges | no edges | clean return | `walker_outcome_total{walker="lldp", outcome="no_neighbours"}` (#98) — protocol up, nothing to report — `lldp.go` |
| unresolvable device/port | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go` |
| non-IP chassis ID + scope filtering active | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go` |
| neighbour IP outside allow-list | not an edge | emitted as `OutOfScopeNeighbour` | `network_topology_out_of_scope_neighbours_total` (graph-layer) — `lldp.go`. **Observable.** |

**Coverage: FULL (#98/#99).** Transport-level failures hard-fail and now also
emit `outcome="error"`. Zero-PDU is distinguished as `mib_unimplemented`, and
the firmware-drift case — `lldpRemTable` returned but every row decode-rejected
— now surfaces as `outcome="walker_drift"` (vs `no_neighbours` when ≥1 row
decodes to nothing usable). Per-row rejections increment `decode_issues_total`
with the specific reason. The `walker_drift` analogue that was previously absent
now exists.

---

## CDP — `internal/discovery/cdp/cdp.go`

Walks `cdpCacheTable` (1.3.6.1.4.1.9.9.23.1.2.1) via `BulkWalk` and IF-MIB
`ifName`/`ifDescr` via `snmputil.WalkIfNamesWithFallback`. As of #98/#99 the
walker emits `walker_outcome_total{walker="cdp", ...}` and reports per-row
decode rejections via `decode_issues_total{module="cdp", oid="1.3.6.1.4.1.9.9.23.1.2.1", reason=...}`.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="cdp", reason="module_walk_error"}`; `module_last_status=2`; `walker_outcome_total{walker="cdp", outcome="error"}` (#98) — `cdp.go` |
| `ifName`/`ifDescr` (both fail) | error | hard error, walk aborts | same as above (incl. `outcome="error"`) — `cdp.go`, fallback logic `snmp/pdu.go` |
| `ifName` present but `ifDescr`-only fallback used | partial | clean; local port falls back to `if<ifIndex>` | **no signal** — `cdp.go`. Note: `WalkIfNamesWithFallback` swallows the ifXTable miss with no degraded marker. **SILENT** |
| `cdpCacheTable` BulkWalk | transport/SNMP error | hard error, walk aborts | `hard_fail_total{module="cdp", ...}` + `walker_outcome_total{walker="cdp", outcome="error"}` (#98) — `cdp.go` |
| `cdpCacheTable` | **zero PDUs (non-Cisco / CDP off)** | clean return, **0 edges** | `walker_outcome_total{walker="cdp", outcome="mib_unimplemented"}` (#98) — expected on non-Cisco, **must not page**. `module_last_status=0` — `cdp.go` |
| OID index components ≤ 0 / unparseable | row rejected | `continue` | `decode_issues_total{module="cdp", oid="1.3.6.1.4.1.9.9.23.1.2.1", reason="index_unparseable"}` (#99) — `cdp.go` |
| empty deviceID or devicePort | row rejected | `continue` | `decode_issues_total{module="cdp", oid="1.3.6.1.4.1.9.9.23.1.2.1", reason="empty_device_id"}` (#99) — `cdp.go` |
| every `cdpCacheTable` row rejected | no edges | `continue` per row | `walker_outcome_total{walker="cdp", outcome="walker_drift"}` (#98); ≥1 row decoding to nothing usable yields `outcome="no_neighbours"` — `cdp.go` |
| non-IP neighbour + scope filtering active | row rejected | `continue` | `slog.Debug` only. **SILENT** — `cdp.go` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` (graph-layer) — `cdp.go`. **Observable.** |

**Coverage: FULL (#98/#99).** Same shape as LLDP: zero-PDU →
`mib_unimplemented`, all-rows-rejected → `walker_drift`, clean-but-empty →
`no_neighbours`, and the per-row index/empty-deviceID drops now increment
`decode_issues_total`. Residual gap: the silent ifXTable→ifDescr fallback
(`cdp.go`, helper `snmp/pdu.go`) still produces no degraded signal, unlike IS-IS
which marks `missing_srcport_mapping` for an equivalent enrichment miss.

---

## OSPF — `internal/discovery/ospf/ospf.go`

Walks `ospfNbrTable` (RFC 4750, 1.3.6.1.2.1.14.10) via `BulkWalk`. As of #98/#99
the walker emits `walker_outcome_total{walker="ospf", ...}` and reports per-row
decode rejections via `decode_issues_total{module="ospf", oid="1.3.6.1.2.1.14.10", reason=...}`.
Module doc-comment explicitly states "Treat empty walk results as normal, not as
an error" (`ospf.go`).

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="ospf", reason="module_walk_error"}`; `module_last_status=2`; `walker_outcome_total{walker="ospf", outcome="error"}` (#98) — `ospf.go` |
| `ospfNbrTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above (incl. `outcome="error"`) — `ospf.go` |
| `ospfNbrTable` | **zero PDUs (OSPF-MIB unimplemented)** | clean return, **0 edges** | `walker_outcome_total{walker="ospf", outcome="mib_unimplemented"}` (#98) — expected on devices without the OSPF-MIB, **must not page**. `module_last_status=0` — `ospf.go` |
| `ospfNbrTable` rows all present but **state never full/twoWay** | no edges | `continue` per row | `walker_outcome_total{walker="ospf", outcome="no_neighbours"}` (#98) — OSPF runs but no adjacency is full; operationally distinct from `mib_unimplemented`. This is the OSPF analogue of BGP's `no_peers` — `ospf.go` |
| OID suffix not `<col>.<4 octets>.<idx>` | row rejected | `continue` | `decode_issues_total{module="ospf", oid="1.3.6.1.2.1.14.10", reason="oid_suffix_malformed"}` (#99) — `ospf.go` |
| `ospfNbrIpAddr` PDU not decodable as IPv4 | `nbrIP==nil`, row skipped | `continue` | `decode_issues_total{module="ospf", oid="1.3.6.1.2.1.14.10", reason="nbr_ip_undecodable"}` (#99) — `ospf.go` |
| every `ospfNbrTable` row rejected (above reasons) | no edges | `continue` per row | `walker_outcome_total{walker="ospf", outcome="walker_drift"}` (#98) — MIB implemented but our decoder matches no row; page-level — `ospf.go` |
| nbr IP unspecified/link-local/loopback | row rejected | `continue` | none (no log) — `ospf.go` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `ospf.go`. **Observable.** |

**Coverage: FULL (#98/#99).** Hard-fail on transport now also emits
`outcome="error"`. The "OSPF runs but no adjacency is full" signal — the direct
analogue of BGP's `no_peers` that was previously entirely unobservable — now
surfaces as `outcome="no_neighbours"`, operationally separated from
`mib_unimplemented` (feature absent) and `walker_drift` (decoder broken).
Per-row decode rejections increment `decode_issues_total`.

---

## IS-IS — `internal/discovery/isis/isis.go`

The first non-BGP walker with a **required-table hard-fail contract**. Reads
`isisISAdjState` and `isisISCircIfIndex` through `WalkToIntMapStrict` (module
arg `"isis"`), so decode anomalies on those two tables **are** accounted.
`isisISAdjIPAddr` is read with a bare `BulkWalk` (no strict accounting).

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="isis", reason="module_walk_error"}` — `isis.go:46-49` |
| `isisISAdjState` (required) | walk error | hard error | `hard_fail_total{module="isis", reason="module_walk_error"}` — `isis.go:52-55, 80-84` |
| `isisISAdjState` (required) | **no valid rows** (`MinValidRows`) | hard error via `PolicyError` | `hard_fail_total{module="isis", reason="required_table_no_valid_rows"}` — `isis.go:85-95`, policy `snmp/pdu.go:148-159` |
| `isisISAdjState` (required) | invalid ratio > 0.50 | hard error via `PolicyError` | `hard_fail_total{module="isis", reason="required_table_invalid_ratio_exceeded"}` — `isis.go:85-95` |
| `isisISAdjState` (required) | some rows bad, ratio ≤ 0.50 | degraded, edges kept | `discovery_degraded_total{module="isis", reason="required_table_partial_decode"}` + `decode_issues_total{module="isis", oid=".1.3.6.1.2.1.138.1.6.1.1.2", reason="invalid_type"|"invalid_oid"}` — `isis.go:59-61`, `snmp/pdu.go:115-132` |
| `isisISCircIfIndex`/`ifDescr` (optional, SrcPort) | walk error | degraded, edges kept | `discovery_degraded_total{module="isis", reason="missing_srcport_mapping"}`; `slog.Debug` — `isis.go:62-70, 102-114` |
| `isisISAdjIPAddr` BulkWalk | walk error | hard error | `hard_fail_total{module="isis", reason="module_walk_error"}` — `isis.go:73-76, 124-128` |
| IPv6 adjacency rows | not yet supported | skipped, IPv4 edges unaffected | `discovery_degraded_total{module="isis", reason="unsupported_ip_version"}` (#102), fired once per walk via the direct `RecordDegraded` sink (so it fires even on an IPv6-only device with zero IPv4 edges) + `slog.Info` ("IPv6 adjacency skipped"). **Observable** — `isis.go` |
| adj state not up(3) / unknown adjKey | row rejected | `continue` | none — `isis.go:159-162` |
| adj IP nil/unspecified/link-local | row rejected | `continue` | none — `isis.go:163-169` |
| circuit→ifName join misses for a row | per-edge degrade | edge kept, SrcPort empty | `discovery_degraded_total{module="isis", reason="missing_srcport_mapping"}` — `isis.go:180-183` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `isis.go:184-194`. **Observable.** |

**Coverage: GOOD (hard-fail + degraded split, matches `architecture.md`).**
The IPv6-skip gap is closed (#102): it now emits
`discovery_degraded_total{module="isis", reason="unsupported_ip_version"}` via
the direct `RecordDegraded` sink, reviving the previously-dead
`DegradedReasonUnsupportedIPVersion` constant. Residual gap: per-row "state not
up" drops are silent (but this is the normal IS-IS case, equivalent to BGP's
`no_peers`, and is *not* surfaced).

---

## BGP — `internal/discovery/bgp/bgp.go`, `bgp_vendor.go`, `bgp_v2.go`

**Reference implementation.** The only walker with a per-walker,
failure-shape-labelled outcome counter:
`network_topology_bgp_walker_outcome_total{walker, outcome}`
(`metrics/metrics.go:300-303`). `walker ∈ {vendor_cisco, vendor_arista,
vendor_juniper, vendor_nokia, rfc4273}`; `outcome ∈ {edges, no_peers,
mib_unimplemented, walker_drift, malformed_index, error}`.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="bgp", reason="module_walk_error"}` — `bgp.go:192-195` |
| vendor peer table (Step 1) | walk error | stashed; falls through to RFC 4273 | `bgp_walker_outcome_total{walker="vendor_*", outcome="error"}` (`bgp_vendor.go:326`); promoted to rate-limited `Warn` iff RFC 4273 then succeeds — `bgp.go:203-216, 251-260` |
| vendor peer table | every row index-rejected | walker_drift | `bgp_walker_outcome_total{walker="vendor_*", outcome="walker_drift"}` — `bgp_vendor.go:334` |
| vendor peer table | some rows index-rejected | per-row counter | `bgp_walker_outcome_total{walker="vendor_*", outcome="malformed_index"}` — `bgp_vendor.go:259` |
| RFC 4273 `bgpPeerTable` BulkWalk | walk error | hard error | `bgp_walker_outcome_total{walker="rfc4273", outcome="error"}` + `hard_fail_total{module="bgp"}` — `bgp.go:226-230` |
| RFC 4273 `bgpPeerTable` | zero PDUs | MIB unimplemented | `bgp_walker_outcome_total{walker="rfc4273", outcome="mib_unimplemented"}` — `bgp.go:238-239` |
| RFC 4273 `bgpPeerTable` | PDUs arrive, no peer established | no peers | `bgp_walker_outcome_total{walker="rfc4273", outcome="no_peers"}` — `bgp.go:236-237` |
| RFC 4273 `bgpPeerTable` | ≥1 established peer | edges | `bgp_walker_outcome_total{walker="rfc4273", outcome="edges"}` — `bgp.go:234-235` |
| peer state not established(6) / no remote addr | row rejected | `continue` | `slog.Debug` for missing addr — `bgp.go:310-318` |
| peer IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `bgp.go:321-330`. **Observable.** |

**Coverage: FULL.** This is the GA target shape: every input-failure class maps
to a distinct, labelled outcome that an operator can alert on. `walker_drift`
(decoder broken) is operationally separated from `no_peers` (BGP down) and
`mib_unimplemented` (expected on non-BGP devices).

---

## FDB — `internal/discovery/fdb/fdb.go`

Walks four+ tables: `dot1dTpFdbTable`, `dot1qTpFdbTable` (Q-BRIDGE),
`dot1dBasePortTable`, `dot1dStpPortTable`, IF-MIB `ifName`, and an optional
per-VLAN community-string FDB walk. As of #98–#100 the walker emits
`walker_outcome_total{walker="fdb", ...}`, reports per-row decode rejections via
`decode_issues_total{module="fdb", oid="1.3.6.1.2.1.17.1.4", reason=...}`, and
emits two new `discovery_degraded_total{module="fdb", ...}` reasons through a
direct `RecordDegraded` sink (so they fire even with zero edges).

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="fdb", reason="module_walk_error"}`; `walker_outcome_total{walker="fdb", outcome="error"}` (#98) — `fdb.go` |
| `dot1dTpFdbTable` BulkWalk | walk error | hard error | same as above (incl. `outcome="error"`) — `fdb.go` |
| `dot1dTpFdbTable` / B-MIB | **zero PDUs (non-bridging device)** | clean return, **0 edges** | `walker_outcome_total{walker="fdb", outcome="mib_unimplemented"}` (#98) — expected on non-bridging devices, **must not page** — `fdb.go` |
| `dot1qTpFdbTable` (Q-BRIDGE) BulkWalk | walk error AND B-MIB produced no usable entries | **non-fatal**, B-MIB only | `discovery_degraded_total{module="fdb", reason="qbridge_walk_failed"}` (#100) via direct sink + `slog.Debug`. **Observable.** Guarded: stays quiet if B-MIB already had entries, and quiet for legitimate B-MIB-only devices with a clean empty Q-BRIDGE walk — `fdb.go` |
| per-VLAN community walk | community contains `@` | skipped | rate-limited `Warn` (`WarnLimiter`) — `fdb.go`. **Observable (log).** |
| per-VLAN community walk | VLAN count > `max_vlans` | truncated | rate-limited `Warn` — `fdb.go`. **Observable (log).** |
| per-VLAN community walk | per-VLAN open/walk error | that VLAN skipped | `discovery_degraded_total{module="fdb", reason="vlan_walk_failed"}` (#100) via direct sink (labelled by reason only, **not** by VLAN id) + `slog.Debug`. **Observable.** — `fdb.go` |
| `dot1dBasePortTable` BulkWalk | walk error | hard error | `hard_fail_total{module="fdb", ...}` + `outcome="error"` — `fdb.go` |
| `dot1dStpPortTable` BulkWalk | walk error | hard error | `hard_fail_total{module="fdb", ...}` + `outcome="error"` — `fdb.go` |
| IF-MIB `ifName`/`ifDescr` (both) | walk error | hard error | `hard_fail_total{module="fdb", ...}` + `outcome="error"` — `fdb.go` |
| bridge port index invalid | row rejected | `continue` | `decode_issues_total{module="fdb", oid="1.3.6.1.2.1.17.1.4", reason="bridge_port_index_invalid"}` (#99) — `fdb.go` |
| bridge port has no ifIndex mapping | edge dropped | `continue` | `decode_issues_total{module="fdb", oid="1.3.6.1.2.1.17.1.4", reason="ifindex_unmapped"}` (#99) — `fdb.go` |
| entries filtered by status/STP/no-ifIndex leaving no edges | no edges | clean return | `walker_outcome_total{walker="fdb", outcome="no_neighbours"}` (#98) — entries present but none usable — `fdb.go` |
| port has >1 learned MAC (indirect) | edges suppressed | `continue` (cardinality guard) | **no per-port signal.** Only the LLDP-correlation-miss path increments `network_topology_fdb_suppressed_macs_total` (`FDBSuppressedMACs`), emitted later in `SynthesizeEdges`, not here — `fdb.go`, counter `metrics/metrics.go` |
| special MAC (multicast/broadcast/zero) | entry dropped | `continue` | none — `fdb.go` |
| STP state present and ≠ forwarding(5) | entry dropped | `continue` | none — `fdb.go` |

**Coverage: FULL (#98–#100).** Required tables hard-fail (now also
`outcome="error"`). The previously-most-silent walker is now observable:
zero-PDU B-MIB → `mib_unimplemented`; the Q-BRIDGE-walk-failure and per-VLAN
walk-failure paths fire `discovery_degraded_total{reason="qbridge_walk_failed"|"vlan_walk_failed"}`
via a direct module sink (so they signal even with zero edges, and the
Q-BRIDGE path is guarded against false alerts when the B-MIB already supplied
entries); per-row bridge-port-index and ifIndex-mapping drops increment
`decode_issues_total`; and entries filtered out leaving no usable edges surface
as `outcome="no_neighbours"`.

---

## MPLS-TE — `internal/discovery/mpls/mpls.go`

Required-table hard-fail contract, same family as IS-IS. Reads
`mplsTunnelOperStatus` (required) and `mplsTunnelAdminStatus` (optional) through
`WalkToIntMapStrict` (module arg `"mpls_te"`), so decode anomalies on both are
accounted.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="mpls_te", reason="module_walk_error"}` — `mpls.go:40-43` |
| `mplsTunnelOperStatus` (required) | walk error | hard error | `hard_fail_total{module="mpls_te", reason="module_walk_error"}` — `mpls.go:46-49` |
| `mplsTunnelOperStatus` (required) | no valid rows | hard error via `PolicyError` | `hard_fail_total{module="mpls_te", reason="required_table_no_valid_rows"}` — `mpls.go:50-59` |
| `mplsTunnelOperStatus` (required) | invalid ratio > 0.50 | hard error via `PolicyError` | `hard_fail_total{module="mpls_te", reason="required_table_invalid_ratio_exceeded"}` — `mpls.go:50-59` |
| `mplsTunnelOperStatus` (required) | some rows bad, ratio ≤ 0.50 | degraded, edges kept | `discovery_degraded_total{module="mpls_te", reason="required_table_partial_decode"}` + `decode_issues_total{module="mpls_te", oid=".1.3.6.1.2.1.10.166.3.2.2.1.17", reason=...}` — `mpls.go:63-68` |
| `mplsTunnelAdminStatus` (optional) | walk error | degraded, edges kept | `discovery_degraded_total{module="mpls_te", reason="missing_admin_status_walk"}`; `slog.Debug` — `mpls.go:69-72` |
| `mplsTunnelAdminStatus` (optional) | some rows bad | degraded, edges kept | `discovery_degraded_total{module="mpls_te", reason="invalid_admin_status_decode"}`; `slog.Debug` — `mpls.go:73-81` |
| tunnel OID suffix not 10 components / bad index | row rejected | `continue` | none — `mpls.go:92-95, 156-174` |
| egress IP unspecified/link-local | row rejected | `continue` | none — `mpls.go:96-98` |
| operStatus ≠ up(1) | row rejected | `continue` | none (normal: tunnel down) — `mpls.go:88-91` |
| egress IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `mpls.go:99-109`. **Observable.** |

**Coverage: GOOD (hard-fail + degraded split, matches `architecture.md`).**

---

## SNMP system walk — `internal/discovery/snmp/snmp.go` (`Walk`)

Runs once per device before any module walk, as
`WalkSystemWithCredentials` (`internal/app/probe.go:104`). Fetches the four
SYSTEM scalars (`sysDescr`, `sysObjectID`, `sysUpTime`, `sysName`) via a single
SNMP `GET`. This is the gate: if it fails, **no module runs for that device**.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| credential resolution | no usable profiles | hard error, device skipped | `Warn("snmp walk failed")`; `hard_fail_total{module="system", reason="system_group_walk_error"}`; `credential_trials_total{status="failed"}` — `probe.go:113-116`, `cycle.go:184-189` |
| all credential candidates fail (non-timeout) | auth failure | hard error, device skipped | `snmp_walks_total{status="error", reason="auth_failed"}`; `discovery_devices_total{status="failed", reason="auth_failed"}` — `cycle.go:198-204` |
| all credential candidates time out | unreachable | hard error, device skipped | `snmp_walks_total{status="timeout", reason="n/a"}`; `discovery_devices_total{status="failed", reason="timeout"}` — `cycle.go:198-200` |
| SYSTEM `GET` after connect | get error | `(nil, err)` | rolls up as above (system_group_walk_error) — `snmp.go:243-245` |
| `sysName` empty/garbage | partial | falls back to mgmt IP as device ID | `system_walk_anomaly_total{reason="empty_sysname"}` (#101) — `snmp.go` |
| `sysObjectID` unknown enterprise | partial | `Vendor="unknown"`; vendor-specific BGP4-V2 walker is skipped (BGP still falls through to the observable rfc4273 path) | `system_walk_anomaly_total{reason="unknown_vendor"}` (#101) — flags which devices were never tried against a vendor table; `VendorFromObjectID` `snmp.go` |
| `sysUpTime` < 24h or 497-day wrap | ambiguous | uptime recorded as-is | `slog.Debug` ("may be recent reboot or counter wrap"). **AMBIGUOUS** by design — wrap and reboot are indistinguishable — `snmp.go:264-271` |
| DNS resolution (hostname targets) | fail | device skipped | `Warn("host resolution failed")`; `discovery_devices_total{status="failed", reason="dns_failed"}` — `cycle.go:160-168` |
| resolved IP outside allow-list | skipped | `discovery_devices_total{status="failed", reason="outside_allow_list"}`; `Warn` — `cycle.go:174-180` |
| cycle budget deadline before goroutine starts | skipped | `cycle_budget_skips_total`; `discovery_devices_total{status="failed", reason="budget_expired"}` — `cycle.go:139-145` |

**Coverage: GOOD for connection/auth/reachability** (richest sub-reason
partitioning of any walker, via `discovery_devices_total` and `snmp_walks_total`
reason labels). The *content* gaps in a successful walk are now closed (#101):
unknown vendor and empty sysName — which degrade downstream behaviour (vendor →
BGP dispatch; sysName → graph identity) — surface via the low-cardinality
`system_walk_anomaly_total{reason}` counter (closed two-value set, no
device/IP/OID label).

---

## ARP — sub-walk inside `RunCycle` (no module package)

There is **no** `internal/discovery/arp/` package. ARP is enrichment only
(MAC→IP for FDB synthesis), gated by `modules.arp.enabled` (`config.go:241-246`),
run inline via `snmputil.WalkARPTable` (`snmp/pdu.go:197-234`). The
`architecture.md` "Intentional non-features" section confirms ARP is not an
edge source.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| ARP SNMP session (`Open`) | fail | non-fatal | `slog.Debug` only. **SILENT** — `cycle.go:336-339` |
| `ipNetToMediaTable` walk | error | non-fatal, no enrichment | `slog.Debug` only. **SILENT** — `cycle.go:341-345` |
| MAC seen with conflicting IPs across devices | first kept | `continue` | `slog.Debug` only. **SILENT** — `cycle.go:348-356` |

**Coverage: NONE (silent by design).** ARP is best-effort enrichment; a total
ARP failure degrades FDB→sysName resolution with no metric. Because it is not
an edge source, the GA contract arguably does not bind it — but the degradation
is invisible.

---

## Summary: outcome-counter coverage vs. silent degradation

| Walker | Hard-fail contract | Degraded signal | Per-walker outcome counter | Verdict |
|---|---|---|---|---|
| **BGP** | yes | n/a (uses outcome counter) | **yes** — `bgp_walker_outcome_total` (6 outcomes) | **FULL** |
| **LLDP** | transport (+`outcome="error"`) | `decode_issues_total` per-row (#99) | **yes** — `walker_outcome_total` (#98) | **FULL** (#98/#99) |
| **CDP** | transport (+`outcome="error"`) | `decode_issues_total` per-row (#99) | **yes** — `walker_outcome_total` (#98) | **FULL** (#98/#99) — residual: silent ifDescr fallback |
| **OSPF** | transport (+`outcome="error"`) | `decode_issues_total` per-row (#99) | **yes** — `walker_outcome_total` (#98) | **FULL** (#98/#99) — `no_neighbours` now surfaces sessions-down |
| **FDB** | required tables (+`outcome="error"`) | yes (`degraded_total` qbridge/vlan #100, `decode_issues_total` #99) | **yes** — `walker_outcome_total` (#98) | **FULL** (#98–#100) |
| **IS-IS** | yes (required-table policy) | yes (`degraded_total` incl. `unsupported_ip_version` #102, `decode_issues_total`) | no | **GOOD** — IPv6-skip gap closed (#102) |
| **MPLS-TE** | yes (required-table policy) | yes (`degraded_total`, `decode_issues_total`) | no | **GOOD** |
| **SNMP system** | yes (rich sub-reasons) | n/a | `system_walk_anomaly_total` (#101) | **FULL** for reach/auth + content (#101) |
| **ARP** | no | no | no | **SILENT by design** (enrichment, not an edge source) |

**Walkers meeting the GA criterion today:** BGP (outcome counter); LLDP, CDP,
OSPF, FDB (generic `walker_outcome_total` + per-row `decode_issues_total`, #98/#99,
plus FDB degraded reasons #100); IS-IS, MPLS-TE (hard-fail + degraded split,
IS-IS IPv6 gap closed by #102); SNMP system walk (sub-reason-partitioned
hard-fail + `system_walk_anomaly_total` content signal, #101). The #67-audit
acceptance criterion of moving LLDP/CDP/OSPF/FDB **from SILENT to FULL** is met
by #98–#102.

**Remaining residual gaps (not edge-source-binding):** CDP's silent
ifXTable→ifDescr fallback; ARP enrichment (silent by design); and per-row
"state not up" drops on IS-IS (the normal case). The `mib_unimplemented` /
`no_neighbours` / `walker_drift` three-way split that BGP pioneered now exists on
all four generic walkers, so a zero-edge result can be told apart as "feature
not present", "feature present but nothing to report", or "our decoder is broken
on this firmware".

See `docs/architecture.md` § "Discovery contracts (hard-fail vs degraded)" for
the IS-IS / MPLS-TE contract and `docs/operator/troubleshooting.md` for the
runbook that consumes these signals.
