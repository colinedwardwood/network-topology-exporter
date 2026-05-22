#!/bin/bash
# Long-running lab mutator: applies the hourly target topology by
# reconciling veth links between always-on base nodes. Emits structured
# JSON events on stdout for Alloy to ship to Loki.
set -uo pipefail

TOPODIR="/work/topologies"
BASE_TOPO="/work/base.clab.yml"
STATE_FILE="/work/.last-topo"
NODES=(spine1 spine2 leaf1 leaf2 leaf3 leaf4)
LAB_NAME="nte-dynamic-1"
TOPOS=("topo-1.yml" "topo-2.yml" "topo-3.yml" "topo-4.yml")

# Emit a JSON line. First arg is the event name, remaining args are key=value
# pairs that become string fields. Numeric and boolean values must be passed
# as raw JSON via the prefix '!' (e.g., 'duration_s=!2.13').
emit() {
  local event="$1"; shift
  local ts; ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  local out="{\"time\":\"$ts\",\"event\":\"$event\""
  while [ "$#" -gt 0 ]; do
    local kv="$1"; shift
    local key="${kv%%=*}"
    local val="${kv#*=}"
    if [[ "$val" == "!"* ]]; then
      out+=",\"$key\":${val#!}"
    else
      val=$(printf '%s' "$val" | sed 's/\\/\\\\/g; s/"/\\"/g')
      out+=",\"$key\":\"$val\""
    fi
  done
  out+="}"
  printf '%s\n' "$out"
}

# Self-heal: if any base node is missing or not running, redeploy the base lab
# AND clear the state file so the next mutation does a full rebuild.
self_heal_if_needed() {
  local missing=()
  for n in "${NODES[@]}"; do
    local cname="clab-$LAB_NAME-$n"
    if ! docker inspect -f '{{.State.Running}}' "$cname" 2>/dev/null | grep -q true; then
      missing+=("$n")
    fi
  done
  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi
  emit self_heal_triggered "missing_count=!${#missing[@]}" "missing=${missing[*]}"
  local t0; t0=$(date +%s)
  containerlab destroy -t "$BASE_TOPO" --cleanup >/dev/null 2>&1 || true
  if containerlab deploy -t "$BASE_TOPO" 2>&1 >/dev/null; then
    : > "$STATE_FILE"    # truncate; next apply_topology will treat current as empty
    emit self_heal_success "duration_s=!$(( $(date +%s) - t0 ))"
    return 0
  else
    emit self_heal_failed "duration_s=!$(( $(date +%s) - t0 ))"
    return 1
  fi
}

# Parse a topology YAML's `links:` block into canonical "a:eth|b:eth" lines,
# one per line, sorted. Endpoints in each pair are lexicographically ordered
# so set operations work regardless of source ordering.
parse_links() {
  local file="$1"
  [ -r "$file" ] || return 0
  yq -r '.topology.links[].endpoints | (.[0] + "|" + .[1])' "$file" 2>/dev/null | \
    awk -F'|' '{ a=$1; b=$2; if (a < b) print a"|"b; else print b"|"a }' | sort -u
}

# Apply one mutation. $1 is the target topology filename (basename).
apply_topology() {
  local topo="$1"
  local file="$TOPODIR/$topo"
  local t0; t0=$(date +%s)

  emit mutation_start "topo=$topo"

  if [ ! -r "$file" ]; then
    emit mutation_failed "topo=$topo" "phase=read" "error=topology file not readable: $file"
    return 1
  fi

  # "Current" = whatever the last applied topology said. Empty if cold start.
  local prev=""
  [ -r "$STATE_FILE" ] && prev=$(tr -d '\n\r ' < "$STATE_FILE")
  local cur_file=""
  [ -n "$prev" ] && cur_file="$TOPODIR/$prev"

  local cur des add del keep
  cur=$(parse_links "$cur_file")
  des=$(parse_links "$file")
  add=$(comm -13 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))
  del=$(comm -23 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))
  keep=$(comm -12 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))

  local n_add n_del n_keep
  n_add=$(printf '%s' "$add" | grep -c .)
  n_del=$(printf '%s' "$del" | grep -c .)
  n_keep=$(printf '%s' "$keep" | grep -c .)

  # Delete first so freed eth slots are available for reuse on add.
  while IFS= read -r link; do
    [ -z "$link" ] && continue
    local left right node iface
    left="${link%%|*}"; right="${link#*|}"
    node="${left%%:*}"; iface="${left#*:}"
    if docker exec "clab-$LAB_NAME-$node" ip link del "$iface" >/dev/null 2>&1; then
      emit link_removed "a=$left" "z=$right"
    else
      emit link_remove_failed "a=$left" "z=$right"
    fi
  done <<< "$del"

  local add_failures=0
  while IFS= read -r link; do
    [ -z "$link" ] && continue
    local a z
    a="${link%%|*}"; z="${link#*|}"
    if containerlab tools veth create --name "$LAB_NAME" --a-endpoint "$a" --z-endpoint "$z" >/dev/null 2>&1; then
      emit link_added "a=$a" "z=$z"
    else
      emit link_add_failed "a=$a" "z=$z"
      add_failures=$((add_failures + 1))
    fi
  done <<< "$add"

  local dt=$(( $(date +%s) - t0 ))
  if [ "$add_failures" -eq 0 ]; then
    # Record success in the state file so the next mutation diffs from here.
    printf '%s\n' "$topo" > "$STATE_FILE"
    emit mutation_success "topo=$topo" "prev=$prev" "duration_s=!$dt" "links_added=!$n_add" "links_removed=!$n_del" "links_kept=!$n_keep"
  else
    emit mutation_failed "topo=$topo" "phase=apply" "duration_s=!$dt" "add_failures=!$add_failures"
    return 1
  fi
}

# --- main loop ---
emit mutator_starting "nodes=${NODES[*]}" "topos=${TOPOS[*]}"

self_heal_if_needed || emit mutator_self_heal_giving_up

LAST_HOUR=-1
while true; do
  HOUR=$(date -u +%H)
  if [ "$HOUR" != "$LAST_HOUR" ]; then
    INDEX=$(( 10#$HOUR % ${#TOPOS[@]} ))
    TOPO=${TOPOS[$INDEX]}
    apply_topology "$TOPO" || true
    LAST_HOUR="$HOUR"
  fi
  sleep 60
done
