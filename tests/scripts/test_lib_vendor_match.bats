#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_vendor_match.sh'
  EXPECTED_SYSOBJECTID_PREFIX="1.3.6.1.4.1.2636"
  SYSDESCR_KEYWORDS=("juniper" "junos")
}

@test "vendor_match prefix hit" {
  result="$(vendor_match "1.3.6.1.4.1.2636.1.1.1.2.57" "irrelevant")"
  [ "$result" = "match:sysObjectID" ]
}

@test "vendor_match keyword hit on sysDescr" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Juniper Networks, Inc. mx240")"
  [ "$result" = "match:sysDescr" ]
}

@test "vendor_match keyword case-insensitive" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Some JUNOS system")"
  [ "$result" = "match:sysDescr" ]
}

@test "vendor_match miss on both" {
  result="$(vendor_match "1.3.6.1.4.1.8072.3.2.10" "Ubiquiti UniFi UDM-Pro")"
  [ "$result" = "miss" ]
}
