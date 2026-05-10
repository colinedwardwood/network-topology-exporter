# Engineering Improvement Plan: Network Topology Exporter

All items shipped. See git log for details.

## Completed work

**§2 Immediate Remediation** — FDB cardinality fix, insecure community fallback removed, metric name corrected, snapshot writer bounded, silent FDB errors surfaced, label input hardened. Hub race, OTLP double-prefix, and device-dedup fixed from adversarial review.

**§3 Architectural** — Cycle budget controller (`CycleBudgetFraction`), per-module deadline partitioning (`TimeoutPerModule`), two-phase graph assembly (LLDP annotates `peer_chassis_mac`; synthesis layer resolves MACs to sysNames or hashes them), memory circuit-breaker (`MaxGraphEdges`/`MaxGraphDevices`), cardinality + schema CI.

**§4 Standards** — IEEE 802.1AB chassis/port subtype validation, RFC 2922 explicitly documented as not implemented, IF-MIB/MIB-II type-enforcement tests.

**§5 Observability** — Stable schema locked in `schema_test.go`, scrape SLOs, module status gauge, FDB label reduction via SHA-256 surrogate.
