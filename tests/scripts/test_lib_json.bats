#!/usr/bin/env bats

setup() {
  load '../../scripts/colleague-capture-lib/lib_json.sh'
}

@test "json_string escapes basic ASCII unchanged" {
  result="$(json_string 'hello world')"
  [ "$result" = '"hello world"' ]
}

@test "json_string escapes double-quote" {
  result="$(json_string 'he said "hi"')"
  [ "$result" = '"he said \"hi\""' ]
}

@test "json_string escapes backslash" {
  result="$(json_string 'a\b')"
  [ "$result" = '"a\\b"' ]
}

@test "json_string escapes newline" {
  result="$(json_string $'line1\nline2')"
  [ "$result" = '"line1\nline2"' ]
}

@test "json_string handles empty" {
  result="$(json_string '')"
  [ "$result" = '""' ]
}

@test "json_null prints null" {
  result="$(json_null)"
  [ "$result" = 'null' ]
}

@test "json_bool true/false" {
  [ "$(json_bool 1)" = "true" ]
  [ "$(json_bool 0)" = "false" ]
}
