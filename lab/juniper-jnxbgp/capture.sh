#!/usr/bin/env bash
#
# capture.sh — capture jnxBgpM2PeerTable (+ context OIDs) from the vJunos-router
# in the juniper-jnxbgp lab and save them under ./captures/ as the #56 fixture,
# named r1_juniper_<label>.txt (numeric -On -Oe form, paste-ready for tests).
#
# WHY THE CAPTURE RUNS FROM THE FRR PEER, NOT THE HOST:
#   vrnetlab fronts vJunos's fxp0 management interface with a container-level
#   NAT that does not forward UDP 161, so SNMP to the lab management IP just
#   times out. A real exporter queries a device over the network it is attached
#   to — so we do the same: snmpwalk runs from the directly-attached FRR peer
#   (p1) against r1's data-plane address (ge-0/0/0, 192.0.2.1), where Junos
#   snmpd answers in the default routing instance.
#
# Prereqs (run on the lab host; needs docker):
#   - `containerlab deploy -t topology.clab.yml` is up.
#   - r1 is CONFIGURED. NB: on some vJunos-router builds the containerlab config
#     drive is not consumed and r1 boots to factory default — see README.md
#     "If r1 boots unconfigured" for the one-time console config.
#   - both eBGP sessions are Established (check: docker exec p1 vtysh -c "show bgp summary").
#
# Exit code is 0 even if individual walks come back empty — a "No Such Object"
# is itself useful evidence (the device doesn't implement that MIB).
set -o pipefail

PEER="${PEER_CONTAINER:-clab-juniper-jnxbgp-p1}"   # directly-attached SNMP client (FRR)
TARGET="${R1_DATA_IP:-192.0.2.1}"                  # r1 ge-0/0/0 (data plane)
COMMUNITY="${SNMP_COMMUNITY:-public}"
NODE="r1"
OUTDIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)/captures"

# OID root | filename label. Comments note which walker each feeds in
# internal/discovery/bgp/.
OIDS=(
  "1.3.6.1.2.1.1|sys_group"                        # sysDescr + sysObjectID — vendor detection (expect .1.3.6.1.4.1.2636.x)
  "1.3.6.1.2.1.15.3|rfc4273_bgpPeerTable"          # RFC 4273 IPv4 baseline — walker: rfc4273 (fallback)
  "1.3.6.1.4.1.2636.5.1.1.2.1.1|jnxBgpM2PeerTable" # THE TARGET — walker: vendor_juniper (#56). Verifies colState/colRemoteAs/index decoder.
  "1.3.6.1.4.1.2636.5.1.1.2.6|jnxBgpM2RouteTable"  # adjacent table — captured for context
)

# Ensure net-snmp tools are present in the (alpine) FRR peer container.
docker exec "$PEER" sh -c 'command -v snmpwalk >/dev/null 2>&1 || apk add --no-cache net-snmp-tools >/dev/null 2>&1' 2>/dev/null
if ! docker exec "$PEER" sh -c 'command -v snmpwalk >/dev/null 2>&1'; then
  echo "ERROR: snmpwalk unavailable in ${PEER} (is the lab deployed? is ${PEER} the right container?)" >&2
  exit 2
fi
mkdir -p "$OUTDIR"

snmpq() { docker exec "$PEER" "$@"; }

sysoid=$(snmpq snmpget -v2c -c "$COMMUNITY" -On -Oqv -t4 -r2 "$TARGET" 1.3.6.1.2.1.1.2.0 2>/dev/null | tr -d '"\r')
sysdescr=$(snmpq snmpget -v2c -c "$COMMUNITY" -Oqv -t4 -r2 "$TARGET" 1.3.6.1.2.1.1.1.0 2>/dev/null | tr -d '"\r' | head -c 120)

echo "--- ${NODE} via ${PEER} -> ${TARGET} (data plane) ---  sysObjectID=${sysoid:-?}"
for entry in "${OIDS[@]}"; do
  oid="${entry%%|*}"; label="${entry#*|}"
  out="${OUTDIR}/${NODE}_juniper_${label}.txt"
  {
    echo "# captured $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# device: vJunos-router, node=${NODE}, queried via peer ${PEER} -> ${TARGET} (data plane)"
    echo "# sysDescr: ${sysdescr:-?}"
    echo "# sysObjectID: ${sysoid:-?}"
    echo "# oid root: ${oid} (${label})"
    echo
    snmpq snmpwalk -v2c -c "${COMMUNITY}" -On -Oe -t4 -r2 "${TARGET}" "${oid}" 2>&1 | tr -d '\r'
  } > "${out}"
  if grep -qiE "No Such Object|No Such Instance|Timeout|No Response" "${out}"; then
    echo "  ${label}: empty/error -> ${out#"$(dirname "$0")"/}"
  else
    echo "  ${label}: $(grep -c '=' "${out}") rows -> ${out#"$(dirname "$0")"/}"
  fi
done

echo
echo "Captures in ${OUTDIR}/"
echo "Key file for #56: ${OUTDIR}/${NODE}_juniper_jnxBgpM2PeerTable.txt"
echo "Addresses are documentation ranges (RFC 5737 192.0.2.x / RFC 3849 2001:db8::),"
echo "so no redaction is needed; scripts/redact-snmp-capture.py is available if you want it."
