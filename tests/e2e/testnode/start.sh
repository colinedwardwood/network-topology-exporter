#!/bin/sh
set -e

# The node name (e.g. "spine1") is the Docker container hostname set by
# containerlab for the linux kind. Strip the "clab-<topo>-" prefix if present
# so that the sysName advertised via SNMP and LLDP matches the logical name.
RAW=$(hostname)
SYSNAME=$(echo "$RAW" | sed 's/^clab-[A-Za-z0-9_-]*-//')

# Write a minimal snmpd config: SNMPv2c community "public", AgentX master.
cat > /tmp/snmpd.conf << EOF
rocommunity public
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

# -d: foreground (debug) mode  -x: register with AgentX master (snmpd)
# -S: override sysName so it matches the logical node name, not the container ID.
# -I !eth0: exclude the containerlab management interface so only the data-plane
#   links (eth1, eth2, ...) participate in LLDP. Without this, all nodes see
#   each other via the shared management network, which breaks topology assertions.
exec lldpd -d -x -S "$SYSNAME" -I '!eth0'
