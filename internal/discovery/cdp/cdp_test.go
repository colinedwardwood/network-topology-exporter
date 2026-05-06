package cdp

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// buildEdges: in-scope neighbor with IPv4 address produces an edge.
func TestBuildEdgesInScope(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("10.0.0.0/8")

	ifNames := map[int]string{3: "GigabitEthernet0/3"}
	entries := map[cacheKey]*cacheEntry{
		{3, 1}: {
			addrType: 1,
			addr:     []byte{10, 1, 2, 3},
			deviceID: "peer-sw",
			devPort:  "Gi0/1",
		},
	}

	edges, oos, err := buildEdges("local-sw", ifNames, entries, []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.SrcDevice != "local-sw" || e.SrcPort != "GigabitEthernet0/3" {
		t.Errorf("src = %q/%q, want local-sw/GigabitEthernet0/3", e.SrcDevice, e.SrcPort)
	}
	if e.DstDevice != "peer-sw" || e.DstPort != "Gi0/1" {
		t.Errorf("dst = %q/%q, want peer-sw/Gi0/1", e.DstDevice, e.DstPort)
	}
	if e.DiscoveryProto != "cdp" {
		t.Errorf("proto = %q, want cdp", e.DiscoveryProto)
	}
	if e.Direction != discovery.DirectionUnidirectional {
		t.Errorf("direction = %q, want unidirectional", e.Direction)
	}
	if e.PrecedenceRank != precedenceRank {
		t.Errorf("rank = %d, want %d", e.PrecedenceRank, precedenceRank)
	}
}

// buildEdges: out-of-scope neighbor is recorded in oos and skipped as an edge.
func TestBuildEdgesOutOfScope(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("10.0.0.0/8")

	ifNames := map[int]string{1: "Gi0/1"}
	entries := map[cacheKey]*cacheEntry{
		{1, 1}: {
			addrType: 1,
			addr:     []byte{192, 168, 1, 1},
			deviceID: "remote-sw",
			devPort:  "Gi0/2",
		},
	}

	edges, oos, err := buildEdges("local-sw", ifNames, entries, []*net.IPNet{allowed})
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected no edges for out-of-scope, got %d", len(edges))
	}
	if len(oos) != 1 {
		t.Fatalf("expected 1 oos entry, got %d", len(oos))
	}
	if oos[0].NeighbourHint != "remote-sw" {
		t.Errorf("NeighbourHint = %q, want remote-sw", oos[0].NeighbourHint)
	}
}

// buildEdges: empty allowedNets means no scope filtering.
func TestBuildEdgesNoFilter(t *testing.T) {
	ifNames := map[int]string{1: "eth0"}
	entries := map[cacheKey]*cacheEntry{
		{1, 1}: {
			addrType: 1,
			addr:     []byte{1, 2, 3, 4},
			deviceID: "far-sw",
			devPort:  "eth1",
		},
	}

	edges, oos, err := buildEdges("me", ifNames, entries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected oos with nil allowedNets")
	}
	if len(edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(edges))
	}
}

// buildEdges: entries without deviceID or devPort are skipped.
func TestBuildEdgesSkipsIncomplete(t *testing.T) {
	ifNames := map[int]string{1: "eth0"}
	entries := map[cacheKey]*cacheEntry{
		{1, 1}: {deviceID: "", devPort: "Gi0/1"}, // no deviceID
		{1, 2}: {deviceID: "sw", devPort: ""},    // no devPort
	}

	edges, _, err := buildEdges("me", ifNames, entries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected no edges, got %d", len(edges))
	}
}

// buildEdges: unknown ifIndex falls back to the numeric string.
func TestBuildEdgesFallbackIfIndex(t *testing.T) {
	entries := map[cacheKey]*cacheEntry{
		{7, 1}: {deviceID: "peer", devPort: "eth0"},
	}

	edges, _, err := buildEdges("me", map[int]string{}, entries, nil)
	if err != nil {
		t.Fatalf("buildEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].SrcPort != "7" {
		t.Errorf("SrcPort = %q, want \"7\"", edges[0].SrcPort)
	}
}

// cdpNeighborIP: addrType 1 with 4-byte addr returns the IPv4 address.
func TestCdpNeighborIP(t *testing.T) {
	e := &cacheEntry{addrType: 1, addr: []byte{10, 0, 0, 1}}
	ip := cdpNeighborIP(e)
	if ip == nil || ip.String() != "10.0.0.1" {
		t.Errorf("got %v, want 10.0.0.1", ip)
	}
}

// cdpNeighborIP: wrong addrType returns nil.
func TestCdpNeighborIPWrongType(t *testing.T) {
	e := &cacheEntry{addrType: 2, addr: []byte{10, 0, 0, 1}}
	if ip := cdpNeighborIP(e); ip != nil {
		t.Errorf("expected nil for addrType=2, got %v", ip)
	}
}

// cdpNeighborIP: addrType 1 but wrong length returns nil.
func TestCdpNeighborIPWrongLen(t *testing.T) {
	e := &cacheEntry{addrType: 1, addr: []byte{10, 0, 0}}
	if ip := cdpNeighborIP(e); ip != nil {
		t.Errorf("expected nil for 3-byte addr, got %v", ip)
	}
}

// Walk: end-to-end test with a fake SNMP agent serving ifName and cdpCacheTable.
func TestWalkEndToEnd(t *testing.T) {
	pdus := buildCDPAgentPDUs()
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: "public",
		Timeout:   3 * time.Second,
	}

	edges, oos, err := Walk(context.Background(), p, "local-sw", nil)
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
	if e.SrcDevice != "local-sw" {
		t.Errorf("SrcDevice = %q, want local-sw", e.SrcDevice)
	}
	if e.SrcPort != "GigabitEthernet0/1" {
		t.Errorf("SrcPort = %q, want GigabitEthernet0/1", e.SrcPort)
	}
	if e.DstDevice != "remote-sw" {
		t.Errorf("DstDevice = %q, want remote-sw", e.DstDevice)
	}
	if e.DstPort != "GigabitEthernet0/2" {
		t.Errorf("DstPort = %q, want GigabitEthernet0/2", e.DstPort)
	}
}

// buildCDPAgentPDUs builds the PDUs a fake agent needs to support one CDP neighbor.
//
// ifName: ifIndex 1 → "GigabitEthernet0/1"
// cdpCacheTable: ifIndex=1, neighIndex=1
//
//	col 3 (addrType) = 1 (IPv4)
//	col 4 (addr)     = 10.0.0.2
//	col 6 (deviceId) = "remote-sw"
//	col 7 (devPort)  = "GigabitEthernet0/2"
func buildCDPAgentPDUs() []gsnmp.SnmpPDU {
	ifNameOID := ".1.3.6.1.2.1.31.1.1.1.1.1"
	// cdpCacheTable: 1.3.6.1.4.1.9.9.23.1.2.1.1.{col}.{ifIndex}.{neighIndex}
	base := ".1.3.6.1.4.1.9.9.23.1.2.1.1."
	idx := "1.1"

	return []gsnmp.SnmpPDU{
		{Name: ifNameOID, Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/1")},
		{Name: base + strconv.Itoa(colAddressType) + "." + idx, Type: gsnmp.Integer, Value: int(1)},
		{Name: base + strconv.Itoa(colAddress) + "." + idx, Type: gsnmp.OctetString, Value: []byte{10, 0, 0, 2}},
		{Name: base + strconv.Itoa(colDeviceID) + "." + idx, Type: gsnmp.OctetString, Value: []byte("remote-sw")},
		{Name: base + strconv.Itoa(colDevicePort) + "." + idx, Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/2")},
	}
}
