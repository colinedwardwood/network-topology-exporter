# Architectural Review: network-topology-exporter

**Date:** Thursday, May 14, 2026
**Scope:** Architectural and standards audit, May 2026.

---

## 1. Executive Summary
The `network-topology-exporter` project is a high-rigor implementation of network topology discovery. It successfully bridges legacy SNMP-based instrumentation with modern observability patterns (Prometheus/OpenTelemetry). The architecture is heavily influenced by IETF standards (RFC 6805, RFC 8345) and foundational academic research on topology reconciliation.

The project's "Source-for-spec, never source-for-code" policy (LD-09) has resulted in a clean-room implementation that avoids the licensing and technical debt common in legacy NMS tools.

---

## 2. Detailed Accounting of Deficiencies

### 2.1 OTLP Implementation Minimalism
The project implements OTLP/HTTP + JSON via hand-rolled structs and `encoding/json` rather than the official OpenTelemetry SDK. 
*   **Deficiency:** Lack of support for OTLP/gRPC or OTLP/HTTP + Protobuf.
*   **Impact:** Limits compatibility with high-throughput OTel collectors that require binary encoding.
*   **Risk:** Hardcoded schema URL (`1.21.0`) makes the exporter fragile to spec evolution that the official SDK would handle transparently.

### 2.2 Protocol & Addressing Gaps
*   **BGP IPv6 Support:** Implements RFC 4273 (BGP4-MIB), which is limited to IPv4. Lacks support for `BGP4-V2-MIB-MIB` or vendor-specific IPv6 tables, creating a blind spot in dual-stack core networks.
*   **Vendor-Specific Port Normalization:** `graph.NormalizePortName` is Cisco-centric. While it handles Junos `ge-` style names via passthrough, it lacks explicit normalization for Nokia (SR-OS) or Extreme, potentially leading to spurious reconciliation conflicts (LD-10).

### 2.3 Identity & Normalization Risks
*   **Aggressive Hostname Stripping:** Default `normalizeDeviceName` strips everything after the first dot. In multi-DC federation, `core-sw.dc1` and `core-sw.dc2` will collide unless `StrictDeviceNameMatching` is enabled.
*   **MAC Identity Dependency:** FDB-derived edges are suppressed if the remote MAC cannot be resolved to a sysName via LLDP. This makes topology visibility strictly dependent on LLDP presence on the neighbor.

### 2.4 Concurrency & Performance
*   **SNMP Timeout Race:** Use of `SetDeadline` to interrupt `gosnmp` reads is a known workaround but remains susceptible to library-level race conditions during connection teardown.
*   **Scrape Latency:** The exporter does not paginate `/metrics`. For 10k+ edges, the multi-megabyte payload may exceed scraper timeouts. There is no mechanism for partial graph emission.

### 2.5 Federation Stitching
*   **External Dependency:** Uncoordinated mode (LD-15) relies on external TSDB (Mimir/Prometheus) recording rules to join labels. If the rule evaluation is out of sync with the discovery cycle, the "Confirmed Edge" metric will be unstable.

---

## 3. Standards Compliance Matrix

| Feature | Standard Reference | Assessment |
| :--- | :--- | :--- |
| **Federation** | RFC 6805 (H-PCE) | **Excellent.** Faithfully implements child-to-parent push with pre-reconciliation. |
| **BGP Adjacency** | RFC 4273 | **Partial.** Correct for IPv4; missing IPv6 support (BGP4-V2). |
| **LLDP Discovery** | IEEE 802.1AB-2016 | **Exhaustive.** Handles complex chassis/port ID subtypes and address families. |
| **FDB Topology** | IEEE 802.1Q / RFC 4188 | **High Rigor.** Uses STP state filtering and per-VLAN communities. |
| **Reconciliation** | Breitbart et al. (2004) | **Excellent.** Implements conflict-surfacing rather than silent arbitration. |

---

## 4. Final Recommendations

1.  **Strict Identity:** Enable `StrictDeviceNameMatching` by default in multi-site deployments to prevent DC-level hostname collisions.
2.  **SDK Transition:** Consider migrating the OTLP output to the official OpenTelemetry Go SDK to gain gRPC support and better spec compliance.
3.  **IPv6 Roadmap:** Prioritize `BGP4-V2-MIB-MIB` support to eliminate the IPv6 visibility gap.
4.  **Pagination:** Investigate a "chunked" or "paginated" metrics output mode for massive topologies to preserve scrape reliability.

