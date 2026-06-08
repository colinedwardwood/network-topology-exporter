#!/usr/bin/env bash
#
# capture.sh — drive snmpwalk against the juniper-jnxbgp vJunos-router lab and
# save raw output per OID root under ./captures/, named to match the fixture
# convention (r1_juniper_<label>.txt) so the jnxBgpM2PeerTable capture lands
# exactly where issue #56 expects it.
#
# Inputs:
#   - A running `containerlab deploy -t ./topology.clab.yml` on an x86-64+KVM
#     host, with r1 (vJunos-router) booted and both BGP sessions Established.
#   - snmpwalk (net-snmp).
#
# Outputs:
#   captures/r1_juniper_<label>.txt — one file per OID root, numeric form
#   (-On -Oe) so the values paste cleanly into []gsnmp.SnmpPDU test literals.
#
# Exit code is 0 even if individual walks come back empty — a "No Such Object"
# is itself useful evidence (the device doesn't implement that MIB).
set -o pipefail

HOST="${R1_HOST:-172.20.20.21}"          # r1 mgmt-ipv4 from topology.clab.yml
COMMUNITY="${SNMP_COMMUNITY:-public}"
NODE="r1"
OUTDIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)/captures"

# OID root | filename label. Comments note which walker each feeds in
# internal/discovery/bgp/.
OIDS=(
  "1.3.6.1.2.1.1|sys_group"                      # sysDescr + sysObjectID — vendor detection (expect .1.3.6.1.4.1.2636.x)
  "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable"        # RFC 4273 IPv4 baseline — walker: rfc4273 (fallback)
  "1.3.6.1.4.1.2636.5.1.1.2.1.1|jnxBgpM2PeerTable" # THE TARGET — walker: vendor_juniper (#56). Verifies colState/colRemoteAs/index decoder.
  "1.3.6.1.4.1.2636.5.1.1.2.6|jnxBgpM2RouteTable"  # adjacent table — captured for evidence/context
)

if ! command -v snmpwalk >/dev/null 2>&1; then
  echo "ERROR: snmpwalk not found (net-snmp). Linux: apt-get install snmp; macOS: brew install net-snmp" >&2
  exit 2
fi
mkdir -p "$OUTDIR"

# Best-effort: record the device sysObjectID/sysDescr in the headers.
sysoid=$(snmpget -v2c -c "$COMMUNITY" -On -Oqv "$HOST" 1.3.6.1.2.1.1.2.0 2>/dev/null | tr -d '"')
sysdescr=$(snmpget -v2c -c "$COMMUNITY" -Oqv "$HOST" 1.3.6.1.2.1.1.1.0 2>/dev/null | tr -d '"' | head -c 120)

echo "--- node ${NODE} (${HOST}) ---  sysObjectID=${sysoid:-?}"
for entry in "${OIDS[@]}"; do
  oid="${entry%%|*}"; label="${entry#*|}"
  out="${OUTDIR}/${NODE}_juniper_${label}.txt"
  {
    echo "# captured $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# device: vJunos-router, node=${NODE}, host=${HOST}"
    echo "# sysDescr: ${sysdescr:-?}"
    echo "# sysObjectID: ${sysoid:-?}"
    echo "# oid root: ${oid} (${label})"
    echo
    snmpwalk -v2c -c "${COMMUNITY}" -On -Oe "${HOST}" "${oid}" 2>&1
  } > "${out}"
  if grep -qiE "No Such Object|No Such Instance|Timeout|No Response" "${out}"; then
    echo "  ${label}: empty/error → ${out#"$(dirname "$0")"/}"
  else
    echo "  ${label}: $(grep -c '=' "${out}") rows → ${out#"$(dirname "$0")"/}"
  fi
done

echo
echo "Captures in ${OUTDIR}/"
echo "Key file for #56: ${OUTDIR}/${NODE}_juniper_jnxBgpM2PeerTable.txt"
echo "These are documentation lab addresses (RFC 5737 192.0.2.x / RFC 3849 2001:db8::), so no"
echo "redaction is required — but you can run scripts/redact-snmp-capture.py"
echo "if you prefer. Hand the file back and the walker spec gets verified."
