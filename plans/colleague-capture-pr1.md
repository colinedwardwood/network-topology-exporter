# PR 1: Foundation — Colleague-Capture Wrapper + Redactor + IOS-XE Lab Refresh

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the shared `scripts/colleague-capture.sh` wrapper, the `scripts/redact-snmp-capture.py` receipt-side redactor, refresh `lab/cisco-iosxe-bgp/` for the colleague flow, and wire CI lint + test for both languages. This is PR 1 of three; PRs 2 and 3 (Juniper, Nokia labs) reuse this foundation.

**Architecture:** Bash wrapper is a thin entrypoint that sources lib modules in `scripts/colleague-capture-lib/` — each module covers one concern (args, preflight, walks, verdict, bundle, etc.) and is bats-testable in isolation. Python redactor is a single file with class-based handlers per encoding form (dotted, hex, OID-octets), driven by the wrapper's `redaction-targets.json` plus a safety-net regex pass. vendor.conf is POSIX-shell key/value, sourced before the lib modules run.

**Tech Stack:** Bash 3.2+ (POSIX-portable for stock macOS), Python 3.10+, `bats-core` (bash tests), `pytest` (python tests), `shellcheck` (bash lint), `ruff` (python lint), `snmpwalk` (net-snmp). New CI job lints both languages and runs both test suites.

**Source spec:** [`plans/colleague-capture.md`](./colleague-capture.md). The 15 acceptance criteria there are the bar for "done."

---

## File structure

### New files

```
scripts/
├── colleague-capture.sh                       # entrypoint, sources libs, orchestrates
├── colleague-capture-lib/
│   ├── lib_args.sh                            # CLI parsing
│   ├── lib_sanity.sh                          # tool + arg sanity checks
│   ├── lib_vendor_conf.sh                    # source vendor.conf, expose values
│   ├── lib_preflight.sh                       # ICMP + sysDescr probe
│   ├── lib_faults.sh                          # v3 fault-string matcher
│   ├── lib_vendor_match.sh                    # sysObjectID + SYSDESCR_KEYWORDS match
│   ├── lib_bgp_probe.sh                       # three-signal BGP probe
│   ├── lib_walks.sh                           # primary + fallback walks, timeout/cap
│   ├── lib_redact_targets.sh                  # IP/MAC scanner for redaction-targets.json
│   ├── lib_verdict.sh                         # precedence + scenario selection
│   ├── lib_diagnostics.sh                     # diagnostics.json emitter
│   ├── lib_bundle.sh                          # SHA256SUMS + tar.gz
│   ├── lib_banner.sh                          # verdict-aware banner output
│   ├── lib_log.sh                             # sanitized wrapper.log writer
│   └── lib_json.sh                            # bash JSON string-escape helper
├── redact-snmp-capture.py                     # redactor entrypoint (single file)
└── (existing: run-scale-bench.sh)

lab/cisco-iosxe-bgp/
├── README.md                                  # rewritten for colleague flow
├── capture-devnet.sh                          # renamed from existing capture.sh
├── colleague-capture.sh                       # 3-line shim
├── vendor.conf                                # new
└── (existing: captures/, configs/)

tests/scripts/
├── test_lib_args.bats
├── test_lib_sanity.bats
├── test_lib_vendor_conf.bats
├── test_lib_faults.bats
├── test_lib_vendor_match.bats
├── test_lib_verdict.bats
├── test_lib_json.bats
├── test_redact_snmp_capture.py
└── fixtures/
    ├── vendor.conf.example
    ├── capture-with-ips.txt
    ├── capture-with-macs.txt
    ├── capture-with-ipv6.txt
    └── redaction-targets.json
```

### Modified files

- `Makefile` — add `lint-scripts`, `test-scripts`, `test-redactor` targets
- `.github/workflows/ci.yml` — add `scripts` job that runs the above
- `CHANGELOG.md` — Added entry under unreleased
- (issue text on GitHub for #56 + #57 — see Task 24)

---

## Task 1: Add test + lint scaffolding

**Files:**
- Modify: `Makefile`
- Create: `.github/workflows/ci.yml` (modify, add `scripts` job)
- Create: `tests/scripts/fixtures/.gitkeep`

- [ ] **Step 1: Add Makefile targets**

Edit `Makefile`, append before the last target:

```makefile
.PHONY: lint-scripts
lint-scripts: ## Run shellcheck on bash scripts and ruff on python scripts
	shellcheck scripts/*.sh scripts/colleague-capture-lib/*.sh lab/*/colleague-capture.sh lab/*/capture*.sh 2>/dev/null || true
	shellcheck scripts/*.sh scripts/colleague-capture-lib/*.sh
	ruff check scripts/redact-snmp-capture.py

.PHONY: test-scripts
test-scripts: ## Run bats tests for shell libs
	bats tests/scripts/test_lib_*.bats

.PHONY: test-redactor
test-redactor: ## Run pytest for the redactor
	pytest tests/scripts/test_redact_snmp_capture.py -v
```

- [ ] **Step 2: Run `make help` to verify targets show up**

Run:
```bash
make help
```
Expected: see `lint-scripts`, `test-scripts`, `test-redactor` listed.

- [ ] **Step 3: Add a `scripts` job to CI**

Edit `.github/workflows/ci.yml`. Find the existing top-level `jobs:` map and add a new entry alongside `build`/`integration`/etc. Copy this block verbatim, matching the indentation of sibling jobs:

```yaml
  scripts:
    name: scripts lint + tests
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: install shellcheck
        run: sudo apt-get install -y shellcheck
      - name: install bats
        run: |
          sudo apt-get install -y bats
      - name: install python + ruff + pytest
        uses: actions/setup-python@v5
        with:
          python-version: '3.11'
      - name: install python deps
        run: pip install ruff pytest
      - name: shellcheck
        run: shellcheck scripts/*.sh scripts/colleague-capture-lib/*.sh lab/*/colleague-capture.sh
      - name: ruff
        run: ruff check scripts/redact-snmp-capture.py
      - name: bats
        run: bats tests/scripts/test_lib_*.bats
      - name: pytest
        run: pytest tests/scripts/test_redact_snmp_capture.py -v
```

- [ ] **Step 4: Create the fixtures dir**

Run:
```bash
mkdir -p tests/scripts/fixtures
touch tests/scripts/fixtures/.gitkeep
```

- [ ] **Step 5: Commit**

```bash
git add Makefile .github/workflows/ci.yml tests/scripts/fixtures/.gitkeep
git commit -m "chore(scripts): add lint + test scaffolding for colleague-capture toolkit"
```

---

## Task 2: Wrapper entrypoint + lib loader

**Files:**
- Create: `scripts/colleague-capture.sh`
- Create: `scripts/colleague-capture-lib/lib_json.sh`
- Create: `tests/scripts/test_lib_json.bats`

The entrypoint sources the lib modules and runs the main orchestration. We TDD the JSON helper first because every other module depends on it for diagnostics emission.

- [ ] **Step 1: Write failing bats test for json_string()**

Create `tests/scripts/test_lib_json.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_json.sh'
}

@test "json_string escapes basic ASCII unchanged" {
  result="$(json_string 'hello world')"
  [ "$result" = '"hello world"' ]
}

@test "json_string escapes double-quote" {
  result="$(json_string 'he said "hi"')"
  [ "$result" = '"he said \"hi\""' ]
}

@test "json_string escapes backslash" {
  result="$(json_string 'a\b')"
  [ "$result" = '"a\\b"' ]
}

@test "json_string escapes newline" {
  result="$(json_string $'line1\nline2')"
  [ "$result" = '"line1\nline2"' ]
}

@test "json_string handles empty" {
  result="$(json_string '')"
  [ "$result" = '""' ]
}

@test "json_null prints null" {
  result="$(json_null)"
  [ "$result" = 'null' ]
}

@test "json_bool true/false" {
  [ "$(json_bool 1)" = "true" ]
  [ "$(json_bool 0)" = "false" ]
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
bats tests/scripts/test_lib_json.bats
```
Expected: every test fails ("file or directory not found").

- [ ] **Step 3: Implement lib_json.sh**

Create `scripts/colleague-capture-lib/lib_json.sh`:

```bash
#!/usr/bin/env bash
# JSON value helpers for bash. POSIX-portable; no jq dependency.

json_string() {
  # Escape a bash string as a JSON string literal (including quotes).
  local s="$1"
  s="${s//\\/\\\\}"   # backslash first
  s="${s//\"/\\\"}"   # then double-quote
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  printf '"%s"' "$s"
}

json_null() {
  printf 'null'
}

json_bool() {
  if [ "$1" -eq 1 ] 2>/dev/null; then
    printf 'true'
  else
    printf 'false'
  fi
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run:
```bash
bats tests/scripts/test_lib_json.bats
```
Expected: all 7 tests pass.

- [ ] **Step 5: Create the entrypoint script**

Create `scripts/colleague-capture.sh`:

```bash
#!/usr/bin/env bash
# Colleague-driven SNMP capture wrapper.
# See plans/colleague-capture.md for the design and plans/colleague-capture-pr1.md
# for the implementation plan.
set -euo pipefail
IFS=$'\n\t'

WRAPPER_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="${SCRIPT_DIR}/colleague-capture-lib"

# Source lib modules. Order matters: log/json have no deps; everything else
# depends on them. Args is sourced last because it consumes the cmdline.
# shellcheck source=colleague-capture-lib/lib_json.sh
. "${LIB_DIR}/lib_json.sh"

# Subsequent tasks will source more libs here.

main() {
  echo "colleague-capture v${WRAPPER_VERSION}: scaffold only (no behavior yet)" >&2
  exit 0
}

main "$@"
```

Make it executable:
```bash
chmod +x scripts/colleague-capture.sh
```

- [ ] **Step 6: Run the entrypoint to sanity-check it loads**

Run:
```bash
./scripts/colleague-capture.sh
```
Expected: prints `colleague-capture v0.1.0: scaffold only (no behavior yet)` and exits 0.

- [ ] **Step 7: Run shellcheck on the new files**

Run:
```bash
shellcheck scripts/colleague-capture.sh scripts/colleague-capture-lib/lib_json.sh
```
Expected: no warnings.

- [ ] **Step 8: Commit**

```bash
git add scripts/colleague-capture.sh scripts/colleague-capture-lib/lib_json.sh tests/scripts/test_lib_json.bats
git commit -m "feat(scripts): wrapper entrypoint scaffold + lib_json"
```

---

## Task 3: lib_args — CLI parsing

**Files:**
- Create: `scripts/colleague-capture-lib/lib_args.sh`
- Create: `tests/scripts/test_lib_args.bats`

- [ ] **Step 1: Write failing bats test**

Create `tests/scripts/test_lib_args.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_args.sh'
}

@test "parse_args accepts v2c minimal" {
  parse_args -h 10.0.0.1 -c public
  [ "${#HOSTS[@]}" -eq 1 ]
  [ "${HOSTS[0]}" = "10.0.0.1" ]
  [ "$SNMP_VERSION" = "2c" ]
  [ "$COMMUNITY" = "public" ]
}

@test "parse_args accepts multiple hosts" {
  parse_args -h 10.0.0.1 -h 10.0.0.2 -h 10.0.0.3 -c public
  [ "${#HOSTS[@]}" -eq 3 ]
  [ "${HOSTS[2]}" = "10.0.0.3" ]
}

@test "parse_args accepts v3 authPriv" {
  parse_args -h 10.0.0.1 -V 3 -u monitor -a SHA -A authpw -x AES -X privpw
  [ "$SNMP_VERSION" = "3" ]
  [ "$V3_USER" = "monitor" ]
  [ "$V3_AUTH_PROTO" = "SHA" ]
  [ "$V3_AUTH_PASS" = "authpw" ]
  [ "$V3_PRIV_PROTO" = "AES" ]
  [ "$V3_PRIV_PASS" = "privpw" ]
}

@test "parse_args sets dry_run flag" {
  parse_args -h 10.0.0.1 -c public --dry-run
  [ "$DRY_RUN" -eq 1 ]
}

@test "parse_args sets preflight_only flag" {
  parse_args -h 10.0.0.1 -c public --preflight-only
  [ "$PREFLIGHT_ONLY" -eq 1 ]
}

@test "parse_args accepts tunables" {
  parse_args -h 10.0.0.1 -c public --per-oid-timeout 30 --total-timeout 120 --retries 3 --per-oid-pdu-cap 1000
  [ "$PER_OID_TIMEOUT" = "30" ]
  [ "$TOTAL_TIMEOUT" = "120" ]
  [ "$RETRIES" = "3" ]
  [ "$PER_OID_PDU_CAP" = "1000" ]
}

@test "parse_args rejects mixing v2c and v3" {
  run parse_args -h 10.0.0.1 -c public -V 3 -u u -a SHA -A a -x AES -X p
  [ "$status" -ne 0 ]
}

@test "parse_args rejects missing host" {
  run parse_args -c public
  [ "$status" -ne 0 ]
}

