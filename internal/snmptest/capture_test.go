package snmptest

import (
	"strings"
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"
)

// TestParseCaptureTypes covers each SNMP type token the lab capture files emit
// (INTEGER, Gauge32, Counter32, Counter64, Timeticks, IpAddress, Hex-STRING,
// STRING) plus the bare empty-string form, asserting both the gosnmp type tag
// and the decoded Go value.
func TestParseCaptureTypes(t *testing.T) {
	in := strings.Join([]string{
		"# a comment line — ignored",
		"# device: fake",
		"",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.3.1.4.10.0.0.2 = INTEGER: 6",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.11.1.4.10.0.0.2 = Gauge32: 65001",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.13.1.4.10.0.0.2 = Counter32: 7",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.30.1.4.10.0.0.2 = Counter64: 123456789012",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.31.1.4.10.0.0.2 = Timeticks: (133) 0:00:01.33",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.9.1.4.10.0.0.2 = IpAddress: 1.1.1.1",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.6.1.4.10.0.0.2 = Hex-STRING: 0A 00 00 01 ",
		".1.3.6.1.4.1.30065.4.1.1.2.1.14.1.1.4.10.0.0.2 = STRING: \"ibgp-r2\"",
		".1.3.6.1.4.1.9.9.187.1.2.5.1.28.1.4.10.0.0.2 = \"\"",
	}, "\n")

	pdus, err := parseCapture(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCapture: %v", err)
	}
	if len(pdus) != 9 {
		t.Fatalf("got %d PDUs, want 9 (comments/blanks skipped)", len(pdus))
	}

	check := func(i int, wantName string, wantType gsnmp.Asn1BER, wantVal any) {
		t.Helper()
		if pdus[i].Name != wantName {
			t.Errorf("[%d] Name = %q, want %q", i, pdus[i].Name, wantName)
		}
		if pdus[i].Type != wantType {
			t.Errorf("[%d] Type = %v, want %v", i, pdus[i].Type, wantType)
		}
		switch want := wantVal.(type) {
		case []byte:
			got, ok := pdus[i].Value.([]byte)
			if !ok || string(got) != string(want) {
				t.Errorf("[%d] Value = %v (%T), want %v", i, pdus[i].Value, pdus[i].Value, want)
			}
		default:
			if pdus[i].Value != wantVal {
				t.Errorf("[%d] Value = %v (%T), want %v (%T)", i, pdus[i].Value, pdus[i].Value, wantVal, wantVal)
			}
		}
	}

	check(0, ".1.3.6.1.4.1.9.9.187.1.2.5.1.3.1.4.10.0.0.2", gsnmp.Integer, 6)
	check(1, ".1.3.6.1.4.1.9.9.187.1.2.5.1.11.1.4.10.0.0.2", gsnmp.Gauge32, uint(65001))
	check(2, ".1.3.6.1.4.1.9.9.187.1.2.5.1.13.1.4.10.0.0.2", gsnmp.Counter32, uint(7))
	check(3, ".1.3.6.1.4.1.9.9.187.1.2.5.1.30.1.4.10.0.0.2", gsnmp.Counter64, uint64(123456789012))
	check(4, ".1.3.6.1.4.1.9.9.187.1.2.5.1.31.1.4.10.0.0.2", gsnmp.TimeTicks, uint32(133))
	check(5, ".1.3.6.1.4.1.9.9.187.1.2.5.1.9.1.4.10.0.0.2", gsnmp.IPAddress, "1.1.1.1")
	check(6, ".1.3.6.1.4.1.9.9.187.1.2.5.1.6.1.4.10.0.0.2", gsnmp.OctetString, []byte{0x0a, 0, 0, 1})
	check(7, ".1.3.6.1.4.1.30065.4.1.1.2.1.14.1.1.4.10.0.0.2", gsnmp.OctetString, []byte("ibgp-r2"))
	check(8, ".1.3.6.1.4.1.9.9.187.1.2.5.1.28.1.4.10.0.0.2", gsnmp.OctetString, []byte{})
}

// TestParseCaptureHexContinuation folds a wrapped Hex-STRING line (snmpwalk
// breaks long octet strings across lines) into the preceding PDU.
func TestParseCaptureHexContinuation(t *testing.T) {
	in := ".1.3.6.1.2.1.1.6 = Hex-STRING: 00 11 22 33\n44 55 66 77\n"
	pdus, err := parseCapture(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCapture: %v", err)
	}
	if len(pdus) != 1 {
		t.Fatalf("got %d PDUs, want 1 (continuation merged)", len(pdus))
	}
	got, _ := pdus[0].Value.([]byte)
	want := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
	if string(got) != string(want) {
		t.Errorf("Value = % x, want % x", got, want)
	}
}

// TestParseCaptureErrors covers the malformed inputs the parser must reject so
// that capture-format drift surfaces as a test failure rather than silent data
// loss.
func TestParseCaptureErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"unknown type", ".1.3.6.1 = WeirdType: 5"},
		{"non-numeric integer", ".1.3.6.1 = INTEGER: notanumber"},
		{"odd hex", ".1.3.6.1 = Hex-STRING: 0A0"},
		{"no separator and no prior hex", "garbage line with no equals"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseCapture(strings.NewReader(c.in)); err == nil {
				t.Errorf("parseCapture(%q) succeeded, want error", c.in)
			}
		})
	}
}
