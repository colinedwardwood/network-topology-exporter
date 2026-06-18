package fdb

import (
	"context"
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
)

// TestWalkFdbTableCorruptStatusReported (#170): a corrupt dot1dTpFdbStatus PDU
// must surface as a status_undecodable decode issue instead of silently
// becoming status 0 (not learned) and dropping the entry unaccounted.
func TestWalkFdbTableCorruptStatusReported(t *testing.T) {
	const base = ".1.3.6.1.2.1.17.4.3.1."
	const mac = "0.17.34.51.68.85" // 00:11:22:33:44:55

	pdus := []gsnmp.SnmpPDU{
		{Name: base + "1." + mac, Type: gsnmp.OctetString, Value: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}},
		{Name: base + "2." + mac, Type: gsnmp.Integer, Value: 3},
		// dot1dTpFdbStatus as an OctetString → PDUIntStrict fails.
		{Name: base + "3." + mac, Type: gsnmp.OctetString, Value: []byte("corrupt")},
	}

	client := openClientToAgent(t, "public", pdus)

	var issues []snmputil.DecodeIssue
	ctx := snmputil.ContextWithDecodeIssueReporter(context.Background(), func(i snmputil.DecodeIssue) {
		issues = append(issues, i)
	})

	entries := make(map[string]*fdbEntry)
	if _, err := walkFdbTableInto(ctx, client, entries); err != nil {
		t.Fatalf("walkFdbTableInto: %v", err)
	}

	var got int
	for _, i := range issues {
		if i.Reason == "status_undecodable" {
			got += i.Count
			if string(i.OID) != oidFdbTable {
				t.Errorf("issue OID = %q, want %q", i.OID, oidFdbTable)
			}
		}
	}
	if got != 1 {
		t.Errorf("status_undecodable count = %d, want 1 (issues: %v)", got, issues)
	}
	// The entry must keep its zero status (no plausible-but-wrong value).
	if e := entries[mac]; e != nil && e.status != 0 {
		t.Errorf("entry.status = %d, want 0 after corrupt PDU", e.status)
	}
}

// TestWalkQBridgeFdbTableCorruptStatusReported (#170): same contract for the
// Q-BRIDGE dot1qTpFdbStatus column.
func TestWalkQBridgeFdbTableCorruptStatusReported(t *testing.T) {
	const base = ".1.3.6.1.2.1.17.7.1.2.2.1."
	const idx = "1.0.17.34.51.68.85" // fdbId 1 + MAC 00:11:22:33:44:55

	pdus := []gsnmp.SnmpPDU{
		{Name: base + "2." + idx, Type: gsnmp.Integer, Value: 3},
		// dot1qTpFdbStatus as an OctetString → PDUIntStrict fails.
		{Name: base + "3." + idx, Type: gsnmp.OctetString, Value: []byte("corrupt")},
	}

	client := openClientToAgent(t, "public", pdus)

	var issues []snmputil.DecodeIssue
	ctx := snmputil.ContextWithDecodeIssueReporter(context.Background(), func(i snmputil.DecodeIssue) {
		issues = append(issues, i)
	})

	entries := make(map[string]*fdbEntry)
	if err := walkQBridgeFdbTable(ctx, client, entries); err != nil {
		t.Fatalf("walkQBridgeFdbTable: %v", err)
	}

	var got int
	for _, i := range issues {
		if i.Reason == "status_undecodable" {
			got += i.Count
			if string(i.OID) != oidQBridgeFdbTable {
				t.Errorf("issue OID = %q, want %q", i.OID, oidQBridgeFdbTable)
			}
		}
	}
	if got != 1 {
		t.Errorf("status_undecodable count = %d, want 1 (issues: %v)", got, issues)
	}
}