@test "parse_args rejects missing auth" {
  run parse_args -h 10.0.0.1
  [ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
bats tests/scripts/test_lib_args.bats
```
Expected: all 9 tests fail.

- [ ] **Step 3: Implement lib_args.sh**

Create `scripts/colleague-capture-lib/lib_args.sh`:

```bash
#!/usr/bin/env bash
# CLI argument parsing. Populates globals consumed by other libs.

# Defaults
HOSTS=()
SNMP_VERSION=""
COMMUNITY=""
V3_USER=""
V3_AUTH_PROTO=""
V3_AUTH_PASS=""
V3_PRIV_PROTO=""
V3_PRIV_PASS=""
DRY_RUN=0
PREFLIGHT_ONLY=0
PER_OID_TIMEOUT="60"
TOTAL_TIMEOUT="300"
RETRIES="1"
PER_OID_PDU_CAP="50000"

_args_die() {
  echo "argument error: $*" >&2
  return 2
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -h) HOSTS+=("$2"); shift 2 ;;
      -c) COMMUNITY="$2"; SNMP_VERSION="2c"; shift 2 ;;
      -V) SNMP_VERSION="$2"; shift 2 ;;
      -u) V3_USER="$2"; shift 2 ;;
      -a) V3_AUTH_PROTO="$2"; shift 2 ;;
      -A) V3_AUTH_PASS="$2"; shift 2 ;;
      -x) V3_PRIV_PROTO="$2"; shift 2 ;;
      -X) V3_PRIV_PASS="$2"; shift 2 ;;
      --dry-run) DRY_RUN=1; shift ;;
      --preflight-only) PREFLIGHT_ONLY=1; shift ;;
      --per-oid-timeout) PER_OID_TIMEOUT="$2"; shift 2 ;;
      --total-timeout) TOTAL_TIMEOUT="$2"; shift 2 ;;
      --retries) RETRIES="$2"; shift 2 ;;
      --per-oid-pdu-cap) PER_OID_PDU_CAP="$2"; shift 2 ;;
      *) _args_die "unknown argument: $1"; return 2 ;;
    esac
  done

  # Validate
  [ "${#HOSTS[@]}" -eq 0 ] && { _args_die "at least one -h HOST is required"; return 2; }

  if [ -n "$COMMUNITY" ] && [ "$SNMP_VERSION" = "3" ]; then
    _args_die "cannot mix -c (v2c) and -V 3 (v3) on the same invocation"; return 2
  fi

  if [ -z "$COMMUNITY" ] && [ "$SNMP_VERSION" != "3" ]; then
    _args_die "must specify either -c COMMUNITY (v2c) or -V 3 ... (v3)"; return 2
  fi

  if [ "$SNMP_VERSION" = "3" ]; then
    [ -z "$V3_USER" ] && { _args_die "v3 requires -u USER"; return 2; }
    [ -z "$V3_AUTH_PROTO" ] && { _args_die "v3 requires -a AUTH_PROTO"; return 2; }
    [ -z "$V3_AUTH_PASS" ] && { _args_die "v3 requires -A AUTH_PASS"; return 2; }
    [ -z "$V3_PRIV_PROTO" ] && { _args_die "v3 requires -x PRIV_PROTO"; return 2; }
    [ -z "$V3_PRIV_PASS" ] && { _args_die "v3 requires -X PRIV_PASS"; return 2; }
  fi

  return 0
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run:
```bash
bats tests/scripts/test_lib_args.bats
```
Expected: all 9 tests pass.

- [ ] **Step 5: Run shellcheck**

Run:
```bash
shellcheck scripts/colleague-capture-lib/lib_args.sh
```
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add scripts/colleague-capture-lib/lib_args.sh tests/scripts/test_lib_args.bats
git commit -m "feat(scripts): lib_args parses CLI for v2c + v3"
```

---

## Task 4: lib_sanity — required tools + arg validation

**Files:**
- Create: `scripts/colleague-capture-lib/lib_sanity.sh`
- Create: `tests/scripts/test_lib_sanity.bats`

- [ ] **Step 1: Write failing bats test**

Create `tests/scripts/test_lib_sanity.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_sanity.sh'
}

@test "require_tool succeeds when tool exists" {
  run require_tool bash
  [ "$status" -eq 0 ]
}

@test "require_tool fails when tool missing" {
  run require_tool definitely-not-a-real-tool-xyz
  [ "$status" -eq 3 ]
}

@test "detect_timeout returns timeout on linux/CI" {
  if command -v timeout >/dev/null 2>&1; then
    result="$(detect_timeout)"
    [ "$result" = "timeout" ]
  else
    skip "no timeout available"
  fi
}

@test "detect_sha256 returns sha256sum or shasum" {
  result="$(detect_sha256)"
  [ "$result" = "sha256sum" ] || [ "$result" = "shasum -a 256" ]
}
```

- [ ] **Step 2: Run to verify failure**

```bash
bats tests/scripts/test_lib_sanity.bats
```
Expected: all 4 tests fail.

- [ ] **Step 3: Implement lib_sanity.sh**

Create `scripts/colleague-capture-lib/lib_sanity.sh`:

```bash
#!/usr/bin/env bash
# Sanity-check the colleague's environment before running anything.

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "error: required tool '${tool}' not found in PATH" >&2
    case "$tool" in
      snmpwalk) echo "  install: apt-get install snmp (linux) or brew install net-snmp (mac)" >&2 ;;
      tar) echo "  install: should be in your base OS" >&2 ;;
      sha256sum|shasum) echo "  install: install coreutils (mac: brew install coreutils)" >&2 ;;
      timeout|gtimeout) echo "  install: install coreutils (mac: brew install coreutils for gtimeout)" >&2 ;;
    esac
    return 3
  fi
  return 0
}

detect_timeout() {
  if command -v timeout >/dev/null 2>&1; then
    echo "timeout"
  elif command -v gtimeout >/dev/null 2>&1; then
    echo "gtimeout"
  else
    return 3
  fi
}

detect_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    echo "shasum -a 256"
  else
    return 3
  fi
}

sanity_all() {
  require_tool snmpwalk || return 3
  require_tool tar || return 3
  detect_timeout >/dev/null || { echo "error: need 'timeout' or 'gtimeout'" >&2; return 3; }
  detect_sha256 >/dev/null || { echo "error: need 'sha256sum' or 'shasum'" >&2; return 3; }
  return 0
}
```

- [ ] **Step 4: Run test to verify pass**

```bash
bats tests/scripts/test_lib_sanity.bats
```
Expected: all 4 pass.

- [ ] **Step 5: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_sanity.sh
git add scripts/colleague-capture-lib/lib_sanity.sh tests/scripts/test_lib_sanity.bats
git commit -m "feat(scripts): lib_sanity tool detection + checks"
```

---

## Task 5: lib_vendor_conf — sourcing and validation

**Files:**
- Create: `scripts/colleague-capture-lib/lib_vendor_conf.sh`
- Create: `tests/scripts/test_lib_vendor_conf.bats`
- Create: `tests/scripts/fixtures/vendor.conf.example`

- [ ] **Step 1: Create the fixture vendor.conf**

Create `tests/scripts/fixtures/vendor.conf.example`:

```bash
VENDOR_NAME="testvendor"
VENDOR_DISPLAY_NAME="Test Vendor"
ISSUE_REF="https://example.com/issues/1"
LAB_DIR_REL="lab/test"

EXPECTED_SYSOBJECTID_PREFIX="1.3.6.1.4.1.99999"
SYSDESCR_KEYWORDS=("testvendor" "tv-os")
VENDOR_TABLE_OID="1.3.6.1.4.1.99999.1.1"
VENDOR_TABLE_LABEL="testVendorPeerTable"

OIDS=(
  "1.3.6.1.2.1.1|sys_group"
  "1.3.6.1.4.1.99999.1.1|testVendorPeerTable"
)

FALLBACK_1_3_6_1_4_1_99999_1_1="1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable_fallback"

EXPECTED_EMPTY_OIDS=()

FIX_COMMANDS_vendor_table_empty_view_restriction_likely="set example view"

VRF_HINT="Test VRF hint"

SEND_RECIPIENT="test@example.com"
SEND_SUBJECT="Test capture"
SEND_EXTRA_NOTE="Test note"
```

- [ ] **Step 2: Write failing test**

Create `tests/scripts/test_lib_vendor_conf.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_vendor_conf.sh'
  FIXTURE_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/fixtures" && pwd)"
}

@test "load_vendor_conf sources the file" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "$VENDOR_NAME" = "testvendor" ]
  [ "$VENDOR_TABLE_OID" = "1.3.6.1.4.1.99999.1.1" ]
}

@test "load_vendor_conf exposes OIDS array" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "${#OIDS[@]}" -eq 2 ]
  [ "${OIDS[0]}" = "1.3.6.1.2.1.1|sys_group" ]
}

@test "load_vendor_conf exposes SYSDESCR_KEYWORDS" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "${#SYSDESCR_KEYWORDS[@]}" -eq 2 ]
  [ "${SYSDESCR_KEYWORDS[1]}" = "tv-os" ]
}

@test "load_vendor_conf fails for missing file" {
  run load_vendor_conf /nonexistent/path/vendor.conf
  [ "$status" -ne 0 ]
}

@test "vendor_conf_required_keys passes for complete fixture" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  run vendor_conf_required_keys
  [ "$status" -eq 0 ]
}

@test "fallback_for returns mapped OID" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  result="$(fallback_for 1.3.6.1.4.1.99999.1.1)"
  [ "$result" = "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable_fallback" ]
}

@test "fallback_for returns empty for OID without fallback" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  result="$(fallback_for 1.3.6.1.2.1.1)"
  [ -z "$result" ]
}
```

- [ ] **Step 3: Run to verify failure**

```bash
bats tests/scripts/test_lib_vendor_conf.bats
```
Expected: all 7 tests fail.

- [ ] **Step 4: Implement lib_vendor_conf.sh**

Create `scripts/colleague-capture-lib/lib_vendor_conf.sh`:

```bash
#!/usr/bin/env bash
# Source a vendor.conf file and validate required keys.

load_vendor_conf() {
  local conf="$1"
  if [ ! -f "$conf" ]; then
    echo "error: vendor.conf not found: $conf" >&2
    return 1
  fi
  # shellcheck disable=SC1090
  . "$conf"
  return 0
}

vendor_conf_required_keys() {
  local missing=()
  [ -z "${VENDOR_NAME:-}" ] && missing+=("VENDOR_NAME")
  [ -z "${VENDOR_DISPLAY_NAME:-}" ] && missing+=("VENDOR_DISPLAY_NAME")
  [ -z "${ISSUE_REF:-}" ] && missing+=("ISSUE_REF")
  [ -z "${EXPECTED_SYSOBJECTID_PREFIX:-}" ] && missing+=("EXPECTED_SYSOBJECTID_PREFIX")
  [ -z "${VENDOR_TABLE_OID:-}" ] && missing+=("VENDOR_TABLE_OID")
  [ -z "${VENDOR_TABLE_LABEL:-}" ] && missing+=("VENDOR_TABLE_LABEL")
  [ -z "${SEND_RECIPIENT:-}" ] && missing+=("SEND_RECIPIENT")
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "error: vendor.conf missing required keys: ${missing[*]}" >&2
    return 1
  fi
  return 0
}

fallback_for() {
  # Given an OID, return the FALLBACK_<safe-oid> value or empty.
  local oid="$1"
  local safe
  safe="$(echo "$oid" | tr '.' '_')"
  local var="FALLBACK_${safe}"
  echo "${!var:-}"
}
```

- [ ] **Step 5: Run test to verify pass**

```bash
bats tests/scripts/test_lib_vendor_conf.bats
```
Expected: all 7 pass.

- [ ] **Step 6: Commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_vendor_conf.sh
git add scripts/colleague-capture-lib/lib_vendor_conf.sh tests/scripts/test_lib_vendor_conf.bats tests/scripts/fixtures/vendor.conf.example
git commit -m "feat(scripts): lib_vendor_conf loads + validates vendor configs"
```

---

## Task 6: lib_faults — v3 stderr fault-string matcher

**Files:**
- Create: `scripts/colleague-capture-lib/lib_faults.sh`
- Create: `tests/scripts/test_lib_faults.bats`

We TDD the fault matcher before the preflight that calls it. The matcher is pure-string, easy to test, and Task 7 needs it.

- [ ] **Step 1: Write failing bats test**

Create `tests/scripts/test_lib_faults.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_faults.sh'
}

@test "match_fault detects Authentication failure" {
  stderr="No log handling enabled - using stderr logging
snmpwalk: Authentication failure (incorrect password, community or key)"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_authpass" ]
}

@test "match_fault detects Unknown user name" {
  stderr="snmpwalk: Unknown user name"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_user" ]
}

@test "match_fault detects authorizationError" {
  stderr="Error in packet.
Reason: authorizationError (access denied to that object)"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_security_level" ]
}

@test "match_fault detects Decryption error" {
  stderr="snmpwalk: Decryption error"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_privpass" ]
}

@test "match_fault detects timeout" {
  stderr="Timeout: No Response from 10.0.0.1"
  result="$(match_fault "$stderr")"
  [ "$result" = "timeout" ]
}

@test "match_fault returns empty for unknown stderr" {
  stderr="Something we have never seen before"
  result="$(match_fault "$stderr")"
  [ -z "$result" ]
}

@test "strip_netsnmp_preamble removes log handling line" {
  raw="No log handling enabled - using stderr logging
real error"
  result="$(strip_netsnmp_preamble "$raw")"
  [ "$result" = "real error" ]
}

@test "strip_netsnmp_preamble passes through unchanged when no preamble" {
  raw="real error"
  result="$(strip_netsnmp_preamble "$raw")"
  [ "$result" = "real error" ]
}
```

- [ ] **Step 2: Run to verify failure**

```bash
bats tests/scripts/test_lib_faults.bats
```
Expected: 8 failures.

- [ ] **Step 3: Implement lib_faults.sh**

Create `scripts/colleague-capture-lib/lib_faults.sh`:

```bash
#!/usr/bin/env bash
# Match snmpwalk stderr against known fault strings.

strip_netsnmp_preamble() {
  # net-snmp prefixes some errors with "No log handling enabled - using stderr logging".
  # Remove that exact line (and any leading blank lines after).
  local s="$1"
  s="${s/No log handling enabled - using stderr logging$'\n'/}"
  s="${s/No log handling enabled - using stderr logging/}"
  echo "$s"
}

