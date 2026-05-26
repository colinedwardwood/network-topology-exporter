#!/usr/bin/env bash
# Pick a single verdict scenario from collected facts.

pick_verdict() {
  local vendor_match="${1:-miss}"
  local auth_outcome="${2:-}"
  local bgp_version_outcome="${3:-}"
  # bgp_version_value="${4:-}"  # currently unused
  local rfc4273_outcome="${5:-}"
  local vendor_table_outcome="${6:-}"
  local vendor_table_rows="${7:-0}"

  case "$auth_outcome" in
    snmp_auth_failed_user|snmp_auth_failed_authpass|snmp_auth_failed_privpass|snmp_auth_failed_security_level)
      echo "$auth_outcome"; return 0 ;;
  esac

  case "$auth_outcome" in
    snmp_unreachable) echo "snmp_unreachable"; return 0 ;;
    snmp_silent_likely_vrf) echo "snmp_silent_likely_vrf"; return 0 ;;
  esac

  if [ "$vendor_match" = "miss" ]; then
    echo "snmp_reachable_vendor_mismatch"; return 0
  fi

  case "$bgp_version_outcome" in
    noSuchObject|noSuchInstance|end-of-mib)
      echo "bgp_mib_module_absent"; return 0 ;;
  esac

  if [ "$vendor_table_outcome" = "ok-rows" ] && [ "${vendor_table_rows:-0}" -gt 0 ] 2>/dev/null; then
    echo "capture_ok"; return 0
  fi

  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-rows" ] && \
     [ "$vendor_table_outcome" = "ok-empty" ]; then
    echo "vendor_table_empty_view_restriction_likely"; return 0
  fi

  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-empty" ]; then
    case "$vendor_table_outcome" in
      noSuchObject|end-of-mib)
        echo "vendor_table_empty_mib_not_implemented"; return 0 ;;
    esac
  fi

  if [ "$bgp_version_outcome" = "ok" ] && \
     [ "$rfc4273_outcome" = "ok-empty" ] && \
     [ "$vendor_table_outcome" = "ok-empty" ]; then
    echo "bgp_up_but_no_peers"; return 0
  fi

  echo "inconclusive"
}
