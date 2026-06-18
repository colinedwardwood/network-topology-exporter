# Lab — nokia-srbgp

Thanks for running this. We can't validate our Nokia BGP MIB walker
without a real SR-OS device, so your snmpwalk against a live router is
what gets this code out of "experimental" status.

**About 10 minutes:** 5 to paste an SNMP config on the router, 1 to run
the capture script, then a quick email back with the tarball. The
script self-diagnoses if anything's off and tells you what to fix.

Any SR-OS device with BGP up works (7705 SAR, 7750 SR, 7250 IXR). Old
Alcatel-Lucent pre-acquisition gear runs the same MIB and works too.

> **One gotcha up front:** if your device runs **SR Linux** (the newer
> OS on some 7220 IXR platforms), this MIB isn't implemented there.
> Capture against an SR-OS box. We've documented the SR Linux gap
> separately in issue #46.

## Before you start

On the machine you'll run snmpwalk from:

| Tool | How |
|---|---|
| `snmpwalk` (net-snmp) | Linux: `apt-get install snmp`. macOS: `brew install net-snmp`. |
| `tar` | already there |
| `bash` 3.2+ | already there |
| `gtimeout` (macOS only) | `brew install coreutils` |

And on the router:
- Reachable from wherever snmpwalk runs
- BGP up with at least one established peer (`show router bgp summary`)

## On your router

SR-OS supports both **classic CLI** and **MD-CLI**. Use whichever your
device is configured for. Pick **v2c** (simpler) or **v3** (auth+priv),
paste the matching block, then save.

**v2c, classic CLI:**
```
/configure system security snmp view "all" subtree 1.3.6.1.4.1.6527 mask ff type included
/configure system security snmp view "all" subtree 1.3.6.1.2.1 mask ff type included
/configure system security snmp access group "public-grp" security-model snmpv1 security-level no-auth-no-privacy read "all"
/configure system security snmp access group "public-grp" security-model snmpv2c security-level no-auth-no-privacy read "all"
/configure system security snmp community "public" rwa version both group "public-grp"
admin save
```

**v2c, MD-CLI:**
```
configure system security snmp view "all" subtree 1.3.6.1.4.1.6527 mask ff type included
configure system security snmp view "all" subtree 1.3.6.1.2.1 mask ff type included
configure system security snmp access group "public-grp" context [{ security-model snmpv1 security-level no-auth-no-privacy read all }]
configure system security snmp access group "public-grp" context [{ security-model snmpv2c security-level no-auth-no-privacy read all }]
configure system security snmp community public access-permissions rwa version both group public-grp
commit
```

**v3 (authPriv, SHA + AES), classic CLI:**
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

**v3 in MD-CLI** is the same shape but in `commit` mode — see the SR-OS
docs. If your first attempt times out, the wrapper's verdict will tell
you which knob to turn.

## Run the capture

From this directory:

```bash
./colleague-capture.sh -h <router-ip> -c public
# or v3:
./colleague-capture.sh -h <router-ip> -V 3 -u monitor -a SHA -A 'authpw' -x AES -X 'privpw'
```

Want to see what it would do without sending any SNMP traffic?

```bash
./colleague-capture.sh -h <router-ip> -c public --dry-run
./colleague-capture.sh -h <router-ip> -c public --preflight-only
```

When it finishes it prints a coloured banner with a verdict. **Green
means good to send.** Yellow or red means the banner spells out
exactly what to try next.

## Send the tarball back

You'll get a file like `topology-capture-nokia-<host>-<timestamp>.tar.gz`,
usually 5-50 KB. Inside:

- raw `snmpwalk` output (one file per OID)
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — execution log, passwords masked
- `SHA256SUMS` — hashes of the above

**Heads up:** this contains real IPs and MACs from your network. Send
it over direct email, not Slack or anything public.

Email to **colin.wood@grafana.com** with subject:

> Capture for issue #57 — Nokia tBgpPeerTable

Paste the sha256 from the green banner into the email so we can verify
nothing got mangled in transit.

## If something didn't work

The script picks one verdict and prints it in the banner. Here's what
each one means and what to do:

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Nokia enterprise OID | Paste the `snmp view "all" subtree 1.3.6.1.4.1.6527 ...` block from the banner, save/commit, re-run |
| `vendor_table_empty_mib_not_implemented` | This MIB isn't on this device | Are you on SR Linux instead of SR-OS? The walker targets SR-OS specifically. |
| `bgp_mib_module_absent` | BGP MIB isn't loaded | Confirm BGP is configured (`show router bgp summary`) |
| `snmp_silent_likely_vrf` | SNMP source-access policy or router-instance blocking | See gotchas below |
| `snmp_auth_failed_*` | Credentials are wrong somewhere | The verdict names the field |
| `snmp_reachable_vendor_mismatch` | This device isn't an SR-OS | Double-check the IP |

## SR-OS-specific gotchas

- **Classic CLI vs MD-CLI.** SR-OS supports both, syntax differs. If
  one paste-able block doesn't parse, try the other variant — both
  are above.
- **SNMP source-access policy.** SR-OS lets you restrict which sources
  can SNMP-walk the device. If preflight times out but ICMP works,
  check `show system security snmp` for a source-access policy that
  excludes the address you're walking from.
- **SR Linux ≠ SR-OS.** The walker targets `tBgpPeerTable` from
  `TIMETRA-BGP-MIB`, which SR Linux doesn't implement. A capture from
  SR Linux will come back with `vendor_table_empty_mib_not_implemented`.
- **Pre-acquisition Alcatel-Lucent gear.** 7705/7750 from before the
  Nokia acquisition runs the same MIB and produces a valid capture.

---

## Maintainer notes (for Colin)

**Closed (2026-06): real-device capture landed.** This lab supported
[issue #57](https://github.com/grafana/network-topology-exporter/issues/57)
— real-device fixtures for the `nokiaTBgpPeerSpec` walker in
`internal/discovery/bgp/bgp_vendor.go`. A Nokia colleague captured a live SR-OS
25.7.R2 (7750 SR): modern SR-OS serves the next-gen `tBgpPeerNgTable`
(`1.3.6.1.4.1.6527.3.1.2.14.4.7`), **not** the legacy `tBgpPeerTable` (`…13.2`,
empty on current releases). The walk is committed as
`captures/r1_nokia_tBgpPeerNgTable.txt` and `nokiaTBgpPeerSpec` is now
`verified: true`. The receipt procedure below is retained for future
re-captures (note: target the `.14.4.7` Ng table, not the legacy `.13.2`).

On receipt:

1. Verify sha256 against the value in the email.
2. Extract: `tar xzf topology-capture-nokia-*.tar.gz`.
3. Read `diagnostics.json`'s verdict.
4. Run redactor: `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/.../r1_1_3_6_1_4_1_6527_3_1_2_13_2.txt` into
   `[]gosnmp.SnmpPDU` literals per `lab/cisco-iol-bgp/README.md` conventions.
   Land as `buildNokiaTBgpPeerRealPDUs` in `internal/discovery/bgp/bgp_v2_test.go`.
6. Confirm the four `vendorTableSpec` fields for `nokiaTBgpPeerSpec`
   (root, colState, colRemoteAs, decodeIndex) match the real capture.
   Update any that diverge.
7. Flip `verified: false` to `true` for `nokiaTBgpPeerSpec`.
8. Update README BGP vendor-coverage table from Experimental → validated.
9. Update `config/example.yaml` BGP block accordingly.
10. If captures came from SR Linux instead of SR-OS, note in the
    closing PR (relates to #46 LLDP gap context).
