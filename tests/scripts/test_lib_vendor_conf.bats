#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_vendor_conf.sh'
  FIXTURE_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/fixtures" && pwd)"
}

@test "load_vendor_conf sources the file" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "$VENDOR_NAME" = "testvendor" ]
  [ "$VENDOR_TABLE_OID" = "1.3.6.1.4.1.99999.1.1" ]
}

@test "load_vendor_conf exposes OIDS array" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "${#OIDS[@]}" -eq 2 ]
  [ "${OIDS[0]}" = "1.3.6.1.2.1.1|sys_group" ]
}

@test "load_vendor_conf exposes SYSDESCR_KEYWORDS" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  [ "${#SYSDESCR_KEYWORDS[@]}" -eq 2 ]
  [ "${SYSDESCR_KEYWORDS[1]}" = "tv-os" ]
}

@test "load_vendor_conf fails for missing file" {
  run load_vendor_conf /nonexistent/path/vendor.conf
  [ "$status" -ne 0 ]
}

@test "vendor_conf_required_keys passes for complete fixture" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  run vendor_conf_required_keys
  [ "$status" -eq 0 ]
}

@test "fallback_for returns mapped OID" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  result="$(fallback_for 1.3.6.1.4.1.99999.1.1)"
  [ "$result" = "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable_fallback" ]
}

@test "fallback_for returns empty for OID without fallback" {
  load_vendor_conf "${FIXTURE_DIR}/vendor.conf.example"
  result="$(fallback_for 1.3.6.1.2.1.1)"
  [ -z "$result" ]
}
