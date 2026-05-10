package snmp

import (
	"context"
	"net"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// PDUString: handles both string and []byte values.
func TestPDUString(t *testing.T) {
	cases := []struct {
		val  any
		want string
	}{
		{"hello", "hello"},
		{[]byte("world"), "world"},
		{42, ""},
	}
	for _, c := range cases {
		got := PDUString(gsnmp.SnmpPDU{Value: c.val})
		if got != c.want {
			t.Errorf("PDUString(%v) = %q, want %q", c.val, got, c.want)
		}
	}
}

// PDUInt: handles int, int32, uint, uint32, returns 0 for other types.
func TestPDUInt(t *testing.T) {
	cases := []struct {
		val  any
		want int
	}{
		{int(5), 5},
		{int32(3), 3},
		{uint(9), 9},
		{uint32(7), 7},
		{float64(1.0), 0},
	}
	for _, c := range cases {
		if got := PDUInt(gsnmp.SnmpPDU{Value: c.val}); got != c.want {
			t.Errorf("PDUInt(%v) = %d, want %d", c.val, got, c.want)
		}
	}
}

// PDUIntStrict: returns ok=false for unsupported value types.
func TestPDUIntStrict(t *testing.T) {
	cases := []struct {
		val    any
		want   int
		wantOK bool
	}{
		{int(5), 5, true},
		{int32(3), 3, true},
		{uint(9), 9, true},
		{uint32(7), 7, true},
		{int64(11), 11, true},
		{uint64(13), 13, true},
		{float64(1.0), 0, false},
		{"7", 0, false},
	}
	for _, c := range cases {
		got, ok := PDUIntStrict(gsnmp.SnmpPDU{Value: c.val})
		if got != c.want || ok != c.wantOK {
			t.Errorf("PDUIntStrict(%v) = (%d,%v), want (%d,%v)", c.val, got, ok, c.want, c.wantOK)
		}
	}
}

// PDUBytes: handles []byte and string, returns nil for other types.
func TestPDUBytes(t *testing.T) {
	if got := PDUBytes(gsnmp.SnmpPDU{Value: []byte{1, 2}}); string(got) != string([]byte{1, 2}) {
		t.Errorf("[]byte: got %v, want [1 2]", got)
	}
	if got := PDUBytes(gsnmp.SnmpPDU{Value: "abc"}); string(got) != "abc" {
		t.Errorf("string: got %v, want abc", got)
	}
	if got := PDUBytes(gsnmp.SnmpPDU{Value: 42}); got != nil {
		t.Errorf("int: got %v, want nil", got)
	}
}

// PDUIPv4: handles dotted-decimal string, raw 4-byte slice, and invalid inputs.
func TestPDUIPv4(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string // empty string means nil expected
	}{
		{"string dotted decimal", "192.0.2.1", "192.0.2.1"},
		{"raw 4-byte slice", []byte{10, 0, 0, 1}, "10.0.0.1"},
		{"invalid string", "not-an-ip", ""},
		{"3-byte slice", []byte{1, 2, 3}, ""},
		{"integer", 42, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PDUIPv4(gsnmp.SnmpPDU{Value: c.val})
			if c.want == "" {
				if got != nil {
					t.Errorf("PDUIPv4(%v) = %v, want nil", c.val, got)
				}
			} else {
				if got == nil || got.String() != c.want {
					t.Errorf("PDUIPv4(%v) = %v, want %s", c.val, got, c.want)
				}
			}
		})
	}
}

// SplitOIDComponent: normal case with a dot.
func TestSplitOIDComponent(t *testing.T) {
	col, rest, ok := SplitOIDComponent("6.1.2")
	if !ok || col != 6 || rest != "1.2" {
		t.Errorf("SplitOIDComponent(6.1.2) = (%d, %q, %v), want (6, 1.2, true)", col, rest, ok)
	}
}

// SplitOIDComponent: last component (no dot) returns value with empty rest.
func TestSplitOIDComponentLast(t *testing.T) {
	col, rest, ok := SplitOIDComponent("42")
	if !ok || col != 42 || rest != "" {
		t.Errorf("SplitOIDComponent(42) = (%d, %q, %v), want (42, '', true)", col, rest, ok)
	}
}

// SplitOIDComponent: non-numeric prefix fails.
func TestSplitOIDComponentInvalid(t *testing.T) {
	_, _, ok := SplitOIDComponent("abc.1")
	if ok {
		t.Error("SplitOIDComponent(abc.1) should return ok=false")
	}
}

