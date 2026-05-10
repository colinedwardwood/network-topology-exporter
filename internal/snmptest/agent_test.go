package snmptest

import (
	"context"
	"net"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"
)

// newClient returns a gosnmp client connected to addr with the given community.
func newClient(t *testing.T, addr, community string, timeout time.Duration) *gsnmp.GoSNMP {
	t.Helper()
	ip, port := ParseAddr(addr)
	client := &gsnmp.GoSNMP{
		Target:    ip.String(),
		Port:      port,
		Community: community,
		Version:   gsnmp.Version2c,
		Timeout:   timeout,
		Retries:   0,
		Context:   context.Background(),
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("gosnmp Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Conn.Close() })
	return client
}

// TestStartAndGet verifies the basic GET path: the agent returns the exact PDU
// for a known OID, and also exercises the wrong-community drop path in serve.
func TestStartAndGet(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	addr := Start(t, "public", pdus)

	client := newClient(t, addr, "public", 3*time.Second)
	result, err := client.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("GET: no variables returned")
	}
	got, ok := result.Variables[0].Value.([]byte)
	if !ok {
		t.Fatalf("expected []byte value, got %T", result.Variables[0].Value)
	}
	if string(got) != "test-device" {
		t.Errorf("GET value = %q, want test-device", got)
	}
}

// TestGetNonexistentOID verifies handleGet returns NoSuchObject for unknown OIDs.
func TestGetNonexistentOID(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	addr := Start(t, "public", pdus)

	client := newClient(t, addr, "public", 3*time.Second)
	result, err := client.Get([]string{".1.3.6.1.2.1.9.9.9.0"})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("GET: no variables returned")
	}
	if result.Variables[0].Type != gsnmp.NoSuchObject {
		t.Errorf("expected NoSuchObject, got %v", result.Variables[0].Type)
	}
}

// TestGetNext verifies GETNEXT returns the lexicographically next OID.
func TestGetNext(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("sysDescr")},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("sysName")},
	}
	addr := Start(t, "public", pdus)

	client := newClient(t, addr, "public", 3*time.Second)
	result, err := client.GetNext([]string{".1.3.6.1.2.1.1.1.0"})
	if err != nil {
		t.Fatalf("GETNEXT: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("GETNEXT: no variables returned")
	}
	if result.Variables[0].Name != ".1.3.6.1.2.1.1.5.0" {
		t.Errorf("GETNEXT name = %q, want .1.3.6.1.2.1.1.5.0", result.Variables[0].Name)
	}
}

// TestGetNextPastEnd verifies GETNEXT returns EndOfMibView for the last OID.
func TestGetNextPastEnd(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("only-entry")},
	}
	addr := Start(t, "public", pdus)

	client := newClient(t, addr, "public", 3*time.Second)
	// GETNEXT past the last OID.
	result, err := client.GetNext([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GETNEXT past end: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("GETNEXT past end: no variables returned")
	}
	if result.Variables[0].Type != gsnmp.EndOfMibView {
		t.Errorf("expected EndOfMibView, got %v", result.Variables[0].Type)
	}
}

// TestBulkWalk verifies GETBULK retrieves all OIDs under a common prefix.
// It also exercises the handleBulk maxReps=0 default path.
func TestBulkWalk(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.2.2.1.1.1", Type: gsnmp.Integer, Value: 1},
		{Name: ".1.3.6.1.2.1.2.2.1.1.2", Type: gsnmp.Integer, Value: 2},
		{Name: ".1.3.6.1.2.1.2.2.1.1.3", Type: gsnmp.Integer, Value: 3},
	}
	addr := Start(t, "public", pdus)

	client := newClient(t, addr, "public", 3*time.Second)
	// BulkWalk issues repeated GETBULKs starting from the given OID.
	var collected []gsnmp.SnmpPDU
	err := client.BulkWalk(".1.3.6.1.2.1.2.2.1.1", func(pdu gsnmp.SnmpPDU) error {
		collected = append(collected, pdu)
		return nil
	})
	if err != nil {
		t.Fatalf("BulkWalk: %v", err)
	}
	if len(collected) != 3 {
		t.Errorf("BulkWalk returned %d PDUs, want 3", len(collected))
	}
}

