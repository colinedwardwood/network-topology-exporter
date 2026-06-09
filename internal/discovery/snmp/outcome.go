package snmp

// Walker outcome label values for the generic per-protocol walker-outcome
// counter (network_topology_walker_outcome_total, issue #98), shared by the
// LLDP, CDP, OSPF, and FDB walkers. BGP uses its own vocabulary
// (network_topology_bgp_walker_outcome_total) and does not share these.
const (
	OutcomeEdges            = "edges"
	OutcomeMIBUnimplemented = "mib_unimplemented"
	OutcomeNoNeighbours     = "no_neighbours"
	OutcomeWalkerDrift      = "walker_drift"
	OutcomeError            = "error"
)

// RecordProtocolWalkerOutcome forwards a {walker, outcome} observation to the
// generic per-protocol counter via the metrics sink on Params. nil-safe: a nil
// Params or nil Params.WalkerMetrics drops the increment rather than panicking.
func RecordProtocolWalkerOutcome(p *Params, walker, outcome string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordProtocolWalkerOutcome(walker, outcome)
}

// RecordBGPWalkerOutcome forwards to the BGP-specific counter
// (network_topology_bgp_walker_outcome_total). Same nil-tolerance.
func RecordBGPWalkerOutcome(p *Params, walker, outcome string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordWalkerOutcome(walker, outcome)
}

// RecordDegraded forwards a {module, reason} observation to
// DiscoveryDegradedTotal. Same nil-tolerance. Zero-edge degraded path (#100).
func RecordDegraded(p *Params, module, reason string) {
	if p == nil || p.WalkerMetrics == nil {
		return
	}
	p.WalkerMetrics.RecordDegraded(module, reason)
}

// ClassifyNeighbourOutcome maps a neighbour-walk result to its terminal outcome
// label, shared by the LLDP/CDP/OSPF/FDB walkers. edgeCount is the edges built;
// hadPDUs is whether the MIB returned any rows; decoded is whether rows decoded
// cleanly even if they produced no edge. (Walk-error early returns emit
// OutcomeError directly and never reach here.)
func ClassifyNeighbourOutcome(edgeCount int, hadPDUs, decoded bool) string {
	switch {
	case edgeCount > 0:
		return OutcomeEdges
	case !hadPDUs:
		return OutcomeMIBUnimplemented
	case decoded:
		return OutcomeNoNeighbours
	default:
		return OutcomeWalkerDrift
	}
}
