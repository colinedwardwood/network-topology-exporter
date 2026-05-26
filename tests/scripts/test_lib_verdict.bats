#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_verdict.sh'
}

@test "verdict capture_ok when vendor table has rows" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok-rows" "5" "ok-rows" "ok-rows" "3")"
  [ "$result" = "capture_ok" ]
}

@test "verdict view_restriction when bgp up + vendor empty + rfc4273 rows" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-rows" "ok-empty" "0")"
  [ "$result" = "vendor_table_empty_view_restriction_likely" ]
}

@test "verdict bgp_mib_module_absent when bgpVersion noSuchObject" {
  result="$(pick_verdict "match:sysObjectID" "ok" "noSuchObject" "0" "noSuchObject" "noSuchObject" "0")"
  [ "$result" = "bgp_mib_module_absent" ]
}

@test "verdict bgp_up_but_no_peers" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-empty" "ok-empty" "0")"
  [ "$result" = "bgp_up_but_no_peers" ]
}

@test "verdict vendor_mismatch short-circuits BGP scenarios" {
  result="$(pick_verdict "miss" "ok" "noSuchObject" "0" "noSuchObject" "noSuchObject" "0")"
  [ "$result" = "snmp_reachable_vendor_mismatch" ]
}

@test "verdict auth_failed_user takes precedence" {
  result="$(pick_verdict "miss" "snmp_auth_failed_user" "" "" "" "" "")"
  [ "$result" = "snmp_auth_failed_user" ]
}

@test "verdict snmp_unreachable" {
  result="$(pick_verdict "miss" "snmp_unreachable" "" "" "" "" "")"
  [ "$result" = "snmp_unreachable" ]
}

@test "verdict vendor_table_empty_mib_not_implemented" {
  result="$(pick_verdict "match:sysObjectID" "ok" "ok" "0" "ok-empty" "noSuchObject" "0")"
  [ "$result" = "vendor_table_empty_mib_not_implemented" ]
}
