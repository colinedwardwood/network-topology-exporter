#!/usr/bin/env bash
# Walk a single OID with timeout + PDU cap. Classify the outcome.
#
# Globals expected:
#   SNMP_VERSION, COMMUNITY, V3_*, PER_OID_TIMEOUT, PER_OID_PDU_CAP, RETRIES
#   TIMEOUT_CMD (set by main() after sanity_all)
#   WRAPPER_VERSION
#   OIDS[] (from vendor.conf)
#   WALK_RESULTS (output, declared by caller)

# do_walk HOST OID OUTFILE → echoes "outcome|rows|duration"
do_walk() {
  local host="${1:-}" oid="${2:-}" outfile="${3:-}"
  [ -z "$host" ] || [ -z "$oid" ] || [ -z "$outfile" ] && { echo "other-error|0|0"; return 1; }

  local -a snmp_args=()
  local _line
  while IFS= read -r _line; do snmp_args+=("$_line"); done < <(build_snmp_args)

  local raw err rc start end duration
  start=$(date +%s)
  local errfile
  errfile="$(mktemp -t cc-walk-err.XXXXXX)"
  set +e
  raw="$("${TIMEOUT_CMD:-timeout}" "${PER_OID_TIMEOUT:-60}" snmpwalk "${snmp_args[@]}" \
            -On -Oe -t 10 -r "${RETRIES:-1}" "$host" "$oid" 2>"$errfile" \
            | head -n "${PER_OID_PDU_CAP:-50000}")"
  rc=$?
  set -e
  err="$(cat "$errfile" 2>/dev/null || echo "")"
  rm -f "$errfile"
  end=$(date +%s)
  duration=$((end - start))

  {
    echo "# captured at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# host: ${host}"
    echo "# oid:  ${oid}"
    echo "# wrapper: colleague-capture v${WRAPPER_VERSION:-?}"
    echo
    echo "$raw"
  } > "$outfile"

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
# Output: WALK_RESULTS array (one element per walk, pipe-separated:
#   oid|label|outcome|rows|duration|outfile|role)
walk_all() {
  local host="${1:-}" dir="${2:-}"
  WALK_RESULTS=()
  local entry oid label safe outfile result
  for entry in "${OIDS[@]:-}"; do
    [ -z "$entry" ] && continue
    oid="${entry%%|*}"
    label="${entry#*|}"
    safe="$(echo "$oid" | tr '.' '_')"
    outfile="${dir}/r1_${safe}.txt"
    result="$(do_walk "$host" "$oid" "$outfile")"
    WALK_RESULTS+=("${oid}|${label}|${result}|${outfile}|primary")
  done
}

# walk_fallbacks HOST CAPTURES_DIR
#   For each WALK_RESULTS entry classified noSuchObject|end-of-mib|ok-empty,
#   look up FALLBACK_<safe-oid> in vendor.conf and walk it. Appended to
#   WALK_RESULTS with role "fallback-for:<primary-oid>".
walk_fallbacks() {
  local host="${1:-}" dir="${2:-}"
  [ -z "$host" ] || [ -z "$dir" ] && return 1

  # Collect work first; walking inside a loop iterating WALK_RESULTS while
  # appending to WALK_RESULTS is brittle in bash.
  local -a fallbacks_to_run=()
  local entry oid outcome fb_entry
  for entry in "${WALK_RESULTS[@]:-}"; do
    [ -z "$entry" ] && continue
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

  local primary primary_oid rest fb_oid fb_label safe outfile result
  for primary in "${fallbacks_to_run[@]:-}"; do
    [ -z "$primary" ] && continue
    primary_oid="${primary%%|*}"
    rest="${primary#*|}"
    fb_oid="${rest%%|*}"
    fb_label="${rest#*|}"
    safe="$(echo "$fb_oid" | tr '.' '_')"
    outfile="${dir}/r1_${safe}.txt"
    result="$(do_walk "$host" "$fb_oid" "$outfile")"
    WALK_RESULTS+=("${fb_oid}|${fb_label}|${result}|${outfile}|fallback-for:${primary_oid}")
  done
}
