#!/usr/bin/env bash
#
# capture.sh — drive snmpwalk against the cisco-iol-bgp lab and save raw
# output for each (node × OID-root) pair under ./captures/.
#
# Inputs:
#   - A running `containerlab deploy -t ./topology.clab.yml` on this host
#   - snmpwalk binary (net-snmp); jq (optional, used if available to derive
#     node IPs from `containerlab inspect`)
#
# Outputs:
#   captures/<node>__<oid-with-dots>.txt — one file per (node, oid-root)
#
# Each file holds the raw `snmpwalk -On -Oe` output. The numeric OID form
# is essential: the test fixture conversion step pastes these into
# []gsnmp.SnmpPDU literals where OID names would be ambiguous.
#
# Exit code is always 0 even if individual walks fail — partial captures
# are still useful (a walk that returns "no such object" tells us the
# device doesn't implement that MIB). Per-walk failures are logged.
#
set -o pipefail

LAB_NAME="arista-ceos-bgp"
COMMUNITY="${SNMP_COMMUNITY:-public}"
OUTDIR="$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "$0")")/captures"
declare -a NODES=()

# OID roots probed on every node. Comments explain which walker each one
# would feed in internal/discovery/bgp/.
declare -a OIDS=(
  # sys group — sysDescr + sysObjectID. Confirms the device responds at all
  # and lets us verify the Cisco enterprise-prefix vendor detection works.
  "1.3.6.1.2.1.1"

  # RFC 4273 bgpPeerTable — IPv4-only baseline. Walker: rfc4273 (fallback).
  "1.3.6.1.2.1.15.3"

  # CISCO-BGP4-MIB cbgpPeer2Table — Cisco vendor table.
  # Walker: vendor_cisco. Column numbers verified 2026-05-16 against
  # the cisco-iol-bgp lab (state=3, remoteAs=11; peer IP in index).
  "1.3.6.1.4.1.9.9.187.1.2.5"

  # IETF draft bgp4V2PeerTable. Captured for evidence — historically
  # the target of the (now removed) v2_draft walker. Probe shows no
  # vendor implements this at 1.3.6.1.3.5; see issue #31.
  "1.3.6.1.3.5.1.1.2"
)

mkdir -p "$OUTDIR"

# Build the (name, host) list. Prefer `containerlab inspect` JSON if jq
# is available; fall back to the topology.clab.yml mgmt-ipv4 pins.
# Note: `containerlab inspect` typically requires sudo because the lab
# was deployed under sudo; we use it here with the assumption that the
# caller has sudo nopasswd or runs this script with sudo. If neither,
# the fallback path takes over.
if command -v jq >/dev/null 2>&1 && command -v containerlab >/dev/null 2>&1; then
  while IFS= read -r line; do
    [ -n "$line" ] && NODES+=("$line")
  done < <(sudo -n containerlab inspect --name "$LAB_NAME" --format json 2>/dev/null \
    | jq -r '.containers[] // empty | "\(.name) \(.ipv4_address)"' 2>/dev/null \
    | sed 's|/[0-9]*$||')
fi
if [ ${#NODES[@]} -eq 0 ]; then
  echo "containerlab inspect failed or jq missing; using topology pinned mgmt-ipv4" >&2
  NODES=(
    "clab-${LAB_NAME}-r1 172.20.20.13"
    "clab-${LAB_NAME}-r2 172.20.20.12"
  )
fi

for entry in "${NODES[@]}"; do
  read -r name host <<<"$entry"
  echo "--- node ${name} (${host}) ---"
  for oid in "${OIDS[@]}"; do
    safe_oid=$(echo "$oid" | tr '.' '_')
    out="${OUTDIR}/${name}__${safe_oid}.txt"
    {
      echo "# captured at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
      echo "# node:   ${name}"
      echo "# host:   ${host}"
      echo "# oid:    ${oid}"
      echo "# walker fed: see comments in capture.sh"
      echo
      snmpwalk -v2c -c "${COMMUNITY}" -On -Oe -Ovq=0 "${host}" "${oid}" 2>&1
    } > "${out}"
    if grep -q "No Such Object\|No Such Instance\|Timeout\|No Response" "${out}"; then
      echo "  ${oid} → empty/error (see ${out#$(dirname "$0")/})"
    else
      lines=$(wc -l < "${out}" | tr -d ' ')
      echo "  ${oid} → ${lines} lines captured (${out#$(dirname "$0")/})"
    fi
  done
done

echo
echo "captures complete in ${OUTDIR}/"
echo "next: review each file by hand; promising captures get translated into"
echo "[]gsnmp.SnmpPDU literals under internal/snmptest/testdata/ — see README.md"
