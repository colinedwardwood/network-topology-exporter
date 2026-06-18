package bgp

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// TestWalkBgpPeerTableCorruptStateReported (#170): a corrupt bgpPeerState PDU
// must surface as a peer_state_undecodable decode issue instead of silently
// becoming state 0 (not established) and dropping the peer edge unaccounted.
func TestWalkBgpPeerTableCorruptStateReported(t *testing.T) {
	const base = ".1.3.6.1.2.1.15.3.1."
	const peer = "10.0.0.1"

	pdus := []gsnmp.SnmpPDU{
		// bgpPeerState as an OctetString → PDUIntStrict fails.
		{Name: base + "2." + peer, Type: gsnmp.OctetString, Value: []byte("corrupt")},
		{Name: base + "7." + peer, Type: gsnmp.IPAddress, Value: []byte{10, 0, 0, 1}},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	p := snmputil.Params{IP: ip, Port: port, Community: []byte("public"), Timeout: 3 * time.Second}
	client, err := snmputil.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	var issues []snmputil.DecodeIssue
	ctx := snmputil.ContextWithDecodeIssueReporter(context.Background(), func(i snmputil.DecodeIssue) {
		issues = append(issues, i)
	})

	peers, _, err := walkBgpPeerTable(ctx, client)
	if err != nil {
		t.Fatalf("walkBgpPeerTable: %v", err)
	}

	var got int
	for _, i := range issues {
		if i.Reason == "peer_state_undecodable" {
			got += i.Count
			if string(i.OID) != oidBgpPeerTable {
				t.Errorf("issue OID = %q, want %q", i.OID, oidBgpPeerTable)
			}
		}
	}
	if got != 1 {
		t.Errorf("peer_state_undecodable count = %d, want 1 (issues: %v)", got, issues)
	}
	// The peer must keep its zero state (no plausible-but-wrong value).
	if p := peers[peer]; p != nil && p.state != 0 {
		t.Errorf("peer.state = %d, want 0 after corrupt PDU", p.state)
	}
}
