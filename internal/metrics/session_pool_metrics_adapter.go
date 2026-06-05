package metrics

// SessionPoolMetricsAdapter is the production implementation of the
// snmp.SessionPoolMetrics interface (issue #83). It bridges session-pool
// observations into the Prometheus counters/gauge exposed on /metrics.
//
// As with WalkerMetricsAdapter, the indirection keeps the discovery/snmp
// package free of any prometheus-client import: the pool holds a
// SessionPoolMetrics interface value, and this adapter — wired in app.go — is
// the only thing that touches the prometheus types.
type SessionPoolMetricsAdapter struct {
	m *Metrics
}

// NewSessionPoolMetricsAdapter returns an adapter wired to the SNMPSessionPool*
// metrics on m. One per Metrics instance; safe for concurrent use because the
// underlying prometheus collectors are goroutine-safe.
func NewSessionPoolMetricsAdapter(m *Metrics) *SessionPoolMetricsAdapter {
	return &SessionPoolMetricsAdapter{m: m}
}

// RecordHit increments network_topology_snmp_session_pool_hits_total.
func (a *SessionPoolMetricsAdapter) RecordHit() { a.m.SNMPSessionPoolHits.Inc() }

// RecordMiss increments network_topology_snmp_session_pool_misses_total.
func (a *SessionPoolMetricsAdapter) RecordMiss() { a.m.SNMPSessionPoolMisses.Inc() }

// SetSize publishes network_topology_snmp_session_pool_size.
func (a *SessionPoolMetricsAdapter) SetSize(n int) { a.m.SNMPSessionPoolSize.Set(float64(n)) }

// RecordEviction increments network_topology_snmp_session_pool_evictions_total
// for the given reason (idle | credential_rotation | connection_error).
func (a *SessionPoolMetricsAdapter) RecordEviction(reason string) {
	a.m.SNMPSessionPoolEvictions.WithLabelValues(reason).Inc()
}
