# Plan: BGP4-V2-MIB IPv6 Support

**Status:** Proposed
**Author:** ARCHITECTURAL_REVIEW.md remediation
**Created:** 2026-05-14
**Estimate:** 2–3 engineering days
**Risk:** Medium — new MIB walker, new SNMP test fixtures, vendor variance

## Problem statement

`internal/discovery/bgp/bgp.go` walks the RFC 4273 `bgpPeerTable` at `1.3.6.1.2.1.15.3`. This table is IPv4-only by design — `bgpPeerRemoteAddr` is a 4-byte IpAddress type. Dual-stack core networks running BGP over IPv6 transport produce zero rows in the IPv4 table, leaving the exporter blind to those adjacencies.

The standards picture:

- **RFC 4273 BGP4-MIB** (the current code path): IPv4 only.
- **RFC 4750 OSPFv2-MIB** has an equivalent gap addressed by **RFC 5643 OSPFv3-MIB**. Same pattern is expected for BGP.
- **draft-ietf-idr-bgp4-mibv2** (expired before standardization, but widely implemented as `BGP4-V2-MIB-JUNIPER`, `CISCO-BGP4-MIB`, and others). Defines `bgp4V2PeerTable` indexed by `(bgp4V2PeerInstance, bgp4V2PeerLocalAddrType, bgp4V2PeerLocalAddr, bgp4V2PeerRemoteAddrType, bgp4V2PeerRemoteAddr)`.
- **Vendor MIBs:**
  - Cisco: `CISCO-BGP4-MIB` at `1.3.6.1.4.1.9.9.187` (`cbgpPeer2Table`)
  - Juniper: `BGP4-V2-MIB-JUNIPER` at `1.3.6.1.4.1.2636.5.1.1` (`jnxBgpM2PeerTable`)
  - Nokia: `TIMETRA-BGP-MIB`
  - Arista: implements the IETF draft directly under `1.3.6.1.3.5`

This plan adds a second BGP walker that targets `bgp4V2PeerTable` first (IETF draft form), with vendor-specific fallbacks for Cisco and Juniper. IPv4 adjacencies discovered by either path deduplicate against the existing RFC 4273 walker.

## Acceptance criteria

| # | Criterion | Verification |
|---|---|---|
| AC1 | When a device responds to `bgp4V2PeerTable` walks, IPv6 peers are emitted as `discovery.Edge` with `DstDevice` set to the peer IPv6 address | Unit test with mock SNMP walker returning an IPv6 peer in `Established(6)` |
| AC2 | When the IETF draft table is empty, fall back to `cbgpPeer2Table` (Cisco) and `jnxBgpM2PeerTable` (Juniper) based on `sysObjectID` enterprise prefix | Unit tests per vendor with mock walkers |
| AC3 | IPv4 peers discovered by `bgp4V2PeerTable` deduplicate with RFC 4273 emissions — the same physical adjacency is not double-counted | Test that a device responding to both tables produces one edge per peer, not two |
| AC4 | An IPv6 peer address is correctly parsed and rendered (e.g. `2001:db8::1`, not the SNMP-encoded byte sequence) | Test with a known-value 16-byte SNMP encoding asserted against `net.IP.String()` |
| AC5 | Per-peer `precedenceRank` and `Confidence` match the existing RFC 4273 path (BGP=7, Confidence=low, Adjacency=unknown) | Cross-check against `bgp.go:60` |
| AC6 | A device that responds to neither table emits zero edges and logs a single warn-level message — no error, no scrape failure | Existing pattern test |
| AC7 | The exporter dual-stack-dedup logic is correct: if device A learns peer B via IPv4 and device B is also a managed device with sysName resolution, the IPv6 edge inherits the sysName (matches `D11` in CHANGELOG) | Integration-style test with peer IP resolution against the discovery inventory |

## Technical approach

### File layout

```
internal/discovery/bgp/
  bgp.go              # RFC 4273 walker (existing) — unchanged
  bgp_v2.go           # NEW: bgp4V2PeerTable walker (IETF draft form)
  bgp_vendor.go       # NEW: vendor-specific fallback dispatch
  bgp_v2_test.go      # NEW
  bgp_vendor_test.go  # NEW
```

A new top-level `Discover` orchestrator runs all three walkers per device and merges results. The merger key is `(local_addr, remote_addr)` so IPv4 adjacencies appearing in both tables collapse to one edge.

### Walker order and short-circuit

```
1. bgp4V2PeerTable (1.3.6.1.3.5.1.1.2)            # IETF draft form
   ├─ any rows? → use these, skip steps 2-3
   └─ empty? → step 2
2. Vendor-specific (based on sysObjectID lookup):
   ├─ Cisco (1.3.6.1.4.1.9.*) → cbgpPeer2Table
   ├─ Juniper (1.3.6.1.4.1.2636.*) → jnxBgpM2PeerTable
   ├─ Nokia (1.3.6.1.4.1.6527.*) → tBgpPeerTable
   └─ unknown vendor → step 3
3. RFC 4273 bgpPeerTable (existing walker)        # IPv4 fallback
```

Rationale: the IETF draft form is the most consistent across implementations that have it. Vendor tables are second because they cover dual-stack but introduce vendor variance. RFC 4273 remains the final fallback so we don't regress on devices that only implement the original MIB.

### Index decoding

`bgp4V2PeerTable` index is variable-length. Structure:

