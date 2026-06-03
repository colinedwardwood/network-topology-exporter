# Vendor Comparison Matrix

**Date:** 2026-06-03
**Scope:** Topology discovery capabilities and Prometheus/Grafana-stack integration. This is not a general feature comparison — it compares only the surfaces relevant to an operator who already runs Prometheus/Mimir/Grafana and wants topology data as a signal.

For a deep bilateral read on LibreNMS specifically, see [`librenms.md`](librenms.md).

---

## Capability matrix

Legend: **✅ supported** / **⚠ partial** (see caveat) / **❌ not supported**

| Feature | network-topology-exporter | LibreNMS | SuzieQ | Nautobot | OpenNMS | SolarWinds NPM |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| **Topology discovery (LLDP/CDP)** | ✅ | ✅ | ✅ | ⚠ (plugin) | ✅ | ✅ |
| **Multi-vendor BGP4-V2 peers** | ✅ | ❌ | ⚠ (read from config, not MIB) | ❌ | ⚠ | ✅ |
| **OSPF adjacency discovery** | ✅ | ⚠ (polled, not topology-first) | ⚠ | ❌ | ✅ | ✅ |
| **IS-IS adjacency discovery** | ✅ | ⚠ (sensor-level only) | ⚠ | ❌ | ⚠ | ⚠ |
| **FDB (Layer-2 bridging)** | ✅ | ✅ | ⚠ | ❌ | ✅ | ✅ |
| **MPLS-TE tunnel topology** | ✅ | ❌ | ❌ | ❌ | ⚠ | ✅ |
| **Prometheus-native metric emission** | ✅ | ❌ | ❌ | ❌ | ⚠ (plugin) | ❌ |
| **OTLP push (metrics + logs)** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Multi-instance federation** | ✅ mTLS hub/spoke | ⚠ shared DB | ❌ | ✅ (via API) | ⚠ distributed pollers | ✅ (proprietary) |
| **MIB clean-room policy** | ✅ | ❌ (GPL accumulation) | N/A (read-only) | N/A | ❌ | N/A (proprietary) |
| **No bespoke database required** | ✅ | ❌ (MariaDB + Redis) | ❌ (SQLite/Postgres) | ❌ (Postgres) | ❌ (Postgres + Cassandra) | ❌ (proprietary DB) |
| **Single-binary footprint** | ✅ ~18 MB | ❌ PHP stack | ⚠ Python + DB | ❌ Django stack | ❌ Java stack | ❌ Windows service stack |
| **Real-time discovery (sub-60s cycle)** | ✅ (default 60s, configurable) | ❌ (6-hour default) | ❌ (poll-on-demand) | ❌ (scheduled) | ⚠ (configurable, but heavyweight) | ⚠ (configurable) |
| **Multi-source conflict detection** | ✅ counter + log | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Structured change events** | ✅ JSON log lines | ❌ | ❌ | ✅ (webhook) | ⚠ | ✅ |
| **AGPL / open-source** | ✅ AGPL-3.0 | ✅ GPL-3.0 | ✅ Apache-2.0 | ✅ Apache-2.0 | ✅ AGPL-3.0 | ❌ proprietary |
| **Grafana-native (no extra UI)** | ✅ | ❌ (own UI) | ❌ (own TUI) | ❌ (own UI) | ❌ (own UI) | ❌ (own UI) |
| **SNMPv3 with authPriv** | ✅ SHA-family + AES-family | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Credential zeroization** | ✅ `[]byte` + `Zeroize()` | ❌ PHP strings | N/A | N/A | ❌ | N/A |

---

## Notes on each tool

### LibreNMS

Full-stack network management system (inventory, polling, syslog, alerting, topology maps). Topology is one of ~50 modules. If you do not already run Prometheus/Grafana, LibreNMS gives you discovery + visualization in a single product; the trade-off is a PHP+MariaDB+Redis runtime and a 6-hour default discovery cadence.

The bilateral comparison — code paths, data model, reconciliation, federation, and credential handling — is in [`librenms.md`](librenms.md).

### SuzieQ

Read-only network observability tool that connects via SSH/NETCONF/REST and normalises device state into a Parquet-backed data model. Excellent at querying current BGP/OSPF/LLDP state from a Python CLI; not an exporter, not a Prometheus-native emitter. No topology-change events. Complements this exporter if you want ad-hoc device-state queries alongside continuous metric emission.

### Nautobot

Network source-of-truth platform (successor to NetBox). Topology data lives in its model as an authoritative record, not as a discovery output. Plugin ecosystem can do LLDP/CDP discovery but the output is records, not metrics. The right tool for tracking intended topology; this exporter tracks observed topology. They compose: Nautobot holds ground-truth, the exporter surfaces deltas.

### OpenNMS

Java-based SNMP poller and network management system. Has Prometheus metric export via plugin but it's not native. OSPF/BGP discovery exists but is not structured as a first-class topology edge set. Federation is a distributed-poller model sharing state via a Postgres+Cassandra backend. AGPL-3.0 licensed. Heavier operationally than this exporter but broader in feature scope (syslog, flow, alarm correlation).

### SolarWinds NPM

Commercial, Windows-hosted, full-stack NMS. Strong topology discovery including MPLS, BGP, and OSPF. Proprietary data store and UI. Relevant for enterprises already committed to the SolarWinds ecosystem. **Not a replacement for this exporter in a Grafana/Prometheus stack** — NPM does not emit Prometheus metrics or OTLP.

---

## "Not trying to be SolarWinds"

SolarWinds NPM is a full NMS: it combines topology discovery, IPSLA probe management, NCM config backup, alert correlation, and a GUI into one product. This exporter is deliberately narrower: discovery → metric emission, nothing else. An operator using this exporter alongside Grafana still needs:

- **Network Configuration Management (NCM):** Rancid, Oxidized, Batfish, or the SolarWinds NCM module. The exporter does not read or diff device configs.
- **IPSLA / synthetic probes:** SmokePing, Blackbox Exporter (ICMP/HTTP probes), or SolarWinds IPSLA. The exporter discovers the topology; probe health is a separate signal.
- **Alert correlation / root-cause analysis:** Grafana Incident (ML correlation), or the classic SolarWinds "root-cause-analysis" engine which cross-correlates alerts with topology. The `network_topology_change_total` counter and change event log lines are the exporter's contribution to correlation — they tell you *what changed*, not *why an alert fired*.

If you need NCM + IPSLA + full alert correlation in one product, SolarWinds NPM (or OpenNMS, or LibreNMS for some of it) is the right shape. If you need topology as a signal inside a stack that already handles those concerns separately, this exporter fills the gap.

---

## Sources

Comparisons are based on public documentation and, where noted, open-source code:

- LibreNMS: [docs.librenms.org](https://docs.librenms.org), source at [github.com/librenms/librenms](https://github.com/librenms/librenms) — see [`librenms.md`](librenms.md) for code-level detail
- SuzieQ: [suzieq.readthedocs.io](https://suzieq.readthedocs.io), source at [github.com/netenglabs/suzieq](https://github.com/netenglabs/suzieq)
- Nautobot: [docs.nautobot.com](https://docs.nautobot.com), source at [github.com/nautobot/nautobot](https://github.com/nautobot/nautobot)
- OpenNMS: [docs.opennms.com](https://docs.opennms.com), source at [github.com/OpenNMS/opennms](https://github.com/OpenNMS/opennms)
- SolarWinds NPM: [documentation.solarwinds.com/en/success_center/npm/](https://documentation.solarwinds.com/en/success_center/npm/) (proprietary; documentation only)
