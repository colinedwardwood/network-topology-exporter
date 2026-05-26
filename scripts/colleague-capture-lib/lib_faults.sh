#!/usr/bin/env bash
# Match snmpwalk stderr against known fault strings.

strip_netsnmp_preamble() {
  local s="${1:-}"
  s="${s/No log handling enabled - using stderr logging$'\n'/}"
  s="${s/No log handling enabled - using stderr logging/}"
  echo "$s"
}

match_fault() {
  local raw="${1:-}"
  local s
  s="$(strip_netsnmp_preamble "$raw")"

  case "$s" in
    *"Authentication failure"*)   echo "snmp_auth_failed_authpass" ;;
    *"Unknown user name"*)         echo "snmp_auth_failed_user" ;;
    *"authorizationError"*)        echo "snmp_auth_failed_security_level" ;;
    *"Decryption error"*)          echo "snmp_auth_failed_privpass" ;;
    *"Timeout"*)                   echo "timeout" ;;
    *"No Response"*)               echo "timeout" ;;
    *)                             echo "" ;;
  esac
}
