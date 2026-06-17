FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11

# lldpd: IEEE 802.1AB daemon with AgentX SNMP sub-agent support (-x flag).
# net-snmp: master SNMP agent (snmpd) that lldpd registers LLDP-MIB into.
RUN apk add --no-cache lldpd net-snmp net-snmp-agent-libs

COPY start.sh /start.sh
RUN chmod +x /start.sh

# SNMP on UDP 161; lldpd listens on AgentX socket /var/agentx/master (default).
EXPOSE 161/udp

CMD ["/start.sh"]
