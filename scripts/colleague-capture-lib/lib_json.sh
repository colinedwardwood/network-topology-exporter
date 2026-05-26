#!/usr/bin/env bash
# JSON value helpers for bash. POSIX-portable; no jq dependency.

json_string() {
  # Escape a bash string as a JSON string literal (including quotes).
  # Handles the RFC 7159 named escapes. Does NOT handle other C0 control
  # bytes (0x00-0x1F except \b \t \n \f \r) via \uXXXX — those are not
  # expected in our inputs (sysDescr/OID values/state names are printable).
  local s="$1"
  s="${s//\\/\\\\}"   # backslash first
  s="${s//\"/\\\"}"
  s="${s//$'\b'/\\b}"
  s="${s//$'\f'/\\f}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  printf '"%s"' "$s"
}

json_null() {
  printf 'null'
}

json_bool() {
  if [ "${1:-}" -eq 1 ] 2>/dev/null; then
    printf 'true'
  else
    printf 'false'
  fi
}
