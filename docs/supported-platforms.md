# Supported Platforms

This matrix records which vendor platforms have been tested against the exporter's discovery modules and at what validation depth. "Real-device validated" means the walker was exercised against a live or containerlab-deployed device and real SNMP captures exist in `lab/`; "synthetic" means the walker was written from MIB documentation and tested against hand-constructed PDU streams only.

Cross-reference:

- Module details and MIB citations: `docs/standards.md`
- Full metric reference: `docs/metrics.md`
- BGP4-V2 vendor walker status: README § "Vendor walker coverage"

---

## Platform matrix

| Vendor | Platform family | OS version tested | Modules verified | Capture path | Notes |
|---|---|---|---|---|---|
| Cisco | IOS (IOL) | 17.12.1 | SNMP system group, BGP4-V2 (`cbgpPeer2Table`) | `lab/cisco-iol-bgp/` containerlab | BGP walker validated against real `snmpwalk` captures 2026-05-16 (issue #1, #31). Column numbers (`state=3`, `remoteAs=11`) and index encoding confirmed byte-for-byte. RFC 4273 `bgpPeerTable` also validated. LLDP and CDP walkers exercised via synthetic tests; no IOL-specific LLDP captures exist yet. |
| Cisco | IOS-XE | Hardware (colleague capture, 2026-05-30) | BGP4-V2 (`cbgpPeer2Table`) | `lab/cisco-iosxe-bgp/` colleague-capture flow | Cross-confirmation of the IOL-derived walker (issue #58). Four sessions (two IPv4, two IPv6, all Established), columns 3–29 of `cbgpPeer2Table`. Column numbers and index encoding matched IOL byte-for-byte — no walker drift between IOL emulator and real IOS-XE. No separate IOS-XE fixture committed; the IOL fixture provides CI regression coverage. |
| Arista | EOS (cEOS) | 4.36.0F | SNMP system group, BGP4-V2 enterprise (`1.3.6.1.4.1.30065.4.1.1.2`) | `lab/arista-ceos-bgp/` containerlab | Walker validated against real cEOS 4.36 captures 2026-05-16 (issue #31). Arista does **not** implement the IETF draft `bgp4V2PeerTable` at `1.3.6.1.3.5.1.1.2` — the enterprise OID is the correct target. Column numbers (`state=13`, `remoteAs=10`) and index format confirmed from captures. LLDP not separately captured on cEOS; walker is IEEE 802.1AB-standard and expected to work on platforms that implement the MIB correctly. |
| Nokia | SR Linux | 24.7.2 | SNMP system group only | E2E containerlab (`make test-e2e-srl`) | **LLDP via SNMP is not supported on SR Linux 24.x.** The standard IEEE 802.1AB LLDP MIB at `1.0.8802.1.1.2` returns `No Such Object` on every probe. LLDP data is only available via gNMI/JSON-RPC on SR Linux — a v2.0.0 work item. The SNMP system group (`sysDescr`, `sysName`, `sysUpTime`, `sysObjectID`) is exposed and works. The classic `TIMETRA-LLDP-MIB` at `1.3.6.1.4.1.6527.3.1.2.43` (used on SR-OS) is also absent on SR Linux. See issue #46. |
| Nokia | SR-OS | 25.7.R2 (7750 SR) | BGP4-V2 (`tBgpPeerNgTable`, `1.3.6.1.4.1.6527.3.1.2.14.4.7`) — **Validated** | Real-device capture via `lab/nokia-srbgp/` colleague-capture flow | Real-device validated against SR-OS 25.7.R2 (issue #57). Modern SR-OS populates the next-gen `tBgpPeerNgTable` (state col 59 `tBgpPeerNgOperStatus`, remote-AS col 66 `tBgpPeerNgPeerAS4Byte`, index = vRtrID + explicit-length InetAddress); the legacy `tBgpPeerTable` (`…3.1.2.13.2`) is empty on modern SR-OS, and pre-Ng SR-OS / SR Linux fall through to the RFC 4273 fallback. Walker surfaces failure modes via `network_topology_bgp_walker_outcome_total{walker="vendor_nokia",outcome=...}` — silent zero-edge dropout is alertable. Alcatel-Lucent pre-acquisition gear (7705 SAR, 7750 SR, 7250 IXR) runs the same MIB family. |
| Juniper | Junos (vJunos-router / vMX / physical) | Lab-validated (vJunos-router JUNOS 25.4R1.12, 2026-06) | BGP4-V2 (`jnxBgpM2PeerTable`, `1.3.6.1.4.1.2636.5.1.1.2.1.1`) — validated | `lab/juniper-jnxbgp/` (containerlab vJunos-router + FRR) | Verified against a real Junos SNMP stack (#56): columns matched the docs, index format corrected to the implicit-length local+remote InetAddress form. `disable_v2_mib` no longer needed on Junos. |

---

## Notes on "modules verified"

**SNMP system group** (always): `sysDescr`, `sysName`, `sysUpTime`, `sysObjectID`. Required by every mode; used for vendor detection (enterprise prefix from `sysObjectID`) and device identity. Validated against every platform listed above.

**LLDP** (IEEE 802.1AB): Walker is specification-derived and has no per-vendor branching. Platforms that correctly implement the MIB at `1.0.8802.1.1.2` work without modification. Platforms confirmed to fail: SR Linux 24.x (gNMI only, #46). Platforms with E2E containerlab coverage via synthetic SNMP agents: all platforms in `make test-e2e`.

**CDP** (CISCO-CDP-MIB): Cisco-only. Not expected on Arista, Nokia, or Juniper platforms.

**BGP4-V2 vendor walkers**: See the table above and README § "Vendor walker coverage" for the definitive validation status of each walker. The RFC 4273 fallback (`bgpPeerTable`) provides IPv4-only BGP peer edges on any platform that implements the base MIB.

**OSPF, IS-IS, FDB, MPLS-TE**: These walkers are implemented from their respective RFCs and tested against synthetic SNMP agents in `make test-integration`. No per-vendor real-device captures exist yet for these modules; they are expected to work on any conformant platform but have not been explicitly validated against the platforms in this matrix.

---

## Adding a new platform

To add a platform to this matrix:

1. Run the exporter against the platform (or a colleague-capture in `lab/<vendor>/`) and confirm that the target modules produce edges rather than `outcome=mib_unimplemented` or `outcome=walker_drift`.
2. Land the `snmpwalk` captures in the appropriate `lab/` directory per the existing lab README conventions.
3. Update this table row with the OS version tested and the module list verified.
4. If a new vendor BGP4-V2 walker is needed, add a `vendorTableSpec` in `internal/discovery/bgp/bgp_vendor.go` per the existing pattern and cite the public vendor MIB source.