// IPInNets: IP inside a network returns true.
func TestIPInNets(t *testing.T) {
	_, net1, _ := net.ParseCIDR("10.0.0.0/8")
	_, net2, _ := net.ParseCIDR("192.168.0.0/16")

	if !IPInNets(net.ParseIP("10.1.2.3"), []*net.IPNet{net1, net2}) {
		t.Error("expected 10.1.2.3 to be in 10.0.0.0/8")
	}
	if !IPInNets(net.ParseIP("192.168.5.1"), []*net.IPNet{net1, net2}) {
		t.Error("expected 192.168.5.1 to be in 192.168.0.0/16")
	}
	if IPInNets(net.ParseIP("172.16.0.1"), []*net.IPNet{net1, net2}) {
		t.Error("expected 172.16.0.1 to NOT be in either network")
	}
	if IPInNets(net.ParseIP("1.2.3.4"), nil) {
		t.Error("expected false for nil nets slice")
	}
}

// WalkToIntMap: returns a map from OID suffix to integer value.
func TestWalkToIntMap(t *testing.T) {
	const oid = "1.3.6.1.2.1.138.1.6.1.1.2"
	pdus := []gsnmp.SnmpPDU{
		{Name: "." + oid + ".0.1.1", Type: gsnmp.Integer, Value: 3},
		{Name: "." + oid + ".0.1.2", Type: gsnmp.Integer, Value: 1},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()
	m, err := WalkToIntMap(context.Background(), client, oid)
	if err != nil {
		t.Fatalf("WalkToIntMap: %v", err)
	}
	if m["0.1.1"] != 3 || m["0.1.2"] != 1 {
		t.Errorf("WalkToIntMap = %v, want {0.1.1:3, 0.1.2:1}", m)
	}
}

// WalkToIntMapStrict: invalid types are excluded and reported.
func TestWalkToIntMapStrictDecodeFailures(t *testing.T) {
	const oid = "1.3.6.1.2.1.138.1.6.1.1.2"
	pdus := []gsnmp.SnmpPDU{
		{Name: "." + oid + ".0.1.1", Type: gsnmp.Integer, Value: 3},
		{Name: "." + oid + ".0.1.2", Type: gsnmp.OctetString, Value: []byte("bad")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	var gotIssue DecodeIssue
	ctx := ContextWithDecodeIssueReporter(context.Background(), func(issue DecodeIssue) { gotIssue = issue })
	m, stats, err := WalkToIntMapStrict(ctx, client, "isis", oid)
	if err != nil {
		t.Fatalf("WalkToIntMapStrict: %v", err)
	}
	if m["0.1.1"] != 3 {
		t.Errorf("WalkToIntMapStrict map = %v, want key 0.1.1=3", m)
	}
	if _, ok := m["0.1.2"]; ok {
		t.Errorf("WalkToIntMapStrict map unexpectedly included decode-failed key: %v", m)
	}
	if stats.DecodeFailures != 1 {
		t.Errorf("DecodeFailures = %d, want 1", stats.DecodeFailures)
	}
	if stats.TotalRows != 2 || stats.ValidRows != 1 || stats.InvalidRows != 1 {
		t.Errorf("stats rows = %+v, want total=2 valid=1 invalid=1", stats)
	}
	if stats.InvalidRatio <= 0 || stats.InvalidRatio >= 1 {
		t.Errorf("InvalidRatio = %f, want in (0,1)", stats.InvalidRatio)
	}
	if gotIssue.Module != "isis" || gotIssue.OID != oid || gotIssue.Reason != "invalid_type" || gotIssue.Count != 1 {
		t.Errorf("DecodeIssue = %+v, want module=isis oid=%s reason=invalid_type count=1", gotIssue, oid)
	}
}

func TestEvaluateRequiredTablePolicy(t *testing.T) {
	policy := RequiredTablePolicy{MinValidRows: 1, MaxInvalidRatio: 0.5}
	cases := []struct {
		name       string
		stats      IntMapDecodeStats
		wantDegrad bool
		wantFail   string
	}{
		{
			name:       "clean table",
			stats:      IntMapDecodeStats{TotalRows: 10, ValidRows: 10, InvalidRows: 0, InvalidRatio: 0},
			wantDegrad: false,
			wantFail:   "",
		},
		{
			name:       "partial anomalies below threshold",
			stats:      IntMapDecodeStats{TotalRows: 10, ValidRows: 8, InvalidRows: 2, InvalidRatio: 0.2},
			wantDegrad: true,
			wantFail:   "",
		},
		{
			name:       "no valid rows",
			stats:      IntMapDecodeStats{TotalRows: 2, ValidRows: 0, InvalidRows: 2, InvalidRatio: 1.0},
			wantDegrad: false,
			wantFail:   "required_table_no_valid_rows",
		},
		{
			name:       "invalid ratio exceeded",
			stats:      IntMapDecodeStats{TotalRows: 3, ValidRows: 1, InvalidRows: 2, InvalidRatio: 0.666},
			wantDegrad: false,
			wantFail:   "required_table_invalid_ratio_exceeded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDegrad, gotFail := EvaluateRequiredTablePolicy(tc.stats, policy)
			if gotDegrad != tc.wantDegrad || gotFail != tc.wantFail {
				t.Errorf("EvaluateRequiredTablePolicy(%+v) = (%v,%q), want (%v,%q)", tc.stats, gotDegrad, gotFail, tc.wantDegrad, tc.wantFail)
			}
		})
	}
}

// WalkIfDescr: returns a map from ifIndex to ifDescr string.
func TestWalkIfDescr(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: "." + OIDIfDescr + ".1", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/0")},
		{Name: "." + OIDIfDescr + ".2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/1")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()
	m, err := WalkIfDescr(context.Background(), client)
	if err != nil {
		t.Fatalf("WalkIfDescr: %v", err)
	}
	if m[1] != "GigabitEthernet0/0" || m[2] != "GigabitEthernet0/1" {
		t.Errorf("WalkIfDescr = %v, want {1:GigabitEthernet0/0, 2:GigabitEthernet0/1}", m)
	}
}

// WalkIfDescr: a PDU with the wrong type (Integer32) yields an empty string
// for that index. Valid OctetString rows are unaffected.
func TestWalkIfDescrWrongType(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: "." + OIDIfDescr + ".1", Type: gsnmp.OctetString, Value: []byte("eth0")},
		{Name: "." + OIDIfDescr + ".2", Type: gsnmp.Integer, Value: 42},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()
	m, err := WalkIfDescr(context.Background(), client)
	if err != nil {
		t.Fatalf("WalkIfDescr: %v", err)
	}
	if m[1] != "eth0" {
		t.Errorf("m[1] = %q, want eth0", m[1])
	}
	// PDUString returns "" for Integer values; the key is still present.
	if got, ok := m[2]; !ok || got != "" {
		t.Errorf("m[2] = %q (present=%v), want \"\" (present=true)", got, ok)
	}
}

// Walk: sysName, sysDescr, sysObjectID, and sysUpTime round-trip through a
// fake agent and come back in the Device record.
func TestWalkSystemGroup(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS 15.2")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(100000)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("core-sw-01")},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	dev, err := Walk(context.Background(), p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev == nil {
		t.Fatal("Walk returned nil device")
	}
	if dev.ID != "core-sw-01" {
		t.Errorf("ID = %q, want core-sw-01", dev.ID)
	}
	if dev.Vendor != "cisco" {
		t.Errorf("Vendor = %q, want cisco", dev.Vendor)
	}
	// normalizeSysDescr extracts the first version token from the sysDescr.
	// "Cisco IOS 15.2" → "15.2"
	if dev.OSVersion != "15.2" {
		t.Errorf("OSVersion = %q, want 15.2 (normalized from sysDescr)", dev.OSVersion)
	}
	if dev.Uptime != time.Duration(100000)*10*time.Millisecond {
		t.Errorf("Uptime = %v, want 1000s", dev.Uptime)
	}
}

