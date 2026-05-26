#!/usr/bin/env bash
# Colleague-driven SNMP capture wrapper. See plans/colleague-capture.md.
set -euo pipefail
IFS=$'\n\t'

WRAPPER_VERSION="0.1.0"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LIB_DIR="${SCRIPT_DIR}/colleague-capture-lib"
WRAPPER_GIT_SHA="$(cd "$SCRIPT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo "unknown")"

# Source order matters: lib_json/lib_log/lib_faults have no deps; everything else
# depends on them or on globals from lib_args/lib_vendor_conf.
# shellcheck source=colleague-capture-lib/lib_json.sh disable=SC1091
. "${LIB_DIR}/lib_json.sh"
# shellcheck source=colleague-capture-lib/lib_args.sh disable=SC1091
. "${LIB_DIR}/lib_args.sh"
# shellcheck source=colleague-capture-lib/lib_sanity.sh disable=SC1091
. "${LIB_DIR}/lib_sanity.sh"
# shellcheck source=colleague-capture-lib/lib_vendor_conf.sh disable=SC1091
. "${LIB_DIR}/lib_vendor_conf.sh"
# shellcheck source=colleague-capture-lib/lib_faults.sh disable=SC1091
. "${LIB_DIR}/lib_faults.sh"
# shellcheck source=colleague-capture-lib/lib_preflight.sh disable=SC1091
. "${LIB_DIR}/lib_preflight.sh"
# shellcheck source=colleague-capture-lib/lib_vendor_match.sh disable=SC1091
. "${LIB_DIR}/lib_vendor_match.sh"
# shellcheck source=colleague-capture-lib/lib_bgp_probe.sh disable=SC1091
. "${LIB_DIR}/lib_bgp_probe.sh"
# shellcheck source=colleague-capture-lib/lib_walks.sh disable=SC1091
. "${LIB_DIR}/lib_walks.sh"
# shellcheck source=colleague-capture-lib/lib_redact_targets.sh disable=SC1091
. "${LIB_DIR}/lib_redact_targets.sh"
# shellcheck source=colleague-capture-lib/lib_verdict.sh disable=SC1091
. "${LIB_DIR}/lib_verdict.sh"
# shellcheck source=colleague-capture-lib/lib_log.sh disable=SC1091
. "${LIB_DIR}/lib_log.sh"
# shellcheck source=colleague-capture-lib/lib_diagnostics.sh disable=SC1091
. "${LIB_DIR}/lib_diagnostics.sh"
# shellcheck source=colleague-capture-lib/lib_bundle.sh disable=SC1091
. "${LIB_DIR}/lib_bundle.sh"
# shellcheck source=colleague-capture-lib/lib_banner.sh disable=SC1091
. "${LIB_DIR}/lib_banner.sh"

