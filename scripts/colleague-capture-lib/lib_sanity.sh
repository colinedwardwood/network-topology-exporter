#!/usr/bin/env bash
# Sanity-check the colleague's environment before running anything.

require_tool() {
  local tool="${1:-}"
  if [ -z "$tool" ]; then
    echo "error: require_tool called with no argument" >&2
    return 3
  fi
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "error: required tool '${tool}' not found in PATH" >&2
    case "$tool" in
      snmpwalk) echo "  install: apt-get install snmp (linux) or brew install net-snmp (mac)" >&2 ;;
      tar) echo "  install: should be in your base OS" >&2 ;;
      sha256sum|shasum) echo "  install: install coreutils (mac: brew install coreutils)" >&2 ;;
      timeout|gtimeout) echo "  install: install coreutils (mac: brew install coreutils for gtimeout)" >&2 ;;
    esac
    return 3
  fi
  return 0
}

detect_timeout() {
  if command -v timeout >/dev/null 2>&1; then
    echo "timeout"
  elif command -v gtimeout >/dev/null 2>&1; then
    echo "gtimeout"
  else
    return 3
  fi
}

detect_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    echo "shasum -a 256"
  else
    return 3
  fi
}

sanity_all() {
  require_tool snmpwalk || return 3
  require_tool tar || return 3
  detect_timeout >/dev/null || { echo "error: need 'timeout' or 'gtimeout'" >&2; return 3; }
  detect_sha256 >/dev/null || { echo "error: need 'sha256sum' or 'shasum'" >&2; return 3; }
  return 0
}
