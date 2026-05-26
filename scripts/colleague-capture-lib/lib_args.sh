#!/usr/bin/env bash
# CLI argument parsing. Populates globals consumed by other libs.

# Defaults — all vars below are consumed by callers after sourcing this lib.
# shellcheck disable=SC2034
HOSTS=()
SNMP_VERSION=""
COMMUNITY=""
V3_USER=""
V3_AUTH_PROTO=""
V3_AUTH_PASS=""
V3_PRIV_PROTO=""
V3_PRIV_PASS=""
DRY_RUN=0
PREFLIGHT_ONLY=0
PER_OID_TIMEOUT="60"
TOTAL_TIMEOUT="300"
RETRIES="1"
PER_OID_PDU_CAP="50000"

_args_die() {
  echo "argument error: $*" >&2
  return 2
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -h) HOSTS+=("$2"); shift 2 ;;
      -c) COMMUNITY="$2"; SNMP_VERSION="2c"; shift 2 ;;
      -V) SNMP_VERSION="$2"; shift 2 ;;
      -u) V3_USER="$2"; shift 2 ;;
      -a) V3_AUTH_PROTO="$2"; shift 2 ;;
      -A) V3_AUTH_PASS="$2"; shift 2 ;;
      -x) V3_PRIV_PROTO="$2"; shift 2 ;;
      -X) V3_PRIV_PASS="$2"; shift 2 ;;
      --dry-run) DRY_RUN=1; shift ;;
      --preflight-only) PREFLIGHT_ONLY=1; shift ;;
      --per-oid-timeout) PER_OID_TIMEOUT="$2"; shift 2 ;;
      --total-timeout) TOTAL_TIMEOUT="$2"; shift 2 ;;
      --retries) RETRIES="$2"; shift 2 ;;
      --per-oid-pdu-cap) PER_OID_PDU_CAP="$2"; shift 2 ;;
      *) _args_die "unknown argument: $1"; return 2 ;;
    esac
  done

  # Validate
  [ "${#HOSTS[@]}" -eq 0 ] && { _args_die "at least one -h HOST is required"; return 2; }

  if [ -n "$COMMUNITY" ] && [ "$SNMP_VERSION" = "3" ]; then
    _args_die "cannot mix -c (v2c) and -V 3 (v3) on the same invocation"; return 2
  fi

  if [ -z "$COMMUNITY" ] && [ "$SNMP_VERSION" != "3" ]; then
    _args_die "must specify either -c COMMUNITY (v2c) or -V 3 ... (v3)"; return 2
  fi

  if [ "$SNMP_VERSION" = "3" ]; then
    [ -z "$V3_USER" ] && { _args_die "v3 requires -u USER"; return 2; }
    [ -z "$V3_AUTH_PROTO" ] && { _args_die "v3 requires -a AUTH_PROTO"; return 2; }
    [ -z "$V3_AUTH_PASS" ] && { _args_die "v3 requires -A AUTH_PASS"; return 2; }
    [ -z "$V3_PRIV_PROTO" ] && { _args_die "v3 requires -x PRIV_PROTO"; return 2; }
    [ -z "$V3_PRIV_PASS" ] && { _args_die "v3 requires -X PRIV_PASS"; return 2; }
  fi

  return 0
}
