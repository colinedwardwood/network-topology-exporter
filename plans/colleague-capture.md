# Plan: snmpwalk Capture Handoff for Vendor BGP Labs

**Status:** Proposed
**Author:** colinedwardwood
**Created:** 2026-05-26
**Estimate:** ~2 days foundation, then half a day per vendor lab
**Risk:** Low — net-new tooling under `scripts/` and `lab/`; production code paths untouched
**Related:** #56 (Juniper), #57 (Nokia), #58 (Cisco IOS-XE)

## Problem

Issues #56, #57, and #58 all need a real-device snmpwalk against vendor hardware we don't have in-house — Juniper jnxBgpM2PeerTable, Nokia tBgpPeerTable, and Cisco IOS-XE cbgpPeer2Table respectively. The pattern is: find a colleague who has the gear, ask them to run snmpwalk, get a tarball back, redact, convert to fixtures.

In practice this goes three to five round-trips per device. `No Such Object` could mean any of five different things and the colleague can't tell which (MIB not implemented, view restriction, BGP MIB module absent, no peers established, wrong device entirely). SNMPv3 password mistakes look identical to a dead device — wrong priv password produces silence, not an error string. Captures contain real network identifiers (IPs, MACs, and link-local IPv6 with the device MAC embedded as EUI-64) that we can't review at a glance let alone commit to git. At three vendors that's enough churn to push v1.4.0-rc.1.

The plan replaces the ad-hoc "run snmpwalk and email me" flow with a wrapper that does enough self-diagnosis on the first run to either close the loop or name the specific router-side fix to try before re-running. A separate receipt-side redactor handles sanitization.

## Goals and non-goals

Goal: a colleague with snmpwalk on a Linux or Mac shell runs one command and gets back a tarball that either closes the issue or tells us (and them) exactly what's wrong. Adding a fourth vendor in future is a `vendor.conf` and a README, not new code.

Non-goal: shipping anything as a release artefact. Bash and Python, no Go binary, no daemon, no upload mechanism — the colleague produces a file and sends it through whatever channel they already use, which in practice is email. Receipt-side conversion from text to `[]gosnmp.SnmpPDU` literals stays a manual review step.

## Acceptance criteria

| # | Criterion |
|---|---|
| AC1 | One command per vendor lab dir produces `topology-capture-<vendor>-<host>-<ts>.tar.gz` containing `captures/`, `diagnostics.json`, `wrapper.log`, `SHA256SUMS`, `SEND-ME-THIS.txt` |
| AC2 | Both `-v 2c` and `-v 3 authPriv` work on the same flag surface |
| AC3 | Wrong v3 auth password produces verdict `snmp_auth_failed_authpass`, not a timeout |
| AC4 | Wrong v3 priv password produces `snmp_auth_failed_privpass`, distinguished from a dead device by an authNoPriv fallback probe |
| AC5 | Wrong v3 username produces `snmp_auth_failed_user` (matches `Unknown user name` in stderr) |
| AC6 | View restriction or security-level mismatch at preflight produces `snmp_auth_failed_security_level` (matches `authorizationError` in stderr) |
| AC7 | Empty vendor table + BGP MIB present + RFC 4273 has rows → `vendor_table_empty_view_restriction_likely`, with the paste-able fix command in the banner |
| AC8 | Wrong vendor → yellow banner; tarball produced but no "send this" prompt |
| AC9 | Per-OID PDU cap (50k default) kills runaway walks |
| AC10 | Total runtime cap (5m default) bounds the worst case and still produces a partial tarball |
| AC11 | Receipt redactor replaces every observed IPv4, IPv6, and MAC with values from RFC documentation ranges |
| AC12 | Redactor leaves netmasks, loopback, multicast, and ASNs alone |
| AC13 | Redactor treats `fe80::/10` as MAC-bearing PII, not structural |
| AC14 | Redactor is idempotent — running twice produces byte-identical output |
| AC15 | Wrapper runs on stock macOS (NET-SNMP 5.6, bash, no GNU coreutils) and modern Linux |

## Architecture

```
scripts/
├── colleague-capture.sh          # the shared wrapper, NEW
└── redact-snmp-capture.py        # receipt-side redactor, NEW

lab/
├── cisco-iosxe-bgp/              # existing scaffold, refresh:
│   ├── README.md                 # rewritten for the colleague flow
│   ├── capture-devnet.sh         # was capture.sh — DevNet path retained as-is
│   ├── colleague-capture.sh      # 3-line shim into scripts/colleague-capture.sh
│   ├── vendor.conf
│   └── captures/
├── juniper-jnxbgp/               # NEW for #56
│   └── ... (same shape)
└── nokia-srbgp/                  # NEW for #57
    └── ... (same shape)
```