match_fault() {
  local raw="$1"
  local s
  s="$(strip_netsnmp_preamble "$raw")"

  case "$s" in
    *"Authentication failure"*)   echo "snmp_auth_failed_authpass" ;;
    *"Unknown user name"*)         echo "snmp_auth_failed_user" ;;
    *"authorizationError"*)        echo "snmp_auth_failed_security_level" ;;
    *"Decryption error"*)          echo "snmp_auth_failed_privpass" ;;
    *"Timeout"*)                   echo "timeout" ;;
    *"No Response"*)               echo "timeout" ;;
    *)                             echo "" ;;
  esac
}
```

- [ ] **Step 4: Run to verify pass**

```bash
bats tests/scripts/test_lib_faults.bats
```
Expected: all 8 pass.

- [ ] **Step 5: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_faults.sh
git add scripts/colleague-capture-lib/lib_faults.sh tests/scripts/test_lib_faults.bats
git commit -m "feat(scripts): lib_faults matches snmpwalk stderr to fault scenarios"
```

---

## Task 7: lib_preflight — ICMP + sysDescr + auth probe

**Files:**
- Create: `scripts/colleague-capture-lib/lib_preflight.sh`

Preflight is hard to unit-test because it makes real network calls. We rely on shellcheck + the integration smoke (Task 25) for correctness, and only unit-test the OS-detection helper.

- [ ] **Step 1: Write bats test for ping flag detection**

Append to `tests/scripts/test_lib_sanity.bats`:

```bash
@test "ping_flags returns -c only on darwin and -c 3 -W 3 on linux" {
  load '../../scripts/colleague-capture-lib/lib_preflight.sh'
  result="$(ping_flags)"
  case "$(uname)" in
    Darwin) [ "$result" = "-c 3" ] ;;
    Linux)  [ "$result" = "-c 3 -W 3" ] ;;
    *)      [ -n "$result" ] ;;
  esac
}
```

- [ ] **Step 2: Implement lib_preflight.sh**

Create `scripts/colleague-capture-lib/lib_preflight.sh`:

```bash
#!/usr/bin/env bash
# Per-host preflight: ICMP + sysDescr + sysObjectID.

# Returns the right ping flags for this OS. Critically: no -W on macOS, where
# it would be interpreted as milliseconds.
ping_flags() {
  case "$(uname)" in
    Darwin) echo "-c 3" ;;
    *)      echo "-c 3 -W 3" ;;
  esac
}

# probe_icmp HOST  → echoes "ok|filtered|fail"
probe_icmp() {
  local host="$1"
  local flags
  # shellcheck disable=SC2086
  read -r -a flags <<< "$(ping_flags)"
  if ping "${flags[@]}" "$host" >/dev/null 2>&1; then
    echo "ok"
  else
    echo "filtered"  # we can't distinguish fail-vs-filtered; assume filtered
  fi
}

# build_snmp_args  → emits the v2c/v3 arg array for snmpwalk on stdout, one per line
build_snmp_args() {
  local override_level="${1:-}"  # optional: authNoPriv for fallback probe
  if [ "$SNMP_VERSION" = "2c" ]; then
    printf -- '-v\n2c\n-c\n%s\n' "$COMMUNITY"
  else
    local level="${override_level:-authPriv}"
    if [ "$level" = "authNoPriv" ]; then
      printf -- '-v\n3\n-l\nauthNoPriv\n-u\n%s\n-a\n%s\n-A\n%s\n' \
        "$V3_USER" "$V3_AUTH_PROTO" "$V3_AUTH_PASS"
    else
      printf -- '-v\n3\n-l\nauthPriv\n-u\n%s\n-a\n%s\n-A\n%s\n-x\n%s\n-X\n%s\n' \
        "$V3_USER" "$V3_AUTH_PROTO" "$V3_AUTH_PASS" "$V3_PRIV_PROTO" "$V3_PRIV_PASS"
    fi
  fi
}

# probe_sysdescr HOST → captures stderr, sets PREFLIGHT_STDOUT/STDERR/EXIT
probe_sysdescr() {
  local host="$1"
  local -a args
  mapfile -t args < <(build_snmp_args)
  PREFLIGHT_STDOUT="$(snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.1.0 2>/tmp/cc-preflight-err.$$)"
  PREFLIGHT_EXIT=$?
  PREFLIGHT_STDERR="$(cat /tmp/cc-preflight-err.$$ 2>/dev/null || echo "")"
  rm -f /tmp/cc-preflight-err.$$
  return $PREFLIGHT_EXIT
}

# probe_sysobjectid HOST → echoes the OID value or empty on failure
probe_sysobjectid() {
  local host="$1"
  local -a args
  mapfile -t args < <(build_snmp_args)
  snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.2.0 2>/dev/null \
    | awk -F'OID: ' '/OID:/ {gsub(/^\./, "", $2); print $2; exit}'
}
```

- [ ] **Step 3: Verify the bats test passes**

```bash
bats tests/scripts/test_lib_sanity.bats
```
Expected: 5 pass (4 prior + 1 new).

- [ ] **Step 4: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_preflight.sh
git add scripts/colleague-capture-lib/lib_preflight.sh tests/scripts/test_lib_sanity.bats
git commit -m "feat(scripts): lib_preflight ICMP + sysDescr + sysObjectID probes"
```

---

## Task 8: Auth probe + wrong-priv-password disambiguation

**Files:**
- Modify: `scripts/colleague-capture-lib/lib_preflight.sh` (append functions)

The disambiguation logic is the headline feature: on timeout under `authPriv`, re-probe at `authNoPriv` with the same auth password. If that succeeds, priv is wrong; if it fails too, the device is silent.

- [ ] **Step 1: Append auth_probe to lib_preflight.sh**

Append to `scripts/colleague-capture-lib/lib_preflight.sh`:

```bash
# auth_probe HOST  → echoes one of:
#   ok
#   snmp_auth_failed_authpass | snmp_auth_failed_user | snmp_auth_failed_security_level |
#   snmp_auth_failed_privpass | snmp_silent_likely_vrf | snmp_unreachable
#
# Uses globals PREFLIGHT_STDERR/EXIT from probe_sysdescr.
auth_probe() {
  local host="$1"
  local icmp_outcome="$2"

  probe_sysdescr "$host"
  if [ "$PREFLIGHT_EXIT" -eq 0 ] && [ -n "$PREFLIGHT_STDOUT" ]; then
    echo "ok"; return 0
  fi

  # match_fault is sourced from lib_faults.sh
  local fault
  fault="$(match_fault "$PREFLIGHT_STDERR")"

  case "$fault" in
    snmp_auth_failed_authpass|snmp_auth_failed_user|snmp_auth_failed_security_level|snmp_auth_failed_privpass)
      echo "$fault"; return 0 ;;
    timeout)
      # Disambiguation: if v3 authPriv timed out and ICMP was OK, re-probe at authNoPriv.
      if [ "$SNMP_VERSION" = "3" ] && [ "$icmp_outcome" = "ok" ]; then
        local -a args
        mapfile -t args < <(build_snmp_args authNoPriv)
        if snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.1.0 >/dev/null 2>&1; then
          # authNoPriv succeeds → priv pw is wrong
          echo "snmp_auth_failed_privpass"; return 0
        fi
        # both timed out → device is silent (likely VRF or just down)
        if [ "$icmp_outcome" = "ok" ]; then
          echo "snmp_silent_likely_vrf"; return 0
        fi
      fi
      if [ "$icmp_outcome" = "ok" ]; then
        echo "snmp_silent_likely_vrf"; return 0
      fi
      echo "snmp_unreachable"; return 0 ;;
    *)
      echo "snmp_unreachable"; return 0 ;;
  esac
}
```

- [ ] **Step 2: Shellcheck**

```bash
shellcheck scripts/colleague-capture-lib/lib_preflight.sh
```
Expected: no warnings (the `lib_faults.sh` source is implicit via the main script's source order; shellcheck may warn — add `# shellcheck disable=SC2034` only if needed).

- [ ] **Step 3: Commit**

```bash
git add scripts/colleague-capture-lib/lib_preflight.sh
git commit -m "feat(scripts): auth_probe with wrong-priv-password disambiguation"
```

---

## Task 9: lib_vendor_match — sysObjectID + SYSDESCR_KEYWORDS

**Files:**
- Create: `scripts/colleague-capture-lib/lib_vendor_match.sh`
- Create: `tests/scripts/test_lib_vendor_match.bats`

- [ ] **Step 1: Write failing test**

Create `tests/scripts/test_lib_vendor_match.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_vendor_match.sh'
  EXPECTED_SYSOBJECTID_PREFIX="1.3.6.1.4.1.2636"
  SYSDESCR_KEYWORDS=("juniper" "junos")
}

@test "vendor_match prefix hit" {
  result="$(vendor_match "1.3.6.1.4.1.2636.1.1.1.2.57" "irrelevant")"
  [ "$result" = "match:sysObjectID" ]
}

@test "vendor_match keyword hit on sysDescr" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Juniper Networks, Inc. mx240")"
  [ "$result" = "match:sysDescr" ]
}

@test "vendor_match keyword case-insensitive" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Some JUNOS system")"
  [ "$result" = "match:sysDescr" ]
}

@test "vendor_match miss on both" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Ubiquiti UniFi UDM-Pro")"
  [ "$result" = "miss" ]
}
```

- [ ] **Step 2: Run to verify failure**

```bash
bats tests/scripts/test_lib_vendor_match.bats
```
Expected: 4 failures.

- [ ] **Step 3: Implement lib_vendor_match.sh**

Create `scripts/colleague-capture-lib/lib_vendor_match.sh`:

```bash
#!/usr/bin/env bash
# Match a device against vendor.conf's identifiers.
# Reads globals: EXPECTED_SYSOBJECTID_PREFIX, SYSDESCR_KEYWORDS[]

vendor_match() {
  local sysobjectid="$1"
  local sysdescr="$2"

  # 1. sysObjectID prefix
  if [ -n "${EXPECTED_SYSOBJECTID_PREFIX:-}" ]; then
    case "$sysobjectid" in
      "${EXPECTED_SYSOBJECTID_PREFIX}"*) echo "match:sysObjectID"; return 0 ;;
    esac
  fi

  # 2. SYSDESCR_KEYWORDS substring, case-insensitive
  if [ -n "${SYSDESCR_KEYWORDS[*]:-}" ]; then
    local descr_lc
    descr_lc="$(echo "$sysdescr" | tr '[:upper:]' '[:lower:]')"
    local kw kw_lc
    for kw in "${SYSDESCR_KEYWORDS[@]}"; do
      kw_lc="$(echo "$kw" | tr '[:upper:]' '[:lower:]')"
      case "$descr_lc" in
        *"${kw_lc}"*) echo "match:sysDescr"; return 0 ;;
      esac
    done
  fi

  echo "miss"
  return 0
}
```

- [ ] **Step 4: Run to verify pass**

```bash
bats tests/scripts/test_lib_vendor_match.bats
```
Expected: all 4 pass.

- [ ] **Step 5: Commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_vendor_match.sh
git add scripts/colleague-capture-lib/lib_vendor_match.sh tests/scripts/test_lib_vendor_match.bats
git commit -m "feat(scripts): lib_vendor_match sysObjectID + SYSDESCR_KEYWORDS"
```

---

## Task 10: lib_walks — primary walk with timeout + PDU cap

**Files:**
- Create: `scripts/colleague-capture-lib/lib_walks.sh`

Walks make real network calls, so we don't bats-test them. We rely on shellcheck + integration smoke. The structure is small enough that careful eyeballing covers it.

- [ ] **Step 1: Create lib_walks.sh**

Create `scripts/colleague-capture-lib/lib_walks.sh`:

```bash
#!/usr/bin/env bash
# Walk a single OID with timeout + PDU cap. Classify the outcome.

# Globals expected:
#   SNMP_VERSION, COMMUNITY, V3_*, PER_OID_TIMEOUT, PER_OID_PDU_CAP, RETRIES
#   TIMEOUT_CMD (set by sanity_all → detect_timeout)

# do_walk HOST OID OUTFILE → echoes outcome|rows
#   outcome: ok-rows | ok-empty | noSuchObject | noSuchInstance |
#            end-of-mib | timeout | auth-error | other-error
do_walk() {
  local host="$1" oid="$2" outfile="$3"
  local -a snmp_args
  mapfile -t snmp_args < <(build_snmp_args)

  local raw err
  local start end duration
  start=$(date +%s)
  # PDU cap via head -n; snmpwalk receives SIGPIPE and stops.
  set +e
  raw="$($TIMEOUT_CMD "$PER_OID_TIMEOUT" snmpwalk "${snmp_args[@]}" \
            -On -Oe -t 10 -r "$RETRIES" "$host" "$oid" 2>/tmp/cc-walk-err.$$ \
            | head -n "$PER_OID_PDU_CAP")"
  local rc=$?
  set -e
  err="$(cat /tmp/cc-walk-err.$$ 2>/dev/null || echo "")"
  rm -f /tmp/cc-walk-err.$$
  end=$(date +%s)
  duration=$((end - start))

  # Write file (header + raw)
  {
    echo "# captured at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# host: ${host}"
    echo "# oid:  ${oid}"
    echo "# wrapper: colleague-capture v${WRAPPER_VERSION}"
    echo
    echo "$raw"
  } > "$outfile"

  # Classify
  local rows
  rows="$(echo "$raw" | grep -c '^\.' || true)"
  local outcome
  if [ "$rc" -eq 124 ]; then
    outcome="timeout"
  elif echo "$err" | grep -q -i "authorizationError\|Authentication failure\|Unknown user name\|Decryption error"; then
    outcome="auth-error"
  elif echo "$raw" | grep -q "No Such Object"; then
    outcome="noSuchObject"
  elif echo "$raw" | grep -q "No Such Instance"; then
    outcome="noSuchInstance"
  elif echo "$raw" | grep -q "No more variables left in this MIB View"; then
    outcome="end-of-mib"
  elif [ "$rows" -eq 0 ]; then
    outcome="ok-empty"
  else
    outcome="ok-rows"
  fi

  echo "${outcome}|${rows}|${duration}"
}

