# Lab — juniper-jnxbgp

Real-device SNMP capture for the **`vendor_juniper`** walker
(`internal/discovery/bgp/bgp_vendor.go`) on Juniper / Junos.
Closes [issue #56](https://github.com/colinedwardwood/network-topology-exporter/issues/56).

## What this lab is for

The walker walks `jnxBgpM2PeerTable` (OID `1.3.6.1.4.1.2636.5.1.1.2.1.1`)
on Junos devices. Column numbers and the index decoder were transcribed
from `BGP4-V2-MIB-JUNIPER` documentation without real-device verification
(walker currently ships with `verified: false`). This lab is the runbook
for a colleague with a Junos device (MX, SRX, EX-routing variants — any
platform that runs BGP) to produce the capture that flips `verified` to
`true`.

## Who runs this

- **You (the colleague with the Junos device)** run `./colleague-capture.sh`
  and send back the tarball.
- **The maintainer (Colin)** receives the tarball, runs the redactor, and
  converts the captures into Go fixtures.

## Prerequisites

| Tool | How |
|---|---|
| `snmpwalk` (net-snmp) | Linux: `apt-get install snmp`. macOS: `brew install net-snmp`. |
| `tar` | preinstalled |
| `bash` 3.2+ | preinstalled |
| `coreutils` on macOS (for `gtimeout`) | `brew install coreutils` |

## Switch-side prep (5 minutes)

Pick v2c or v3, paste the matching config in Junos configuration mode,
then `commit`. Junos `set` commands stage changes; nothing applies until
the `commit`.

**v2c**:
```
configure
set snmp view all-mibs oid 1.3.6.1.4.1.2636 include
set snmp view all-mibs oid 1.3.6.1.2.1 include
set snmp community public authorization read-only
set snmp community public view all-mibs
commit
```

**v3 (authPriv with SHA + AES)**:
```
configure
set snmp view all-mibs oid 1.3.6.1.4.1.2636 include
set snmp view all-mibs oid 1.3.6.1.2.1 include
set snmp v3 usm local-engine user monitor authentication-sha authentication-password "authpw"
set snmp v3 usm local-engine user monitor privacy-aes128 privacy-password "privpw"
set snmp v3 vacm security-to-group security-model usm security-name monitor group monitor-grp
set snmp v3 vacm access group monitor-grp default-context-prefix security-model usm security-level privacy read-view all-mibs
commit
```

If your management interface is in a non-default routing-instance (the
common case on MX is `mgmt_junos`), add:
```
set snmp routing-instance mgmt_junos
commit
```

Confirm BGP is up with at least one established peer (`show bgp summary`)
before running the capture. The wrapper detects "BGP up but vendor table
empty" and tells you which case applies, but starting from a known-good
state shortens iteration.

## Run the capture

```bash
cd lab/juniper-jnxbgp
./colleague-capture.sh -h <router-ip> -c public
# or v3:
./colleague-capture.sh -h <router-ip> -V 3 -u monitor -a SHA -A 'authpw' -x AES -X 'privpw'
```

Safety affordances:

```bash
./colleague-capture.sh -h <router-ip> -c public --dry-run
./colleague-capture.sh -h <router-ip> -c public --preflight-only
```

## What you'll get

A file named `topology-capture-juniper-<host>-<timestamp>.tar.gz`,
about 5-50 KB, containing:

- `captures/` — raw `snmpwalk` text, one file per OID per host
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — sanitized execution log (passwords masked)
- `SHA256SUMS` — hashes of every file above

**This tarball contains real IP and MAC addresses from your network.**
Send via direct email only.

## Where to send it

Email the tarball to **colin.wood@grafana.com** with subject
`Capture for issue #56 — Juniper jnxBgpM2PeerTable`. Include the sha256
the wrapper printed in the green banner so we can verify integrity.

## If something didn't work

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Juniper enterprise OID | Paste the `set snmp view all-mibs ...` block from the banner, `commit`, re-run |
| `bgp_mib_module_absent` | BGP MIB not loaded on this device | Confirm BGP is configured and a peer is established |
| `snmp_silent_likely_vrf` | Device doesn't reply on the routing-instance the wrapper reached it via | Try `set snmp routing-instance mgmt_junos` (or your mgmt instance) |
| `snmp_auth_failed_*` | Credentials wrong | Re-check the field named in the verdict |
| `snmp_reachable_vendor_mismatch` | This device isn't a Juniper | Confirm you pointed at the right host |

## Vendor-specific gotchas

- **Routing-instance for SNMP.** MX-series devices commonly put management
  in `mgmt_junos`. SNMP doesn't follow management traffic automatically —
  `set snmp routing-instance mgmt_junos` is required if you're reaching
  the device over that instance.
- **`commit` semantics.** Junos `set` commands stage changes; nothing
  applies until `commit`. If a paste-able fix doesn't take effect, check
  whether you ran `commit`.
- **`jnxBgpM2RouteTable` cross-validation.** This lab also walks
  `1.3.6.1.4.1.2636.5.1.1.2.6` for peer-keyed cross-validation of the
  index encoding. If the peer table is empty but the route table has rows,
  that's a strong signal of a view restriction specifically on the peer
  table OID — uncommon but worth flagging on receipt.

## Maintainer notes (for Colin)

On receipt:

1. Verify sha256 against the value in the email.
2. Extract: `tar xzf topology-capture-juniper-*.tar.gz`.
3. Read `diagnostics.json`'s verdict.
4. Run redactor: `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/.../r1_1_3_6_1_4_1_2636_5_1_1_2_1_1.txt` into
   `[]gosnmp.SnmpPDU` literals per `lab/cisco-iol-bgp/README.md` conventions.
   Land as `buildJuniperJnxBgpM2RealPDUs` in `internal/discovery/bgp/bgp_v2_test.go`
   (or a new `bgp_v2_juniper_test.go` file matching the IOS-XE scaffold pattern).
6. Confirm the four `vendorTableSpec` fields for `juniperJnxBgpM2PeerSpec` in
   `bgp_vendor.go` (root, colState, colRemoteAs, decodeIndex) match the
   real capture. Update any that diverge.
7. Flip `verified: false` to `true` for `juniperJnxBgpM2PeerSpec`.
8. Update README BGP vendor-coverage table from Experimental → validated.
9. Update `config/example.yaml` BGP block accordingly.
