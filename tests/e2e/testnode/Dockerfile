FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# lldpd: IEEE 802.1AB daemon with AgentX SNMP sub-agent support (-x flag).
# net-snmp: master SNMP agent (snmpd) that lldpd registers LLDP-MIB into.
RUN apk add --no-cache lldpd net-snmp net-snmp-agent-libs

COPY start.sh /start.sh
RUN chmod +x /start.sh

# SNMP on UDP 161; lldpd listens on AgentX socket /var/agentx/master (default).
EXPOSE 161/udp

CMD ["/start.sh"]
