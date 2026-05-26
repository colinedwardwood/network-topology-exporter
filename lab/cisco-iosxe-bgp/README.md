# Lab — cisco-iosxe-bgp

Real-device SNMP capture for the **`vendor_cisco`** walker
(`internal/discovery/bgp/bgp_vendor.go`) on Cisco IOS-XE.
Closes [issue #58](https://github.com/colinedwardwood/network-topology-exporter/issues/58).

## What this lab is for

The walker walks `cbgpPeer2Table` (OID `1.3.6.1.4.1.9.9.187.1.2.5`) on Cisco
devices. Column numbers were validated against IOL 17.12.1 (see
`lab/cisco-iol-bgp/`), but issue #58 asks for IOS-XE cross-validation. This
lab is the runbook + capture script for a colleague (or you, via DevNet)
to produce that capture.

## Who runs this

- **You (the colleague with the IOS-XE device)** run `./colleague-capture.sh`
  and send back the tarball.
- **The maintainer (Colin)** receives the tarball, runs the redactor, and
  converts the captures into Go fixtures.

If you don't have an IOS-XE device on hand, the **DevNet self-serve** path
at the bottom of this README is an alternate.

## Prerequisites

| Tool | How |
|---|---|
| `snmpwalk` (net-snmp) | Linux: `apt-get install snmp`. macOS: `brew install net-snmp`. |
| `tar` | preinstalled |
| `bash` 3.2+ | preinstalled |
| `coreutils` on macOS (for `gtimeout`) | `brew install coreutils` |

## Switch-side prep (5 minutes)

Pick v2c or v3, paste the matching config onto the router, then save.

**v2c**:
```
configure terminal
snmp-server community public RO
snmp-server view ALL 1.3.6.1.4.1.9.9.187 included
snmp-server community public view ALL RO
end
write memory
```

**v3 (authPriv with SHA + AES)**:
```
configure terminal
snmp-server view ALL 1.3.6.1.4.1.9.9.187 included
snmp-server group MONGRP v3 priv read ALL
snmp-server user monitor MONGRP v3 auth sha 'authpw' priv aes 128 'privpw'
end
write memory
```

Confirm BGP is up with at least one established peer (`show ip bgp summary`)
before running the capture.

## Run the capture

```bash
cd lab/cisco-iosxe-bgp
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

A file named `topology-capture-cisco-iosxe-<host>-<timestamp>.tar.gz`,
about 5-50 KB, containing:

- `captures/` — raw `snmpwalk` text, one file per OID per host
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — sanitized execution log
- `SHA256SUMS` — hashes of every file above
- `SEND-ME-THIS.txt` — what to do with this tarball

**This tarball contains real IP and MAC addresses from your network.**
Send via direct email only.

## Where to send it

Email the tarball to **colin.wood@grafana.com** with subject
`Capture for issue #58 — Cisco IOS-XE cbgpPeer2Table`. Include the sha256
the wrapper printed in the green banner.

## If something didn't work

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Cisco enterprise OID | Paste the `snmp-server view` block from the banner, save, re-run |
| `bgp_mib_module_absent` | BGP MIB not loaded | Confirm BGP is configured and running |
| `snmp_silent_likely_vrf` | Device doesn't reply on this VRF | Try `snmp-server vrf <your-mgmt-vrf>` |
| `snmp_auth_failed_*` | Credentials wrong | Re-check the field named in the verdict |
| `snmp_reachable_vendor_mismatch` | This device isn't a Cisco IOS-XE | Confirm you pointed at the right host |

## Vendor-specific gotchas

- **VRF binding.** Non-default VRF mgmt? Add `snmp-server vrf <name>`.
- **SNMPv3 engine ID change after image upgrade.** Newer IOS-XE images can
  regenerate the engine ID — re-create the v3 user after upgrade.
- **View ordering.** `snmp-server view ALL ... included` must be defined
  before `snmp-server community ... view ALL RO`.

## Maintainer notes (for Colin)

On receipt:

1. Verify sha256 against the value in the email.
2. Extract: `tar xzf topology-capture-cisco-iosxe-*.tar.gz`.
3. Read `diagnostics.json`'s verdict.
4. Run redactor: `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/.../r1_1_3_6_1_4_1_9_9_187_1_2_5.txt` into
   `[]gosnmp.SnmpPDU` literals per `lab/cisco-iol-bgp/README.md` conventions.
   Land as `buildCiscoCbgpPeer2IOSXERealPDUs` in
   `internal/discovery/bgp/bgp_v2_iosxe_test.go`.
6. Drop the `t.Skip` line in `bgp_v2_iosxe_test.go:79`.
7. If captures match IOL byte-for-byte, note as cross-confirmation.

## Alternate: DevNet self-serve

If you (Colin) want to produce this capture yourself, the original
DevNet-driven flow is preserved at `capture-devnet.sh` in this directory.
See its inline comments for the openconnect + CML reservation walkthrough.
