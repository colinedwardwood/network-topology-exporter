package metrics

// WalkerMetricsAdapter is the production implementation of the
// snmputil.WalkerMetrics interface. It bridges per-walker outcome
// observations emitted from the discovery layer into the Prometheus
// counter exposed on /metrics.
//
// The discovery layer (internal/discovery/snmp.Params.WalkerMetrics) holds a
// reference to one of these per process; calls from bgp.Walk and any future
// protocol module land here without the discovery code needing to import the
// prometheus client library. That keeps the dependency edge pointing into
// discovery from main, not the other way around, and is the whole reason
// the indirection exists (replacing the previous walkerOutcomeCounter
// package-global in internal/discovery/bgp/, see issue #18).
type WalkerMetricsAdapter struct {
	m *Metrics
}

// NewWalkerMetricsAdapter returns an adapter wired to m.BGPWalkerOutcomeTotal.
// One adapter per Metrics instance; the result is goroutine-safe because the
// underlying CounterVec is goroutine-safe.
func NewWalkerMetricsAdapter(m *Metrics) *WalkerMetricsAdapter {
	return &WalkerMetricsAdapter{m: m}
}

// RecordWalkerOutcome increments network_topology_bgp_walker_outcome_total
// for the (walker, outcome) tuple. The label set and semantics are
// documented on Metrics.BGPWalkerOutcomeTotal.
func (a *WalkerMetricsAdapter) RecordWalkerOutcome(walker, outcome string) {
	a.m.BGPWalkerOutcomeTotal.WithLabelValues(walker, outcome).Inc()
}

// RecordProtocolWalkerOutcome increments network_topology_walker_outcome_total
// for the (walker, outcome) tuple. This is the generic, non-BGP counter used
// by the LLDP, CDP, OSPF, and FDB walkers (issue #98). The label set and
// semantics are documented on Metrics.WalkerOutcomeTotal.
func (a *WalkerMetricsAdapter) RecordProtocolWalkerOutcome(walker, outcome string) {
	a.m.WalkerOutcomeTotal.WithLabelValues(walker, outcome).Inc()
}

// RecordDegraded increments network_topology_discovery_degraded_total for the
// (module, reason) tuple. Used for zero-edge degraded runs that cannot be
// carried by the orchestrator's edge-metadata path (issue #100); the label
// set and semantics are documented on Metrics.DiscoveryDegradedTotal.
func (a *WalkerMetricsAdapter) RecordDegraded(module, reason string) {
	a.m.DiscoveryDegradedTotal.WithLabelValues(module, reason).Inc()
}

// RecordSystemWalkAnomaly increments network_topology_system_walk_anomaly_total
// for the given low-cardinality reason. Used by the SNMP system walk to surface
// the empty-sysName fallback and the unresolved-vendor outcome (issue #101);
// the closed reason set and semantics are documented on
// Metrics.SystemWalkAnomalyTotal.
func (a *WalkerMetricsAdapter) RecordSystemWalkAnomaly(reason string) {
	a.m.SystemWalkAnomalyTotal.WithLabelValues(reason).Inc()
}
