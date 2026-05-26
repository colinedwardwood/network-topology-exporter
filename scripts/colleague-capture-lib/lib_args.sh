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

_args_need_value() {
  # Called as: _args_need_value "$@"  (with the full remaining args)
  # $1 is the current flag; $2 should be its value. Errors out if value is
  # missing or looks like another flag.
  if [ $# -lt 2 ]; then
    _args_die "$1 requires a value"; return 2
  fi
  case "$2" in
    -*) _args_die "$1 requires a value, got flag '$2'"; return 2 ;;
  esac
  return 0
}

parse_args() {
  local saw_dash_c=0
  while [ $# -gt 0 ]; do
    case "$1" in
      -h)
        _args_need_value "$@" || return 2
        HOSTS+=("$2"); shift 2 ;;
      -c)
        _args_need_value "$@" || return 2
        COMMUNITY="$2"; saw_dash_c=1; shift 2 ;;
      -V)
        _args_need_value "$@" || return 2
        SNMP_VERSION="$2"; shift 2 ;;
      -u)
        _args_need_value "$@" || return 2
        V3_USER="$2"; shift 2 ;;
      -a)
        _args_need_value "$@" || return 2
        V3_AUTH_PROTO="$2"; shift 2 ;;
      -A)
        _args_need_value "$@" || return 2
        V3_AUTH_PASS="$2"; shift 2 ;;
      -x)
        _args_need_value "$@" || return 2
        V3_PRIV_PROTO="$2"; shift 2 ;;
      -X)
        _args_need_value "$@" || return 2
        V3_PRIV_PASS="$2"; shift 2 ;;
      --dry-run) DRY_RUN=1; shift ;;
      --preflight-only) PREFLIGHT_ONLY=1; shift ;;
      --per-oid-timeout)
        _args_need_value "$@" || return 2
        PER_OID_TIMEOUT="$2"; shift 2 ;;
      --total-timeout)
        _args_need_value "$@" || return 2
        TOTAL_TIMEOUT="$2"; shift 2 ;;
      --retries)
        _args_need_value "$@" || return 2
        RETRIES="$2"; shift 2 ;;
      --per-oid-pdu-cap)
        _args_need_value "$@" || return 2
        PER_OID_PDU_CAP="$2"; shift 2 ;;
      *) _args_die "unknown argument: $1"; return 2 ;;
    esac
  done

  # Resolve mutex: -c implies v2c; reject if both -c and -V 3
  if [ "$saw_dash_c" -eq 1 ] && [ "$SNMP_VERSION" = "3" ]; then
    _args_die "cannot mix -c (v2c) and -V 3 (v3) on the same invocation"; return 2
  fi
  if [ "$saw_dash_c" -eq 1 ] && [ -z "$SNMP_VERSION" ]; then
    SNMP_VERSION="2c"
  fi

  [ "${#HOSTS[@]}" -eq 0 ] && { _args_die "at least one -h HOST is required"; return 2; }

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
