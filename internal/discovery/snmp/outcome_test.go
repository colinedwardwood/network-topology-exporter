package snmp

import "testing"

func TestClassifyNeighbourOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		edgeCount int
		hadPDUs   bool
		decoded   bool
		want      string
	}{
		{name: "edges built", edgeCount: 3, hadPDUs: true, decoded: true, want: OutcomeEdges},
		{name: "edges win even if no PDUs flagged", edgeCount: 1, hadPDUs: false, decoded: false, want: OutcomeEdges},
		{name: "no PDUs is mib_unimplemented", edgeCount: 0, hadPDUs: false, decoded: false, want: OutcomeMIBUnimplemented},
		{name: "decoded but no edge is no_neighbours", edgeCount: 0, hadPDUs: true, decoded: true, want: OutcomeNoNeighbours},
		{name: "PDUs but undecoded is walker_drift", edgeCount: 0, hadPDUs: true, decoded: false, want: OutcomeWalkerDrift},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyNeighbourOutcome(c.edgeCount, c.hadPDUs, c.decoded); got != c.want {
				t.Errorf("ClassifyNeighbourOutcome(%d, %t, %t) = %q, want %q",
					c.edgeCount, c.hadPDUs, c.decoded, got, c.want)
			}
		})
	}
}

// recordingSink captures which WalkerMetrics method was invoked with which
// (walker, outcome) / (module, reason) args, so the forwarders' routing can be
// asserted: RecordProtocolWalkerOutcome → protocol counter, RecordWalkerOutcome
// → BGP counter, RecordDegraded → degraded counter.
type recordingSink struct {
	protocol [][2]string
	bgp      [][2]string
	degraded [][2]string
}

func (s *recordingSink) RecordProtocolWalkerOutcome(walker, outcome string) {
	s.protocol = append(s.protocol, [2]string{walker, outcome})
}

func (s *recordingSink) RecordWalkerOutcome(walker, outcome string) {
	s.bgp = append(s.bgp, [2]string{walker, outcome})
}

func (s *recordingSink) RecordDegraded(module, reason string) {
	s.degraded = append(s.degraded, [2]string{module, reason})
}

func (s *recordingSink) RecordSystemWalkAnomaly(string) {}

func TestRecordProtocolWalkerOutcomeRoutesToProtocolSink(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	RecordProtocolWalkerOutcome(&Params{WalkerMetrics: sink}, "lldp", OutcomeEdges)

	if len(sink.protocol) != 1 || sink.protocol[0] != [2]string{"lldp", OutcomeEdges} {
		t.Errorf("protocol sink = %v, want one {lldp, edges}", sink.protocol)
	}
	if len(sink.bgp) != 0 || len(sink.degraded) != 0 {
		t.Errorf("unexpected routing: bgp=%v degraded=%v", sink.bgp, sink.degraded)
	}
}

func TestRecordBGPWalkerOutcomeRoutesToBGPSink(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	RecordBGPWalkerOutcome(&Params{WalkerMetrics: sink}, "rfc4273", OutcomeEdges)

	if len(sink.bgp) != 1 || sink.bgp[0] != [2]string{"rfc4273", OutcomeEdges} {
		t.Errorf("bgp sink = %v, want one {rfc4273, edges}", sink.bgp)
	}
	if len(sink.protocol) != 0 || len(sink.degraded) != 0 {
		t.Errorf("unexpected routing: protocol=%v degraded=%v", sink.protocol, sink.degraded)
	}
}

func TestRecordDegradedRoutesToDegradedSink(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	RecordDegraded(&Params{WalkerMetrics: sink}, "fdb", "qbridge_walk_failed")

	if len(sink.degraded) != 1 || sink.degraded[0] != [2]string{"fdb", "qbridge_walk_failed"} {
		t.Errorf("degraded sink = %v, want one {fdb, qbridge_walk_failed}", sink.degraded)
	}
	if len(sink.protocol) != 0 || len(sink.bgp) != 0 {
		t.Errorf("unexpected routing: protocol=%v bgp=%v", sink.protocol, sink.bgp)
	}
}

func TestOutcomeForwardersNilSafe(t *testing.T) {
	t.Parallel()

	// nil *Params must drop, not panic.
	RecordProtocolWalkerOutcome(nil, "lldp", OutcomeEdges)
	RecordBGPWalkerOutcome(nil, "rfc4273", OutcomeEdges)
	RecordDegraded(nil, "fdb", "reason")

	// Params with nil WalkerMetrics must drop, not panic.
	p := &Params{}
	RecordProtocolWalkerOutcome(p, "lldp", OutcomeEdges)
	RecordBGPWalkerOutcome(p, "rfc4273", OutcomeEdges)
	RecordDegraded(p, "fdb", "reason")
}