main() {
  # VENDOR_CONF_PATH expected from the lab-dir shim; default to ./vendor.conf
  local vendor_conf="${VENDOR_CONF_PATH:-./vendor.conf}"
  load_vendor_conf "$vendor_conf" || exit 2
  vendor_conf_required_keys || exit 2

  parse_args "$@" || exit 2

  if [ "${DRY_RUN:-0}" -eq 1 ]; then
    echo "Would run:"
    local h
    for h in "${HOSTS[@]}"; do
      local -a sa
      mapfile -t sa < <(build_snmp_args)
      local masked="${sa[*]}"
      if [ -n "${V3_AUTH_PASS:-}" ]; then masked="${masked//${V3_AUTH_PASS}/***}"; fi
      if [ -n "${V3_PRIV_PASS:-}" ]; then masked="${masked//${V3_PRIV_PASS}/***}"; fi
      if [ -n "${COMMUNITY:-}" ]; then masked="${masked//${COMMUNITY}/***}"; fi
      echo "  snmpwalk ${masked} -On -Oe -t 10 -r ${RETRIES} ${h} <each-OID-from-vendor.conf>"
    done
    exit 4
  fi

  sanity_all || exit 3
  # shellcheck disable=SC2034  # consumed by lib_walks / lib_sanity via global
  TIMEOUT_CMD="$(detect_timeout)"
  # shellcheck disable=SC2034  # consumed by lib_bundle / lib_diagnostics via global
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
  # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
  ANY_WALK_TIMED_OUT=0
  # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
  ANY_PDU_CAP_HIT=0

  local last_verdict="inconclusive" last_fix_cmd="" last_sysdescr=""
  local started
  started=$(date +%s)

  local host
  for host in "${HOSTS[@]}"; do
    # Total-runtime cap (AC10): if elapsed exceeds TOTAL_TIMEOUT, stop walking.
    local now elapsed
    now=$(date +%s)
    elapsed=$((now - started))
    if [ "$elapsed" -ge "${TOTAL_TIMEOUT:-300}" ]; then
      log_event "total-timeout exceeded ($elapsed >= ${TOTAL_TIMEOUT:-300}); skipping remaining hosts"
      COMPLETED="partial-timeout"
      # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
      ANY_WALK_TIMED_OUT=1
      break
    fi
    log_event "host start: $host"
    local hostdir
    hostdir="${workdir}/$(echo "$host" | tr '.' '_' | tr ':' '_')"
    mkdir -p "$hostdir"

    HOST_ICMP_OUTCOME="$(probe_icmp "$host")"
    HOST_AUTH_OUTCOME="$(auth_probe "$host" "$HOST_ICMP_OUTCOME")"

    if [ "$HOST_AUTH_OUTCOME" != "ok" ]; then
      log_event "host $host: auth_probe → $HOST_AUTH_OUTCOME"
      # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
      HOST_VENDOR_MATCH_BOOL=0
      HOST_VERDICT_SCENARIO="$HOST_AUTH_OUTCOME"
      HOST_VERDICT_FIX_CMD=""
      # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
      HOST_VERDICT_INTERPRETATION="see verdict scenario"
      # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
      HOST_VERDICT_NEXT_ACTION="see verdict scenario"
      HOST_DIAGNOSTICS+=("$(build_host_diagnostic_entry "$host")")
      last_verdict="$HOST_VERDICT_SCENARIO"
      continue
    fi
    HOSTS_PREFLIGHT_OK=$((HOSTS_PREFLIGHT_OK+1))
    HOST_SYSDESCR="$(echo "${PREFLIGHT_STDOUT:-}" | awk -F'STRING: ' '/STRING:/ {print $2; exit}')"
    HOST_SYSOID="$(probe_sysobjectid "$host")"
    last_sysdescr="$HOST_SYSDESCR"

    local match_result
    match_result="$(vendor_match "$HOST_SYSOID" "$HOST_SYSDESCR")"
    # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
    if [ "$match_result" != "miss" ]; then
      HOST_VENDOR_MATCH_BOOL=1
    else
      HOST_VENDOR_MATCH_BOOL=0
    fi

    bgp_three_signal_probe "$host"
    walk_all "$host" "$hostdir"
    walk_fallbacks "$host" "$hostdir"

    if [ "${VENDOR_TABLE_ROWS:-0}" -gt 0 ] 2>/dev/null; then
      HOSTS_WITH_VENDOR_ROWS=$((HOSTS_WITH_VENDOR_ROWS+1))
    fi

    HOST_VERDICT_SCENARIO="$(pick_verdict "$match_result" "$HOST_AUTH_OUTCOME" \
      "${BGP_VERSION_OUTCOME:-}" "${BGP_VERSION_VALUE:-}" \
      "${RFC4273_OUTCOME:-}" "${VENDOR_TABLE_OUTCOME:-}" "${VENDOR_TABLE_ROWS:-0}")"

    local fix_var="FIX_COMMANDS_${HOST_VERDICT_SCENARIO}"
    HOST_VERDICT_FIX_CMD="${!fix_var:-}"
    # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
    HOST_VERDICT_INTERPRETATION="see verdict scenario"
    # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
    HOST_VERDICT_NEXT_ACTION="see verdict scenario"

    scan_for_redaction_targets "$hostdir"
    HOST_DIAGNOSTICS+=("$(build_host_diagnostic_entry "$host")")
    last_verdict="$HOST_VERDICT_SCENARIO"
    last_fix_cmd="$HOST_VERDICT_FIX_CMD"
    log_event "host $host: verdict=$HOST_VERDICT_SCENARIO"
  done

  local ended
  ended=$(date +%s)
  # shellcheck disable=SC2034  # consumed by lib_diagnostics via global
  TOTAL_DURATION=$((ended - started))
  COMPLETED="${COMPLETED:-fully}"

  emit_diagnostics "${workdir}/diagnostics.json"
  write_sha256sums "$workdir"

  local hosts_joined
  hosts_joined="$(IFS=,; echo "${HOSTS[*]}")"
  local tarball
  tarball="$(bundle_tarball "$workdir" "$VENDOR_NAME" "$hosts_joined")"
  local tar_sha
  tar_sha="$(tarball_sha256 "$tarball")"
  log_event "tarball: $tarball sha256=$tar_sha"

  print_banner "$last_verdict" "$tarball" "$tar_sha" "${SEND_RECIPIENT:-}" \
    "${VENDOR_DISPLAY_NAME:-}" "$last_sysdescr" "$last_fix_cmd"

  case "$last_verdict" in
    snmp_unreachable|snmp_auth_failed_*)
      [ "$HOSTS_PREFLIGHT_OK" -eq 0 ] && exit 1
      ;;
  esac
  exit 0
}

main "$@"
