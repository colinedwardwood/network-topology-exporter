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
  if containerlab deploy -t "$BASE_TOPO" >/dev/null 2>&1; then
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

# True if interface $2 exists inside base node $1 (e.g. node_iface_exists leaf1 eth1).
node_iface_exists() {
  docker exec "clab-$LAB_NAME-$1" ip link show "$2" >/dev/null 2>&1
}

# Ensure one veth link exists, creating it if absent. Idempotent and
# self-healing: this is the core reliability guarantee. A veth create that
# fails transiently, or a veth lost out-of-band, must never leave a desired
# link permanently missing (a partial topology with no alertable signal).
# Endpoints are "node:iface" strings. Returns 0 if present/created, 1 if not.
ensure_link() {
  local a="$1" z="$2"
  local an="${a%%:*}" ai="${a#*:}" zn="${z%%:*}" zi="${z#*:}"
  local a_ok=1 z_ok=1
  node_iface_exists "$an" "$ai" || a_ok=0
  node_iface_exists "$zn" "$zi" || z_ok=0
  if [ "$a_ok" = 1 ] && [ "$z_ok" = 1 ]; then
    return 0   # both ends present — link already up
  fi
  # Half-broken pair (one end survived a partial create/teardown): remove the
  # dangling end so veth create can succeed cleanly.
  [ "$a_ok" = 1 ] && docker exec "clab-$LAB_NAME-$an" ip link del "$ai" >/dev/null 2>&1
  [ "$z_ok" = 1 ] && docker exec "clab-$LAB_NAME-$zn" ip link del "$zi" >/dev/null 2>&1
  local attempt
  for attempt in 1 2 3; do
    if containerlab tools veth create \
         --a-endpoint "clab-$LAB_NAME-$a" \
         --b-endpoint "clab-$LAB_NAME-$z" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
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

  local cur des del
  cur=$(parse_links "$cur_file")
  des=$(parse_links "$file")
  # Links the previous topology had that the target does not want.
  del=$(comm -23 <(printf '%s\n' "$cur") <(printf '%s\n' "$des"))

  local n_del n_des
  n_del=$(printf '%s' "$del" | grep -c .)
  n_des=$(printf '%s' "$des" | grep -c .)

  # Delete unwanted links first so freed eth slots are available for reuse.
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

  # Ensure EVERY desired link exists — not just the diff 'add' set. This is the
  # self-healing fix: a link that previously failed to attach or was lost
  # out-of-band used to sit in the 'keep' set and was never recreated, leaving a
  # permanent partial topology (e.g. a star missing one spoke) with no alertable
  # signal. We now verify each desired link and (re)create any that are missing,
  # with retries, every pass — so a transient veth failure self-corrects on the
  # next apply (and a same-topology re-apply is a valid recovery).
  local ensure_failures=0 n_created=0
  while IFS= read -r link; do
    [ -z "$link" ] && continue
    local a z
    a="${link%%|*}"; z="${link#*|}"
    if node_iface_exists "${a%%:*}" "${a#*:}" && node_iface_exists "${z%%:*}" "${z#*:}"; then
      continue   # both ends already present
    fi
    if ensure_link "$a" "$z"; then
      emit link_added "a=$a" "z=$z"
      n_created=$((n_created + 1))
    else
      emit link_add_failed "a=$a" "z=$z"
      ensure_failures=$((ensure_failures + 1))
    fi
  done <<< "$des"

  local dt=$(( $(date +%s) - t0 ))
  if [ "$ensure_failures" -eq 0 ]; then
    # Record success so the next mutation diffs removals from here. Because we
    # ensure all desired links every pass, re-applying the same topology now
    # also self-corrects any missing link.
    printf '%s\n' "$topo" > "$STATE_FILE"
    emit mutation_success "topo=$topo" "prev=$prev" "duration_s=!$dt" "links_desired=!$n_des" "links_created=!$n_created" "links_removed=!$n_del"
  else
    emit mutation_failed "topo=$topo" "phase=apply" "duration_s=!$dt" "ensure_failures=!$ensure_failures" "links_created=!$n_created"
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