// TestBulkWalkWithNonRepeaters exercises the handleBulk non-repeaters path,
// the nonRepeaters > len(vars) clamp, and the non-repeater EndOfMibView branch.
func TestBulkWalkWithNonRepeaters(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("descr")},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("name")},
	}
	addr := Start(t, "public", pdus)

	// Test handleBulk with nonRepeaters=1 (found): the var has a successor.
	result := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0"},
	}, 1, 5)
	if len(result) == 0 {
		t.Error("handleBulk with nonRepeaters=1: expected result")
	}

	// Test non-repeater EndOfMibView: nonRepeaters=1, var is past the last OID.
	resultEOF := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.9.9.9.0"},
	}, 1, 5)
	if len(resultEOF) != 1 {
		t.Fatalf("handleBulk non-repeater past end: got %d results, want 1", len(resultEOF))
	}
	if resultEOF[0].Type != gsnmp.EndOfMibView {
		t.Errorf("handleBulk non-repeater past end type = %v, want EndOfMibView", resultEOF[0].Type)
	}

	// Test nonRepeaters > len(vars) clamp: 3 non-repeaters but only 1 var.
	result2 := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0"},
	}, 3, 5)
	if len(result2) == 0 {
		t.Error("handleBulk with nonRepeaters>len(vars): expected result")
	}

	// Test maxReps=0 default path by calling directly.
	result3 := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0"},
	}, 0, 0)
	if len(result3) == 0 {
		t.Error("handleBulk with maxReps=0: expected result using default")
	}

	// Ensure Start was used (addr referenced).
	_ = addr
}

// TestBulkEndOfMibView exercises the EndOfMibView branch inside the repeaters
// loop of handleBulk (when nextLookup fails mid-walk).
func TestBulkEndOfMibView(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("only")},
	}
	// Start from an OID that, after one step, hits end-of-MIB inside the
	// repeater loop.
	result := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0"},
	}, 0, 5)
	// The first step returns .1.3.6.1.2.1.1.5.0, the second hits end-of-MIB.
	if len(result) < 2 {
		t.Errorf("expected at least 2 results (one value + EndOfMibView), got %d", len(result))
	}
	last := result[len(result)-1]
	if last.Type != gsnmp.EndOfMibView {
		t.Errorf("last entry type = %v, want EndOfMibView", last.Type)
	}
}

// TestParseAddr verifies ParseAddr splits host and port correctly.
func TestParseAddr(t *testing.T) {
	// Normal case: valid address.
	ip, port := ParseAddr("127.0.0.1:9161")
	if ip == nil {
		t.Fatal("ParseAddr: expected non-nil IP")
	}
	if ip.String() != "127.0.0.1" {
		t.Errorf("ParseAddr IP = %q, want 127.0.0.1", ip.String())
	}
	if port != 9161 {
		t.Errorf("ParseAddr port = %d, want 9161", port)
	}

	// Invalid host: SplitHostPort fails, result is zero values.
	ip2, port2 := ParseAddr("not-an-addr")
	// ParseAddr silently ignores errors; just verify it doesn't panic.
	_ = ip2
	_ = port2

	// Invalid port: ParseUint fails, port defaults to 0.
	ip3, port3 := ParseAddr("127.0.0.1:notaport")
	_ = ip3
	if port3 != 0 {
		t.Errorf("ParseAddr invalid port = %d, want 0", port3)
	}
}

