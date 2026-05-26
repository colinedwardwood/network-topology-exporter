#!/usr/bin/env bash
# Match a device against vendor.conf's identifiers.
# Reads globals: EXPECTED_SYSOBJECTID_PREFIX, SYSDESCR_KEYWORDS[]

vendor_match() {
  local sysobjectid="${1:-}"
  local sysdescr="${2:-}"

  # 1. sysObjectID prefix
  if [ -n "${EXPECTED_SYSOBJECTID_PREFIX:-}" ]; then
    case "$sysobjectid" in
      "${EXPECTED_SYSOBJECTID_PREFIX}"*) echo "match:sysObjectID"; return 0 ;;
    esac
  fi

  # 2. SYSDESCR_KEYWORDS substring, case-insensitive
  if [ "${#SYSDESCR_KEYWORDS[@]:-0}" -gt 0 ]; then
    local descr_lc
    descr_lc="$(echo "$sysdescr" | tr '[:upper:]' '[:lower:]')"
    local kw kw_lc
    for kw in "${SYSDESCR_KEYWORDS[@]}"; do
      kw_lc="$(echo "$kw" | tr '[:upper:]' '[:lower:]')"
      case "$descr_lc" in
        *"${kw_lc}"*) echo "match:sysDescr"; return 0 ;;
      esac
    done
  fi

  echo "miss"
  return 0
}
