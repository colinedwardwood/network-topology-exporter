# Lab — cisco-iosxe-bgp

Thanks for running this. We've already validated our Cisco BGP MIB
walker against IOL 17.12.1, but IOS-XE is a different code stream and
the only way to be sure the walker handles it is to walk a real IOS-XE
device. Your capture is what closes that gap.

**About 10 minutes:** 5 to paste an SNMP config on the router, 1 to
run the capture script, then a quick email back with the tarball. The
script self-diagnoses if anything's off and tells you what to fix.

> **Don't have an IOS-XE router?** The bottom of this README has a
> self-serve DevNet path that runs against Cisco's sandbox gear.

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
- BGP up with at least one established peer (`show ip bgp summary`)

## On your router

Pick **v2c** (simpler) or **v3** (auth+priv), paste the matching block,
then save.

**v2c:**
```
configure terminal
snmp-server community public RO
snmp-server view ALL 1.3.6.1.4.1.9.9.187 included
snmp-server community public view ALL RO
end
write memory
```

**v3 (authPriv, SHA + AES):**
```
configure terminal
snmp-server view ALL 1.3.6.1.4.1.9.9.187 included
snmp-server group MONGRP v3 priv read ALL
snmp-server user monitor MONGRP v3 auth sha 'authpw' priv aes 128 'privpw'
end
write memory
```

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

You'll get a file like
`topology-capture-cisco-iosxe-<host>-<timestamp>.tar.gz`, usually 5-50
KB. Inside:

- raw `snmpwalk` output (one file per OID)
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — execution log, passwords masked
- `SHA256SUMS` — hashes of the above
- `SEND-ME-THIS.txt` — short reminder of what to do with the tarball

**Heads up:** this contains real IPs and MACs from your network. Send
it over direct email, not Slack or anything public.

Email to **colin.wood@grafana.com** with subject:

> Capture for issue #58 — Cisco IOS-XE cbgpPeer2Table

Paste the sha256 from the green banner into the email so we can verify
nothing got mangled in transit.

## If something didn't work

The script picks one verdict and prints it in the banner. Here's what
each one means and what to do:

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Cisco enterprise OID | Paste the `snmp-server view` block from the banner, save, re-run |
| `bgp_mib_module_absent` | BGP MIB isn't loaded | Confirm BGP is configured and running |
| `snmp_silent_likely_vrf` | Device isn't replying on this VRF | Try `snmp-server vrf <your-mgmt-vrf>` |
| `snmp_auth_failed_*` | Credentials are wrong somewhere | The verdict names the field |
| `snmp_reachable_vendor_mismatch` | This device isn't a Cisco IOS-XE | Double-check the IP |

## IOS-XE-specific gotchas

- **VRF binding.** If management lives in a non-default VRF, SNMP needs
  to be told explicitly: `snmp-server vrf <name>`.
- **SNMPv3 engine ID after image upgrade.** Newer IOS-XE images can
  regenerate the engine ID, which silently breaks existing v3 users.
  If v3 starts failing after an upgrade, re-create the user.
- **View ordering matters.** `snmp-server view ALL ... included` has to
  exist before `snmp-server community ... view ALL RO` references it.

---

## Alternate: DevNet self-serve

No IOS-XE router on hand? Cisco's DevNet sandbox has reservable IOS-XE
gear that produces a valid capture. The original DevNet-driven flow
lives in `capture-devnet.sh` in this directory — see the inline
comments for the openconnect + CML reservation walkthrough.

---

## Maintainer notes (for Colin)

This lab closes [issue #58](https://github.com/colinedwardwood/network-topology-exporter/issues/58)
— IOS-XE cross-validation for the `ciscoCbgpPeer2Spec` walker in
`internal/discovery/bgp/bgp_vendor.go` (`cbgpPeer2Table`,
`1.3.6.1.4.1.9.9.187.1.2.5`). Column numbers were already validated
against IOL 17.12.1 in `lab/cisco-iol-bgp/`; this lab cross-confirms
on IOS-XE.

On receipt:

1. Verify sha256 against the value in the email (or, if the colleague
   ran the raw `snmpwalk` path instead of the wrapper, just confirm the
   file looks sane — same OID prefix, expected column range).
2. Extract if it's a tarball: `tar xzf topology-capture-cisco-iosxe-*.tar.gz`.
3. Cross-check the capture against the four `ciscoCbgpPeer2Spec` claims
   in `internal/discovery/bgp/bgp_vendor.go`:
   - Table root `1.3.6.1.4.1.9.9.187.1.2.5` on every row
   - Index encoding: IPv4 rows `.1.4.<4 bytes>`, IPv6 rows `.2.16.<16 bytes>`
   - Column 3 (state) returns INTEGER 6 for established peers
   - Column 11 (remoteAs) returns Gauge32 values matching expected AS numbers
4. **Do not commit the capture or convert it to a Go fixture.** The IOL
   fixture in `bgp_v2_test.go` already provides byte-level regression
   protection in CI; an IOS-XE-derived near-duplicate adds maintenance
   cost without coverage. Per the #58 closing PR pattern (2026-05-30):
   record cross-confirmation in the closing PR description as a
   column-match table, add a CHANGELOG entry under Discovery, and update
   the comment block above `ciscoCbgpPeer2Spec` with the validation date.
5. Archive the original capture locally (outside the repo) in case anyone
   ever wants to re-verify.

Different shape for #56 (Juniper) and #57 (Nokia): those walkers have no
IOL-equivalent in CI today, so when those captures arrive they **do**
get redacted and committed as fixtures — see the maintainer notes in
those labs.
