# Lab — juniper-jnxbgp

Thanks for running this. We can't validate our Juniper BGP MIB walker
without a real Junos device, so your snmpwalk against a live router is
what gets this code out of "experimental" status.

**About 10 minutes:** 5 to paste an SNMP config on the router, 1 to run
the capture script, then a quick email back with the tarball. The
script self-diagnoses if anything's off and tells you what to fix.

Any Junos platform running BGP works — MX, SRX, EX-routing variants,
whatever you have handy.

## Before you start

On the machine you'll run snmpwalk from (laptop, jump host, wherever):

| Tool | How |
|---|---|
| `snmpwalk` (net-snmp) | Linux: `apt-get install snmp`. macOS: `brew install net-snmp`. |
| `tar` | already there |
| `bash` 3.2+ | already there |
| `gtimeout` (macOS only) | `brew install coreutils` |

And on the router:
- Reachable from wherever snmpwalk runs
- BGP up with at least one established peer (`show bgp summary`)

## On your router

Pick **v2c** (simpler) or **v3** (auth+priv). Paste the matching block,
then `commit`. Reminder that Junos `set` commands stage changes —
nothing actually applies until you commit.

**v2c:**
```
configure
set snmp view all-mibs oid 1.3.6.1.4.1.2636 include
set snmp view all-mibs oid 1.3.6.1.2.1 include
set snmp community public authorization read-only
set snmp community public view all-mibs
commit
```

**v3 (authPriv, SHA + AES):**
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

If management lives in a non-default routing-instance (the common MX
case is `mgmt_junos`), also:
```
set snmp routing-instance mgmt_junos
commit
```

## Run the capture

From this directory:

```bash
./colleague-capture.sh -h <router-ip> -c public
# or v3:
./colleague-capture.sh -h <router-ip> -V 3 -u monitor -a SHA -A 'authpw' -x AES -X 'privpw'
```

Want to see what it would do before sending any SNMP traffic?

```bash
./colleague-capture.sh -h <router-ip> -c public --dry-run
./colleague-capture.sh -h <router-ip> -c public --preflight-only
```

When it finishes it prints a coloured banner with a verdict. **Green
means good to send.** Yellow or red means the banner spells out exactly
what to try next.

## Send the tarball back

You'll get a file like `topology-capture-juniper-<host>-<timestamp>.tar.gz`,
usually 5-50 KB. Inside:

- raw `snmpwalk` output (one file per OID)
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — execution log, passwords masked
- `SHA256SUMS` — hashes of the above

**Heads up:** this contains real IPs and MACs from your network. Send
it over direct email, not Slack or anything public.

Email to **colin.wood@grafana.com** with subject:

> Capture for issue #56 — Juniper jnxBgpM2PeerTable

Paste the sha256 from the green banner into the email so we can verify
nothing got mangled in transit.

## If something didn't work

The script picks one verdict and prints it in the banner. Here's what
each one means and what to do:

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | Your SNMP view doesn't include the Juniper enterprise OID | Paste the `set snmp view all-mibs ...` block from the banner, `commit`, re-run |
| `bgp_mib_module_absent` | BGP MIB isn't loaded on this device | Confirm BGP is configured with at least one established peer |
| `snmp_silent_likely_vrf` | Device isn't replying on the routing-instance you're reaching it on | Try `set snmp routing-instance mgmt_junos` (or your mgmt instance) |
| `snmp_auth_failed_*` | Credentials are wrong somewhere | The verdict names the field — user, auth password, priv password, or security level |
| `snmp_reachable_vendor_mismatch` | This device isn't a Juniper | Double-check the IP — wrong host? |

## Junos-specific gotchas

- **Routing-instance for SNMP.** MX commonly puts mgmt in `mgmt_junos`,
  and SNMP doesn't automatically follow management traffic. Has to be
  declared explicitly: `set snmp routing-instance mgmt_junos`.
- **`commit` matters.** If a paste-able fix didn't take effect, the
  first thing to check is whether you actually ran `commit`.
- **Peer table empty but route table has rows.** Uncommon but a strong
  tell that there's a view restriction specifically on the peer table
  OID. The wrapper walks both tables so we'll spot this on receipt.

---

## Maintainer notes (for Colin)

This lab closes [issue #56](https://github.com/colinedwardwood/network-topology-exporter/issues/56)
— real-device fixtures for the `juniperJnxBgpM2PeerSpec` walker in
`internal/discovery/bgp/bgp_vendor.go` (`jnxBgpM2PeerTable`,
`1.3.6.1.4.1.2636.5.1.1.2.1.1`). Walker ships with `verified: false`
until this lands.

On receipt:

1. Verify sha256 against the value in the email.
2. Extract: `tar xzf topology-capture-juniper-*.tar.gz`.
3. Read `diagnostics.json`'s verdict.
4. Run redactor: `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/.../r1_1_3_6_1_4_1_2636_5_1_1_2_1_1.txt` into
   `[]gosnmp.SnmpPDU` literals per `lab/cisco-iol-bgp/README.md` conventions.
   Land as `buildJuniperJnxBgpM2RealPDUs` in `internal/discovery/bgp/bgp_v2_test.go`
   (or a new `bgp_v2_juniper_test.go` matching the IOS-XE scaffold pattern).
6. Confirm the four `vendorTableSpec` fields for `juniperJnxBgpM2PeerSpec`
   (root, colState, colRemoteAs, decodeIndex) match the real capture.
   Update any that diverge.
7. Flip `verified: false` to `true` for `juniperJnxBgpM2PeerSpec`.
8. Update README BGP vendor-coverage table from Experimental → validated.
9. Update `config/example.yaml` BGP block accordingly.

**Cross-validation bonus.** The wrapper also walks
`jnxBgpM2RouteTable` (`1.3.6.1.4.1.2636.5.1.1.2.6`) for peer-keyed
index-encoding cross-validation. If the peer table is empty but the
route table has rows, that's a view restriction specifically on the
peer OID — worth flagging in the PR.