```
bgp4V2PeerInstance       (Unsigned32, 1 byte)
bgp4V2PeerLocalAddrType  (InetAddressType: 1=ipv4, 2=ipv6)
bgp4V2PeerLocalAddr      (InetAddress, length-prefixed: 4 bytes for ipv4, 16 for ipv6)
bgp4V2PeerRemoteAddrType (InetAddressType)
bgp4V2PeerRemoteAddr     (InetAddress, length-prefixed)
```

The walker emits each OID with the full index suffix encoded after the column OID. The decoder is non-trivial — needs to:
1. Parse the column OID prefix off
2. Parse `peerInstance` (1 byte)
3. Parse `localAddrType` (1 byte) + length-prefixed address
4. Same for remote
5. Verify total length consumed equals the index suffix length

`gosnmp` does not provide this decoder; we write it. Test vectors for all four type combinations (v4/v4, v4/v6, v6/v4, v6/v6) are mandatory.

### Vendor enterprise prefix mapping

Already exists in `internal/discovery/snmp/` per the `D21` CHANGELOG entry (vendor lookup is now deterministic). Reuse `enterprisePrefixes` rather than duplicating.

### Dual-stack peer deduplication

When the same physical router peers over both IPv4 and IPv6 with the same neighbor:

```
sw-a learns peer 192.0.2.5  (IPv4 session)
sw-a learns peer 2001:db8::5 (IPv6 session, same physical router)
```

Today these would emit as two separate edges. Per AC3, we deduplicate only when the IP-to-sysName resolver (existing per D11) maps both addresses to the same sysName. If sysName resolution is unavailable for one of the two addresses, emit both — the reconciliation layer can handle the cleanup downstream.

This is the same approach taken for LLDP IPv4+IPv6 mgmt addresses. The dedup key becomes `(local_sysname, remote_sysname)` once both sides resolve.

### Test fixtures

`internal/snmptest/` already provides a mock SNMP walker. Need new fixtures:

- `bgp4v2_ipv4_only.json`: 3 peers, all IPv4, all established
- `bgp4v2_ipv6_only.json`: 3 peers, all IPv6, all established
- `bgp4v2_dual_stack.json`: 4 peers — 2 IPv4 sessions, 2 IPv6, where 1 IPv4 and 1 IPv6 are the same neighbor
- `bgp4v2_empty_falls_back.json`: empty draft table → triggers vendor lookup
- `cbgppeer2_ios_xe.json`: real-shape Cisco fixture for the cbgpPeer2Table walker
- `jnxbgpm2_junos.json`: real-shape Juniper fixture

Fixtures should be captured from real devices where possible (anonymized via the existing `snmptest` redaction tooling) so they exercise actual vendor quirks.

### Configuration

No new config keys. The new walkers run automatically when `modules.bgp.enabled: true`. If we want a kill-switch for the new path (e.g. for operators who hit a vendor bug), add:

```yaml
modules:
  bgp:
    enabled: true
    use_v2_mib: true   # default true; set false to use only RFC 4273
```

Defer this decision until first production canary.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Vendor MIB rows have incomplete fields (e.g. missing remote sysName), producing edges with poor metadata | Same fallback the existing walker uses: emit `DstDevice` as the peer IP string; resolution happens later in the reconciliation pass |
| Long-tail vendor implementations (Nokia, Arista, MikroTik) walk strangely | Out of scope for the first PR; document as known limitation; accept patches that add fixtures + dispatch entries |
| Walking three tables per device per cycle increases SNMP load | The short-circuit logic above keeps it to one walk per device in steady state (first-table-non-empty wins). Worst case is 3× SNMP gets on devices with no BGP, which already short-circuit at the BGP-disabled check |
| IPv6 peer addresses inflate label cardinality | Already a known issue per `docs/operator/cardinality.md` — defer to that doc's mitigations |
| The draft MIB OID `1.3.6.1.3.5` is unofficially assigned and could collide with a real registration in the future | Pin to the IANA-registered OID if/when one is assigned; in the meantime the draft OID is what every implementer ships |

## Resolved scope decisions

1. **Kill-switch: yes.** `modules.bgp.use_v2_mib` config flag, defaults to **true**. Operators who hit a vendor bug can revert to RFC 4273-only with one line. Documented in `config/example.yaml` and the operator docs.
2. **Streaming telemetry (gNMI): out of scope for this work.** Captured at `bgp.go:36-39` as the eventual better path on modern Cisco/Arista/Juniper. BGP4-V2 explicitly serves the long tail of SNMP-managed gear; gNMI is a separate future initiative.
3. **Vendor coverage: full.** First PR ships with all four primary walkers — IETF draft form (covers Arista), `cbgpPeer2Table` (Cisco), `jnxBgpM2PeerTable` (Juniper), `tBgpPeerTable` (Nokia SR-OS). The dispatcher uses `sysObjectID` enterprise prefix lookup (already deterministic per CHANGELOG D21).

## Out of scope

- gNMI / streaming telemetry path for BGP. Mentioned at `bgp.go:36-39` as a known better fit for modern gear; that is a separate initiative.
- BGP-LS (RFC 7752) for full link-state discovery. Different protocol, different scope.
- IPv6 routing protocols other than BGP (OSPFv3, IS-IS over IPv6). The architectural review only flagged BGP IPv6 specifically.

## Sign-off

- [x] Vendor coverage: full (IETF draft + Cisco + Juniper + Nokia SR-OS)
- [x] Kill-switch: `modules.bgp.use_v2_mib`, default true
- [x] gNMI deferred to a separate initiative
- [ ] Real-device fixtures: synthetic fixtures land with the first PR; real captures **must** be added before the kill-switch default flips to silent (i.e. before the next minor release after the v2 walker ships)
