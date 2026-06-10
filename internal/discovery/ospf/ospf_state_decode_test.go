package ospf

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// TestWalkOspfNbrTableCorruptStateReported (#170): a corrupt ospfNbrState PDU
// must surface as a nbr_state_undecodable decode issue instead of silently
// becoming state 0 (not Full/TwoWay) and dropping the adjacency unaccounted.
func TestWalkOspfNbrTableCorruptStateReported(t *testing.T) {
	const base = ".1.3.6.1.2.1.14.10.1."
	const idx = "192.0.2.1.0"

	pdus := []gsnmp.SnmpPDU{
		{Name: base + "1." + idx, Type: gsnmp.IPAddress, Value: "192.0.2.1"},
		// ospfNbrState as an OctetString → PDUIntStrict fails.
		{Name: base + "6." + idx, Type: gsnmp.OctetString, Value: []byte("corrupt")},
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

	rows, _, err := walkOspfNbrTable(ctx, client)
	if err != nil {
		t.Fatalf("walkOspfNbrTable: %v", err)
	}

	var got int
	for _, i := range issues {
		if i.Reason == "nbr_state_undecodable" {
			got += i.Count
			if string(i.OID) != oidOspfNbrTable {
				t.Errorf("issue OID = %q, want %q", i.OID, oidOspfNbrTable)
			}
		}
	}
	if got != 1 {
		t.Errorf("nbr_state_undecodable count = %d, want 1 (issues: %v)", got, issues)
	}
	// The row must keep its zero state (no plausible-but-wrong value).
	if row := rows[idx]; row != nil && row.state != 0 {
		t.Errorf("row.state = %d, want 0 after corrupt PDU", row.state)
	}
}
