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
`slog.Debug`) produces **no metric** for that drop. Such drops are listed
below as **silent** and are the basis for the proposed follow-up issues.

The only walker that emits a *per-walker, failure-shape-labelled* counter today
is BGP, via `network_topology_bgp_walker_outcome_total{walker, outcome}`
(`metrics/metrics.go:300-303`, emitted through `recordWalkerOutcome`,
`bgp/bgp.go:66-71`). No other walker calls `WalkerMetrics.RecordWalkerOutcome`
(`metrics/walker_metrics_adapter.go:29`).

---

## LLDP — `internal/discovery/lldp/lldp.go`

Walks `lldpLocPortTable` (`oidLocPortTable` 1.0.8802.1.1.2.1.3.7) and
`lldpRemTable` (`oidRemTable` ...4.1) via `snmputil.BulkWalk`. No
`WalkToIntMapStrict`, so **no decode-issue / quarantine accounting**.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="lldp", reason="module_walk_error"}`; `module_last_status=2` — `lldp.go:100-102` |
| `lldpLocPortTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above — `lldp.go:106-109` |
| `lldpRemTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above — `lldp.go:111-114` |
| `lldpRemTable` BulkWalk | **zero PDUs (MIB unimplemented)** | clean return, **0 edges** | `module_last_status=0` — **indistinguishable from "implemented, no neighbours"**. No `mib_unimplemented` outcome (BGP has one; LLDP does not). **SILENT** — `lldp.go:155-209` |
| `lldpRemChassisIdSubtype` out of range 1–7 | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:223-227` |
| `lldpRemPortIdSubtype` out of range 1–7 | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:230-234` |
| MAC chassis/port ID wrong length | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:237-241, 262-266` |
| malformed network-address chassis ID | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:245-259` |
| unresolvable device/port | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:285-290` |
| non-IP chassis ID + scope filtering active | row rejected | `continue` | `slog.Debug` only. **SILENT** — `lldp.go:315-320` |
| neighbour IP outside allow-list | not an edge | emitted as `OutOfScopeNeighbour` | `network_topology_out_of_scope_neighbours_total` (graph-layer) — `lldp.go:304-314`. **Observable.** |

**Coverage: PARTIAL / degrades silently.** Transport-level failures are
hard-fail and observable. Every per-row decode rejection is a silent
`continue`+`slog.Debug`. There is no `walker_drift` analogue: a firmware that
returns the whole `lldpRemTable` with malformed subtypes would yield zero edges
and `module_last_status=0`, identical to a device with no neighbours.

---

## CDP — `internal/discovery/cdp/cdp.go`

Walks `cdpCacheTable` (1.3.6.1.4.1.9.9.23.1.2.1) via `BulkWalk` and IF-MIB
`ifName`/`ifDescr` via `snmputil.WalkIfNamesWithFallback`. No
`WalkToIntMapStrict`, so **no decode-issue / quarantine accounting**.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="cdp", reason="module_walk_error"}`; `module_last_status=2` — `cdp.go:59-62` |
| `ifName`/`ifDescr` (both fail) | error | hard error, walk aborts | same as above — `cdp.go:65-68`, fallback logic `snmp/pdu.go:179-192` |
| `ifName` present but `ifDescr`-only fallback used | partial | clean; local port falls back to `if<ifIndex>` | **no signal** — `cdp.go:146-149`. Note: `WalkIfNamesWithFallback` swallows the ifXTable miss with no degraded marker. **SILENT** |
| `cdpCacheTable` BulkWalk | transport/SNMP error | hard error, walk aborts | `hard_fail_total{module="cdp", ...}` — `cdp.go:70-73` |
| `cdpCacheTable` | **zero PDUs (non-Cisco / CDP off)** | clean return, **0 edges** | `module_last_status=0`. No `mib_unimplemented` outcome. **SILENT** (expected on non-Cisco; still unobservable) — `cdp.go:78-82` |
| OID index components ≤ 0 / unparseable | row rejected | `continue` | none (no log) — `cdp.go:100-109` |
| empty deviceID or devicePort | row rejected | `continue` | none (no log) — `cdp.go:142-144` |
| non-IP neighbour + scope filtering active | row rejected | `continue` | `slog.Debug` only. **SILENT** — `cdp.go:153-158` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` (graph-layer) — `cdp.go:159-169`. **Observable.** |

**Coverage: PARTIAL / degrades silently.** Same shape as LLDP. Additional gap:
the silent ifXTable→ifDescr fallback (`cdp.go:146`, helper `snmp/pdu.go:179`)
produces no degraded signal, unlike IS-IS which marks `missing_srcport_mapping`
for an equivalent enrichment miss.

---

## OSPF — `internal/discovery/ospf/ospf.go`

