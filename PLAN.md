# Engineering Improvement Plan: Network Topology Exporter

## Open Items

- [ ] **Replace FDB Edge Emission With Identity-Gated Link Synthesis**: FDB should produce raw observations, not public topology edges, unless the MAC can be correlated to known infrastructure through LLDP chassis MAC, ARP/IP-MIB, ENTITY-MIB, or explicit operator inventory. This is a deep architectural rework requiring a two-phase graph assembly boundary (raw observations → canonical link synthesis); scope and design decision required before implementation.