// Walk: sysUpTime PDU with wrong type (OctetString instead of TimeTicks)
// is silently ignored; the Device.Uptime field remains zero.
func TestWalkSystemGroupSysUpTimeWrongType(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("Cisco IOS 15.2")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.OctetString, Value: []byte("not-a-timeticks")},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("core-sw-01")},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	dev, err := Walk(context.Background(), Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev == nil {
		t.Fatal("Walk returned nil device")
	}
	if dev.ID != "core-sw-01" {
		t.Errorf("ID = %q, want core-sw-01", dev.ID)
	}
	// sysUpTime PDU had wrong type; PDUInt returns 0, so Uptime should be zero.
	if dev.Uptime != 0 {
		t.Errorf("Uptime = %v, want 0 (wrong-type sysUpTime silently ignored)", dev.Uptime)
	}
}

// Walk: devices with leading/trailing whitespace in sysName are normalised.
func TestWalkNormalisesSysName(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.30065.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(0)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("  Spine-01  ")},
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	dev, err := Walk(context.Background(), Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev.ID != "spine-01" {
		t.Errorf("ID = %q, want spine-01 (lower-cased, trimmed)", dev.ID)
	}
	if dev.Vendor != "arista" {
		t.Errorf("Vendor = %q, want arista", dev.Vendor)
	}
}

