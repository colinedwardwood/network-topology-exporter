# Lab — nokia-srbgp

Real-device SNMP capture for the **`vendor_nokia`** walker
(`internal/discovery/bgp/bgp_vendor.go`) on Nokia SR-OS (formerly Alcatel-Lucent
7705/7750; pre-acquisition gear uses the same MIB).
Closes [issue #57](https://github.com/colinedwardwood/network-topology-exporter/issues/57).

## What this lab is for

The walker walks `tBgpPeerTable` (OID `1.3.6.1.4.1.6527.3.1.2.13.2`) on
Nokia / Alcatel-Lucent SR-OS devices. Column numbers and the index decoder
were transcribed from `TIMETRA-BGP-MIB` documentation without real-device
verification (walker currently ships with `verified: false`). This lab
produces the capture that flips `verified` to `true`.

## Who runs this

- **You (the colleague with the Nokia / SR-OS device)** run
  `./colleague-capture.sh` and send back the tarball.
- **The maintainer (Colin)** receives the tarball, runs the redactor,
  and converts the captures into Go fixtures.

SR-OS supports both **classic CLI** and **MD-CLI** modes; the prep
section below covers both.

## Prerequisites

| Tool | How |
|---|---|
| `snmpwalk` (net-snmp) | Linux: `apt-get install snmp`. macOS: `brew install net-snmp`. |
| `tar` | preinstalled |
| `bash` 3.2+ | preinstalled |
| `coreutils` on macOS (for `gtimeout`) | `brew install coreutils` |

## Switch-side prep (5 minutes)

Pick v2c or v3, paste the matching config in your operating mode, then
save. Use whichever (classic CLI or MD-CLI) your device runs.

**v2c, classic CLI**:
```
/configure system security snmp view "all" subtree 1.3.6.1.4.1.6527 mask ff type included
/configure system security snmp view "all" subtree 1.3.6.1.2.1 mask ff type included
/configure system security snmp access group "public-grp" security-model snmpv1 security-level no-auth-no-privacy read "all"
/configure system security snmp access group "public-grp" security-model snmpv2c security-level no-auth-no-privacy read "all"
/configure system security snmp community "public" rwa version both group "public-grp"
admin save
```

**v2c, MD-CLI**:
```
configure system security snmp view "all" subtree 1.3.6.1.4.1.6527 mask ff type included
configure system security snmp view "all" subtree 1.3.6.1.2.1 mask ff type included
configure system security snmp access group "public-grp" context [{ security-model snmpv1 security-level no-auth-no-privacy read all }]
configure system security snmp access group "public-grp" context [{ security-model snmpv2c security-level no-auth-no-privacy read all }]
configure system security snmp community public access-permissions rwa version both group public-grp
commit
```

**v3 (authPriv with SHA + AES), classic CLI**:
```
/configure system security user "monitor" access snmp
/configure system security user "monitor" snmp authentication hash hmac-sha priv aes-128
/configure system security user "monitor" snmp authentication-key "authpw"
/configure system security user "monitor" snmp privacy-key "privpw"
/configure system security snmp view "all" subtree 1.3.6.1.4.1.6527 mask ff type included
/configure system security snmp view "all" subtree 1.3.6.1.2.1 mask ff type included
/configure system security snmp access group "v3grp" security-model usm security-level privacy read "all" notify "all"
/configure system security snmp usm-community community "monitor" group "v3grp"
admin save
```

**v3, MD-CLI**: the same shape but in `commit`-mode. See SR-OS docs;
the wrapper's verdict will tell you which knobs to turn if the first
attempt times out.

Confirm BGP is up with at least one established peer (`show router bgp summary`)
before running the capture.

## Run the capture

```bash
cd lab/nokia-srbgp
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

A file named `topology-capture-nokia-<host>-<timestamp>.tar.gz`,
about 5-50 KB, containing:

- `captures/` — raw `snmpwalk` text, one file per OID per host
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — sanitized execution log
- `SHA256SUMS` — hashes of every file above

**This tarball contains real IP and MAC addresses from your network.**
Send via direct email only.

## Where to send it

Email the tarball to **colin.wood@grafana.com** with subject
`Capture for issue #57 — Nokia tBgpPeerTable`. Include the sha256
the wrapper printed in the green banner.

## If something didn't work

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Nokia enterprise OID | Paste the `snmp view "all" subtree 1.3.6.1.4.1.6527 ...` block from the banner, save/commit, re-run |
| `bgp_mib_module_absent` | BGP MIB not loaded | Confirm BGP is configured (`show router bgp summary`) |
| `snmp_silent_likely_vrf` | SNMP source-access policy or router instance blocking | See vendor-specific gotchas below |
| `snmp_auth_failed_*` | Credentials wrong | Re-check the field named in the verdict |
| `snmp_reachable_vendor_mismatch` | This device isn't a Nokia / SR-OS | Confirm you pointed at the right host |

## Vendor-specific gotchas

- **Classic CLI vs MD-CLI.** SR-OS supports both modes; the syntax is
  different. Operators routinely run mixed. If a paste-able block doesn't
  parse, try the other variant — both are shown in the prep section.
- **SNMP source-access policy.** SR-OS lets you restrict which interfaces
  can be SNMP-walked. If preflight times out but ICMP works, check
  `show system security snmp` and look for a source-access policy that
  excludes the address you're walking from.
- **SR Linux versus SR-OS.** SR Linux (the modern OS for some 7220 IXR
  platforms) has incomplete standard-MIB coverage — issue #46 in this
  repo documents the LLDP gap. The `tBgpPeerTable` walker targets SR-OS
  specifically; if your device runs SR Linux, the capture will likely
  show `noSuchObject` and a verdict of `vendor_table_empty_mib_not_implemented`.
- **Old Alcatel-Lucent platforms.** Pre-acquisition 7705/7750 gear runs
  the same `TIMETRA-BGP-MIB` and will produce a valid capture. The
  walker doesn't distinguish; that's fine.

## Maintainer notes (for Colin)

On receipt:

1. Verify sha256 against the value in the email.
2. Extract: `tar xzf topology-capture-nokia-*.tar.gz`.
3. Read `diagnostics.json`'s verdict.
4. Run redactor: `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/.../r1_1_3_6_1_4_1_6527_3_1_2_13_2.txt` into
   `[]gosnmp.SnmpPDU` literals per `lab/cisco-iol-bgp/README.md` conventions.
   Land as `buildNokiaTBgpPeerRealPDUs` in `internal/discovery/bgp/bgp_v2_test.go`.
6. Confirm the four `vendorTableSpec` fields for `nokiaTBgpPeerSpec` in
   `bgp_vendor.go` (root, colState, colRemoteAs, decodeIndex) match the
   real capture. Update any that diverge.
7. Flip `verified: false` to `true` for `nokiaTBgpPeerSpec`.
8. Update README BGP vendor-coverage table from Experimental → validated.
9. Update `config/example.yaml` BGP block accordingly.
10. If captures came from SR Linux instead of SR-OS, note in the closing
    PR (relates to issue #46 LLDP gap context).
