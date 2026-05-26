#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_sanity.sh'
}

@test "require_tool succeeds when tool exists" {
  run require_tool bash
  [ "$status" -eq 0 ]
}

@test "require_tool fails when tool missing" {
  run require_tool definitely-not-a-real-tool-xyz
  [ "$status" -eq 3 ]
}

@test "detect_timeout returns timeout on linux/CI" {
  if command -v timeout >/dev/null 2>&1; then
    result="$(detect_timeout)"
    [ "$result" = "timeout" ]
  else
    skip "no timeout available"
  fi
}

@test "detect_sha256 returns sha256sum or shasum" {
  result="$(detect_sha256)"
  [ "$result" = "sha256sum" ] || [ "$result" = "shasum -a 256" ]
}