Walks `ospfNbrTable` (RFC 4750, 1.3.6.1.2.1.14.10) via `BulkWalk`. No
`WalkToIntMapStrict` — **no decode-issue / quarantine accounting**. Module
doc-comment explicitly states "Treat empty walk results as normal, not as an
error" (`ospf.go:31-33`).

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="ospf", reason="module_walk_error"}`; `module_last_status=2` — `ospf.go:78-81` |
| `ospfNbrTable` BulkWalk | transport/SNMP error | hard error, walk aborts | same as above — `ospf.go:84-87` |
| `ospfNbrTable` | **zero PDUs (OSPF-MIB unimplemented)** | clean return, **0 edges** | `module_last_status=0`. No `mib_unimplemented` outcome. **SILENT** (common on modern OS — `ospf.go:31`) |
| `ospfNbrTable` rows all present but **state never full/twoWay** | no edges | `continue` per row | **SILENT** — no `no_peers` analogue. Indistinguishable from MIB-unimplemented and from neighbour-less. The BGP walker draws exactly this distinction (`no_peers` vs `mib_unimplemented`); OSPF does not — `ospf.go:133-135` |
| OID suffix not `<col>.<4 octets>.<idx>` | row rejected | `continue` | none (no log) — `ospf.go:99-103`, validation `ospf.go:164-194` |
| `ospfNbrIpAddr` PDU not decodable as IPv4 | `nbrIP==nil`, row skipped | `continue` | none (no log) — `ospf.go:111,125-127` |
| nbr IP unspecified/link-local/loopback | row rejected | `continue` | none (no log) — `ospf.go:130-132` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `ospf.go:136-145`. **Observable.** |

**Coverage: PARTIAL / degrades silently.** Hard-fail on transport only. The
"BGP is configured but every session is down" signal that BGP exposes as
`outcome="no_peers"` has a direct OSPF analogue ("OSPF runs but no adjacency is
full") that is **entirely unobservable** here.

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
| IPv6 adjacency rows | not yet supported | skipped, IPv4 edges unaffected | `slog.Info` ("IPv6 adjacency skipped"), logged once per walk. **Partially observable** (log only, no metric) — `isis.go:144-154` |
| adj state not up(3) / unknown adjKey | row rejected | `continue` | none — `isis.go:159-162` |
| adj IP nil/unspecified/link-local | row rejected | `continue` | none — `isis.go:163-169` |
| circuit→ifName join misses for a row | per-edge degrade | edge kept, SrcPort empty | `discovery_degraded_total{module="isis", reason="missing_srcport_mapping"}` — `isis.go:180-183` |
| neighbour IP outside allow-list | not an edge | `OutOfScopeNeighbour` | `out_of_scope_neighbours_total` — `isis.go:184-194`. **Observable.** |

**Coverage: GOOD (hard-fail + degraded split, matches `architecture.md`).**
Residual gaps: IPv6 skip is log-only (no `unsupported_ip_version` metric,
though the constant `DegradedReasonUnsupportedIPVersion` exists at
`discovery.go:67` and is unused here); per-row "state not up" drops are silent
(but this is the normal IS-IS case, equivalent to BGP's `no_peers`, and is
*not* surfaced).

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
per-VLAN community-string FDB walk. No `WalkToIntMapStrict` — **no decode-issue
/ quarantine accounting**.

| Dependency | Failure | Behaviour | Operator signal |
|---|---|---|---|
| SNMP session (`Open`) | connect/auth fail | hard error, walk aborts | `hard_fail_total{module="fdb", reason="module_walk_error"}` — `fdb.go:131-134` |
| `dot1dTpFdbTable` BulkWalk | walk error | hard error | same as above — `fdb.go:137-140` |
| `dot1qTpFdbTable` (Q-BRIDGE) BulkWalk | walk error / no-such-object | **non-fatal**, B-MIB only | `slog.Debug` only. **SILENT** (documented as intentional) — `fdb.go:143-145` |
| per-VLAN community walk | community contains `@` | skipped | rate-limited `Warn` (`WarnLimiter`) — `fdb.go:330-342`. **Observable (log).** |
| per-VLAN community walk | VLAN count > `max_vlans` | truncated | rate-limited `Warn` — `fdb.go:344-357`. **Observable (log).** |
| per-VLAN community walk | per-VLAN open/walk error | that VLAN skipped | `slog.Debug` only. **SILENT** — `fdb.go:387-395` |
| `dot1dBasePortTable` BulkWalk | walk error | hard error | `hard_fail_total{module="fdb", ...}` — `fdb.go:151-154` |
| `dot1dStpPortTable` BulkWalk | walk error | hard error | `hard_fail_total{module="fdb", ...}` — `fdb.go:155-158` |
| IF-MIB `ifName`/`ifDescr` (both) | walk error | hard error | `hard_fail_total{module="fdb", ...}` — `fdb.go:159-162` |
| bridge port has no ifIndex mapping | edge dropped | `continue` | `slog.Debug` only. **SILENT** — `fdb.go:517-521` |
| port has >1 learned MAC (indirect) | edges suppressed | `continue` (cardinality guard) | **no per-port signal.** Only the LLDP-correlation-miss path increments `network_topology_fdb_suppressed_macs_total` (`FDBSuppressedMACs`), which is emitted later in `SynthesizeEdges`, not here — `fdb.go:527-534`, counter `metrics/metrics.go:268-271` |
| special MAC (multicast/broadcast/zero) | entry dropped | `continue` | none — `fdb.go:500-505, 476-488` |
| STP state present and ≠ forwarding(5) | entry dropped | `continue` | none — `fdb.go:506-508` |

**Coverage: PARTIAL / degrades silently.** Required tables hard-fail. But FDB
has the **most silent drop paths of any walker**: Q-BRIDGE miss, per-VLAN walk
error, ifIndex-mapping miss, and indirect-port suppression are all
unobservable (the one related counter, `fdb_suppressed_macs_total`, covers a
*different* drop reason and fires in the synthesis layer). There is no
`mib_unimplemented` analogue — a non-bridging device returns 0 edges with
`module_last_status=0`.

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
| `sysName` empty/garbage | partial | falls back to mgmt IP as device ID | **no signal** — `snmp.go:247-258` |
| `sysObjectID` unknown enterprise | partial | `Vendor="unknown"`; affects BGP vendor dispatch | **no signal** — `snmp.go:261`, `VendorFromObjectID` `snmp.go:468-480` |
| `sysUpTime` < 24h or 497-day wrap | ambiguous | uptime recorded as-is | `slog.Debug` ("may be recent reboot or counter wrap"). **AMBIGUOUS** by design — wrap and reboot are indistinguishable — `snmp.go:264-271` |
| DNS resolution (hostname targets) | fail | device skipped | `Warn("host resolution failed")`; `discovery_devices_total{status="failed", reason="dns_failed"}` — `cycle.go:160-168` |
| resolved IP outside allow-list | skipped | `discovery_devices_total{status="failed", reason="outside_allow_list"}`; `Warn` — `cycle.go:174-180` |
| cycle budget deadline before goroutine starts | skipped | `cycle_budget_skips_total`; `discovery_devices_total{status="failed", reason="budget_expired"}` — `cycle.go:139-145` |

**Coverage: GOOD for connection/auth/reachability** (richest sub-reason
partitioning of any walker, via `discovery_devices_total` and `snmp_walks_total`
reason labels). Gaps are in *content* of a successful walk: unknown vendor and
empty sysName degrade downstream behaviour (vendor → BGP dispatch; sysName →
graph identity) with no metric.

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
| **IS-IS** | yes (required-table policy) | yes (`degraded_total`, `decode_issues_total`) | no | **GOOD** — gap: no `no_peers`/IPv6 metric |
| **MPLS-TE** | yes (required-table policy) | yes (`degraded_total`, `decode_issues_total`) | no | **GOOD** |
| **SNMP system** | yes (rich sub-reasons) | n/a | no (uses `devices_total`/`snmp_walks_total`) | **GOOD** for reach/auth; content gaps |
| **LLDP** | transport only | no | no | **SILENT** per-row + no `mib_unimplemented`/`drift` |
| **CDP** | transport only | no | no | **SILENT** per-row + silent ifDescr fallback |
| **OSPF** | transport only | no | no | **SILENT** — no `no_peers` analogue (sessions-down invisible) |
| **FDB** | required tables | no | no | **SILENT** — most drop paths of any walker |
| **ARP** | no | no | no | **SILENT by design** (enrichment, not an edge source) |

**Walkers meeting the GA criterion today:** BGP (outcome counter), IS-IS,
MPLS-TE (hard-fail + degraded split), SNMP system walk (sub-reason-partitioned
hard-fail).

**Walkers that degrade silently and need follow-up:** LLDP, CDP, OSPF, FDB.
The common gap is the lack of a `mib_unimplemented` / `no_peers` / `walker_drift`
three-way split that BGP pioneered: today these walkers cannot tell an operator
whether a zero-edge result means "feature not present", "feature present but
nothing to report", or "our decoder is broken on this firmware". Per-row decode
rejections are also uniformly silent (`continue` + at most `slog.Debug`),
unlike IS-IS/MPLS-TE which route required tables through
`WalkToIntMapStrict` for decode-issue accounting.

See `docs/architecture.md` § "Discovery contracts (hard-fail vs degraded)" for
the IS-IS / MPLS-TE contract and `docs/operator/troubleshooting.md` for the
runbook that consumes these signals.
