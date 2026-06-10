#!/bin/sh
set -e

# The node name (e.g. "spine1") is the Docker container hostname set by
# containerlab for the linux kind. Strip the "clab-<topo>-" prefix if present
# so that the sysName advertised via SNMP and LLDP matches the logical name.
RAW=$(hostname)
SYSNAME=$(echo "$RAW" | sed 's/^clab-[A-Za-z0-9_-]*-//')

# Reset the kernel hostname to the logical name so lldpd advertises the short
# name (e.g. "spine1") as the LLDP system name TLV, not the full container ID.
hostname "$SYSNAME"

# Write a minimal snmpd config: SNMPv2c community "public", an SNMPv3
# authPriv USM user (SHA auth / AES privacy) so the exporter's v3 walk path
# gets live-agent coverage, and AgentX master. createUser is consumed by
# snmpd at startup and converted to localised keys; the container is
# ephemeral so no persistent-store handling is needed.
cat > /tmp/snmpd.conf << EOF
rocommunity public
createUser nte-e2e-v3 SHA nte-auth-pass AES nte-priv-pass
rouser nte-e2e-v3 priv
sysName $SYSNAME
master agentx
agentaddress udp:0.0.0.0:161
agentXSocket /var/agentx/master
EOF

mkdir -p /var/agentx
snmpd -C -c /tmp/snmpd.conf -f -Lo -p /var/run/snmpd.pid &

# Wait for the AgentX socket before starting lldpd as a sub-agent.
for i in $(seq 1 20); do
    [ -S /var/agentx/master ] && break
    sleep 0.5
done

# containerlab attaches data-plane interfaces (eth1, eth2, ...) after the
# container starts. Wait up to 30s for eth1 to come up so lldpd's initial
# scan picks it up. If eth1 never appears (long-running-lab base deploy
# attaches links lazily via the mutator), start lldpd anyway and rely on
# its netlink listener to handle RTM_NEWLINK events for later additions.
for _ in $(seq 1 60); do
    ip link show eth1 2>/dev/null | grep -q "LOWER_UP" && break
    sleep 0.5
done

# -d: foreground mode (do not daemonize — keeps this script as PID 1 supervisor)
# -x: register with AgentX master (snmpd)
# -I eth*,!eth0: include all eth interfaces, exclude eth0 (the containerlab
#   management interface) so only data-plane links (eth1, eth2, ...) participate
#   in LLDP. An exclusion-only pattern like !eth0 is broken in lldpd 1.0.18 —
#   it defaults to "include nothing" unless at least one positive pattern is
#   present. eth* covers the positive side.
# System name TLV comes from the kernel hostname, set above via hostname(1).
lldpd -d -x -I 'eth*,!eth0' &

# Keep this script running as PID 1; exit when any background daemon exits.
wait
