#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_faults.sh'
}

@test "match_fault detects Authentication failure" {
  stderr="No log handling enabled - using stderr logging
snmpwalk: Authentication failure (incorrect password, community or key)"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_authpass" ]
}

@test "match_fault detects Unknown user name" {
  stderr="snmpwalk: Unknown user name"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_user" ]
}

@test "match_fault detects authorizationError" {
  stderr="Error in packet.
Reason: authorizationError (access denied to that object)"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_security_level" ]
}

@test "match_fault detects Decryption error" {
  stderr="snmpwalk: Decryption error"
  result="$(match_fault "$stderr")"
  [ "$result" = "snmp_auth_failed_privpass" ]
}

@test "match_fault detects timeout" {
  stderr="Timeout: No Response from 10.0.0.1"
  result="$(match_fault "$stderr")"
  [ "$result" = "timeout" ]
}

@test "match_fault returns empty for unknown stderr" {
  stderr="Something we have never seen before"
  result="$(match_fault "$stderr")"
  [ -z "$result" ]
}

@test "strip_netsnmp_preamble removes log handling line" {
  raw="No log handling enabled - using stderr logging
real error"
  result="$(strip_netsnmp_preamble "$raw")"
  [ "$result" = "real error" ]
}

@test "strip_netsnmp_preamble passes through unchanged when no preamble" {
  raw="real error"
  result="$(strip_netsnmp_preamble "$raw")"
  [ "$result" = "real error" ]
}
