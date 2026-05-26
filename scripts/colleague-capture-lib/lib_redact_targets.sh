#!/usr/bin/env bash
# Scan capture files for IPv4, IPv6, and MAC values in supported encoding
# forms. Write a redaction-targets.json the receipt-side redactor consumes.
#
# Output: <captures_dir>/redaction-targets.json
# Schema: { "ipv4": ["a.b.c.d", ...], "ipv6": [...], "mac": [...] }
#
# Depends on lib_json.sh (json_string).

scan_for_redaction_targets() {
  local captures_dir="${1:-}"
  [ -z "$captures_dir" ] || [ ! -d "$captures_dir" ] && return 1
  local out="${captures_dir}/redaction-targets.json"

  local -a ipv4=() ipv6=() macs=()
  local file
  while IFS= read -r -d '' file; do
    # IPv4 typed values + dotted bare IPs
    while IFS= read -r ip; do
      [ -n "$ip" ] && ipv4+=("$ip")
    done < <(grep -oE '(IpAddress: )?[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' "$file" 2>/dev/null \
              | sed 's/IpAddress: //' \
              | awk '/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/' \
              | sort -u)

    # IPv6 typed values (loose match; redactor disambiguates)
    while IFS= read -r ip; do
      [ -n "$ip" ] && ipv6+=("$ip")
    done < <(grep -oE 'STRING: [0-9a-fA-F:]+:[0-9a-fA-F:]+' "$file" 2>/dev/null \
              | awk '{print $2}' \
              | grep ':.*:' \
              | sort -u)

    # MAC STRING form
    while IFS= read -r mac; do
      [ -n "$mac" ] && macs+=("$mac")
    done < <(grep -oE 'STRING: [0-9a-fA-F]{1,2}(:[0-9a-fA-F]{1,2}){5}' "$file" 2>/dev/null \
              | awk '{print $2}' \
              | sort -u)
  done < <(find "$captures_dir" -name 'r1_*.txt' -print0 2>/dev/null)

  # Emit JSON. json_string must be sourced before this is called.
  {
    echo "{"
    echo -n '  "ipv4": ['
    local i=0 v
    for v in "${ipv4[@]:-}"; do
      [ -z "$v" ] && continue
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "],"
    echo -n '  "ipv6": ['
    i=0
    for v in "${ipv6[@]:-}"; do
      [ -z "$v" ] && continue
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "],"
    echo -n '  "mac":  ['
    i=0
    for v in "${macs[@]:-}"; do
      [ -z "$v" ] && continue
      [ $i -gt 0 ] && echo -n ", "
      json_string "$v" | tr -d '\n'
      i=$((i+1))
    done
    echo "]"
    echo "}"
  } > "$out"
}
