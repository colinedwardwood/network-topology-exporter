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