# walk_all HOST CAPTURES_DIR → walks every OID in $OIDS into <dir>/r1_<safe>.txt
#   Returns 0; per-walk results are written to a global WALK_RESULTS array
#   (one element per walk: "oid|label|outcome|rows|duration|outfile").
walk_all() {
  local host="$1" dir="$2"
  WALK_RESULTS=()
  local entry oid label safe outfile result
  for entry in "${OIDS[@]}"; do
    oid="${entry%%|*}"
    label="${entry#*|}"
    safe="$(echo "$oid" | tr '.' '_')"
    outfile="${dir}/r1_${safe}.txt"
    result="$(do_walk "$host" "$oid" "$outfile")"
    WALK_RESULTS+=("${oid}|${label}|${result}|${outfile}|primary")
  done
}
```

- [ ] **Step 2: Shellcheck**

```bash
shellcheck scripts/colleague-capture-lib/lib_walks.sh
```

- [ ] **Step 3: Commit**

```bash
git add scripts/colleague-capture-lib/lib_walks.sh
git commit -m "feat(scripts): lib_walks with per-OID timeout + PDU cap"
```

---

## Task 11: lib_bgp_probe — three-signal probe

**Files:**
- Create: `scripts/colleague-capture-lib/lib_bgp_probe.sh`

- [ ] **Step 1: Create lib_bgp_probe.sh**

Create `scripts/colleague-capture-lib/lib_bgp_probe.sh`:

```bash
#!/usr/bin/env bash
# Three-signal BGP state probe: bgpVersion + RFC 4273 + vendor table.

# Globals expected: VENDOR_TABLE_OID, $TIMEOUT_CMD, SNMP creds
# Sets globals:
#   BGP_VERSION_OUTCOME / _VALUE
#   RFC4273_OUTCOME / _ROWS
#   VENDOR_TABLE_OUTCOME / _ROWS

_probe_one() {
  local host="$1" oid="$2"
  local -a snmp_args
  mapfile -t snmp_args < <(build_snmp_args)
  local raw err rc rows
  set +e
  raw="$($TIMEOUT_CMD 10 snmpwalk "${snmp_args[@]}" -On -Oe -t 5 -r 0 "$host" "$oid" 2>/tmp/cc-probe-err.$$)"
  rc=$?
  set -e
  err="$(cat /tmp/cc-probe-err.$$ 2>/dev/null || echo "")"
  rm -f /tmp/cc-probe-err.$$

  rows="$(echo "$raw" | grep -c '^\.' || true)"

  if [ "$rc" -eq 124 ]; then
    echo "timeout|0|"; return 0
  fi
  if echo "$err" | grep -q -i "authorizationError\|Authentication failure"; then
    echo "auth-error|0|"; return 0
  fi
  if echo "$raw" | grep -q "No Such Object"; then
    echo "noSuchObject|0|"; return 0
  fi
  if echo "$raw" | grep -q "No more variables left in this MIB View"; then
    echo "end-of-mib|0|"; return 0
  fi
  if [ "$rows" -eq 0 ]; then
    echo "ok-empty|0|"; return 0
  fi
  # Extract first value for scalars (used by bgpVersion)
  local first_value
  first_value="$(echo "$raw" | head -1 | awk -F'= ' '{print $2}')"
  echo "ok-rows|${rows}|${first_value}"
}

bgp_three_signal_probe() {
  local host="$1"
  local r

  r="$(_probe_one "$host" 1.3.6.1.2.1.15.1.0)"
  BGP_VERSION_OUTCOME="${r%%|*}"
  BGP_VERSION_VALUE="${r##*|}"

  r="$(_probe_one "$host" 1.3.6.1.2.1.15.3)"
  RFC4273_OUTCOME="${r%%|*}"
  RFC4273_ROWS="$(echo "$r" | cut -d'|' -f2)"

  r="$(_probe_one "$host" "$VENDOR_TABLE_OID")"
  VENDOR_TABLE_OUTCOME="${r%%|*}"
  VENDOR_TABLE_ROWS="$(echo "$r" | cut -d'|' -f2)"
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_bgp_probe.sh
git add scripts/colleague-capture-lib/lib_bgp_probe.sh
git commit -m "feat(scripts): lib_bgp_probe three-signal BGP state probe"
```

---

## Task 12: Fallback walks

**Files:**
- Modify: `scripts/colleague-capture-lib/lib_walks.sh` (append `walk_fallbacks`)

- [ ] **Step 1: Append walk_fallbacks function**

Append to `scripts/colleague-capture-lib/lib_walks.sh`:

```bash
# walk_fallbacks HOST CAPTURES_DIR
#   For each WALK_RESULTS entry classified noSuchObject|end-of-mib|ok-empty,
#   look up FALLBACK_<safe-oid> in vendor.conf and walk it. Appended to
#   WALK_RESULTS with role "fallback".
walk_fallbacks() {
  local host="$1" dir="$2"
  local -a fallbacks_to_run=()  # collect first; walking inside the loop while iterating WALK_RESULTS is brittle

  local entry oid outcome fb_entry fb_oid fb_label safe outfile
  for entry in "${WALK_RESULTS[@]}"; do
    oid="$(echo "$entry" | cut -d'|' -f1)"
    outcome="$(echo "$entry" | cut -d'|' -f3)"
    case "$outcome" in
      noSuchObject|end-of-mib|ok-empty)
        fb_entry="$(fallback_for "$oid")"
        if [ -n "$fb_entry" ]; then
          fallbacks_to_run+=("${oid}|${fb_entry}")
        fi
        ;;
    esac
  done

  local primary
  for primary in "${fallbacks_to_run[@]}"; do
    local primary_oid="${primary%%|*}"
    local rest="${primary#*|}"
    fb_oid="${rest%%|*}"
    fb_label="${rest#*|}"
    safe="$(echo "$fb_oid" | tr '.' '_')"
    outfile="${dir}/r1_${safe}.txt"
    local result
    result="$(do_walk "$host" "$fb_oid" "$outfile")"
    WALK_RESULTS+=("${fb_oid}|${fb_label}|${result}|${outfile}|fallback-for:${primary_oid}")
  done
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_walks.sh
git add scripts/colleague-capture-lib/lib_walks.sh
git commit -m "feat(scripts): walk_fallbacks for noSuchObject / end-of-mib / empty primaries"
```

---

## Task 13: lib_verdict — precedence + scenario selection

**Files:**
- Create: `scripts/colleague-capture-lib/lib_verdict.sh`
- Create: `tests/scripts/test_lib_verdict.bats`

The verdict logic is pure deduction from collected facts — fully unit-testable.

- [ ] **Step 1: Write failing test**

Create `tests/scripts/test_lib_verdict.bats`:

```bash
#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_verdict.sh'
}

@test "verdict capture_ok when vendor table has rows" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok-rows" "5" "ok-rows" "ok-rows" "3")"
  [ "$result" = "capture_ok" ]
}

@test "verdict view_restriction when bgp up + vendor empty + rfc4273 rows" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-rows" "ok-empty" "0")"
  [ "$result" = "vendor_table_empty_view_restriction_likely" ]
}

@test "verdict bgp_mib_module_absent when bgpVersion noSuchObject" {
  result="$(pick_verdict "match:sysObjectID" "ok" "noSuchObject" "0" "noSuchObject" "noSuchObject" "0")"
  [ "$result" = "bgp_mib_module_absent" ]
}

@test "verdict bgp_up_but_no_peers" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-empty" "ok-empty" "0")"
  [ "$result" = "bgp_up_but_no_peers" ]
}

@test "verdict vendor_mismatch short-circuits BGP scenarios" {
  result="$(pick_verdict "miss" "ok" "noSuchObject" "0" "noSuchObject" "noSuchObject" "0")"
  [ "$result" = "snmp_reachable_vendor_mismatch" ]
}

@test "verdict auth_failed_user takes precedence" {
  result="$(pick_verdict "miss" "snmp_auth_failed_user" "" "" "" "" "")"
  [ "$result" = "snmp_auth_failed_user" ]
}

@test "verdict snmp_unreachable" {
  result="$(pick_verdict "miss" "snmp_unreachable" "" "" "" "" "")"
  [ "$result" = "snmp_unreachable" ]
}

@test "verdict vendor_table_empty_mib_not_implemented" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-empty" "noSuchObject" "0")"
  [ "$result" = "vendor_table_empty_mib_not_implemented" ]
}
```

- [ ] **Step 2: Run to verify failure**

```bash
bats tests/scripts/test_lib_verdict.bats
```
Expected: 8 failures.

- [ ] **Step 3: Implement lib_verdict.sh**

Create `scripts/colleague-capture-lib/lib_verdict.sh`:

```bash
#!/usr/bin/env bash
# Pick a single verdict scenario from the collected facts.
# Precedence (highest first):
#   snmp_auth_failed_user
#   snmp_auth_failed_authpass
#   snmp_auth_failed_privpass
#   snmp_auth_failed_security_level
#   snmp_unreachable
#   snmp_silent_likely_vrf
#   snmp_reachable_vendor_mismatch  (short-circuits below)
#   bgp_mib_module_absent
#   bgp_up_but_no_peers
#   vendor_table_empty_view_restriction_likely
#   vendor_table_empty_mib_not_implemented
#   capture_ok
#   inconclusive

# pick_verdict VENDOR_MATCH AUTH_OUTCOME BGP_VERSION_OUTCOME BGP_VERSION_VALUE \
#              RFC4273_OUTCOME VENDOR_TABLE_OUTCOME VENDOR_TABLE_ROWS
pick_verdict() {
  local vendor_match="$1"
  local auth_outcome="$2"
  local bgp_version_outcome="$3"
  # bgp_version_value="$4"  # unused for now
  local rfc4273_outcome="$5"
  local vendor_table_outcome="$6"
  local vendor_table_rows="$7"

  # 1-4. Auth failures
  case "$auth_outcome" in
    snmp_auth_failed_user|snmp_auth_failed_authpass|snmp_auth_failed_privpass|snmp_auth_failed_security_level)
      echo "$auth_outcome"; return 0 ;;
  esac

  # 5-6. Unreachable / silent
  case "$auth_outcome" in
    snmp_unreachable) echo "snmp_unreachable"; return 0 ;;
    snmp_silent_likely_vrf) echo "snmp_silent_likely_vrf"; return 0 ;;
  esac

  # 7. Vendor mismatch — short-circuits the BGP-specific verdicts
  if [ "$vendor_match" = "miss" ]; then
    echo "snmp_reachable_vendor_mismatch"; return 0
  fi

  # 8. BGP MIB module absent
  case "$bgp_version_outcome" in
    noSuchObject|noSuchInstance|end-of-mib)
      echo "bgp_mib_module_absent"; return 0 ;;
  esac

  # 9. capture_ok if vendor table has rows
  if [ "$vendor_table_outcome" = "ok-rows" ] && [ "$vendor_table_rows" -gt 0 ]; then
    echo "capture_ok"; return 0
  fi

  # 10. View restriction: BGP MIB present + RFC 4273 has rows + vendor empty
  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-rows" ] && \
     [ "$vendor_table_outcome" = "ok-empty" ]; then
    echo "vendor_table_empty_view_restriction_likely"; return 0
  fi

  # 11. MIB-not-implemented: BGP module present + RFC empty + vendor noSuchObject/end-of-mib
  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-empty" ]; then
    case "$vendor_table_outcome" in
      noSuchObject|end-of-mib)
        echo "vendor_table_empty_mib_not_implemented"; return 0 ;;
    esac
  fi

  # 12. bgp_up_but_no_peers: BGP module present + both tables empty
  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-empty" ] && \
     [ "$vendor_table_outcome" = "ok-empty" ]; then
    echo "bgp_up_but_no_peers"; return 0
  fi

  # 13. Safety valve
  echo "inconclusive"
}
```

- [ ] **Step 4: Run to verify pass**

```bash
bats tests/scripts/test_lib_verdict.bats
```
Expected: all 8 pass.

- [ ] **Step 5: Commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_verdict.sh
git add scripts/colleague-capture-lib/lib_verdict.sh tests/scripts/test_lib_verdict.bats
git commit -m "feat(scripts): lib_verdict precedence + scenario selection"
```

---

## Task 14: lib_redact_targets — IP/MAC scanner

**Files:**
- Create: `scripts/colleague-capture-lib/lib_redact_targets.sh`

This scanner walks every capture file and emits `redaction-targets.json` listing IPs and MACs found, with byte offset, length, encoding form, and real value. The redactor consumes this on receipt. We delegate the scan to a small awk/grep pipeline; the JSON assembly uses `json_string`.

- [ ] **Step 1: Create lib_redact_targets.sh**

Create `scripts/colleague-capture-lib/lib_redact_targets.sh`:

```bash
#!/usr/bin/env bash
# Scan capture files for IPv4, IPv6, and MAC values in supported encoding
# forms. Write a redaction-targets.json the receipt-side redactor consumes.
#
# Supported forms:
#   - IPv4 typed value:   /IpAddress: (\d+\.\d+\.\d+\.\d+)/
#   - IPv4 in OID index:  /\.4\.(\d+)\.(\d+)\.(\d+)\.(\d+)/ (InetAddress len=4)
#   - IPv6 in OID index:  /\.16\.(\d+(?:\.\d+){15})/        (InetAddress len=16)
#   - IPv6 typed value:   /STRING: ([0-9a-f:]+)/i where it parses as IPv6
#   - MAC STRING:         /STRING: ([0-9a-f]{1,2}(:[0-9a-f]{1,2}){5})/i
#   - MAC Hex-STRING:     /Hex-STRING: ([0-9a-f]{2}( [0-9a-f]{2}){5})/i
#
# Output: <captures_dir>/redaction-targets.json
# Schema: { "ipv4": ["a.b.c.d", ...], "ipv6": [...], "mac": [...] }
#   (Byte-offset precision is desirable; this v1 emits unique-values only.
#    The redactor's regex-based substitution handles position correctly.)

scan_for_redaction_targets() {
  local captures_dir="$1"
  local out="${captures_dir}/redaction-targets.json"

  # Collect unique values
  local -a ipv4=() ipv6=() macs=()
  local file
  while IFS= read -r -d '' file; do
    while IFS= read -r ip; do
      [ -n "$ip" ] && ipv4+=("$ip")
    done < <(grep -oE '(IpAddress: )?[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' "$file" \
              | sed 's/IpAddress: //' \
              | awk '/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/' \
              | sort -u)

    while IFS= read -r ip; do
      [ -n "$ip" ] && ipv6+=("$ip")
    done < <(grep -oE 'STRING: [0-9a-fA-F:]+:[0-9a-fA-F:]+' "$file" \
              | awk '{print $2}' \
              | grep ':.*:' \
              | sort -u)

    while IFS= read -r mac; do
      [ -n "$mac" ] && macs+=("$mac")
    done < <(grep -oE 'STRING: [0-9a-fA-F]{1,2}(:[0-9a-fA-F]{1,2}){5}' "$file" \
              | awk '{print $2}' \
              | sort -u)
  done < <(find "$captures_dir" -name 'r1_*.txt' -print0)

  # Emit JSON
  {
    echo "{"
    echo -n '  "ipv4": ['
    local i=0 v
    for v in "${ipv4[@]}"; do
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "],"
    echo -n '  "ipv6": ['
    i=0
    for v in "${ipv6[@]}"; do
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "],"
    echo -n '  "mac":  ['
    i=0
    for v in "${macs[@]}"; do
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "]"
    echo "}"
  } > "$out"
}
```

- [ ] **Step 2: Shellcheck**

```bash
shellcheck scripts/colleague-capture-lib/lib_redact_targets.sh
```

- [ ] **Step 3: Commit**

```bash
git add scripts/colleague-capture-lib/lib_redact_targets.sh
git commit -m "feat(scripts): lib_redact_targets scans captures into redaction-targets.json"
```

---

## Task 15: lib_log — sanitized wrapper.log

**Files:**
- Create: `scripts/colleague-capture-lib/lib_log.sh`

- [ ] **Step 1: Create lib_log.sh**

Create `scripts/colleague-capture-lib/lib_log.sh`:

```bash
#!/usr/bin/env bash
# Append-only execution log. Sanitizes auth/priv passwords before writing.

WRAPPER_LOG=""    # set to <captures-dir>/wrapper.log by main()

log_init() {
  WRAPPER_LOG="$1"
  : > "$WRAPPER_LOG"
}

log_event() {
  local msg="$1"
  # Replace any occurrence of the live passwords with ***. Safe to call even
  # when V3_* are empty.
  if [ -n "${V3_AUTH_PASS:-}" ]; then
    msg="${msg//${V3_AUTH_PASS}/***}"
  fi
  if [ -n "${V3_PRIV_PASS:-}" ]; then
    msg="${msg//${V3_PRIV_PASS}/***}"
  fi
  if [ -n "${COMMUNITY:-}" ]; then
    msg="${msg//${COMMUNITY}/***}"
  fi
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $msg" >> "$WRAPPER_LOG"
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_log.sh
git add scripts/colleague-capture-lib/lib_log.sh
git commit -m "feat(scripts): lib_log writes a sanitized wrapper.log"
```

---

## Task 16: lib_diagnostics — diagnostics.json emitter

**Files:**
- Create: `scripts/colleague-capture-lib/lib_diagnostics.sh`

This is the largest pure-bash JSON writer. It reads the globals set by every prior lib and emits the structured diagnostics.json the spec defines.

- [ ] **Step 1: Create lib_diagnostics.sh**

Create `scripts/colleague-capture-lib/lib_diagnostics.sh`:

```bash
#!/usr/bin/env bash
# Emit diagnostics.json. Reads everything from globals set by the rest of the libs.
# This is intentionally one big function rather than many small ones; bash-JSON
# composition is fiddly and easier to read in one place.

# emit_diagnostics OUTFILE
emit_diagnostics() {
  local outfile="$1"
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  {
    echo '{'
    echo "  \"schema_version\": 1,"
    printf '  "wrapper_version": '; json_string "$WRAPPER_VERSION"; echo ','
    printf '  "wrapper_git_sha": '; json_string "${WRAPPER_GIT_SHA:-unknown}"; echo ','
    printf '  "vendor_lab": '; json_string "$VENDOR_NAME"; echo ','
    printf '  "issue_ref": '; json_string "${ISSUE_REF:-}"; echo ','
    printf '  "captured_at": '; json_string "$now"; echo ','
    printf '  "duration_seconds": %s,\n' "${TOTAL_DURATION:-0}"
    printf '  "completed": '; json_string "${COMPLETED:-fully}"; echo ','

    echo '  "environment": {'
    printf '    "os": '; json_string "$(uname -a)"; echo ','
    printf '    "snmpwalk_version": '; json_string "$(snmpwalk --version 2>&1 | head -1)"; echo ','
    printf '    "bash_version": '; json_string "${BASH_VERSION}"; echo ','
    printf '    "locale": '; json_string "${LC_ALL:-${LANG:-unknown}}"; echo ','
    printf '    "timezone": '; json_string "$(date +%Z)"; echo ''
    echo '  },'

    echo '  "snmp_config": {'
    printf '    "version": '; json_string "$SNMP_VERSION"; echo ','
    if [ "$SNMP_VERSION" = "3" ]; then
      printf '    "user": '; json_string "$V3_USER"; echo ','
      printf '    "auth_proto": '; json_string "$V3_AUTH_PROTO"; echo ','
      printf '    "priv_proto": '; json_string "$V3_PRIV_PROTO"; echo ','
      echo '    "community": null,'
    else
      echo '    "user": null, "auth_proto": null, "priv_proto": null,'
      printf '    "community": '; json_string "$COMMUNITY"; echo ','
    fi
    printf '    "per_oid_timeout_seconds": %s,\n' "$PER_OID_TIMEOUT"
    printf '    "total_timeout_seconds": %s,\n' "$TOTAL_TIMEOUT"
    printf '    "retries": %s,\n' "$RETRIES"
    printf '    "per_oid_pdu_cap": %s\n' "$PER_OID_PDU_CAP"
    echo '  },'

    echo '  "hosts": ['
    local i=0
    local host_entry
    for host_entry in "${HOST_DIAGNOSTICS[@]}"; do
      [ $i -gt 0 ] && echo ','
      echo -n "$host_entry"
      i=$((i+1))
    done
    echo
    echo '  ],'

    echo '  "aggregate": {'
    printf '    "hosts_total": %s,\n' "${#HOSTS[@]}"
    printf '    "hosts_preflight_ok": %s,\n' "${HOSTS_PREFLIGHT_OK:-0}"
    printf '    "hosts_with_vendor_table_rows": %s,\n' "${HOSTS_WITH_VENDOR_ROWS:-0}"
    printf '    "any_walk_timed_out": %s,\n' "$(json_bool "${ANY_WALK_TIMED_OUT:-0}")"
    printf '    "any_pdu_cap_hit": %s\n' "$(json_bool "${ANY_PDU_CAP_HIT:-0}")"
    echo '  }'
    echo '}'
  } > "$outfile"
}

# build_host_diagnostic_entry HOST
#   Returns a JSON object for one host as a string (no trailing comma).
#   Reads per-host globals set during the host's preflight/probe/walks.
build_host_diagnostic_entry() {
  local host="$1"
  {
    echo '    {'
    printf '      "host": '; json_string "$host"; echo ','

    echo '      "preflight": {'
    printf '        "icmp": {"outcome": '; json_string "${HOST_ICMP_OUTCOME:-unknown}"; echo ', "rtt_ms_avg": 0},'
    printf '        "sysDescr_value": '; json_string "${HOST_SYSDESCR:-}"; echo ','
    printf '        "sysObjectID_value": '; json_string "${HOST_SYSOID:-}"; echo ','
    printf '        "expected_sysObjectID_prefix": '; json_string "${EXPECTED_SYSOBJECTID_PREFIX:-}"; echo ','
    printf '        "sysdescr_keywords_matched": [],\n'
    printf '        "vendor_match": %s,\n' "$(json_bool "${HOST_VENDOR_MATCH_BOOL:-0}")"
    printf '        "auth_probe": {"outcome": '; json_string "${HOST_AUTH_OUTCOME:-unknown}"; echo ', "stderr_fault": null}'
    echo '      },'

    echo '      "bgp_state_probe": {'
    printf '        "bgpVersion_outcome": '; json_string "${BGP_VERSION_OUTCOME:-}"; echo ','
    printf '        "rfc4273_bgpPeerTable_rows": %s,\n' "${RFC4273_ROWS:-0}"
    printf '        "rfc4273_bgpPeerTable_outcome": '; json_string "${RFC4273_OUTCOME:-}"; echo ','
    printf '        "vendor_table_rows": %s,\n' "${VENDOR_TABLE_ROWS:-0}"
    printf '        "vendor_table_outcome": '; json_string "${VENDOR_TABLE_OUTCOME:-}"; echo ''
    echo '      },'

    echo '      "walks": ['
    local i=0 entry oid label outcome rows duration outfile role
    for entry in "${WALK_RESULTS[@]}"; do
      oid="$(echo "$entry" | cut -d'|' -f1)"
      label="$(echo "$entry" | cut -d'|' -f2)"
      outcome="$(echo "$entry" | cut -d'|' -f3)"
      rows="$(echo "$entry" | cut -d'|' -f4)"
      duration="$(echo "$entry" | cut -d'|' -f5)"
      outfile="$(echo "$entry" | cut -d'|' -f6)"
      role="$(echo "$entry" | cut -d'|' -f7)"
      [ $i -gt 0 ] && echo ','
      echo -n '        {'
      printf '"oid": '; json_string "$oid"; echo -n ', '
      printf '"label": '; json_string "$label"; echo -n ', '
      printf '"outcome": '; json_string "$outcome"; echo -n ", "
      echo -n "\"rows\": ${rows}, "
      echo -n "\"duration_seconds\": ${duration}, "
      printf '"capture_file": '; json_string "$outfile"; echo -n ', '
      printf '"role": '; json_string "$role"; echo -n '}'
      i=$((i+1))
    done
    echo
    echo '      ],'

    echo '      "verdict": {'
    printf '        "scenario": '; json_string "${HOST_VERDICT_SCENARIO:-inconclusive}"; echo ','
    printf '        "confidence": '; json_string "${HOST_VERDICT_CONFIDENCE:-medium}"; echo ','
    printf '        "interpretation": '; json_string "${HOST_VERDICT_INTERPRETATION:-}"; echo ','
    printf '        "next_action_for_operator": '; json_string "${HOST_VERDICT_NEXT_ACTION:-}"; echo ','
    printf '        "next_action_router_command": '; json_string "${HOST_VERDICT_FIX_CMD:-}"; echo ','
    printf '        "next_action_router_vendor": '; json_string "${VENDOR_NAME:-}"; echo ''
    echo '      }'
    echo '    }'
  }
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_diagnostics.sh
git add scripts/colleague-capture-lib/lib_diagnostics.sh
git commit -m "feat(scripts): lib_diagnostics emits diagnostics.json"
```

---

## Task 17: lib_bundle — SHA256SUMS + tar.gz

**Files:**
- Create: `scripts/colleague-capture-lib/lib_bundle.sh`

- [ ] **Step 1: Create lib_bundle.sh**

Create `scripts/colleague-capture-lib/lib_bundle.sh`:

```bash
#!/usr/bin/env bash
# Compute SHA256SUMS for everything in <captures-dir> and tar it up.

# SHA256_CMD is set by sanity_all (e.g. "sha256sum" or "shasum -a 256")
write_sha256sums() {
  local dir="$1"
  ( cd "$dir" && find . -type f ! -name 'SHA256SUMS' -print0 \
    | xargs -0 $SHA256_CMD ) > "${dir}/SHA256SUMS"
}

# bundle_tarball CAPTURES_DIR VENDOR HOSTS_JOINED → echoes the tarball path
bundle_tarball() {
  local dir="$1" vendor="$2" hosts_joined="$3"
  local ts safe_hosts tarball
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  safe_hosts="$(echo "$hosts_joined" | tr ',' '-' | tr '.' '_' | tr ':' '_')"
  tarball="$(dirname "$dir")/topology-capture-${vendor}-${safe_hosts}-${ts}.tar.gz"
  tar -czf "$tarball" -C "$(dirname "$dir")" "$(basename "$dir")"
  echo "$tarball"
}

tarball_sha256() {
  local tarball="$1"
  $SHA256_CMD "$tarball" | awk '{print $1}'
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_bundle.sh
git add scripts/colleague-capture-lib/lib_bundle.sh
git commit -m "feat(scripts): lib_bundle SHA256SUMS manifest + timestamped tarball"
```

---

## Task 18: lib_banner — verdict-aware output

**Files:**
- Create: `scripts/colleague-capture-lib/lib_banner.sh`

- [ ] **Step 1: Create lib_banner.sh**

Create `scripts/colleague-capture-lib/lib_banner.sh`:

```bash
#!/usr/bin/env bash
# Render the verdict-aware end-of-run banner.
#
# Colors: terminal ANSI. If stdout isn't a TTY, suppress color codes.

_color_enabled() { [ -t 1 ]; }

_g() { _color_enabled && printf '\033[32m'; }    # green
_y() { _color_enabled && printf '\033[33m'; }    # yellow
_r() { _color_enabled && printf '\033[31m'; }    # red
_b() { _color_enabled && printf '\033[1m'; }     # bold
_n() { _color_enabled && printf '\033[0m'; }     # reset

# print_banner VERDICT TARBALL SHA256 RECIPIENT VENDOR_DISPLAY SYSDESCR FIX_COMMAND
print_banner() {
  local verdict="$1" tarball="$2" sha="$3" recipient="$4" \
        vendor_display="$5" sysdescr="$6" fix_cmd="$7"

  case "$verdict" in
    capture_ok)
      _g; _b
      echo
      echo "===================================================================="
      echo "  CAPTURE COMPLETE — please send the file below."
      echo "===================================================================="
      _n
      echo "  Tarball: ${tarball}"
      echo "  sha256:  ${sha}"
      echo "  Send to: ${recipient}"
      ;;
    vendor_table_empty_view_restriction_likely|vendor_table_empty_mib_not_implemented|bgp_mib_module_absent|bgp_up_but_no_peers)
      _g
      echo
      echo "  Capture complete; tarball is useful but the vendor table is empty."
      _n
      echo "  Tarball: ${tarball}"
      echo "  sha256:  ${sha}"
      echo "  Send to: ${recipient}"
      if [ -n "$fix_cmd" ]; then
        _y; echo
        echo "  Suggested fix to try on the router, then re-run this script:"
        _n
        echo
        echo "$fix_cmd" | sed 's/^/    /'
      fi
      ;;
    snmp_reachable_vendor_mismatch)
      _y; _b
      echo
      echo "  This device responded to SNMP but does not look like a ${vendor_display}."
      _n
      echo "  sysDescr: ${sysdescr}"
      echo "  Tarball was written to ${tarball} but you do NOT need to send it"
      echo "  unless ${recipient} specifically asks."
      ;;
    snmp_unreachable|snmp_auth_failed_user|snmp_auth_failed_authpass|snmp_auth_failed_privpass|snmp_auth_failed_security_level|snmp_silent_likely_vrf)
      _r; _b
      echo
      echo "  CAPTURE FAILED: ${verdict}"
      _n
      echo "  No tarball produced. Fix the cause above and re-run."
      ;;
    *)
      _y
      echo
      echo "  Capture complete with inconclusive verdict."
      _n
      echo "  Tarball: ${tarball}"
      echo "  sha256:  ${sha}"
      ;;
  esac
}
```

- [ ] **Step 2: Shellcheck + commit**

```bash
shellcheck scripts/colleague-capture-lib/lib_banner.sh
git add scripts/colleague-capture-lib/lib_banner.sh
git commit -m "feat(scripts): lib_banner verdict-aware output"
```

---

## Task 19: Wire main() in colleague-capture.sh

**Files:**
- Modify: `scripts/colleague-capture.sh` (full rewrite of main())

- [ ] **Step 1: Replace the script body**

Replace the contents of `scripts/colleague-capture.sh` with:

```bash
#!/usr/bin/env bash
# Colleague-driven SNMP capture wrapper. See plans/colleague-capture.md.
set -euo pipefail
IFS=$'\n\t'

WRAPPER_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="${SCRIPT_DIR}/colleague-capture-lib"
WRAPPER_GIT_SHA="$(cd "$SCRIPT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo "unknown")"

# Source order matters.
# shellcheck source=colleague-capture-lib/lib_json.sh
. "${LIB_DIR}/lib_json.sh"
# shellcheck source=colleague-capture-lib/lib_args.sh
. "${LIB_DIR}/lib_args.sh"
# shellcheck source=colleague-capture-lib/lib_sanity.sh
. "${LIB_DIR}/lib_sanity.sh"
# shellcheck source=colleague-capture-lib/lib_vendor_conf.sh
. "${LIB_DIR}/lib_vendor_conf.sh"
# shellcheck source=colleague-capture-lib/lib_faults.sh
. "${LIB_DIR}/lib_faults.sh"
# shellcheck source=colleague-capture-lib/lib_preflight.sh
. "${LIB_DIR}/lib_preflight.sh"
# shellcheck source=colleague-capture-lib/lib_vendor_match.sh
. "${LIB_DIR}/lib_vendor_match.sh"
# shellcheck source=colleague-capture-lib/lib_bgp_probe.sh
. "${LIB_DIR}/lib_bgp_probe.sh"
# shellcheck source=colleague-capture-lib/lib_walks.sh
. "${LIB_DIR}/lib_walks.sh"
# shellcheck source=colleague-capture-lib/lib_redact_targets.sh
. "${LIB_DIR}/lib_redact_targets.sh"
# shellcheck source=colleague-capture-lib/lib_verdict.sh
. "${LIB_DIR}/lib_verdict.sh"
# shellcheck source=colleague-capture-lib/lib_log.sh
. "${LIB_DIR}/lib_log.sh"
# shellcheck source=colleague-capture-lib/lib_diagnostics.sh
. "${LIB_DIR}/lib_diagnostics.sh"
# shellcheck source=colleague-capture-lib/lib_bundle.sh
. "${LIB_DIR}/lib_bundle.sh"
# shellcheck source=colleague-capture-lib/lib_banner.sh
. "${LIB_DIR}/lib_banner.sh"

main() {
  # VENDOR_CONF_PATH is expected to be set by the lab-dir shim. If unset,
  # default to ./vendor.conf in the current directory.
  local vendor_conf="${VENDOR_CONF_PATH:-./vendor.conf}"
  load_vendor_conf "$vendor_conf" || exit 2
  vendor_conf_required_keys || exit 2

  parse_args "$@" || exit 2

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "Would run:"
    local h
    for h in "${HOSTS[@]}"; do
      local -a sa
      mapfile -t sa < <(build_snmp_args)
      # Mask passwords in the printed command
      local masked="${sa[*]}"
      if [ -n "${V3_AUTH_PASS:-}" ]; then masked="${masked//${V3_AUTH_PASS}/***}"; fi
      if [ -n "${V3_PRIV_PASS:-}" ]; then masked="${masked//${V3_PRIV_PASS}/***}"; fi
      if [ -n "${COMMUNITY:-}" ]; then masked="${masked//${COMMUNITY}/***}"; fi
      echo "  snmpwalk ${masked} -On -Oe -t 10 -r ${RETRIES} ${h} <each-OID-from-vendor.conf>"
    done
    exit 4
  fi

  sanity_all || exit 3
  TIMEOUT_CMD="$(detect_timeout)"
  SHA256_CMD="$(detect_sha256)"

  local ts
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  local workdir="captures-${ts}"
  mkdir -p "$workdir"
  log_init "${workdir}/wrapper.log"
  log_event "wrapper start v${WRAPPER_VERSION} sha=${WRAPPER_GIT_SHA}"

  HOST_DIAGNOSTICS=()
  HOSTS_PREFLIGHT_OK=0
  HOSTS_WITH_VENDOR_ROWS=0
  ANY_WALK_TIMED_OUT=0
  ANY_PDU_CAP_HIT=0

  local last_verdict="inconclusive" last_fix_cmd="" last_sysdescr=""
  local started
  started=$(date +%s)

  local host
  for host in "${HOSTS[@]}"; do
    # Total-runtime cap (AC10): if elapsed exceeds TOTAL_TIMEOUT, stop walking
    # remaining hosts and mark the run partial. We still bundle what we have.
    local now elapsed
    now=$(date +%s)
    elapsed=$((now - started))
    if [ "$elapsed" -ge "$TOTAL_TIMEOUT" ]; then
      log_event "total-timeout exceeded ($elapsed >= $TOTAL_TIMEOUT); skipping remaining hosts"
      COMPLETED="partial-timeout"
      ANY_WALK_TIMED_OUT=1
      break
    fi
    log_event "host start: $host"
    local hostdir="${workdir}/$(echo "$host" | tr '.' '_' | tr ':' '_')"
    mkdir -p "$hostdir"

    HOST_ICMP_OUTCOME="$(probe_icmp "$host")"
    HOST_AUTH_OUTCOME="$(auth_probe "$host" "$HOST_ICMP_OUTCOME")"

    if [ "$HOST_AUTH_OUTCOME" != "ok" ]; then
      log_event "host $host: auth_probe → $HOST_AUTH_OUTCOME"
      HOST_VENDOR_MATCH_BOOL=0
      HOST_VERDICT_SCENARIO="$HOST_AUTH_OUTCOME"
      HOST_VERDICT_FIX_CMD=""
      HOST_VERDICT_INTERPRETATION="see verdict scenario"
      HOST_VERDICT_NEXT_ACTION="see verdict scenario"
      HOST_DIAGNOSTICS+=("$(build_host_diagnostic_entry "$host")")
      last_verdict="$HOST_VERDICT_SCENARIO"
      continue
    fi
    HOSTS_PREFLIGHT_OK=$((HOSTS_PREFLIGHT_OK+1))
    HOST_SYSDESCR="$(echo "$PREFLIGHT_STDOUT" | awk -F'STRING: ' '/STRING:/ {print $2; exit}')"
    HOST_SYSOID="$(probe_sysobjectid "$host")"
    last_sysdescr="$HOST_SYSDESCR"

    local match_result
    match_result="$(vendor_match "$HOST_SYSOID" "$HOST_SYSDESCR")"
    if [ "$match_result" != "miss" ]; then
      HOST_VENDOR_MATCH_BOOL=1
    else
      HOST_VENDOR_MATCH_BOOL=0
    fi

    bgp_three_signal_probe "$host"
    walk_all "$host" "$hostdir"
    walk_fallbacks "$host" "$hostdir"

    [ "$VENDOR_TABLE_ROWS" -gt 0 ] && HOSTS_WITH_VENDOR_ROWS=$((HOSTS_WITH_VENDOR_ROWS+1))

    HOST_VERDICT_SCENARIO="$(pick_verdict "$match_result" "$HOST_AUTH_OUTCOME" \
      "$BGP_VERSION_OUTCOME" "${BGP_VERSION_VALUE:-}" \
      "$RFC4273_OUTCOME" "$VENDOR_TABLE_OUTCOME" "$VENDOR_TABLE_ROWS")"

    # Look up the fix command for this scenario in vendor.conf
    local fix_var="FIX_COMMANDS_${HOST_VERDICT_SCENARIO}"
    HOST_VERDICT_FIX_CMD="${!fix_var:-}"
    HOST_VERDICT_INTERPRETATION="see verdict scenario"
    HOST_VERDICT_NEXT_ACTION="see verdict scenario"

    scan_for_redaction_targets "$hostdir"
    HOST_DIAGNOSTICS+=("$(build_host_diagnostic_entry "$host")")
    last_verdict="$HOST_VERDICT_SCENARIO"
    last_fix_cmd="$HOST_VERDICT_FIX_CMD"
    log_event "host $host: verdict=$HOST_VERDICT_SCENARIO"
  done

  local ended
  ended=$(date +%s)
  TOTAL_DURATION=$((ended - started))
  COMPLETED="fully"

  emit_diagnostics "${workdir}/diagnostics.json"
  write_sha256sums "$workdir"

  local hosts_joined
  hosts_joined="$(IFS=,; echo "${HOSTS[*]}")"
  local tarball
  tarball="$(bundle_tarball "$workdir" "$VENDOR_NAME" "$hosts_joined")"
  local tar_sha
  tar_sha="$(tarball_sha256 "$tarball")"
  log_event "tarball: $tarball sha256=$tar_sha"

  print_banner "$last_verdict" "$tarball" "$tar_sha" "$SEND_RECIPIENT" \
    "$VENDOR_DISPLAY_NAME" "$last_sysdescr" "$last_fix_cmd"

  # Exit code: 0 if any host produced a tarball-worth-having; 1 if all failed preflight.
  case "$last_verdict" in
    snmp_unreachable|snmp_auth_failed_*)
      [ "$HOSTS_PREFLIGHT_OK" -eq 0 ] && exit 1
      ;;
  esac
  exit 0
}

main "$@"
```

- [ ] **Step 2: Shellcheck the whole script set**

```bash
shellcheck scripts/colleague-capture.sh scripts/colleague-capture-lib/*.sh
```
Expected: no errors. (Some `unused variable` warnings on globals set by other libs are acceptable — silence them with targeted `# shellcheck disable=SC2034` comments at the top of relevant libs.)

- [ ] **Step 3: Run the existing bats suite to make sure nothing broke**

```bash
bats tests/scripts/test_lib_*.bats
```
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add scripts/colleague-capture.sh
git commit -m "feat(scripts): wire libs into colleague-capture.sh main()"
```

---

## Task 20: Redactor scaffold + CLI + load redaction-targets + pool allocator

**Files:**
- Create: `scripts/redact-snmp-capture.py`
- Create: `tests/scripts/test_redact_snmp_capture.py`
- Create: `tests/scripts/fixtures/redaction-targets.json`
- Create: `tests/scripts/fixtures/capture-with-ips.txt`

- [ ] **Step 1: Create test fixtures**

Create `tests/scripts/fixtures/redaction-targets.json`:

```json
{
  "ipv4": ["10.0.0.1", "10.0.0.2", "192.168.1.1", "24.150.96.57"],
  "ipv6": ["fe80::1856:9eff:fe86:fa46", "2001:dead:beef::1"],
  "mac":  ["62:22:32:96:11:e9", "00:1f:5b:de:ad:be"]
}
```

Create `tests/scripts/fixtures/capture-with-ips.txt`:

```
# captured at 2026-05-26T14:32:11Z
# host: 10.0.0.1
# oid:  1.3.6.1.2.1.4.20.1
# wrapper: colleague-capture v0.1.0

.1.3.6.1.2.1.4.20.1.1.10.0.0.1 = IpAddress: 10.0.0.1
.1.3.6.1.2.1.4.20.1.1.10.0.0.2 = IpAddress: 10.0.0.2
.1.3.6.1.2.1.4.20.1.1.192.168.1.1 = IpAddress: 192.168.1.1
.1.3.6.1.2.1.4.20.1.1.24.150.96.57 = IpAddress: 24.150.96.57
.1.3.6.1.2.1.4.20.1.3.10.0.0.1 = IpAddress: 255.255.255.0
.1.3.6.1.2.1.2.2.1.6.1 = STRING: 62:22:32:96:11:e9
.1.3.6.1.2.1.2.2.1.6.2 = STRING: 00:1f:5b:de:ad:be
```

- [ ] **Step 2: Write failing pytest**

Create `tests/scripts/test_redact_snmp_capture.py`:

```python
"""Tests for scripts/redact-snmp-capture.py."""
import json
import re
import subprocess
import shutil
from pathlib import Path