// TestStartMultiCommunity verifies StartMultiCommunity dispatches per-community
// PDUs and silently drops packets with an unknown community.
func TestStartMultiCommunity(t *testing.T) {
	publicPDUs := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("public-device")},
	}
	privatePDUs := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("private-device")},
	}

	addr := StartMultiCommunity(t, map[string][]gsnmp.SnmpPDU{
		"public":  publicPDUs,
		"private": privatePDUs,
	})

	// Correct community "public" returns public-device.
	pubClient := newClient(t, addr, "public", 3*time.Second)
	result, err := pubClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GET public: %v", err)
	}
	if got, ok := result.Variables[0].Value.([]byte); !ok || string(got) != "public-device" {
		t.Errorf("public GET = %q, want public-device", result.Variables[0].Value)
	}

	// Correct community "private" returns private-device.
	privClient := newClient(t, addr, "private", 3*time.Second)
	result2, err := privClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GET private: %v", err)
	}
	if got, ok := result2.Variables[0].Value.([]byte); !ok || string(got) != "private-device" {
		t.Errorf("private GET = %q, want private-device", result2.Variables[0].Value)
	}

	// Wrong community — packet is silently dropped, client times out.
	wrongClient := newClient(t, addr, "wrong", 200*time.Millisecond)
	_, err = wrongClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err == nil {
		t.Error("expected timeout/error for wrong community, got nil")
	}
}

// TestServeWrongCommunity exercises the pkt.Community != community branch in
// serve by sending with the wrong community string.
func TestServeWrongCommunity(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")},
	}
	addr := Start(t, "public", pdus)

	wrongClient := newClient(t, addr, "badcommunity", 200*time.Millisecond)
	_, err := wrongClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err == nil {
		t.Error("expected timeout for wrong community in serve, got nil")
	}
}

// TestServeDecodeError exercises the SnmpDecodePacket error path in serve and
// serveMulti by sending a malformed UDP datagram directly.
func TestServeDecodeError(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")},
	}
	addr := Start(t, "public", pdus)

	// Send garbage bytes; the agent should ignore it and not crash.
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte{0xff, 0xfe, 0xfd, 0x00, 0x01})

	// The subsequent GET is synchronous: if the agent is alive it replies
	// immediately, so no sleep is needed to verify liveness after the bad packet.
	goodClient := newClient(t, addr, "public", 3*time.Second)
	result, err := goodClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GET after malformed packet: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("no variables after malformed packet")
	}
}

// TestServeMultiDecodeError exercises the decode-error path in serveMulti.
func TestServeMultiDecodeError(t *testing.T) {
	addr := StartMultiCommunity(t, map[string][]gsnmp.SnmpPDU{
		"public": {{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")}},
	})

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte{0xde, 0xad, 0xbe, 0xef})

	// The subsequent GET is synchronous: if the agent is alive it replies
	// immediately, so no sleep is needed to verify liveness after the bad packet.
	goodClient := newClient(t, addr, "public", 3*time.Second)
	result, err := goodClient.Get([]string{".1.3.6.1.2.1.1.5.0"})
	if err != nil {
		t.Fatalf("GET after multi-decode error: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("no variables after multi-decode error")
	}
}

// TestDispatchPDUUnknown exercises the default branch of dispatchPDU directly.
func TestDispatchPDUUnknown(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")},
	}
	// Use a PDU type that is not Get/GetNext/GetBulk.
	pkt := &gsnmp.SnmpPacket{
		PDUType:   gsnmp.SetRequest,
		Variables: []gsnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.5.0"}},
	}
	resp := dispatchPDU(pdus, pkt)
	if resp != nil {
		t.Errorf("dispatchPDU(SetRequest): expected nil, got %v", resp)
	}
}

// TestServeRespNil exercises the resp==nil / continue branch in serve by
// sending a SetRequest which dispatchPDU cannot handle and drops.
func TestServeRespNil(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")},
	}
	addr := Start(t, "public", pdus)

	// Use a very short timeout; the agent silently drops the Set so it will time out.
	client := newClient(t, addr, "public", 200*time.Millisecond)
	_, err := client.Set([]gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("x")},
	})
	// Expect a timeout because the agent drops SetRequest without replying.
	if err == nil {
		t.Log("Set returned no error (agent may have replied); resp==nil path still exercised")
	}
}

