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
  local host="${1:-}"
  [ -z "$host" ] && { echo "fail"; return 0; }
  local flags
  # shellcheck disable=SC2086
  read -r -a flags <<< "$(ping_flags)"
  if ping "${flags[@]}" "$host" >/dev/null 2>&1; then
    echo "ok"
  else
    echo "filtered"
  fi
}

# build_snmp_args [override_level] → emits snmpwalk arg list, one per line
# Reads globals: SNMP_VERSION, COMMUNITY, V3_USER, V3_AUTH_PROTO, V3_AUTH_PASS,
#                V3_PRIV_PROTO, V3_PRIV_PASS.
build_snmp_args() {
  local override_level="${1:-}"
  if [ "${SNMP_VERSION:-}" = "2c" ]; then
    printf -- '-v\n2c\n-c\n%s\n' "${COMMUNITY:-}"
  else
    local level="${override_level:-authPriv}"
    if [ "$level" = "authNoPriv" ]; then
      printf -- '-v\n3\n-l\nauthNoPriv\n-u\n%s\n-a\n%s\n-A\n%s\n' \
        "${V3_USER:-}" "${V3_AUTH_PROTO:-}" "${V3_AUTH_PASS:-}"
    else
      printf -- '-v\n3\n-l\nauthPriv\n-u\n%s\n-a\n%s\n-A\n%s\n-x\n%s\n-X\n%s\n' \
        "${V3_USER:-}" "${V3_AUTH_PROTO:-}" "${V3_AUTH_PASS:-}" \
        "${V3_PRIV_PROTO:-}" "${V3_PRIV_PASS:-}"
    fi
  fi
}

# probe_sysdescr HOST → captures stderr, sets PREFLIGHT_STDOUT/STDERR/EXIT
# shellcheck disable=SC2034  # PREFLIGHT_* are output globals read by the caller
probe_sysdescr() {
  local host="${1:-}"
  [ -z "$host" ] && { PREFLIGHT_EXIT=1; PREFLIGHT_STDOUT=""; PREFLIGHT_STDERR="no host"; return 1; }
  local -a args
  mapfile -t args < <(build_snmp_args)
  local errfile
  errfile="$(mktemp -t cc-preflight-err.XXXXXX)"
  set +e
  PREFLIGHT_STDOUT="$(snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.1.0 2>"$errfile")"
  PREFLIGHT_EXIT=$?
  set -e
  PREFLIGHT_STDERR="$(cat "$errfile" 2>/dev/null || echo "")"
  rm -f "$errfile"
  return "$PREFLIGHT_EXIT"
}

# probe_sysobjectid HOST → echoes the OID value or empty on failure
probe_sysobjectid() {
  local host="${1:-}"
  [ -z "$host" ] && return 1
  local -a args
  mapfile -t args < <(build_snmp_args)
  snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.2.0 2>/dev/null \
    | awk -F'OID: ' '/OID:/ {gsub(/^\./, "", $2); print $2; exit}'
}

# auth_probe HOST ICMP_OUTCOME  → echoes one of:
#   ok | snmp_auth_failed_authpass | snmp_auth_failed_user
#   | snmp_auth_failed_security_level | snmp_auth_failed_privpass
#   | snmp_silent_likely_vrf | snmp_unreachable
#
# Depends on lib_faults.sh (match_fault) being sourced before this.
auth_probe() {
  local host="${1:-}"
  local icmp_outcome="${2:-fail}"
  [ -z "$host" ] && { echo "snmp_unreachable"; return 0; }

  probe_sysdescr "$host"
  if [ "${PREFLIGHT_EXIT:-1}" -eq 0 ] && [ -n "${PREFLIGHT_STDOUT:-}" ]; then
    echo "ok"; return 0
  fi

  local fault
  fault="$(match_fault "${PREFLIGHT_STDERR:-}")"

  case "$fault" in
    snmp_auth_failed_authpass|snmp_auth_failed_user|snmp_auth_failed_security_level|snmp_auth_failed_privpass)
      echo "$fault"; return 0 ;;
    timeout)
      # Disambiguation: if v3 authPriv timed out and ICMP was OK, re-probe at authNoPriv.
      if [ "${SNMP_VERSION:-}" = "3" ] && [ "$icmp_outcome" = "ok" ]; then
        local -a args
        mapfile -t args < <(build_snmp_args authNoPriv)
        if snmpwalk "${args[@]}" -On -Oe -t 5 -r 0 "$host" 1.3.6.1.2.1.1.1.0 >/dev/null 2>&1; then
          echo "snmp_auth_failed_privpass"; return 0
        fi
        echo "snmp_silent_likely_vrf"; return 0
      fi
      if [ "$icmp_outcome" = "ok" ]; then
        echo "snmp_silent_likely_vrf"; return 0
      fi
      echo "snmp_unreachable"; return 0 ;;
    *)
      echo "snmp_unreachable"; return 0 ;;
  esac
}
