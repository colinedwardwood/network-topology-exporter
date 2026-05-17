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
