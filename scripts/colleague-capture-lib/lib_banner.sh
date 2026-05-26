#!/usr/bin/env bash
# Render the verdict-aware end-of-run banner.

_color_enabled() { [ -t 1 ]; }
_g() { _color_enabled && printf '\033[32m' || true; }
_y() { _color_enabled && printf '\033[33m' || true; }
_r() { _color_enabled && printf '\033[31m' || true; }
_b() { _color_enabled && printf '\033[1m' || true; }
_n() { _color_enabled && printf '\033[0m' || true; }

# print_banner VERDICT TARBALL SHA256 RECIPIENT VENDOR_DISPLAY SYSDESCR FIX_COMMAND
print_banner() {
  local verdict="${1:-inconclusive}" tarball="${2:-}" sha="${3:-}" \
        recipient="${4:-}" vendor_display="${5:-}" sysdescr="${6:-}" fix_cmd="${7:-}"

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
        # shellcheck disable=SC2001  # sed needed for multi-line prefix
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
