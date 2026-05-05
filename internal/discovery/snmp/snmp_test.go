package snmp

import (
	"context"
	"net"
	"strings"
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
	if !strings.Contains(dev.OSVersion, "Cisco IOS") {
		t.Errorf("OSVersion = %q, want to contain Cisco IOS", dev.OSVersion)
	}
	if dev.Uptime != time.Duration(100000)*10*time.Millisecond {
		t.Errorf("Uptime = %v, want 1000s", dev.Uptime)
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
	defer client.Conn.Close()

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
	defer client.Conn.Close()

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
	defer client.Conn.Close()

	results, err := BulkWalk(context.Background(), client, "1.3.6.1.2.1.1")
	if err != nil {
		t.Fatalf("BulkWalk: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("BulkWalk returned %d PDUs, want at least 2", len(results))
	}
}