import pytest

FIXTURES = Path(__file__).parent / "fixtures"
SCRIPT = Path(__file__).parent.parent.parent / "scripts" / "redact-snmp-capture.py"


@pytest.fixture
def capture_dir(tmp_path):
    """Stage a captures dir with the fixture content."""
    d = tmp_path / "captures"
    d.mkdir()
    shutil.copy(FIXTURES / "capture-with-ips.txt", d / "r1_sample.txt")
    shutil.copy(FIXTURES / "redaction-targets.json", d / "redaction-targets.json")
    return d


def run_redactor(capture_dir, *extra):
    out = capture_dir.parent / "captures-redacted"
    return subprocess.run(
        ["python3", str(SCRIPT), "--in", str(capture_dir), "--out", str(out), *extra],
        capture_output=True, text=True, check=False,
    ), out


def test_ipv4_substituted(capture_dir):
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "10.0.0.1" not in redacted
    assert "24.150.96.57" not in redacted
    assert "192.0.2." in redacted   # at least one IPv4 mapped into RFC 5737


def test_mac_substituted(capture_dir):
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "62:22:32:96:11:e9" not in redacted
    assert "00:00:5e:00:53:" in redacted.lower()


def test_subnet_mask_preserved(capture_dir):
    """255.255.255.0 is a netmask and should NOT be redacted."""
    _, out = run_redactor(capture_dir)
    redacted = (out / "r1_sample.txt").read_text()
    assert "255.255.255.0" in redacted


def test_idempotent(capture_dir):
    _, out1 = run_redactor(capture_dir)
    content1 = (out1 / "r1_sample.txt").read_text()
    shutil.rmtree(out1)
    _, out2 = run_redactor(capture_dir)
    content2 = (out2 / "r1_sample.txt").read_text()
    assert content1 == content2


def test_strict_pass_finds_no_real_ips(capture_dir):
    result, out = run_redactor(capture_dir, "--strict")
    assert result.returncode == 0, result.stderr
    redacted = (out / "r1_sample.txt").read_text()
    # Anything that looks like an IPv4 should be in a documentation range or be a netmask.
    for ip in re.findall(r"\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", redacted):
        octets = [int(x) for x in ip.split(".")]
        in_doc_range = (
            (octets[0] == 192 and octets[1] == 0 and octets[2] == 2) or
            (octets[0] == 198 and octets[1] == 51 and octets[2] == 100) or
            (octets[0] == 203 and octets[1] == 0 and octets[2] == 113)
        )
        is_netmask = is_contiguous_netmask(octets)
        is_loopback = (octets[0] == 127)
        is_zero = (octets == [0, 0, 0, 0])
        assert in_doc_range or is_netmask or is_loopback or is_zero, (
            f"unredacted IP-like value: {ip}"
        )


def is_contiguous_netmask(octets):
    bits = "".join(f"{o:08b}" for o in octets)
    return re.fullmatch(r"1*0*", bits) is not None


def test_redaction_targets_dropped_from_output(capture_dir):
    _, out = run_redactor(capture_dir)
    assert not (out / "redaction-targets.json").exists()


def test_summary_includes_counts(capture_dir):
    result, _ = run_redactor(capture_dir)
    assert "IPv4" in result.stdout
    assert "MAC" in result.stdout
```

- [ ] **Step 3: Implement scripts/redact-snmp-capture.py**

Create `scripts/redact-snmp-capture.py`:

```python
#!/usr/bin/env python3
"""Receipt-side redactor for the colleague-capture toolkit.

See plans/colleague-capture.md for the design.

Substitutes IPv4, IPv6, and MAC values from documentation ranges. Leaves
netmasks, loopback, multicast, and ASNs alone. Idempotent: same input
always produces the same output.
"""
from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import re
import shutil
import sys
from pathlib import Path
from typing import Iterable

# --- Pools (RFC 5737, RFC 3849, RFC 7042) ---

IPV4_POOLS = [
    ipaddress.IPv4Network("192.0.2.0/24"),
    ipaddress.IPv4Network("198.51.100.0/24"),
    ipaddress.IPv4Network("203.0.113.0/24"),
]
IPV6_GLOBAL_POOL = ipaddress.IPv6Network("2001:db8::/32")
IPV6_LINKLOCAL_BASE = ipaddress.IPv6Address("fe80::5e:0:53:0")
MAC_BASE = "00:00:5E:00:53:"


# --- Preserved-as-structural ---

def is_loopback_v4(s: str) -> bool:
    try:
        return ipaddress.IPv4Address(s).is_loopback
    except ValueError:
        return False


def is_multicast_v4(s: str) -> bool:
    try:
        return ipaddress.IPv4Address(s).is_multicast
    except ValueError:
        return False


def is_zero_or_bcast(s: str) -> bool:
    return s in {"0.0.0.0", "255.255.255.255"}


def is_contiguous_netmask(s: str) -> bool:
    try:
        bits = "{:032b}".format(int(ipaddress.IPv4Address(s)))
    except ValueError:
        return False
    return re.fullmatch(r"1*0*", bits) is not None


def is_linklocal_v6(addr: ipaddress.IPv6Address) -> bool:
    return addr.is_link_local


# --- Pool allocator ---

class SubstitutionMap:
    def __init__(self, keep_loopback: bool = True, keep_multicast: bool = True):
        self.v4: dict[str, str] = {}
        self.v6: dict[str, str] = {}
        self.mac: dict[str, str] = {}
        self._v4_iter = self._iter_v4()
        self._v6_global_iter = self._iter_v6_global()
        self._v6_ll_iter = self._iter_v6_linklocal()
        self._mac_idx = 0
        self.keep_loopback = keep_loopback
        self.keep_multicast = keep_multicast

    def _iter_v4(self) -> Iterable[str]:
        for pool in IPV4_POOLS:
            for ip in pool.hosts():
                yield str(ip)

    def _iter_v6_global(self) -> Iterable[str]:
        addr = int(IPV6_GLOBAL_POOL.network_address) + 1
        while True:
            yield str(ipaddress.IPv6Address(addr))
            addr += 1

    def _iter_v6_linklocal(self) -> Iterable[str]:
        addr = int(IPV6_LINKLOCAL_BASE)
        while True:
            yield str(ipaddress.IPv6Address(addr))
            addr += 1

    def _next_mac(self) -> str:
        if self._mac_idx >= 256 * 256:
            raise RuntimeError("MAC substitution pool exhausted")
        hi = (self._mac_idx // 256) & 0xFF
        lo = self._mac_idx & 0xFF
        self._mac_idx += 1
        return f"{MAC_BASE}{hi:02x}:{lo:02x}".lower()

    def get_v4(self, real: str) -> str:
        # Preserve structural addresses
        if is_zero_or_bcast(real):
            return real
        if is_contiguous_netmask(real):
            return real
        if self.keep_loopback and is_loopback_v4(real):
            return real
        if self.keep_multicast and is_multicast_v4(real):
            return real
        if real not in self.v4:
            self.v4[real] = next(self._v4_iter)
        return self.v4[real]

    def get_v6(self, real: str) -> str:
        try:
            addr = ipaddress.IPv6Address(real)
        except ValueError:
            return real
        # Preserve some structural v6
        if addr in (ipaddress.IPv6Address("::"), ipaddress.IPv6Address("::1")):
            return real
        if self.keep_multicast and addr.is_multicast:
            return real
        if is_linklocal_v6(addr):
            # link-local always redacted (EUI-64 leaks MAC)
            if real not in self.v6:
                self.v6[real] = next(self._v6_ll_iter)
            return self.v6[real]
        if real not in self.v6:
            self.v6[real] = next(self._v6_global_iter)
        return self.v6[real]

    def get_mac(self, real: str) -> str:
        real_lc = real.lower()
        if real_lc not in self.mac:
            self.mac[real_lc] = self._next_mac()
        return self.mac[real_lc]


# --- Apply substitutions to text ---

# Match a 6-octet MAC with `:`, `-`, or `.` separators, 1-2 hex chars per octet.
MAC_RE = re.compile(
    r"\b(?:[0-9a-fA-F]{1,2}[:.\-]){5}[0-9a-fA-F]{1,2}\b"
)
# Match the Hex-STRING form: 6 space-separated 2-hex-char bytes following
# the `Hex-STRING:` prefix. Captured separately because the space-separator
# form isn't matched by MAC_RE.
HEXSTRING_MAC_RE = re.compile(
    r"(Hex-STRING:\s+)([0-9a-fA-F]{2}(?:\s+[0-9a-fA-F]{2}){5})\b"
)
IPV4_RE = re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
IPV6_RE = re.compile(
    r"\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{1,4})*\b"
)


def apply_substitutions(text: str, sub: SubstitutionMap) -> str:
    # Order matters: substitute longer / more-specific patterns first.
    # MACs first (most distinctive), then IPv6, then IPv4 (most permissive).
    def _mac(m):
        return sub.get_mac(m.group(0))

    def _v6(m):
        token = m.group(0)
        # Reject tokens that don't actually parse as IPv6 (the regex is permissive)
        try:
            ipaddress.IPv6Address(token)
        except ValueError:
            return token
        return sub.get_v6(token)

    def _v4(m):
        token = m.group(0)
        try:
            ipaddress.IPv4Address(token)
        except ValueError:
            return token
        # Skip octets inside IPv6 4-mapped representations the v6 regex didn't capture
        return sub.get_v4(token)

    def _hex_mac(m):
        prefix = m.group(1)
        bytes_str = m.group(2)
        # Convert "62 22 32 96 11 E9" → "62:22:32:96:11:e9", substitute, then
        # re-emit in space-separated form to preserve the Hex-STRING shape.
        colon_form = ":".join(bytes_str.split())
        sub_mac = sub.get_mac(colon_form)
        space_form = " ".join(sub_mac.split(":")).lower()
        return f"{prefix}{space_form}"

    text = HEXSTRING_MAC_RE.sub(_hex_mac, text)
    text = MAC_RE.sub(_mac, text)
    text = IPV6_RE.sub(_v6, text)
    text = IPV4_RE.sub(_v4, text)
    return text


# --- Strict-pass ---

DOC_V4_NETS = [ipaddress.IPv4Network(n) for n in
               ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24")]


def strict_check(text: str) -> list[str]:
    """Return a list of IP/MAC strings that look like real (un-redacted) values."""
    leaks = []
    for m in IPV4_RE.findall(text):
        try:
            ip = ipaddress.IPv4Address(m)
        except ValueError:
            continue
        if any(ip in n for n in DOC_V4_NETS):
            continue
        if is_zero_or_bcast(m) or is_contiguous_netmask(m):
            continue
        if ip.is_loopback or ip.is_multicast:
            continue
        leaks.append(m)
    for m in MAC_RE.findall(text):
        if not m.lower().startswith("00:00:5e:00:53:"):
            leaks.append(m)
    return leaks


# --- File walking ---

def redact_dir(in_dir: Path, out_dir: Path, sub: SubstitutionMap):
    if out_dir.exists():
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True)

    # Process every file except redaction-targets.json (drop it).
    for path in sorted(in_dir.rglob("*")):
        if path.is_dir():
            continue
        if path.name == "redaction-targets.json":
            continue
        rel = path.relative_to(in_dir)
        out_path = out_dir / rel
        out_path.parent.mkdir(parents=True, exist_ok=True)
        if path.name == "SHA256SUMS":
            continue  # will be recomputed
        try:
            text = path.read_text()
        except UnicodeDecodeError:
            shutil.copy(path, out_path)
            continue
        new_text = apply_substitutions(text, sub)
        out_path.write_text(new_text)


def recompute_sha256sums(out_dir: Path):
    lines = []
    for path in sorted(out_dir.rglob("*")):
        if path.is_dir() or path.name == "SHA256SUMS":
            continue
        h = hashlib.sha256(path.read_bytes()).hexdigest()
        rel = path.relative_to(out_dir)
        lines.append(f"{h}  ./{rel}")
    (out_dir / "SHA256SUMS").write_text("\n".join(lines) + "\n")


# --- main ---

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--in", dest="in_dir", default="./captures",
                   help="Source captures dir (default: ./captures)")
    p.add_argument("--out", dest="out_dir", default="./captures-redacted",
                   help="Destination dir (default: ./captures-redacted)")
    p.add_argument("--map", dest="map_file", default=None,
                   help="redaction-targets.json (default: <in>/redaction-targets.json)")
    p.add_argument("--strict", action="store_true", default=True,
                   help="Exit non-zero if strict-pass finds unredacted IPs/MACs")
    p.add_argument("--no-strict", dest="strict", action="store_false")
    p.add_argument("--keep-loopback", action="store_true", default=True)
    p.add_argument("--keep-multicast", action="store_true", default=True)
    args = p.parse_args()

    in_dir = Path(args.in_dir)
    out_dir = Path(args.out_dir)
    if not in_dir.is_dir():
        print(f"error: input dir not found: {in_dir}", file=sys.stderr)
        sys.exit(2)

    sub = SubstitutionMap(
        keep_loopback=args.keep_loopback,
        keep_multicast=args.keep_multicast,
    )

    # Pre-seed the map from redaction-targets.json (deterministic sort)
    map_file = Path(args.map_file) if args.map_file else (in_dir / "redaction-targets.json")
    if map_file.exists():
        targets = json.loads(map_file.read_text())
        for v in sorted(targets.get("ipv4", [])):
            sub.get_v4(v)
        for v in sorted(targets.get("ipv6", [])):
            sub.get_v6(v)
        for v in sorted(targets.get("mac", [])):
            sub.get_mac(v)

    redact_dir(in_dir, out_dir, sub)
    recompute_sha256sums(out_dir)

    # Strict-pass over the output
    leaks = []
    for path in out_dir.rglob("*"):
        if path.is_dir() or path.name == "SHA256SUMS":
            continue
        try:
            text = path.read_text()
        except UnicodeDecodeError:
            continue
        leaks.extend(strict_check(text))

    # Local-only debug map (gitignored via leading dot)
    debug_map = out_dir / ".redaction-map.json"
    debug_map.write_text(json.dumps({
        "_warning": "DO NOT COMMIT — contains real network identifiers",
        "ipv4": sub.v4, "ipv6": sub.v6, "mac": sub.mac,
    }, indent=2))

    print(f"Redacted: IPv4 {len(sub.v4)}, IPv6 {len(sub.v6)}, MAC {len(sub.mac)}")
    if leaks:
        print(f"WARNING: strict-pass found {len(leaks)} unredacted-looking values:", file=sys.stderr)
        for x in leaks[:10]:
            print(f"  {x}", file=sys.stderr)
        if args.strict:
            sys.exit(1)


