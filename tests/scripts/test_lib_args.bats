#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_args.sh'
}

@test "parse_args accepts v2c minimal" {
  parse_args -h 10.0.0.1 -c public
  [ "${#HOSTS[@]}" -eq 1 ]
  [ "${HOSTS[0]}" = "10.0.0.1" ]
  [ "$SNMP_VERSION" = "2c" ]
  [ "$COMMUNITY" = "public" ]
}

@test "parse_args accepts multiple hosts" {
  parse_args -h 10.0.0.1 -h 10.0.0.2 -h 10.0.0.3 -c public
  [ "${#HOSTS[@]}" -eq 3 ]
  [ "${HOSTS[2]}" = "10.0.0.3" ]
}

@test "parse_args accepts v3 authPriv" {
  parse_args -h 10.0.0.1 -V 3 -u monitor -a SHA -A authpw -x AES -X privpw
  [ "$SNMP_VERSION" = "3" ]
  [ "$V3_USER" = "monitor" ]
  [ "$V3_AUTH_PROTO" = "SHA" ]
  [ "$V3_AUTH_PASS" = "authpw" ]
  [ "$V3_PRIV_PROTO" = "AES" ]
  [ "$V3_PRIV_PASS" = "privpw" ]
}

@test "parse_args sets dry_run flag" {
  parse_args -h 10.0.0.1 -c public --dry-run
  [ "$DRY_RUN" -eq 1 ]
}

@test "parse_args sets preflight_only flag" {
  parse_args -h 10.0.0.1 -c public --preflight-only
  [ "$PREFLIGHT_ONLY" -eq 1 ]
}

@test "parse_args accepts tunables" {
  parse_args -h 10.0.0.1 -c public --per-oid-timeout 30 --total-timeout 120 --retries 3 --per-oid-pdu-cap 1000
  [ "$PER_OID_TIMEOUT" = "30" ]
  [ "$TOTAL_TIMEOUT" = "120" ]
  [ "$RETRIES" = "3" ]
  [ "$PER_OID_PDU_CAP" = "1000" ]
}

@test "parse_args rejects mixing v2c and v3" {
  run parse_args -h 10.0.0.1 -c public -V 3 -u u -a SHA -A a -x AES -X p
  [ "$status" -ne 0 ]
}

@test "parse_args rejects missing host" {
  run parse_args -c public
  [ "$status" -ne 0 ]
}

@test "parse_args rejects missing auth" {
  run parse_args -h 10.0.0.1
  [ "$status" -ne 0 ]
}
