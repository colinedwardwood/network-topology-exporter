# Engineering Improvement Plan: Network Topology Exporter

## Open Items

- [ ] **Formalize Two-Phase Graph Assembly Boundary**: FDB emits raw MAC-addressed edges; `resolveEdgeDstDevices` synthesizes them into canonical device edges using LLDP chassis MAC and ARP table correlation (both now implemented). The remaining work is architectural: move the synthesis step into a named type/package so the boundary between "protocol observations" and "canonical graph" is explicit in the type system rather than implicit in function call order in `runCycle`. This is a refactor for maintainability; the observable behavior is already correct.