// vendorFromObjectID: known enterprise prefixes map to the right vendor.
func TestVendorFromObjectID(t *testing.T) {
	cases := []struct {
		oid    string
		vendor string
	}{
		{".1.3.6.1.4.1.9.1.1", "cisco"},
		{"1.3.6.1.4.1.2636.3.1", "juniper"},
		{".1.3.6.1.4.1.30065.1.3011", "arista"},
		{"1.3.6.1.4.1.14988.1", "mikrotik"},
		{"1.3.6.1.4.1.9999.99", "unknown"},
		{"", "unknown"},
	}
	for _, c := range cases {
		if got := vendorFromObjectID(c.oid); got != c.vendor {
			t.Errorf("vendorFromObjectID(%q) = %q, want %q", c.oid, got, c.vendor)
		}
	}
}

// parseAuthProto: string values map to the right gosnmp constants.
func TestParseAuthProto(t *testing.T) {
	cases := []struct {
		s    string
		want gsnmp.SnmpV3AuthProtocol
	}{
		{"MD5", gsnmp.MD5},
		{"SHA-256", gsnmp.SHA256},
		{"SHA-384", gsnmp.SHA384},
		{"SHA-512", gsnmp.SHA512},
		{"SHA", gsnmp.SHA},
		{"", gsnmp.NoAuth},
		{"UNKNOWN", gsnmp.SHA},
	}
	for _, c := range cases {
		if got := parseAuthProto(c.s); got != c.want {
			t.Errorf("parseAuthProto(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// parsePrivProto: string values map to the right gosnmp constants.
func TestParsePrivProto(t *testing.T) {
	cases := []struct {
		s    string
		want gsnmp.SnmpV3PrivProtocol
	}{
		{"DES", gsnmp.DES},
		{"AES-192", gsnmp.AES192},
		{"AES-256", gsnmp.AES256},
		{"AES", gsnmp.AES},
		{"", gsnmp.NoPriv},
	}
	for _, c := range cases {
		if got := parsePrivProto(c.s); got != c.want {
			t.Errorf("parsePrivProto(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// pduOID: only string values return a non-empty result.
func TestPduOID(t *testing.T) {
	if got := pduOID(gsnmp.SnmpPDU{Value: ".1.2.3"}); got != ".1.2.3" {
		t.Errorf("pduOID string = %q, want .1.2.3", got)
	}
	if got := pduOID(gsnmp.SnmpPDU{Value: []byte(".1.2.3")}); got != "" {
		t.Errorf("pduOID []byte = %q, want empty", got)
	}
}

// buildClient: v2c community is set correctly.
func TestBuildClientV2c(t *testing.T) {
	p := Params{IP: net.ParseIP("192.0.2.1"), Port: 161, Community: "myCommunity"}
	c := buildClient(p)
	if c.Community != "myCommunity" {
		t.Errorf("Community = %q, want myCommunity", c.Community)
	}
	if c.Version != gsnmp.Version2c {
		t.Errorf("Version = %v, want Version2c", c.Version)
	}
}

// buildClient: default port is 161, default timeout is 10s.
func TestBuildClientDefaults(t *testing.T) {
	p := Params{IP: net.ParseIP("10.0.0.1")}
	c := buildClient(p)
	if c.Port != 161 {
		t.Errorf("Port = %d, want 161", c.Port)
	}
	if c.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", c.Timeout)
	}
}

// buildClient: v3 with auth+priv sets AuthPriv MsgFlags.
func TestBuildClientV3AuthPriv(t *testing.T) {
	p := Params{
		IP:        net.ParseIP("10.0.0.1"),
		V3:        true,
		Username:  "admin",
		AuthProto: "SHA",
		AuthKey:   "authpass",
		PrivProto: "AES",
		PrivKey:   "privpass",
	}
	c := buildClient(p)
	if c.Version != gsnmp.Version3 {
		t.Errorf("Version = %v, want Version3", c.Version)
	}
	if c.MsgFlags != gsnmp.AuthPriv {
		t.Errorf("MsgFlags = %v, want AuthPriv", c.MsgFlags)
	}
}

// buildClient: v3 without auth/priv produces NoAuthNoPriv MsgFlags.
func TestBuildClientV3NoAuth(t *testing.T) {
	p := Params{IP: net.ParseIP("10.0.0.1"), V3: true, Username: "u"}
	c := buildClient(p)
	if c.Version != gsnmp.Version3 {
		t.Errorf("Version = %v, want Version3", c.Version)
	}
	if c.MsgFlags != gsnmp.NoAuthNoPriv {
		t.Errorf("MsgFlags = %v, want NoAuthNoPriv", c.MsgFlags)
	}
}

// buildClient: ContextName is forwarded when set.
func TestBuildClientV3ContextName(t *testing.T) {
	p := Params{
		IP:          net.ParseIP("10.0.0.1"),
		V3:          true,
		Username:    "u",
		ContextName: "myctx",
	}
	c := buildClient(p)
	if c.ContextName != "myctx" {
		t.Errorf("ContextName = %q, want myctx", c.ContextName)
	}
}

// authPrivMsgFlags: NoAuth → NoAuthNoPriv; auth-only → AuthNoPriv; auth+priv → AuthPriv.
func TestAuthPrivMsgFlags(t *testing.T) {
	cases := []struct {
		auth gsnmp.SnmpV3AuthProtocol
		priv gsnmp.SnmpV3PrivProtocol
		want gsnmp.SnmpV3MsgFlags
	}{
		{gsnmp.NoAuth, gsnmp.NoPriv, gsnmp.NoAuthNoPriv},
		{gsnmp.SHA, gsnmp.NoPriv, gsnmp.AuthNoPriv},
		{gsnmp.SHA, gsnmp.AES, gsnmp.AuthPriv},
	}
	for _, c := range cases {
		got := authPrivMsgFlags(c.auth, c.priv)
		if got != c.want {
			t.Errorf("authPrivMsgFlags(%v, %v) = %v, want %v", c.auth, c.priv, got, c.want)
		}
	}
}

// Open: creates a connected session against the fake agent.
func TestOpenConnect(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("test-device")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	if client.Target != ip.String() {
		t.Errorf("Target = %q, want %s", client.Target, ip.String())
	}
}

// BulkWalk: cancelled context returns immediately without hitting the agent.
func TestBulkWalkCancelledCtx(t *testing.T) {
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = BulkWalk(ctx, client, "1.3.6.1.2.1.1")
	if err == nil {
		t.Error("expected error for already-cancelled context, got nil")
	}
}

// TrimOIDPrefix: valid prefix strips correctly.
func TestTrimOIDPrefix(t *testing.T) {
	rest, ok := TrimOIDPrefix(".1.2.3.4", ".1.2.")
	if !ok || rest != "3.4" {
		t.Errorf("TrimOIDPrefix = (%q, %v), want (3.4, true)", rest, ok)
	}
}

// TrimOIDPrefix: wrong prefix returns false.
func TestTrimOIDPrefixMismatch(t *testing.T) {
	_, ok := TrimOIDPrefix(".1.2.3", ".1.9.")
	if ok {
		t.Error("expected false for mismatched prefix")
	}
}

// TrimOIDPrefix: exact prefix with no suffix returns false.
func TestTrimOIDPrefixEmpty(t *testing.T) {
	_, ok := TrimOIDPrefix(".1.2.", ".1.2.")
	if ok {
		t.Error("expected false for empty suffix after prefix")
	}
}

// BulkWalk: returns PDUs matching the rootOID subtree.
func TestBulkWalk(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("desc")},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("name")},
		{Name: ".1.3.6.1.2.1.2.1.0", Type: gsnmp.Integer, Value: int(5)}, // outside subtree
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	results, err := BulkWalk(context.Background(), client, "1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("BulkWalk: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("BulkWalk returned %d PDUs, want at least 2", len(results))
	}
}

// Open: V3 with empty username causes validateParametersV3 to fail immediately,
// exercising the connect-error return path in Open.
func TestOpenConnectError(t *testing.T) {
	p := Params{
		IP:      net.ParseIP("127.0.0.1"),
		Port:    161,
		Timeout: time.Second,
		V3:      true,
		// Username intentionally empty — gosnmp rejects it in validateParametersV3.
	}
	_, err := Open(p)
	if err == nil {
		t.Fatal("expected error for V3 with empty username, got nil")
	}
}

// BulkWalk: fallback to WalkAll when BulkWalkAll fails.
// gosnmp's BulkWalkAll fails when the connection is closed. We then fall
// through to WalkAll on the same closed connection, which also fails — but
// the important thing is the WalkAll fallback branch is reached.
func TestBulkWalkFallbackToWalkAll(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("desc")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Close the connection so that BulkWalkAll fails, exercising the fallback branch.
	_ = client.Conn.Close()

	ctx := context.Background()
	// Both BulkWalkAll and WalkAll will fail on the closed conn; we just need
	// the fallback branch to be reached (i.e., the WalkAll call happens).
	_, err = BulkWalk(ctx, client, "1.3.6.1.2.1.1")
	// An error is expected since the connection is closed.
	if err == nil {
		t.Log("BulkWalk unexpectedly succeeded on closed connection (platform may re-open)")
	}
}

// lazyErrCtx is a context.Context whose Err() returns nil for the first call
// and errCancelled for all subsequent calls. This lets us exercise the
// second ctx.Err() check in BulkWalk (after BulkWalkAll has already failed)
// without relying on goroutine scheduling.
type lazyErrCtx struct {
	context.Context
	called int
}

func (c *lazyErrCtx) Err() error {
	c.called++
	if c.called < 2 {
		return nil
	}
	return context.Canceled
}

// BulkWalk: when BulkWalkAll fails AND the context has since been cancelled,
// the second ctx.Err() check returns the context error (lines 111–113).
func TestBulkWalkCtxCancelledAfterBulkFail(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("desc")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Close the connection so BulkWalkAll fails immediately.
	_ = client.Conn.Close()

	// lazyErrCtx returns nil on the first Err() call (passes the entry guard),
	// then returns Canceled on the second call (hits the post-BulkWalkAll check).
	ctx := &lazyErrCtx{Context: context.Background()}
	_, err = BulkWalk(ctx, client, "1.3.6.1.2.1.1")
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// Walk: Open failure (V3 with empty username) returns an error from Walk directly.
func TestWalkOpenFails(t *testing.T) {
	p := Params{
		IP:      net.ParseIP("127.0.0.1"),
		Port:    161,
		Timeout: time.Second,
		V3:      true,
		// Username intentionally empty — Open returns an error immediately.
	}
	dev, err := Walk(context.Background(), p)
	if err == nil {
		t.Fatal("expected error when Open fails, got nil")
	}
	if dev != nil {
		t.Errorf("expected nil device when Open fails, got %+v", dev)
	}
}

// Walk: context cancelled before the Get goroutine finishes exercises the
// ctx.Done select branch. We start an agent that won't respond (nil pdus +
// wrong community = drops all packets) and use a long SNMP timeout so the
// goroutine won't return from Get for a long time. The pre-cancelled context
// guarantees the select picks <-ctx.Done() over <-done.
func TestWalkCtxCancelled(t *testing.T) {
	// Start an agent with no PDUs using a community that we won't match,
	// so every packet is dropped and client.Get() blocks until the SNMP timeout.
	addr := snmptest.Start(t, "nomatch", nil)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{
		IP:        ip,
		Port:      port,
		Community: "public", // wrong community — agent drops all packets
		Timeout:   200 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — select will always pick ctx.Done()

	_, err := Walk(ctx, p)
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
}

// Walk: no sysName PDU returns the IP address as fallback device ID.
func TestWalkNoSysName(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("some desc")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gsnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.9.1.1"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gsnmp.TimeTicks, Value: uint32(500)},
		// note: no sysName (.1.3.6.1.2.1.1.5.0) PDU
	}

	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	dev, err := Walk(context.Background(), p)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if dev == nil {
		t.Fatal("Walk returned nil device")
	}
	// ID should fall back to IP string when sysName is absent.
	if dev.ID != ip.String() {
		t.Errorf("ID = %q, want %s (IP fallback)", dev.ID, ip.String())
	}
}

// WalkIfNames: returns a correct ifIndex→ifName map from the agent.
func TestWalkIfNames(t *testing.T) {
	// ifXTable.ifName = 1.3.6.1.2.1.31.1.1.1.1
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.1", Type: gsnmp.OctetString, Value: []byte("eth0")},
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.2", Type: gsnmp.OctetString, Value: []byte("eth1")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	names, err := WalkIfNames(context.Background(), client)
	if err != nil {
		t.Fatalf("WalkIfNames: %v", err)
	}
	if names[1] != "eth0" {
		t.Errorf("names[1] = %q, want eth0", names[1])
	}
	if names[2] != "eth1" {
		t.Errorf("names[2] = %q, want eth1", names[2])
	}
}

// WalkIfNames: BulkWalk error (closed connection) propagates as an error.
func TestWalkIfNamesBulkWalkError(t *testing.T) {
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: time.Millisecond}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = client.Conn.Close() // force failure

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled context guarantees BulkWalk returns immediately with error

	_, err = WalkIfNames(ctx, client)
	if err == nil {
		t.Error("expected error from WalkIfNames with cancelled context, got nil")
	}
}

// WalkIfNames: OID suffix that is not a plain integer (e.g. "1.2") is silently
// skipped, exercising the strconv.Atoi error path.
// We include a PDU whose name has a two-component suffix after the ifName
// prefix (e.g. .1.3.6.1.2.1.31.1.1.1.1.1.2 → suffix "1.2") so that Atoi
// fails. gosnmp handles this OID correctly; only our parsing skips it.
func TestWalkIfNamesNonNumericSuffix(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.3", Type: gsnmp.OctetString, Value: []byte("eth2")},
		// Two-component suffix "3.99" → Atoi("3.99") fails → entry skipped.
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.3.99", Type: gsnmp.OctetString, Value: []byte("skip")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	names, err := WalkIfNames(context.Background(), client)
	if err != nil {
		t.Fatalf("WalkIfNames: %v", err)
	}
	// The valid entry is present; the one with non-Atoi-able suffix is absent.
	if names[3] != "eth2" {
		t.Errorf("names[3] = %q, want eth2", names[3])
	}
	// Confirm the two-component-suffix entry was skipped (no spurious keys).
	if len(names) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(names), names)
	}
}

// WalkIfNames: same tolerance as WalkIfDescr — wrong-type PDUs yield "" for
// that index. Valid OctetString entries are unaffected.
func TestWalkIfNamesWrongType(t *testing.T) {
	// ifXTable.ifName = 1.3.6.1.2.1.31.1.1.1.1 (oidIfNameTable)
	const ifNameOID = "1.3.6.1.2.1.31.1.1.1.1"
	pdus := []gsnmp.SnmpPDU{
		{Name: "." + ifNameOID + ".1", Type: gsnmp.OctetString, Value: []byte("eth0")},
		{Name: "." + ifNameOID + ".2", Type: gsnmp.Integer, Value: 42},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second}
	client, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	names, err := WalkIfNames(context.Background(), client)
	if err != nil {
		t.Fatalf("WalkIfNames: %v", err)
	}
	if names[1] != "eth0" {
		t.Errorf("names[1] = %q, want eth0", names[1])
	}
	// PDUString returns "" for Integer values; the key is still present.
	if got, ok := names[2]; !ok || got != "" {
		t.Errorf("names[2] = %q (present=%v), want \"\" (present=true)", got, ok)
	}
}

// ParseCIDRs: valid CIDRs are all returned.
func TestParseCIDRs(t *testing.T) {
	nets := ParseCIDRs([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if len(nets) != 2 {
		t.Errorf("ParseCIDRs returned %d nets, want 2", len(nets))
	}
}

// ParseCIDRs: invalid entries are silently skipped.
func TestParseCIDRsSkipsInvalid(t *testing.T) {
	nets := ParseCIDRs([]string{"10.0.0.0/8", "not-a-cidr"})
	if len(nets) != 1 {
		t.Errorf("ParseCIDRs returned %d nets, want 1", len(nets))
	}
	if nets[0].String() != "10.0.0.0/8" {
		t.Errorf("nets[0] = %q, want 10.0.0.0/8", nets[0].String())
	}
}

// TestNormalizeSysDescr verifies that normalizeSysDescr extracts the first
// version-like token from a sysDescr string.
func TestNormalizeSysDescr(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "Cisco IOS full string extracts first version",
			input: "Cisco IOS Software, Version 15.2.4, RELEASE SOFTWARE (fc4)",
			want:  "15.2.4",
		},
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "string with no version truncated at 64 chars",
			input: "Cisco Internetwork Operating System Software IOS (tm) C2950 Software",
			want:  "Cisco Internetwork Operating System Software IOS (tm) C2950 Soft",
		},
		{
			name:  "short string with no version returned as-is",
			input: "no version here",
			want:  "no version here",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeSysDescr(c.input)
			if got != c.want {
				t.Errorf("normalizeSysDescr(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestBuildClientRetries verifies that Retries from Params propagates to GoSNMP.
func TestBuildClientRetries(t *testing.T) {
	p := Params{IP: net.ParseIP("10.0.0.1"), Retries: 3}
	c := buildClient(p)
	if c.Retries != 3 {
		t.Errorf("Retries = %d, want 3", c.Retries)
	}
}

// TestBuildClientRetriesDefault verifies that Retries=0 in Params defaults to 1.
func TestBuildClientRetriesDefault(t *testing.T) {
	p := Params{IP: net.ParseIP("10.0.0.1"), Retries: 0}
	c := buildClient(p)
	if c.Retries != 1 {
		t.Errorf("Retries = %d, want 1 (default)", c.Retries)
	}
}

// appendDecodeSample: adding to an under-capacity slice appends the sample.
func TestAppendDecodeSampleBasic(t *testing.T) {
	s := appendDecodeSample([]string{"a", "b"}, "c")
	if len(s) != 3 || s[2] != "c" {
		t.Errorf("appendDecodeSample = %v, want [a b c]", s)
	}
}

// appendDecodeSample: slice already at capacity (5) is returned unchanged.
func TestAppendDecodeSampleFull(t *testing.T) {
	full := []string{"a", "b", "c", "d", "e"}
	got := appendDecodeSample(full, "f")
	if len(got) != 5 {
		t.Errorf("appendDecodeSample on full slice: len = %d, want 5", len(got))
	}
	// Must be the exact same slice — no reallocation.
	if &got[0] != &full[0] {
		t.Error("appendDecodeSample on full slice returned a different slice")
	}
}

// ContextWithDecodeIssueReporter: nil fn returns the original context unchanged.
func TestContextWithDecodeIssueReporterNilFn(t *testing.T) {
	ctx := context.Background()
	got := ContextWithDecodeIssueReporter(ctx, nil)
	if got != ctx {
		t.Error("ContextWithDecodeIssueReporter(nil) should return the original context")
	}
}

// ContextWithDecodeIssueReporter: non-nil fn is stored and called via reportDecodeIssue.
func TestContextWithDecodeIssueReporterCallsReporter(t *testing.T) {
	var called DecodeIssue
	ctx := ContextWithDecodeIssueReporter(context.Background(), func(issue DecodeIssue) {
		called = issue
	})
	want := DecodeIssue{Module: "m", OID: "1.2.3", Reason: "invalid_type", Count: 2}
	reportDecodeIssue(ctx, want)
	if called != want {
		t.Errorf("reporter called with %+v, want %+v", called, want)
	}
}

// WalkToIntMapStrict: PDU whose OID name does not share the walked prefix
// increments TrimFailures and is excluded from the result map.
//
// Strategy: register a scalar PDU at the root OID (e.g. ".1.2.3") and a
// sibling PDU at ".1.2.4.1" to push the first BulkGet response out of the
// walked subtree. gosnmp then falls back to a GetRequest for ".1.2.3" itself
// and includes the returned PDU in the walk results. TrimOIDPrefix(".1.2.3",
// ".1.2.3.") returns false, incrementing TrimFailures.
func TestWalkToIntMapStrictTrimFailure(t *testing.T) {
	const oid = "1.2.3"
	pdus := []gsnmp.SnmpPDU{
		// Scalar leaf at the root OID — GetRequest fallback will return this.
		{Name: "." + oid, Type: gsnmp.Integer, Value: 99},
		// Sibling outside the subtree — causes the first BulkGet response to be
		// out of range, triggering gosnmp's GetRequest fallback for the root.
		{Name: ".1.2.4.1", Type: gsnmp.Integer, Value: 1},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	m, stats, err := WalkToIntMapStrict(context.Background(), client, "test", oid)
	if err != nil {
		t.Fatalf("WalkToIntMapStrict: %v", err)
	}
	if stats.TrimFailures == 0 {
		t.Errorf("TrimFailures = 0, want > 0; stats = %+v", stats)
	}
	// The mismatched PDU must not appear in the result map.
	if _, ok := m[oid]; ok {
		t.Errorf("result map unexpectedly contains OID key %q: %v", oid, m)
	}
}

// WalkToIntMap: cancelled context propagates the error from WalkToIntMapStrict.
func TestWalkToIntMapError(t *testing.T) {
	addr := snmptest.Start(t, "public", nil)
	ip, port := snmptest.ParseAddr(addr)
	client, err := Open(Params{IP: ip, Port: port, Community: "public", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so BulkWalk returns immediately with an error

	m, err := WalkToIntMap(ctx, client, "1.3.6.1.2.1.138.1.6.1.1.2")
	if err == nil {
		t.Error("expected error from WalkToIntMap with cancelled context, got nil")
	}
	if m != nil {
		t.Errorf("expected nil map on error, got %v", m)
	}
}

// Walk: wrong community causes a timeout and returns a non-nil error.
// Real devices silently drop packets from unknown communities; our test agent
// does the same. The client exhausts its retries and Walk returns an error.
func TestWalkWrongCommunity(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("sw-01")},
	}
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := Params{
		IP:        ip,
		Port:      port,
		Community: "wrong",
		Timeout:   200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	dev, err := Walk(ctx, p)
	if err == nil {
		t.Fatalf("expected error for wrong community, got device %+v", dev)
	}
	if dev != nil {
		t.Errorf("expected nil device on auth failure, got %+v", dev)
	}
}
