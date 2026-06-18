package lldp

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// TestWalkLocPortsCorruptSubtypeReported (#170): a corrupt
// lldpLocPortIdSubtype PDU must surface as a port_subtype_undecodable decode
// issue instead of silently becoming subtype 0 and degrading local-port
// naming unaccounted.
func TestWalkLocPortsCorruptSubtypeReported(t *testing.T) {
	const base = ".1.0.8802.1.1.2.1.3.7.1."

	pdus := []gsnmp.SnmpPDU{
		// lldpLocPortIdSubtype as an OctetString → PDUIntStrict fails.
		{Name: base + "2.1", Type: gsnmp.OctetString, Value: []byte("corrupt")},
		{Name: base + "3.1", Type: gsnmp.OctetString, Value: []byte("Gi0/1")},
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

	ports, err := walkLocPorts(ctx, client)
	if err != nil {
		t.Fatalf("walkLocPorts: %v", err)
	}

	var got int
	for _, i := range issues {
		if i.Reason == "port_subtype_undecodable" {
			got += i.Count
			if string(i.OID) != oidLocPortTable {
				t.Errorf("issue OID = %q, want %q", i.OID, oidLocPortTable)
			}
		}
	}
	if got != 1 {
		t.Errorf("port_subtype_undecodable count = %d, want 1 (issues: %v)", got, issues)
	}
	// The port keeps subtype 0 and the lldpLocPortId bytes still load.
	if lp := ports[1]; lp.idSubtype != 0 {
		t.Errorf("idSubtype = %d, want 0 after corrupt PDU", lp.idSubtype)
	}
}
