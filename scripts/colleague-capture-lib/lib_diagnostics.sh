#!/usr/bin/env bash
# Emit diagnostics.json. Reads from globals set across the rest of the libs.

# emit_diagnostics OUTFILE
emit_diagnostics() {
  local outfile="${1:-}"
  [ -z "$outfile" ] && return 1
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  {
    echo '{'
    echo "  \"schema_version\": 1,"
    printf '  "wrapper_version": '; json_string "${WRAPPER_VERSION:-unknown}"; echo ','
    printf '  "wrapper_git_sha": '; json_string "${WRAPPER_GIT_SHA:-unknown}"; echo ','
    printf '  "vendor_lab": '; json_string "${VENDOR_NAME:-}"; echo ','
    printf '  "issue_ref": '; json_string "${ISSUE_REF:-}"; echo ','
    printf '  "captured_at": '; json_string "$now"; echo ','
    printf '  "duration_seconds": %s,\n' "${TOTAL_DURATION:-0}"
    printf '  "completed": '; json_string "${COMPLETED:-fully}"; echo ','

    echo '  "environment": {'
    printf '    "os": '; json_string "$(uname -a)"; echo ','
    printf '    "snmpwalk_version": '; json_string "$(snmpwalk --version 2>&1 | head -1)"; echo ','
    printf '    "bash_version": '; json_string "${BASH_VERSION:-}"; echo ','
    printf '    "locale": '; json_string "${LC_ALL:-${LANG:-unknown}}"; echo ','
    printf '    "timezone": '; json_string "$(date +%Z)"; echo ''
    echo '  },'

    echo '  "snmp_config": {'
    printf '    "version": '; json_string "${SNMP_VERSION:-}"; echo ','
    if [ "${SNMP_VERSION:-}" = "3" ]; then
      printf '    "user": '; json_string "${V3_USER:-}"; echo ','
      printf '    "auth_proto": '; json_string "${V3_AUTH_PROTO:-}"; echo ','
      printf '    "priv_proto": '; json_string "${V3_PRIV_PROTO:-}"; echo ','
      echo '    "community": null,'
    else
      echo '    "user": null, "auth_proto": null, "priv_proto": null,'
      printf '    "community": '; json_string "${COMMUNITY:-}"; echo ','
    fi
    printf '    "per_oid_timeout_seconds": %s,\n' "${PER_OID_TIMEOUT:-60}"
    printf '    "total_timeout_seconds": %s,\n' "${TOTAL_TIMEOUT:-300}"
    printf '    "retries": %s,\n' "${RETRIES:-1}"
    printf '    "per_oid_pdu_cap": %s\n' "${PER_OID_PDU_CAP:-50000}"
    echo '  },'

    echo '  "hosts": ['
    local i=0
    local host_entry
    for host_entry in "${HOST_DIAGNOSTICS[@]:-}"; do
      [ -z "$host_entry" ] && continue
      [ $i -gt 0 ] && echo ','
      echo -n "$host_entry"
      i=$((i+1))
    done
    echo
    echo '  ],'

    echo '  "aggregate": {'
    printf '    "hosts_total": %s,\n' "$([ -n "${HOSTS+x}" ] && echo "${#HOSTS[@]}" || echo 0)"
    printf '    "hosts_preflight_ok": %s,\n' "${HOSTS_PREFLIGHT_OK:-0}"
    printf '    "hosts_with_vendor_table_rows": %s,\n' "${HOSTS_WITH_VENDOR_ROWS:-0}"
    printf '    "any_walk_timed_out": %s,\n' "$(json_bool "${ANY_WALK_TIMED_OUT:-0}")"
    printf '    "any_pdu_cap_hit": %s\n' "$(json_bool "${ANY_PDU_CAP_HIT:-0}")"
    echo '  }'
    echo '}'
  } > "$outfile"
}

# build_host_diagnostic_entry HOST → echoes a JSON object string for one host
build_host_diagnostic_entry() {
  local host="${1:-}"
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
    for entry in "${WALK_RESULTS[@]:-}"; do
      [ -z "$entry" ] && continue
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
      echo -n "\"rows\": ${rows:-0}, "
      echo -n "\"duration_seconds\": ${duration:-0}, "
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
