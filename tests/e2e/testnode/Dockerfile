FROM alpine:3.21

# lldpd: IEEE 802.1AB daemon with AgentX SNMP sub-agent support (-x flag).
# net-snmp: master SNMP agent (snmpd) that lldpd registers LLDP-MIB into.
RUN apk add --no-cache lldpd net-snmp net-snmp-agent-libs

COPY start.sh /start.sh
RUN chmod +x /start.sh

# SNMP on UDP 161; lldpd listens on AgentX socket /var/agentx/master (default).
EXPOSE 161/udp

CMD ["/start.sh"]
