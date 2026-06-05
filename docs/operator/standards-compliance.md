# Standards Compliance

## RFC 2922 / Physical Topology MIB (PTOPO-MIB)

RFC 2922 defines the Physical Topology MIB (PTOPO-MIB) for network topology discovery via SNMP.
This exporter does **not** implement PTOPO-MIB. The practical replacement is LLDP-MIB
(IEEE 802.1AB), which is deployed on all modern enterprise switches and provides equivalent
topology information with better vendor support.

What you lose by not polling PTOPO-MIB:
- No topology data from pre-LLDP devices (those without LLDP-MIB support).
- Physical-layer topology (cable plant, media type) is not surfaced; LLDP provides logical adjacency only.
- PTOPO-MIB chassis component inventory is not available.

If your fleet includes devices that only support PTOPO-MIB (typically very old infrastructure),
add explicit `credentials.assignments` for those devices and expect no topology edges from them.

## RFC 1213 / MIB-II

### sysName (1.3.6.1.2.1.1.5.0)
sysName is capped at 255 bytes before use as a graph key or Prometheus label
(RFC 1213 limit). Values exceeding this are silently truncated at a UTF-8 rune boundary (≤255 bytes), so a multi-byte rune is never split.
This is enforced in `snmputil.NormaliseName`.

### sysUpTime (1.3.6.1.2.1.1.3.0)
sysUpTime is a 32-bit counter of centiseconds since the last re-initialisation.
It wraps to zero after approximately 497 days. The exporter emits
`network_topology_device_uptime_seconds` as a gauge computed from the raw
sysUpTime value. **Wrap is not detected**. After 497 days of uptime, the gauge
will appear to reset to near-zero; this is an artifact of MIB-II, not a reboot.

Operators using uptime for change detection should treat small uptime values
(< 24 h) on devices with known-long uptimes as a probable wrap, not a reboot.
A future enhancement may emit a wrap counter backed by persisted previous ticks.

## IEEE 802.1AB (LLDP)

The exporter walks `lldpRemTable` (1.0.8802.1.1.2.1.4.1.1) for topology discovery.
Table liveness is determined by LLDP agent aging (IEEE 802.1AB-2016 §9.6.3):
agents remove expired entries before our walk, so no explicit TTL field check is needed.

Chassis ID subtype 7 (`local`) and Port ID subtype 7 (`local`) are treated as
opaque binary values. Non-UTF-8 bytes are hex-encoded rather than passed raw
to avoid invalid Prometheus label values.

## RFC 2863 / IF-MIB

Port names are resolved via ifXTable.ifName (RFC 2863 §3.1.4). If the agent
does not implement ifXTable, the exporter falls back to ifTable.ifDescr (§3.1.2).
If both fail, port names are synthesised as `"if{ifIndex}"`.

Note: ifDescr is not guaranteed to be unique across module boundaries on chassis
devices. On such devices, port names from the ifDescr fallback may collide across
line cards. Use explicit `credentials.assignments` with devices that exhibit this
and consider opening a feature request for ENTITY-MIB port enrichment.
