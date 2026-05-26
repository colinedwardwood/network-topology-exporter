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

@test "json_string escapes backspace" {
  result="$(json_string $'\b')"
  [ "$result" = '"\b"' ]
}

@test "json_string escapes formfeed" {
  result="$(json_string $'\f')"
  [ "$result" = '"\f"' ]
}

@test "json_string escapes tab" {
  result="$(json_string $'\t')"
  [ "$result" = '"\t"' ]
}

@test "json_string escapes carriage return" {
  result="$(json_string $'\r')"
  [ "$result" = '"\r"' ]
}

@test "json_string handles backslash immediately before n" {
  # Ensures backslash escape doesn't double-fire on top of \n escape
  result="$(json_string $'\\n')"
  [ "$result" = '"\\n"' ]
}

@test "json_bool no argument returns false (no crash under set -u)" {
  set -u
  result="$(json_bool)"
  [ "$result" = "false" ]
}

@test "json_bool non-binary integer returns false" {
  [ "$(json_bool 2)" = "false" ]
  [ "$(json_bool -1)" = "false" ]
}