The shared wrapper is the only place capture logic lives. Each lab dir is a vendor.conf plus a thin shim that sources it before calling the shared wrapper. vendor.conf is POSIX-shell key/value — no jq or yq required on the colleague's machine.

The receipt redactor runs on tarballs we receive. It produces a sibling `captures-redacted/`; raw captures never enter git.

## Wrapper contract

### Invocation

```bash
# v2c
./colleague-capture.sh -h HOST [-h HOST2 ...] -c COMMUNITY

# v3 (authPriv only — noAuth/authNoPriv add scope without benefit)
./colleague-capture.sh -h HOST -V 3 -u USER -a SHA -A 'authpass' -x AES -X 'privpass'

# Safety affordances
./colleague-capture.sh -h HOST ... --dry-run         # prints what it would run
./colleague-capture.sh -h HOST ... --preflight-only  # auth probe + sysDescr, then exits

# Tunables
--per-oid-timeout 60s
--total-timeout 5m
--retries 1
--per-oid-pdu-cap 50000
```

Multiple `-h` flags are supported so the Cisco Option A (CML two-router) case in #58 can capture both devices in one run.

### Execution

1. Sanity-check tools (`snmpwalk`, `tar`, `sha256sum`/`shasum`, `timeout`/`gtimeout`) and args. Print the install command for any missing tool, exit 3.
2. If `--dry-run`, print the snmpwalk lines we'd run (passwords masked), the parsed vendor.conf, and the host list; exit 4.
3. Buffer environment context for diagnostics: `uname -a`, `snmpwalk --version`, locale, timezone, wrapper git SHA.
4. Per host: ping (`-c 3`, warning only — ICMP is often filtered), then walk sysDescr.0 with a 5s timeout, capturing stderr.
5. If the sysDescr walk failed, match stderr against known fault strings:
    - `Authentication failure` → wrong auth password
    - `Unknown user name` → wrong user
    - `authorizationError` → wrong security level, or view restriction at this scope
    - `Decryption error` → wrong priv password (rare; net-snmp usually doesn't surface this one)

   Net-snmp prefixes some errors with `No log handling enabled - using stderr logging`; strip that before matching.

   The wrong-priv-password case is the nasty one. The client side sees a plain timeout, not "decryption error", because the device silently drops packets it can't decrypt. We disambiguate by probing once at `authNoPriv` with the same auth password — if that succeeds the auth password is fine and the problem is priv. If it also fails, the device is genuinely silent and we fall through to `snmp_silent_likely_vrf`, which prints the vendor-specific VRF hint from vendor.conf.

6. If `--preflight-only`, print the result table and exit 4.
7. Read sysObjectID.0. Match against `EXPECTED_SYSOBJECTID_PREFIX` or against any case-insensitive substring in `SYSDESCR_KEYWORDS`. Either match counts; both failing isn't fatal but the verdict will land on `snmp_reachable_vendor_mismatch`. (The sysObjectID-only match is unreliable: any device running stock net-snmp returns `1.3.6.1.4.1.8072.*` regardless of vendor.)
8. Three-signal BGP probe, in parallel:
    - `bgpVersion` scalar at `1.3.6.1.2.1.15.1.0`
    - RFC 4273 `bgpPeerTable` head at `1.3.6.1.2.1.15.3`
    - vendor table from `VENDOR_TABLE_OID`

   Outcome enum: `ok-rows`, `ok-empty`, `noSuchObject`, `noSuchInstance`, `end-of-mib`, `timeout`, `auth-error`, `other-error`. `end-of-mib` is what snmpwalk emits when it walks past the end of the MIB tree ("No more variables left in this MIB View"); for verdict purposes it's the same as `noSuchObject`.

9. Walk the full OID list from vendor.conf. Each walk wraps `snmpwalk -On -Oe -t 10 -r RETRIES` in `timeout PER_OID_TIMEOUT`, and a line counter kills the snmpwalk process if it exceeds `PER_OID_PDU_CAP` PDUs (catches MIB loops). Outputs go to `captures/<safe-host>/r1_<safe-oid>.txt`.
10. For any primary OID that came back `noSuchObject`, `end-of-mib`, or `ok-empty`, look up `FALLBACK_<safe-oid>` in vendor.conf and walk that. Same timeout/cap rules.
11. Scan captures for IPs and MACs in supported encoding forms (IPv4/v6 typed values, InetAddress-prefixed `.4.<v4>` / `.16.<v6>` in OID indexes, MAC `STRING:` and `Hex-STRING:`) and write `captures/<safe-host>/redaction-targets.json` (byte offset, length, form, real value). Legacy non-InetAddress tables like `ipNetToMediaTable` are out of scope for the scanner — they're not on our OID list anyway.
12. Enforce the total wall-clock timeout. On hit, stop walking, mark `completed: partial-timeout`, proceed to bundle.
13. Flush the wrapper command log to `captures/wrapper.log` with auth/priv passwords replaced by `***`.
14. Write `captures/SHA256SUMS`.
15. Tar to `topology-capture-<vendor>-<host>-<timestamp>.tar.gz`. Timestamped so re-runs never clobber; the working `captures-<ts>/` directory stays on disk for re-run safety.
16. Print the banner.

### Verdict precedence

Highest priority first:

```
snmp_auth_failed_user
snmp_auth_failed_authpass
snmp_auth_failed_privpass
snmp_auth_failed_security_level
snmp_unreachable
snmp_silent_likely_vrf
snmp_reachable_vendor_mismatch   # short-circuits the BGP verdicts below
bgp_mib_module_absent
bgp_up_but_no_peers
vendor_table_empty_view_restriction_likely
vendor_table_empty_mib_not_implemented
capture_ok
inconclusive                      # safety valve
```

The vendor-mismatch case has to short-circuit — otherwise a UDM Pro pointed at by the Juniper lab dir hits both `vendor_mismatch` and `bgp_mib_module_absent`, and the natural reading of "enable the BGP MIB" is worse than useless when the device is wrong in the first place.

### Banner

The banner is the colleague's "did this work" signal. It branches on the verdict:

- `capture_ok` — green box, tarball path, sha256, send instructions.
- `vendor_table_empty_*` / `bgp_*` — green-with-caveat. Tarball is still useful (RFC 4273 captures, sys group), but the banner prints the paste-able router-side fix from `FIX_COMMANDS_<scenario>` and a "try this, then re-run" line.
- `snmp_reachable_vendor_mismatch` — yellow. Tarball produced but the banner does *not* ask the colleague to send it: "this responded but isn't a <VENDOR_DISPLAY_NAME>. sysDescr: <value>. Capture is on disk if you want to keep it."
- `snmp_unreachable` or `snmp_auth_failed_*` — red. No tarball. Names the cause; exits 1.

### Exit codes

| 0 | tarball produced |
| 1 | all hosts failed preflight, no tarball |
| 2 | argument error |
| 3 | missing required local tool |
| 4 | --dry-run or --preflight-only |

### Portability

Stock macOS bash + NET-SNMP 5.6 has to work. That means:

- `#!/usr/bin/env bash`, `set -euo pipefail`, `IFS=$'\n\t'`.
- No `readlink -f`; the `cd "$(dirname "$0")" && pwd` idiom already in `lab/cisco-iol-bgp/capture.sh` works everywhere.
- No GNU-only date formats; `date -u +%Y%m%dT%H%M%SZ` is enough.
- `ping -c 3` only. macOS reads `-W` as milliseconds, not seconds.
- `mktemp -d -t topology-capture.XXXXXX`.
- `shasum -a 256` fallback when `sha256sum` is missing.
- Detect `gtimeout` as the `timeout` fallback on macOS.

Stock macOS ships old enough NET-SNMP that SHA-2 family auth (256/384/512) isn't supported. The README points colleagues at `brew install net-snmp` if they need it.

## diagnostics.json

JSON, versioned. The receiver can grep one field and know the verdict.

```json
{
  "schema_version": 1,
  "wrapper_version": "0.1.0",
  "wrapper_git_sha": "...",
  "vendor_lab": "juniper-jnxbgp",
  "issue_ref": "https://github.com/.../issues/56",
  "captured_at": "<RFC 3339>",
  "duration_seconds": 47.3,
  "completed": "fully | partial-timeout | partial-error | preflight-failed",

  "environment": { "os": "...", "snmpwalk_version": "...", "bash_version": "...", "locale": "...", "timezone": "..." },
  "snmp_config": {
    "version": "3", "user": "...", "auth_proto": "SHA", "priv_proto": "AES",
    "community": null,
    "per_oid_timeout_seconds": 60, "total_timeout_seconds": 300,
    "retries": 1, "per_oid_pdu_cap": 50000
  },

  "hosts": [
    {
      "host": "...",
      "preflight": {
        "icmp": {"outcome": "ok|filtered|fail", "rtt_ms_avg": 0.4},
        "sysDescr_value": "...",
        "sysObjectID_value": "...",
        "expected_sysObjectID_prefix": "...",
        "sysdescr_keywords_matched": ["..."],
        "vendor_match": true,
        "auth_probe": {"outcome": "ok|fault", "stderr_fault": "...|null"}
      },
      "bgp_state_probe": {
        "bgpVersion_outcome": "ok|noSuchObject|...",
        "rfc4273_bgpPeerTable_rows": 0,
        "rfc4273_bgpPeerTable_outcome": "...",
        "vendor_table_rows": 0,
        "vendor_table_outcome": "..."
      },
      "walks": [
        {
          "oid": "...", "label": "...",
          "outcome": "...",
          "rows": 0, "duration_seconds": 0.6, "capture_file": "...",
          "pdu_cap_hit": false, "timed_out": false,
          "expected_empty": false, "fallback_for": null
        }
      ],
      "verdict": {
        "scenario": "...",
        "confidence": "high|medium|low",
        "evidence": ["..."],
        "interpretation": "...",
        "next_action_for_operator": "...",
        "next_action_router_command": "...|null",
        "next_action_router_vendor": "junos|nokia|iosxe|null"
      }
    }
  ],

  "aggregate": {
    "hosts_total": 1, "hosts_preflight_ok": 1,
    "hosts_with_vendor_table_rows": 0,
    "any_walk_timed_out": false, "any_pdu_cap_hit": false
  }
}
```

The `evidence` array on the verdict matters more than the verdict label: it's the specific facts that justified the scenario, so a human reader can spot-check whether the inference is right. Verdicts can be wrong; the raw data is always there.

## Receipt redactor

`scripts/redact-snmp-capture.py`:

```
./redact-snmp-capture.py [--in DIR] [--out DIR] [--map FILE] [--strict]
                         [--keep-loopback] [--keep-multicast]
```

Defaults to `./captures/` → `./captures-redacted/` and reads the wrapper's `redaction-targets.json` for what to substitute.

Redacted: IPv4, IPv6 (including `fe80::/10` — the EUI-64 link-local form embeds the MAC, so keeping it leaks what we just redacted), MACs.

Preserved: ASNs, sysDescr text, sysName, FQDNs, subnet masks (any IPv4 whose 32-bit network-byte-order value matches `1*0*`), `0.0.0.0`, `127.0.0.0/8`, `::`, `::1`, `224.0.0.0/4` and `ff00::/8` multicast.

Substitution pools (sorted, deterministic, fail-loud on exhaustion):

- IPv4: 192.0.2.0/24 → 198.51.100.0/24 → 203.0.113.0/24 (RFC 5737)
- IPv6 global: 2001:db8::/32 (RFC 3849)
- IPv6 link-local: fe80::5e:0:53:0 upward, paired with the redacted MAC of the same device
- MAC: 00:00:5E:00:53:00 upward (RFC 7042)

Algorithm:

1. Load redaction-targets.json, sort real values, allocate substitutes.
2. For each capture file, apply substitutions in decreasing-length order (so 10.1.1.10 wins over 10.1.1.1 when they overlap). Handles three encoding forms: dotted-decimal, hex bytes, OID-octets.
3. Apply the same map recursively to diagnostics.json and wrapper.log.
4. Drop redaction-targets.json from the output (don't ship the bridge).
5. Recompute SHA256SUMS.
6. Strict-pass regex scan over the output. Any IP or MAC outside the documentation ranges → exit 1 (with `--strict`) or warn (without). MAC pattern accepts 1–2 hex chars per octet with `:`, `.`, or `-` separators.
7. Write `.redaction-map.json` (leading dot, gitignored) with the real-to-fake mapping for our own postmortems; banner at the top: "DO NOT COMMIT".
8. Print summary: substitution counts, any sysName/FQDN/email-like strings retained for human review, pool usage.

Idempotency falls out of the sorted allocation — same input, same output.

## vendor.conf

POSIX-shell key/value, sourced by the wrapper:

```bash
VENDOR_NAME="juniper"
VENDOR_DISPLAY_NAME="Juniper / Junos"
ISSUE_REF="https://github.com/.../issues/56"
LAB_DIR_REL="lab/juniper-jnxbgp"

EXPECTED_SYSOBJECTID_PREFIX="1.3.6.1.4.1.2636"
SYSDESCR_KEYWORDS=("juniper" "junos")
VENDOR_TABLE_OID="1.3.6.1.4.1.2636.5.1.1.2.1.1"
VENDOR_TABLE_LABEL="jnxBgpM2PeerTable"

OIDS=(
  "1.3.6.1.2.1.1|sys_group"
  "1.3.6.1.2.1.15.1|rfc4273_bgpVersion_scalars"
  "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable"
  "1.3.6.1.4.1.2636.5.1.1.2.1.1|jnxBgpM2PeerTable"
)

FALLBACK_1_3_6_1_4_1_2636_5_1_1_2_1_1="1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable_fallback"

EXPECTED_EMPTY_OIDS=()

FIX_COMMANDS_vendor_table_empty_view_restriction_likely=$(cat <<'EOF'
set snmp view all-mibs oid 1.3.6.1.4.1.2636 include
set snmp community public view all-mibs
commit
EOF
)

VRF_HINT="Junos may need: set snmp routing-instance <name>"

SEND_RECIPIENT="colin.wood@grafana.com"
SEND_SUBJECT="Capture for issue #56 — Juniper jnxBgpM2PeerTable"
SEND_EXTRA_NOTE="This capture contains REAL IP and MAC addresses from your network. Please send only via direct email."
```

Per-vendor values that differ:

| | #58 Cisco IOS-XE | #56 Juniper | #57 Nokia |
|---|---|---|---|
| `VENDOR_NAME` | `cisco-iosxe` | `juniper` | `nokia` |
| `EXPECTED_SYSOBJECTID_PREFIX` | `1.3.6.1.4.1.9` | `1.3.6.1.4.1.2636` | `1.3.6.1.4.1.6527` |
| `SYSDESCR_KEYWORDS` | cisco, ios | juniper, junos | nokia, sr-os, timos, alcatel |
| `VENDOR_TABLE_OID` | `1.3.6.1.4.1.9.9.187.1.2.5` | `1.3.6.1.4.1.2636.5.1.1.2.1.1` | `1.3.6.1.4.1.6527.3.1.2.13.2` |
| `VENDOR_TABLE_LABEL` | `cbgpPeer2Table` | `jnxBgpM2PeerTable` | `tBgpPeerTable` |
| Bonus walk for cross-validation | `1.3.6.1.4.1.9.9.187.1.2.4` (cbgpPeerTable) | `1.3.6.1.4.1.2636.5.1.1.2.6` (jnxBgpM2RouteTable) | `1.3.6.1.4.1.6527.3.1.2.13.5` (tBgpPeerStatsTable) |

Nokia gets both classic-CLI and MD-CLI variants of the fix commands — SR-OS supports both and operators routinely run mixed.

## Per-vendor README

Same outline in each lab dir:

1. What this lab is for (link to issue, name the walker and OID).
2. Who runs this — explicitly the colleague vs the maintainer.
3. Prerequisites — net-snmp, tar, optionally coreutils on macOS. (No containerlab/Docker/KVM — those belong in the existing lab READMEs.)
4. Five-minute switch-side prep — paste-able SNMP config for v2c and v3, per CLI mode where it differs (Junos / SR-OS classic + MD-CLI / IOS-XE).
5. Run the capture — single command, plus `--dry-run` and `--preflight-only`.
6. What you'll get — tarball contents at a glance.
7. Where to send it.
8. If something didn't work — short table mapping verdict to action.
9. Vendor-specific gotchas (Junos routing-instance, Nokia BOF vs config-mode, IOS-XE engine ID after upgrade).
10. Maintainer notes for receipt-side processing.

Written by hand three times rather than templated. The reuse across vendors is real, but a template would harden the wrong abstraction; the per-vendor differences in sections 4 and 9 are where most of the colleague's confusion actually lives.

## PR sequencing

| PR | Scope | Cost |
|---|---|---|
| **#1 Foundation** | `scripts/colleague-capture.sh`, `scripts/redact-snmp-capture.py`, refresh `lab/cisco-iosxe-bgp/` (rename existing `capture.sh` to `capture-devnet.sh`, add colleague shim + vendor.conf, rewrite README). CI shellcheck + pyflakes targets. | ~2 days |
| **#2 Juniper lab** | `lab/juniper-jnxbgp/` with vendor.conf, shim, README, empty captures dir | ~0.5 day |
| **#3 Nokia lab** | `lab/nokia-srbgp/` analogous to #2 — both classic-CLI and MD-CLI fix commands | ~0.5 day |

Issue closures (#56/#57/#58) happen in follow-up PRs when real-device captures land. Each follow-up adds the redacted capture files, flips `verified: true` in `bgp_vendor.go`, adds a `build*RealPDUs` fixture builder, and updates the README BGP coverage table and `config/example.yaml`.

The acceptance criteria for #56 and #57 say "Anonymise via `internal/snmptest` redaction" — there's no such Go tool. PR 1 amends the issue text to point at `scripts/redact-snmp-capture.py` instead. Python is the right tool for text substitution, and the Go fixtures themselves never need redaction since they're synthesized from the already-redacted text.

## Testing notes

Tested the design against my UDM Pro (10.0.0.1) on 2026-05-26. It's deliberately not a target vendor, which is what made it useful — it exercised the vendor-mismatch and BGP-MIB-absent paths instead of the happy path.

Things that worked first try:

- Preflight order (ICMP → sysDescr → sysObjectID).
- Three-signal BGP probe correctly told `bgp_mib_module_absent` from `vendor_table_empty`.
- v3 happy path with SHA+AES authPriv.
- The `Authentication failure` and `Unknown user name` regexes matched stderr verbatim.
- 50k PDU cap is well-sized. UDM Pro's `ifTable` is 976 rows; production switches with sub-interfaces are in the same ballpark.

Things the testing surfaced that I'd otherwise have shipped wrong:

1. macOS `ping -W` is milliseconds, not seconds. Use `-c 3` only.
2. `No more variables left in this MIB View` is a distinct snmpwalk outcome — different stderr string from `No Such Object`. Classified equivalent for verdict purposes but called out as `end-of-mib` in the outcome enum so we can keep them straight in logs.
3. sysObjectID matching alone is unreliable. UDM Pro returns `1.3.6.1.4.1.8072.*` because it runs stock net-snmp, not Ubiquiti's enterprise OID. Anything Linux-based with off-the-shelf net-snmp will do the same. Added `SYSDESCR_KEYWORDS` substring match as the second signal.
4. UDM Pro hits both `vendor_mismatch` and `bgp_mib_module_absent` — needed an explicit precedence so the verdict says "wrong device" not "enable the BGP MIB."
5. The green "send this" banner is wrong when the device is the wrong vendor. Added the yellow branch.
6. MACs came back as `STRING: aa:bb:cc:dd:ee:ff`, not `Hex-STRING:`, on this snmpwalk. And single-digit hex octets show up (`fa:8:44:55:6b:29`). Redactor regex has to handle both forms and 1–2 hex chars per octet.
7. Wrong v3 priv password is invisible. It produces a timeout, not "Decryption error". The only way to disambiguate is a second probe at authNoPriv. This is the single biggest reason for the wrapper existing.
8. `authorizationError` is what modern snmpwalk emits for security-level mismatches and view restrictions — not "Unsupported security level" as I'd expected. Added to the fault-string set; also have to strip the `No log handling enabled` preamble net-snmp prepends.

Three more for the redactor that the testing pointed at:

- IPv6 link-local can't be in the structural-preserve list. EUI-64 link-locals embed the MAC; preserving them leaks what we just redacted.
- Subnet masks share the IPv4 regex. Preserve any 32-bit value matching `1*0*` in network byte order.
- IP-in-OID scanning has to require an InetAddress length prefix (`.4.` or `.16.`). Without that, `35.10.0.0` in `1.3.6.1.2.1.4.22.1.1.35.10.0.0.2` looks like an IP but isn't — it's an ifIndex catenated with a real IP. Our BGP target tables all use InetAddress so this is safe; legacy tables like ipNetToMediaTable are out of scope.

## Things still to verify

- Hex-STRING MAC encoding (UDM Pro renders STRING: instead). Will be exercised on the first real Juniper or Nokia run.
- `-x AES-256` auto-retry path. The device under test was AES-128.
- Per-OID timeout enforcement against a deliberately slow MIB. AC9 covers this via snmpsim.
- macOS NET-SNMP 5.6 with SHA-2 family auth — likely unsupported. README points at `brew install net-snmp`.
- Less common snmpwalk error paths (engine ID changed, time-window violations) aren't in the fault-string regex. They'll fall through to `inconclusive` until we see one in the wild and name it.