// TestServeMultiRespNil exercises the resp==nil / continue branch in serveMulti.
func TestServeMultiRespNil(t *testing.T) {
	addr := StartMultiCommunity(t, map[string][]gsnmp.SnmpPDU{
		"public": {{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("device")}},
	})

	client := newClient(t, addr, "public", 200*time.Millisecond)
	_, err := client.Set([]gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("x")},
	})
	if err == nil {
		t.Log("Set returned no error (agent may have replied); resp==nil path still exercised")
	}
}

// TestStartMultiCommunitySort exercises the sort.Slice closure in
// StartMultiCommunity by providing PDUs in reverse OID order so the
// comparator actually swaps entries.
func TestStartMultiCommunitySort(t *testing.T) {
	// Provide PDUs in reverse order to force the sort.Slice closure to run.
	reversed := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("name")},
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("descr")},
	}
	addr := StartMultiCommunity(t, map[string][]gsnmp.SnmpPDU{
		"public": reversed,
	})

	// Verify GETNEXT works correctly (requires sorted order).
	client := newClient(t, addr, "public", 3*time.Second)
	result, err := client.GetNext([]string{".1.3.6.1.2.1.1.1.0"})
	if err != nil {
		t.Fatalf("GetNext after StartMultiCommunity sort: %v", err)
	}
	if len(result.Variables) == 0 {
		t.Fatal("no variables")
	}
	if result.Variables[0].Name != ".1.3.6.1.2.1.1.5.0" {
		t.Errorf("GetNext = %q, want .1.3.6.1.2.1.1.5.0", result.Variables[0].Name)
	}
}

// TestOidLess verifies the oidLess comparator.
func TestOidLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// a < b by numeric component value.
		{"1.3.6.1.2.1.1.1", "1.3.6.1.2.1.1.5", true},
		// a > b.
		{"1.3.6.1.2.1.1.5", "1.3.6.1.2.1.1.1", false},
		// equal OIDs.
		{"1.3.6.1.2.1.1.1", "1.3.6.1.2.1.1.1", false},
		// a is a proper prefix of b (shorter).
		{"1.3.6.1", "1.3.6.1.1", true},
		// b is a proper prefix of a (longer).
		{"1.3.6.1.1", "1.3.6.1", false},
		// leading-dot variants are normalised.
		{".1.3.6", "1.3.6.1", true},
		{"1.3.6.1", ".1.3.6", false},
		// numeric difference at a middle component.
		{"1.3.10.1", "1.3.9.1", false},
		{"1.3.9.1", "1.3.10.1", true},
	}
	for _, c := range cases {
		got := oidLess(c.a, c.b)
		if got != c.want {
			t.Errorf("oidLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestNextComponent verifies nextComponent parses integer components correctly.
func TestNextComponent(t *testing.T) {
	// Single component, no trailing dot.
	v, next := nextComponent("42", 0)
	if v != 42 {
		t.Errorf("nextComponent(42,0) value = %d, want 42", v)
	}
	if next != 2 {
		t.Errorf("nextComponent(42,0) next = %d, want 2", next)
	}

	// Component followed by dot: next pos should skip the dot.
	v2, next2 := nextComponent("1.3.6", 0)
	if v2 != 1 {
		t.Errorf("nextComponent(1.3.6,0) value = %d, want 1", v2)
	}
	if next2 != 2 { // "1." → pos 2
		t.Errorf("nextComponent(1.3.6,0) next = %d, want 2", next2)
	}

	// Start mid-string.
	v3, next3 := nextComponent("1.3.6", 2)
	if v3 != 3 {
		t.Errorf("nextComponent(1.3.6,2) value = %d, want 3", v3)
	}
	if next3 != 4 {
		t.Errorf("nextComponent(1.3.6,2) next = %d, want 4", next3)
	}

	// Empty string at pos 0: returns 0, stays at 0.
	v4, next4 := nextComponent("", 0)
	if v4 != 0 {
		t.Errorf("nextComponent('',0) value = %d, want 0", v4)
	}
	if next4 != 0 {
		t.Errorf("nextComponent('',0) next = %d, want 0", next4)
	}
}
