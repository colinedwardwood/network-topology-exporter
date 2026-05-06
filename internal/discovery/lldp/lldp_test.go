package lldp

import (
	"context"
	"net"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// decodePortID: MAC address subtype produces colon-hex notation.
func TestDecodePortIDMAC(t *testing.T) {
	raw := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	got := decodePortID(portSubtypeMACAddress, raw)
	if got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("got %q, want aa:bb:cc:dd:ee:ff", got)
	}
}

// decodePortID: interface name subtype is a raw string, no transformation.
func TestDecodePortIDInterfaceName(t *testing.T) {
	raw := []byte("GigabitEthernet0/1")
	got := decodePortID(portSubtypeInterfaceName, raw)
	if got != "GigabitEthernet0/1" {
		t.Errorf("got %q, want GigabitEthernet0/1", got)
	}
}

// decodePortID: networkAddress subtype with IPv4 (family byte 1, then 4 octets).
func TestDecodePortIDNetworkAddressIPv4(t *testing.T) {
	raw := []byte{1, 192, 168, 1, 1}
	got := decodePortID(portSubtypeNetworkAddress, raw)
	if got != "192.168.1.1" {
		t.Errorf("got %q, want 192.168.1.1", got)
	}
}

// decodePortID: null bytes at end of string are trimmed.
func TestDecodePortIDNullTrim(t *testing.T) {
	raw := []byte{'e', 't', 'h', '0', 0, 0}
	got := decodePortID(portSubtypeLocal, raw)
	if got != "eth0" {
		t.Errorf("got %q, want eth0", got)
	}
}

// decodeChassisID: MAC chassis (most common) produces colon-hex notation.
func TestDecodeChassisIDMAC(t *testing.T) {
	raw := []byte{0x00, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e}
	got := decodeChassisID(chassisSubtypeMACAddress, raw)
	if got != "00:1a:2b:3c:4d:5e" {
		t.Errorf("got %q, want 00:1a:2b:3c:4d:5e", got)
	}
}

// extractChassisIP: non-networkAddress subtype returns nil.
func TestExtractChassisIPWrongSubtype(t *testing.T) {
	if ip := extractChassisIP(chassisSubtypeMACAddress, []byte{0, 0, 0, 0, 0, 0}); ip != nil {
		t.Errorf("expected nil for MAC subtype, got %v", ip)
	}
}

// extractChassisIP: networkAddress IPv4 returns parsed IP.
func TestExtractChassisIPv4(t *testing.T) {
	raw := []byte{1, 10, 0, 0, 1}
	ip := extractChassisIP(chassisSubtypeNetworkAddress, raw)
	if ip == nil || ip.String() != "10.0.0.1" {
		t.Errorf("got %v, want 10.0.0.1", ip)
	}
}

// buildEdges: LD-11 out-of-scope neighbor (network-address chassis outside allow-list).
func TestBuildEdgesOutOfScope(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("10.0.0.0/24")

	locPorts := map[int]locPort{
		1: {idSubtype: portSubtypeInterfaceName, id: []byte("Gi0/1")},
	}
	remEntries := map[remKey]*remEntry{
		{1, 1}: {
			chassisSubtype: chassisSubtypeNetworkAddress,
			chassisID:      []byte{1, 192, 168, 100, 1}, // 192.168.100.1 — outside 10.0.0.0/24
			portSubtype:    portSubtypeInterfaceName,
			portID:         []byte("Gi0/2"),
			sysName:        "remote-sw",
		},
	}

	edges, oos, err := buildEdges("local-sw", locPorts, remEntries, []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected no edges for out-of-scope neighbor, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 out-of-scope entry, got %d", len(oos))
	}
	if oos[0].NeighbourHint != "remote-sw" {
		t.Errorf("NeighbourHint = %q, want remote-sw", oos[0].NeighbourHint)
	}
}

