# Engineering Improvement Plan: Network Topology Exporter

## Open Items

- [ ] **Replace FDB Edge Emission With Identity-Gated Link Synthesis**: FDB should produce raw observations, not public topology edges, unless the MAC can be correlated to known infrastructure through LLDP chassis MAC, ARP/IP-MIB, ENTITY-MIB, or explicit operator inventory. This is a deep architectural rework requiring a two-phase graph assembly boundary (raw observations → canonical link synthesis); scope and design decision required before implementation.

- [ ] **Prove Reconciliation Complexity With Synthetic Scale Tests at Production Scale**: `BenchmarkReconcile10000Edges` is now in CI, but no test fails when it regresses. Add a `testing.B`-based assertion that `Reconcile` on a 10 000-edge graph stays under a declared ns/op budget, or add a scale test that generates a 5 000-device graph and validates `graph.Reconcile` completes in under 2× the baseline.

- [ ] **Cardinality Budget Hard Limits in CI**: `TestCardinalityBudget` uses a budget of 15 samples/device. Tighten to the actual measured budget (currently ~3 samples/device) so the test catches real cardinality regressions, not just catastrophic blowups.
