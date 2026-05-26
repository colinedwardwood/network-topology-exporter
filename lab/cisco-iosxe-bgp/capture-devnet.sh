#!/usr/bin/env bash
#
# capture.sh — drive snmpwalk against Cisco IOS-XE router(s) reachable
# over the DevNet Sandbox VPN, save raw output for each (host × OID-root)
# pair under ./captures/.
#
# Inputs:
#   - One or more router IPs as positional arguments
#   - Active openconnect VPN session to the DevNet Sandbox
#   - SNMP community matching what's configured on the router (default: public)
#   - snmpwalk binary (net-snmp)
#
# Outputs:
#   captures/<host-with-underscores>__<oid-with-dots>.txt
#
# Each file holds raw `snmpwalk -On -Oe` output. Numeric OID form is
# essential: fixture conversion pastes these into []gosnmp.SnmpPDU
# literals where OID names would be ambiguous.
#
# Unlike the containerlab-based sibling labs (arista-ceos-bgp,
# cisco-iol-bgp), there is no `containerlab inspect` here — the hosts
# come from the DevNet reservation email and are passed on the
# command line.
#
# Exit code is always 0 even if individual walks fail — partial
# captures are still useful (a walk that returns "No Such Object"
# proves the device doesn't implement that MIB).
#
# Usage:
#   ./capture.sh <ip1> [ip2 ...]
#   SNMP_COMMUNITY=mycomm ./capture.sh 10.10.20.48
#
set -o pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <router-ip> [router-ip ...]" >&2
  echo "" >&2
  echo "example (Option A, two-router CML):" >&2
  echo "  $0 10.10.20.48 10.10.20.49" >&2
  echo "" >&2
  echo "example (Option B, single-CSR + external speaker):" >&2
  echo "  $0 10.10.20.48" >&2
  exit 2
fi

COMMUNITY="${SNMP_COMMUNITY:-public}"
OUTDIR="$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "$0")")/captures"

# OID roots probed on every router. Comments explain which walker each
# one would feed in internal/discovery/bgp/.
declare -a OIDS=(
  # sys group — sysDescr + sysObjectID. Confirms SNMP reachability and
  # lets us verify the Cisco enterprise-prefix vendor detection works
  # (sysObjectID should start with 1.3.6.1.4.1.9.*).
  "1.3.6.1.2.1.1"

  # RFC 4273 bgpPeerTable — IPv4-only baseline. Walker: rfc4273 (fallback).
  # Populated whenever any IPv4 BGP session exists.
  "1.3.6.1.2.1.15.3"

  # CISCO-BGP4-MIB cbgpPeer2Table — the canonical Cisco vendor table.
  # Walker: vendor_cisco. THIS is the high-value capture for issue #1.
  "1.3.6.1.4.1.9.9.187.1.2.5"

  # IETF draft bgp4V2PeerTable. Captured for negative-result evidence:
  # IOS-XE does not implement this OID. See issue #31 lineage.
  "1.3.6.1.3.5.1.1.2"
)

mkdir -p "$OUTDIR"

# Sanity-check VPN reachability before walking. If the first router
# is unreachable, the operator forgot to start openconnect or the
# reservation expired.
first_host="$1"
if ! snmpwalk -v2c -c "${COMMUNITY}" -t 3 -r 0 "${first_host}" 1.3.6.1.2.1.1.1.0 >/dev/null 2>&1; then
  echo "ERROR: cannot reach ${first_host} via snmpwalk." >&2
  echo "  - Is openconnect running?  (ip addr show tun0)" >&2
  echo "  - Is the DevNet reservation still active?" >&2
  echo "  - Did you apply 'snmp-server community ${COMMUNITY} RO' on the router?" >&2
  exit 1
fi

for host in "$@"; do
  safe_host=$(echo "$host" | tr '.:' '__')
  echo "--- host ${host} ---"
  for oid in "${OIDS[@]}"; do
    safe_oid=$(echo "$oid" | tr '.' '_')
    out="${OUTDIR}/${safe_host}__${safe_oid}.txt"
    {
      echo "# captured at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
      echo "# host:   ${host}"
      echo "# oid:    ${oid}"
      echo "# source: DevNet Sandbox IOS-XE (see ../README.md)"
      echo "# walker fed: see comments in capture.sh"
      echo
      snmpwalk -v2c -c "${COMMUNITY}" -On -Oe "${host}" "${oid}" 2>&1
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
echo "[]gosnmp.SnmpPDU literals under internal/snmptest/testdata/ — see README.md"