// buildEdges: in-scope neighbor produces an edge with correct fields.
func TestBuildEdgesNormal(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("10.0.0.0/8")

	locPorts := map[int]locPort{
		2: {idSubtype: portSubtypeInterfaceName, id: []byte("Ethernet1")},
	}
	remEntries := map[remKey]*remEntry{
		{2, 1}: {
			chassisSubtype: chassisSubtypeMACAddress,
			chassisID:      []byte{0, 1, 2, 3, 4, 5},
			portSubtype:    portSubtypeInterfaceName,
			portID:         []byte("Ethernet2"),
			sysName:        "spine-01",
		},
	}

	edges, oos, err := buildEdges("leaf-01", locPorts, remEntries, []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope entries: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.SrcDevice != "leaf-01" || e.SrcPort != "Ethernet1" {
		t.Errorf("src = %q/%q, want leaf-01/Ethernet1", e.SrcDevice, e.SrcPort)
	}
	if e.DstDevice != "spine-01" || e.DstPort != "Ethernet2" {
		t.Errorf("dst = %q/%q, want spine-01/Ethernet2", e.DstDevice, e.DstPort)
	}
	if e.DiscoveryProto != "lldp" {
		t.Errorf("proto = %q, want lldp", e.DiscoveryProto)
	}
	if e.Direction != discovery.DirectionUnidirectional {
		t.Errorf("direction = %q, want unidirectional", e.Direction)
	}
	if e.PrecedenceRank != precedenceRank {
		t.Errorf("rank = %d, want %d", e.PrecedenceRank, precedenceRank)
	}
}

// decodeChassisID: networkAddress subtype decodes to IP string.
func TestDecodeChassisIDNetworkAddress(t *testing.T) {
	raw := []byte{1, 10, 0, 0, 1} // IANA family 1 (IPv4) + 4 octets
	got := decodeChassisID(chassisSubtypeNetworkAddress, raw)
	if got != "10.0.0.1" {
		t.Errorf("got %q, want 10.0.0.1", got)
	}
}

// decodeChassisID: unknown subtype falls back to the raw string.
func TestDecodeChassisIDUnknown(t *testing.T) {
	raw := []byte("my-chassis")
	got := decodeChassisID(99, raw)
	if got != "my-chassis" {
		t.Errorf("got %q, want my-chassis", got)
	}
}

// fmtMAC: non-6-byte input falls back to hex encoding.
func TestFmtMACShort(t *testing.T) {
	got := fmtMAC([]byte{0xaa, 0xbb})
	if got != "aabb" {
		t.Errorf("got %q, want aabb", got)
	}
}

// TrimOIDPrefix: valid prefix strips correctly.
func TestTrimOIDPrefixOK(t *testing.T) {
	rest, ok := snmputil.TrimOIDPrefix(".1.2.3.4", ".1.2.")
	if !ok || rest != "3.4" {
		t.Errorf("TrimOIDPrefix = (%q, %v), want (3.4, true)", rest, ok)
	}
}

// TrimOIDPrefix: wrong prefix returns false.
func TestTrimOIDPrefixMismatch(t *testing.T) {
	_, ok := snmputil.TrimOIDPrefix(".1.2.3", ".1.9.")
	if ok {
		t.Error("expected false for mismatched prefix")
	}
}

// TrimOIDPrefix: exact prefix (no suffix) returns false.
func TestTrimOIDPrefixEmpty(t *testing.T) {
	_, ok := snmputil.TrimOIDPrefix(".1.2.", ".1.2.")
	if ok {
		t.Error("expected false for empty suffix after prefix")
	}
}

// buildEdges: entry with no portDesc falls back to portNum string.
func TestBuildEdgesFallbackPortNum(t *testing.T) {
	locPorts := map[int]locPort{}
	remEntries := map[remKey]*remEntry{
		{5, 1}: {
			chassisSubtype: chassisSubtypeMACAddress,
			chassisID:      []byte{0, 1, 2, 3, 4, 5},
			portSubtype:    portSubtypeMACAddress,
			portID:         []byte{0, 1, 2, 3, 4, 6},
			portDesc:       "",
			sysName:        "peer",
		},
	}

	edges, _, err := buildEdges("me", locPorts, remEntries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	// port ID is a MAC address for portSubtypeMACAddress — should produce a non-empty port
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
}

// buildEdges: portID that decodes to "" (null-only bytes) falls back to portDesc.
func TestBuildEdgesUsesPortDesc(t *testing.T) {
	locPorts := map[int]locPort{
		1: {idSubtype: portSubtypeInterfaceName, id: []byte("eth0")},
	}
	remEntries := map[remKey]*remEntry{
		{1, 1}: {
			chassisSubtype: chassisSubtypeMACAddress,
			chassisID:      []byte{0, 1, 2, 3, 4, 5},
			portSubtype:    portSubtypeLocal,
			portID:         []byte{0, 0}, // null bytes TrimRight → "" → use portDesc
			portDesc:       "GigabitEthernet0/1",
			sysName:        "peer",
		},
	}

	edges, _, err := buildEdges("me", locPorts, remEntries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].DstPort != "GigabitEthernet0/1" {
		t.Errorf("DstPort = %q, want GigabitEthernet0/1", edges[0].DstPort)
	}
}

// buildEdges: entries with empty chassisID or portID are skipped.
func TestBuildEdgesSkipsEmpty(t *testing.T) {
	locPorts := map[int]locPort{}
	remEntries := map[remKey]*remEntry{
		{1, 1}: {chassisID: []byte{}, portID: []byte("eth0"), sysName: "peer"},
		{1, 2}: {chassisID: []byte{1}, portID: []byte{}, sysName: "peer"},
	}

	edges, _, err := buildEdges("me", locPorts, remEntries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

// Walk: end-to-end with a fake agent serving lldpLocPortTable and lldpRemTable.
func TestWalkEndToEnd(t *testing.T) {
	pdus := buildLLDPAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "leaf-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected oos: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	e := edges[0]
	if e.SrcDevice != "leaf-01" {
		t.Errorf("SrcDevice = %q, want leaf-01", e.SrcDevice)
	}
	if e.SrcPort != "Ethernet1" {
		t.Errorf("SrcPort = %q, want Ethernet1", e.SrcPort)
	}
	if e.DstDevice != "spine-01" {
		t.Errorf("DstDevice = %q, want spine-01", e.DstDevice)
	}
	if e.DstPort != "Ethernet2" {
		t.Errorf("DstPort = %q, want Ethernet2", e.DstPort)
	}
}

// buildLLDPAgentPDUs builds the minimal PDU set for one local port and one remote entry.
//
// lldpLocPortTable (1.0.8802.1.1.2.1.3.7.1.{col}.{portNum}):
//
//	col 2 (idSubtype) = 5 (interfaceName)
//	col 3 (id)        = "Ethernet1"
//
// lldpRemTable (1.0.8802.1.1.2.1.4.1.1.{col}.{timeMark}.{portNum}.{remIndex}):
//
//	col 4 (chassisSubtype) = 4 (MAC)
//	col 5 (chassisID)      = 00:01:02:03:04:05
//	col 6 (portSubtype)    = 5 (interfaceName)
//	col 7 (portID)         = "Ethernet2"
//	col 9 (sysName)        = "spine-01"
func buildLLDPAgentPDUs() []gsnmp.SnmpPDU {
	locBase := ".1.0.8802.1.1.2.1.3.7.1."
	remBase := ".1.0.8802.1.1.2.1.4.1.1."
	portNum := "1"
	timeMark := "0"
	remIdx := "1"
	remSuffix := timeMark + "." + portNum + "." + remIdx

	return []gsnmp.SnmpPDU{
		// lldpLocPortTable
		{Name: locBase + "2." + portNum, Type: gsnmp.Integer, Value: int(portSubtypeInterfaceName)},
		{Name: locBase + "3." + portNum, Type: gsnmp.OctetString, Value: []byte("Ethernet1")},

		// lldpRemTable
		{Name: remBase + "4." + remSuffix, Type: gsnmp.Integer, Value: int(chassisSubtypeMACAddress)},
		{Name: remBase + "5." + remSuffix, Type: gsnmp.OctetString, Value: []byte{0, 1, 2, 3, 4, 5}},
		{Name: remBase + "6." + remSuffix, Type: gsnmp.Integer, Value: int(portSubtypeInterfaceName)},
		{Name: remBase + "7." + remSuffix, Type: gsnmp.OctetString, Value: []byte("Ethernet2")},
		{Name: remBase + "9." + remSuffix, Type: gsnmp.OctetString, Value: []byte("spine-01")},
	}
}
