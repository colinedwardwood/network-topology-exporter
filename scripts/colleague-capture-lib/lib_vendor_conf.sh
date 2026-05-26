#!/usr/bin/env bash
# Source a vendor.conf file and validate required keys.

load_vendor_conf() {
  local conf="${1:-}"
  if [ -z "$conf" ]; then
    echo "error: load_vendor_conf requires a path argument" >&2
    return 1
  fi
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
  local oid="${1:-}"
  [ -z "$oid" ] && return 0
  local safe
  safe="$(echo "$oid" | tr '.' '_')"
  local var="FALLBACK_${safe}"
  echo "${!var:-}"
}
