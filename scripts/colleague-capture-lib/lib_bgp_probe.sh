#!/usr/bin/env bash
# Three-signal BGP state probe: bgpVersion + RFC 4273 + vendor table.
#
# Globals expected:
#   VENDOR_TABLE_OID  (from vendor.conf)
#   TIMEOUT_CMD       (from sanity_all)
#   SNMP creds        (via build_snmp_args)
# Sets globals (consumed by lib_verdict + lib_diagnostics):
#   BGP_VERSION_OUTCOME / BGP_VERSION_VALUE
#   RFC4273_OUTCOME / RFC4273_ROWS
#   VENDOR_TABLE_OUTCOME / VENDOR_TABLE_ROWS

# shellcheck disable=SC2034  # BGP_VERSION_OUTCOME, BGP_VERSION_VALUE, RFC4273_OUTCOME,
#                            # RFC4273_ROWS, VENDOR_TABLE_OUTCOME, VENDOR_TABLE_ROWS
#                            # are consumed by callers (lib_verdict, lib_diagnostics)

_probe_one() {
  local host="${1:-}" oid="${2:-}"
  [ -z "$host" ] || [ -z "$oid" ] && { echo "other-error|0|"; return 1; }

  local -a snmp_args=()
  local _line
  while IFS= read -r _line; do snmp_args+=("$_line"); done < <(build_snmp_args)
  local raw err rc rows
  local errfile
  errfile="$(mktemp -t cc-probe-err.XXXXXX)"
  set +e
  raw="$("${TIMEOUT_CMD:-timeout}" 10 snmpwalk "${snmp_args[@]}" -On -Oe -t 5 -r 0 "$host" "$oid" 2>"$errfile")"
  rc=$?
  set -e
  err="$(cat "$errfile" 2>/dev/null || echo "")"
  rm -f "$errfile"

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
  local first_value
  first_value="$(echo "$raw" | head -1 | awk -F'= ' '{print $2}')"
  echo "ok-rows|${rows}|${first_value}"
}

bgp_three_signal_probe() {
  local host="${1:-}"
  [ -z "$host" ] && return 1

  local r

  r="$(_probe_one "$host" 1.3.6.1.2.1.15.1.0)"
  BGP_VERSION_OUTCOME="${r%%|*}"
  BGP_VERSION_VALUE="${r##*|}"

  r="$(_probe_one "$host" 1.3.6.1.2.1.15.3)"
  RFC4273_OUTCOME="${r%%|*}"
  RFC4273_ROWS="$(echo "$r" | cut -d'|' -f2)"

  r="$(_probe_one "$host" "${VENDOR_TABLE_OID:-}")"
  VENDOR_TABLE_OUTCOME="${r%%|*}"
  VENDOR_TABLE_ROWS="$(echo "$r" | cut -d'|' -f2)"
}