if __name__ == "__main__":
    main()
```

Make executable:
```bash
chmod +x scripts/redact-snmp-capture.py
```

- [ ] **Step 4: Run pytest**

```bash
pytest tests/scripts/test_redact_snmp_capture.py -v
```
Expected: all 7 tests pass.

- [ ] **Step 5: Run ruff**

```bash
ruff check scripts/redact-snmp-capture.py
```
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add scripts/redact-snmp-capture.py tests/scripts/test_redact_snmp_capture.py tests/scripts/fixtures/
git commit -m "feat(scripts): redact-snmp-capture.py with IP/MAC substitution from RFC docs ranges"
```

---

## Task 21: IOS-XE lab refresh

**Files:**
- Rename: `lab/cisco-iosxe-bgp/capture.sh` → `lab/cisco-iosxe-bgp/capture-devnet.sh`
- Create: `lab/cisco-iosxe-bgp/vendor.conf`
- Create: `lab/cisco-iosxe-bgp/colleague-capture.sh` (3-line shim)
- Modify: `lab/cisco-iosxe-bgp/README.md` (rewrite for colleague flow)

- [ ] **Step 1: Rename the DevNet capture script**

```bash
git -C /Users/colin/Code/grafana/network-topology-exporter mv lab/cisco-iosxe-bgp/capture.sh lab/cisco-iosxe-bgp/capture-devnet.sh
```

- [ ] **Step 2: Create vendor.conf**

Create `lab/cisco-iosxe-bgp/vendor.conf`:

```bash
VENDOR_NAME="cisco-iosxe"
VENDOR_DISPLAY_NAME="Cisco IOS-XE"
ISSUE_REF="https://github.com/colinedwardwood/network-topology-exporter/issues/58"
LAB_DIR_REL="lab/cisco-iosxe-bgp"

EXPECTED_SYSOBJECTID_PREFIX="1.3.6.1.4.1.9"
SYSDESCR_KEYWORDS=("cisco" "ios")
VENDOR_TABLE_OID="1.3.6.1.4.1.9.9.187.1.2.5"
VENDOR_TABLE_LABEL="cbgpPeer2Table"

OIDS=(
  "1.3.6.1.2.1.1|sys_group"
  "1.3.6.1.2.1.15.1|rfc4273_bgpVersion_scalars"
  "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable"
  "1.3.6.1.4.1.9.9.187.1.2.5|cbgpPeer2Table"
  "1.3.6.1.4.1.9.9.187.1.2.4|cbgpPeerTable"
)

FALLBACK_1_3_6_1_4_1_9_9_187_1_2_5="1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable_fallback"

EXPECTED_EMPTY_OIDS=()

FIX_COMMANDS_vendor_table_empty_view_restriction_likely="snmp-server view ALL 1.3.6.1.4.1.9.9.187 included
snmp-server community public view ALL RO"

FIX_COMMANDS_snmp_silent_likely_vrf="snmp-server vrf <name>"

VRF_HINT="IOS-XE may need: snmp-server vrf <name>"

SEND_RECIPIENT="colin.wood@grafana.com"
SEND_SUBJECT="Capture for issue #58 — Cisco IOS-XE cbgpPeer2Table"
SEND_EXTRA_NOTE="This capture contains real IP and MAC addresses from your network. Please send only via direct email."
```

- [ ] **Step 3: Create the shim**

Create `lab/cisco-iosxe-bgp/colleague-capture.sh`:

```bash
#!/usr/bin/env bash
# Thin shim. Exports the vendor.conf path and delegates to the shared wrapper.
SHIM_DIR="$(cd "$(dirname "$0")" && pwd)"
export VENDOR_CONF_PATH="${SHIM_DIR}/vendor.conf"
exec "${SHIM_DIR}/../../scripts/colleague-capture.sh" "$@"
```

```bash
chmod +x lab/cisco-iosxe-bgp/colleague-capture.sh
```

- [ ] **Step 4: Rewrite README.md**

Read the current `lab/cisco-iosxe-bgp/README.md` first (the existing DevNet-focused content will be preserved as a sub-section at the bottom). Then overwrite the file with this content:

````markdown
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
before running the capture. The wrapper detects "BGP up but vendor table
empty" and tells you which case applies, but starting from a known-good
state shortens iteration.

## Run the capture

```bash
cd lab/cisco-iosxe-bgp
./colleague-capture.sh -h <router-ip> -c public
# or v3:
./colleague-capture.sh -h <router-ip> -V 3 -u monitor -a SHA -A 'authpw' -x AES -X 'privpw'
```

Safety affordances (no network calls, useful for nervous operators):

```bash
./colleague-capture.sh -h <router-ip> -c public --dry-run         # prints what it would run
./colleague-capture.sh -h <router-ip> -c public --preflight-only  # checks auth, exits
```

## What you'll get

A file named `topology-capture-cisco-iosxe-<host>-<timestamp>.tar.gz`,
about 5-50 KB, containing:

- `captures/` — raw `snmpwalk` text, one file per OID per host
- `diagnostics.json` — structured summary of what happened
- `wrapper.log` — sanitized execution log (passwords masked)
- `SHA256SUMS` — hashes of every file above
- `SEND-ME-THIS.txt` — what to do with this tarball

**This tarball contains real IP and MAC addresses from your network.**
Send via direct email only — do not attach to public issues, gists, or
chat channels.

## Where to send it

Email the tarball to **colin.wood@grafana.com** with subject
`Capture for issue #58 — Cisco IOS-XE cbgpPeer2Table`. Include the
sha256 the wrapper printed in the green banner so we can verify integrity.

## If something didn't work

| Banner verdict | What it means | What to do |
|---|---|---|
| `vendor_table_empty_view_restriction_likely` | SNMP view excludes the Cisco enterprise OID | Paste the `snmp-server view` block from the banner, save, re-run |
| `bgp_mib_module_absent` | BGP MIB not loaded on this device | Confirm BGP is configured and running |
| `snmp_silent_likely_vrf` | Device doesn't reply on this VRF | Try `snmp-server vrf <your-mgmt-vrf>` |
| `snmp_auth_failed_*` | Credentials wrong | Re-check the field named in the verdict |
| `snmp_reachable_vendor_mismatch` | This device isn't a Cisco IOS-XE | Confirm you pointed at the right host |

## Vendor-specific gotchas

- **VRF binding.** If your management IP is in a non-default VRF, the SNMP
  daemon may not respond on that path. Add `snmp-server vrf <name>`.
- **SNMPv3 engine ID change after image upgrade.** Newer IOS-XE images can
  regenerate the engine ID. If v3 used to work and now silently times out,
  re-create the v3 user after the upgrade.
- **View ordering.** `snmp-server view ALL ... included` must be defined
  before `snmp-server community ... view ALL RO`. The pasted block is in
  the correct order; if you edit it, keep the view-first ordering.

## Maintainer notes (for Colin)

On receipt:

1. Verify sha256 against the value in the colleague's email.
2. Extract: `tar xzf topology-capture-cisco-iosxe-*.tar.gz`.
3. Read `diagnostics.json`'s verdict — if `capture_ok`, proceed; otherwise
   the verdict tells you what to ask for.
4. Run the redactor:
   `scripts/redact-snmp-capture.py --in captures-* --out captures-redacted`.
5. Hand-convert `captures-redacted/r1_1_3_6_1_4_1_9_9_187_1_2_5.txt` into
   `[]gosnmp.SnmpPDU` literals per the conventions in
   `lab/cisco-iol-bgp/README.md`. Land as
   `buildCiscoCbgpPeer2IOSXERealPDUs` in
   `internal/discovery/bgp/bgp_v2_iosxe_test.go`.
6. Drop the `t.Skip` line in `bgp_v2_iosxe_test.go:79`.
7. If the IOS-XE captures match the IOL-derived helper byte-for-byte at the
   column level, note that as cross-confirmation in the closing PR.

## Alternate: DevNet self-serve

If you (Colin) want to produce this capture yourself rather than via a
colleague, the original DevNet-driven flow is preserved at
`capture-devnet.sh` in this directory. See its inline comments for the
openconnect + CML reservation walkthrough.
````

- [ ] **Step 5: Verify the file structure**

```bash
ls lab/cisco-iosxe-bgp/
```
Expected: `README.md`, `capture-devnet.sh`, `captures/`, `colleague-capture.sh`, `configs/`, `vendor.conf`.

- [ ] **Step 5: Commit**

```bash
git add lab/cisco-iosxe-bgp/vendor.conf lab/cisco-iosxe-bgp/colleague-capture.sh lab/cisco-iosxe-bgp/README.md
git commit -m "feat(lab/cisco-iosxe-bgp): refresh for colleague-capture flow (#58)"
```

---

## Task 22: Amend issue text for #56 and #57

**Files:** GitHub issues #56 and #57

- [ ] **Step 1: Fetch current issue body for #56**

```bash
gh issue view 56 --json body --jq .body > /tmp/issue-56-body.md
```

- [ ] **Step 2: Edit /tmp/issue-56-body.md**

Open `/tmp/issue-56-body.md` and replace the line that says `Anonymise via internal/snmptest redaction.` with `Anonymise via scripts/redact-snmp-capture.py (added in plans/colleague-capture.md PR 1).`

- [ ] **Step 3: Apply the edit**

```bash
gh issue edit 56 --body-file /tmp/issue-56-body.md
```

- [ ] **Step 4: Repeat for #57**

```bash
gh issue view 57 --json body --jq .body > /tmp/issue-57-body.md
# edit /tmp/issue-57-body.md the same way
gh issue edit 57 --body-file /tmp/issue-57-body.md
```

- [ ] **Step 5: Verify**

```bash
gh issue view 56 | grep -i "redact-snmp-capture"
gh issue view 57 | grep -i "redact-snmp-capture"
```
Expected: both show the updated reference.

---

## Task 23: CHANGELOG entry + final integration smoke

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add an unreleased entry**

Add to `CHANGELOG.md` under the unreleased section (matching existing format):

```markdown
### Added

- Vendor lab capture toolkit at `scripts/colleague-capture.sh` and `scripts/redact-snmp-capture.py` — lets a colleague with vendor hardware produce a self-diagnosing snmpwalk tarball in one command, then redact the result before fixture conversion. Lab dirs `lab/cisco-iosxe-bgp/` (#58), with `lab/juniper-jnxbgp/` (#56) and `lab/nokia-srbgp/` (#57) to follow in subsequent PRs.
```

- [ ] **Step 2: Run the full local test suite**

```bash
make lint-scripts
make test-scripts
make test-redactor
```
Expected: all green.

- [ ] **Step 3: Integration smoke — run against your UDM Pro**

(Only if the UDM Pro is reachable and you want to confirm end-to-end.)

```bash
cd lab/cisco-iosxe-bgp
./colleague-capture.sh -h 10.0.0.1 -c public
```

Expected (since UDM Pro is not a Cisco device):
- exit 0
- a tarball at `captures-<ts>/topology-capture-cisco-iosxe-*.tar.gz`
- banner shows **yellow** "doesn't look like a Cisco IOS-XE" message
- `captures-<ts>/diagnostics.json` has `verdict.scenario` set to `snmp_reachable_vendor_mismatch`

Extract and inspect:

```bash
mkdir /tmp/cc-smoke && cd /tmp/cc-smoke
tar xzf <path-to-tarball>
cat captures-*/diagnostics.json | python3 -m json.tool | grep -A2 verdict
```

If the verdict is `snmp_reachable_vendor_mismatch`: smoke passed.

- [ ] **Step 4: Run the redactor on the smoke output**

```bash
cd /tmp/cc-smoke
python3 /Users/colin/Code/grafana/network-topology-exporter/scripts/redact-snmp-capture.py \
  --in captures-*/ --out captures-redacted/
ls captures-redacted/
```

Expected: a `captures-redacted/` directory with no raw IPs in any txt file.

- [ ] **Step 5: Final commit**

```bash
cd /Users/colin/Code/grafana/network-topology-exporter
git add CHANGELOG.md
git commit -m "docs(changelog): note colleague-capture toolkit (PR 1 of 3)"
```

---

## Self-review checklist (before opening PR)

- [ ] All 23 tasks above are committed.
- [ ] `make lint-scripts test-scripts test-redactor` passes cleanly.
- [ ] CI green on the new `scripts` job.
- [ ] Manual smoke against UDM Pro produced expected yellow-banner output.
- [ ] Redactor output contains zero real IPs / MACs (verify with `grep -Eo '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' captures-redacted/ | sort -u`).
- [ ] No Co-Authored-By trailers on any commits.
- [ ] Spec coverage: every AC1-AC15 from `plans/colleague-capture.md` is exercised by either a bats test, a pytest test, or the manual smoke.

## Out of scope for this PR (becomes PRs 2 + 3)

- `lab/juniper-jnxbgp/` directory and vendor.conf (PR 2 closes #56)
- `lab/nokia-srbgp/` directory and vendor.conf (PR 3 closes #57)
- Real-device captures landing under any `lab/*/captures/` (follow-up "capture lands" PRs once colleagues actually run the wrapper)
- The `verified: true` flip in `internal/discovery/bgp/bgp_vendor.go` (same follow-up PRs)
