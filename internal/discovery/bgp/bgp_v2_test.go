package bgp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// TestDecodeBgp4V2Index covers the four InetAddressType combinations the
// decoder must handle: v4/v4, v4/v6, v6/v4, v6/v6. Each case asserts the
// parsed local + remote IPs and that the decoder consumed the entire suffix.
func TestDecodeBgp4V2Index(t *testing.T) {
	type want struct {
		local, remote string
	}
	cases := []struct {
		name   string
		suffix string
		want   want
	}{
		{
			name:   "v4_v4",
			suffix: "0.1.4.192.0.2.1.1.4.192.0.2.2",
			want:   want{"192.0.2.1", "192.0.2.2"},
		},
		{
			name:   "v4_v6",
			suffix: "0.1.4.192.0.2.1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2",
			want:   want{"192.0.2.1", "2001:db8::2"},
		},
		{
			name:   "v6_v4",
			suffix: "0.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1.1.4.192.0.2.2",
			want:   want{"2001:db8::1", "192.0.2.2"},
		},
		{
			name:   "v6_v6",
			suffix: "0.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2",
			want:   want{"2001:db8::1", "2001:db8::2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localIP, remoteIP, ok := decodeBgp4V2Index(tc.suffix)
			if !ok {
				t.Fatalf("decode failed for suffix %q", tc.suffix)
			}
			if localIP.String() != tc.want.local {
				t.Errorf("local = %q, want %q", localIP.String(), tc.want.local)
			}
			if remoteIP.String() != tc.want.remote {
				t.Errorf("remote = %q, want %q", remoteIP.String(), tc.want.remote)
			}
		})
	}
}

// TestDecodeBgp4V2IndexRejectsMalformed verifies that truncated, length-
// mismatched, and unknown-family indices are rejected rather than producing
// partial peers.
func TestDecodeBgp4V2IndexRejectsMalformed(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
	}{
		{"empty", ""},
		{"too_short", "0.1.4.192"},
		{"v4_length_mismatch", "0.1.6.192.0.2.1.0.0.1.4.192.0.2.2"}, // localAddrLen=6 for v4
		{"unknown_family", "0.99.4.192.0.2.1.1.4.192.0.2.2"},       // addrType 99
		{"trailing_junk", "0.1.4.192.0.2.1.1.4.192.0.2.2.99"},      // extra byte
		{"byte_out_of_range", "0.1.4.999.0.2.1.1.4.192.0.2.2"},     // 999 > 255
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := decodeBgp4V2Index(tc.suffix); ok {
				t.Errorf("decode unexpectedly succeeded for %q", tc.suffix)
			}
		})
	}
}

// TestWalkV2IPv6Peer drives Walk end-to-end against a stub SNMP agent that
// only responds to bgp4V2PeerTable with one established IPv6 peer. The
// kill-switch is on (default); RFC 4273 fallback must not be triggered.
func TestWalkV2IPv6Peer(t *testing.T) {
	addr := snmptest.Start(t, "public", buildV2IPv6PeerPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
	}

	edges, oos, err := Walk(context.Background(), p, "rtr-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(oos) != 0 {
		t.Errorf("unexpected out-of-scope entries: %v", oos)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	if edges[0].DstDevice != "2001:db8::2" {
		t.Errorf("DstDevice = %q, want 2001:db8::2", edges[0].DstDevice)
	}
	if edges[0].DiscoveryProto != "bgp" {
		t.Errorf("DiscoveryProto = %q, want bgp", edges[0].DiscoveryProto)
	}
}

// TestWalkV2DisabledFallsBackToRFC4273 verifies the kill-switch: when
// UseBGPV2MIB is false, the walker only invokes the RFC 4273 path even if
// the device would respond to bgp4V2PeerTable.
func TestWalkV2DisabledFallsBackToRFC4273(t *testing.T) {
	// Serve both tables. v2 has an IPv6 peer; RFC 4273 has an IPv4 peer.
	// Kill-switch off → only the IPv4 RFC 4273 edge appears.
	pdus := append(buildV2IPv6PeerPDUs(), buildBgpAgentPDUs()...)
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: false,
	}

	edges, _, err := Walk(context.Background(), p, "rtr-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RFC 4273, got %d: %v", len(edges), edges)
	}
	if edges[0].DstDevice != "10.0.0.1" {
		t.Errorf("DstDevice = %q, want 10.0.0.1 (RFC 4273 v4 peer); v2 walker may have run despite kill-switch", edges[0].DstDevice)
	}
}

// TestWalkVendorDispatchCisco verifies that when the IETF draft form returns
// no rows but the Cisco cbgpPeer2Table does, the vendor walker handles it.
// Params.Vendor steers dispatch.
func TestWalkVendorDispatchCisco(t *testing.T) {
	addr := snmptest.Start(t, "public", buildCiscoCbgpPeer2PDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-cisco", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Cisco vendor table, got %d", len(edges))
	}
	if edges[0].DstDevice != "2001:db8::5" {
		t.Errorf("DstDevice = %q, want 2001:db8::5", edges[0].DstDevice)
	}
}

// TestWalkVendorFallsBackToRFC4273WhenVendorTableEmpty verifies the final
// RFC 4273 fallback when neither v2 draft nor vendor table responds.
func TestWalkVendorFallsBackToRFC4273WhenVendorTableEmpty(t *testing.T) {
	// Only RFC 4273 PDUs.
	addr := snmptest.Start(t, "public", buildBgpAgentPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   "public",
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco", // would dispatch to Cisco but the table is empty
	}

	edges, _, err := Walk(context.Background(), p, "rtr-01", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RFC 4273 fallback, got %d", len(edges))
	}
	if edges[0].DstDevice != "10.0.0.1" {
		t.Errorf("DstDevice = %q, want 10.0.0.1", edges[0].DstDevice)
	}
}

// TestVendorSpecForKnownVendors covers vendor → spec dispatch.
func TestVendorSpecForKnownVendors(t *testing.T) {
	cases := []struct {
		vendor string
		want   string // expected spec.name; empty means nil
	}{
		{"cisco", "cisco-cbgpPeer2Table"},
		{"juniper", "juniper-jnxBgpM2PeerTable"},
		{"nokia", "nokia-tBgpPeerTable"},
		{"alcatel-lucent", "nokia-tBgpPeerTable"},
		{"arista", ""}, // arista uses IETF draft form, not a vendor table
		{"mikrotik", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, c := range cases {
		t.Run(c.vendor, func(t *testing.T) {
			spec := vendorSpecFor(c.vendor)
			if c.want == "" {
				if spec != nil {
					t.Errorf("vendorSpecFor(%q) = %+v, want nil", c.vendor, spec)
				}
				return
			}
			if spec == nil {
				t.Fatalf("vendorSpecFor(%q) = nil, want spec %q", c.vendor, c.want)
			}
			if spec.name != c.want {
				t.Errorf("spec.name = %q, want %q", spec.name, c.want)
			}
		})
	}
}

// TestBuildV2EdgesSkipsNonEstablished covers the state filter on the v2 path.
func TestBuildV2EdgesSkipsNonEstablished(t *testing.T) {
	peers := map[string]*bgp4V2Peer{
		"idx-established": {state: bgpStateEstablished, remoteAddr: net.ParseIP("2001:db8::1")},
		"idx-active":      {state: 3, remoteAddr: net.ParseIP("2001:db8::2")},
		"idx-idle":        {state: 1, remoteAddr: net.ParseIP("2001:db8::3")},
	}
	edges, _ := buildV2Edges("rtr", peers, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (established only), got %d", len(edges))
	}
	if edges[0].DstDevice != "2001:db8::1" {
		t.Errorf("DstDevice = %q, want 2001:db8::1", edges[0].DstDevice)
	}
}

// buildV2IPv6PeerPDUs returns the PDU set for one established IPv6 peer on
// bgp4V2PeerTable.
//
// Index: peerInstance=0, localAddrType=2 (ipv6), localAddrLen=16,
//        localAddr=2001:db8::1 (16 bytes), remoteAddrType=2, remoteAddrLen=16,
//        remoteAddr=2001:db8::2 (16 bytes)
//
// Columns:
//   13 (state) = 6 (established)
//   8  (remoteAddrType) = 2 (ipv6)
//   9  (remoteAddr) = 2001:db8::2 (as InetAddress OCTET STRING)
func buildV2IPv6PeerPDUs() []gsnmp.SnmpPDU {
	const base = ".1.3.6.1.3.5.1.1.2.1."
	const idx = "0." +
		"2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1." + // localAddr 2001:db8::1 (16 bytes)
		"2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2" // remoteAddr 2001:db8::2 (16 bytes)

	remoteBytes := []byte(net.ParseIP("2001:db8::2"))
	return []gsnmp.SnmpPDU{
		{Name: base + "13." + idx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "8." + idx, Type: gsnmp.Integer, Value: inetAddrTypeIPv6},
		{Name: base + "9." + idx, Type: gsnmp.OctetString, Value: remoteBytes},
	}
}

// buildCiscoCbgpPeer2PDUs returns the PDU set for one established IPv6 peer
// on Cisco's cbgpPeer2Table (1.3.6.1.4.1.9.9.187.1.2.5).
func buildCiscoCbgpPeer2PDUs() []gsnmp.SnmpPDU {
	const base = ".1.3.6.1.4.1.9.9.187.1.2.5.1."
	const idx = "0." +
		"2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.4." + // localAddr 2001:db8::4 (16 bytes)
		"2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.5" // remoteAddr 2001:db8::5 (16 bytes)

	remoteBytes := []byte(net.ParseIP("2001:db8::5"))
	// Cisco columns: 3=state, 11=remoteAddr, 13=remoteAs (per ciscoCbgpPeer2Spec).
	return []gsnmp.SnmpPDU{
		{Name: base + "3." + idx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		{Name: base + "11." + idx, Type: gsnmp.OctetString, Value: remoteBytes},
		{Name: base + "13." + idx, Type: gsnmp.Integer, Value: 65001},
	}
}

// TestSplitOIDPartsErrors exercises the malformed-input branches of
// splitOIDParts so the decoder's error path stays covered.
func TestSplitOIDPartsErrors(t *testing.T) {
	cases := []string{
		"1..2",   // empty component
		"1.2.x",  // non-numeric
		".1.2",   // leading dot is empty component
		"1.2.",   // trailing dot is empty component
	}
	for _, c := range cases {
		if _, err := splitOIDParts(c); err == nil {
			t.Errorf("splitOIDParts(%q) unexpectedly succeeded", c)
		}
	}
}

// TestSplitOIDPartsRoundTrip is a positive control for the parser.
func TestSplitOIDPartsRoundTrip(t *testing.T) {
	got, err := splitOIDParts("0.1.4.192.0.2.1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []int{0, 1, 4, 192, 0, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestPduInetAddressNil verifies that a malformed InetAddress PDU value
// (wrong type, wrong length) returns nil rather than a partial IP.
func TestPduInetAddressNil(t *testing.T) {
	cases := []struct {
		name string
		pdu  gsnmp.SnmpPDU
	}{
		{"nil value", gsnmp.SnmpPDU{Value: nil}},
		{"wrong type int", gsnmp.SnmpPDU{Value: 42}},
		{"truncated bytes", gsnmp.SnmpPDU{Value: []byte{1, 2, 3}}},
		{"oversize bytes", gsnmp.SnmpPDU{Value: make([]byte, 7)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ip := pduInetAddress(tc.pdu); ip != nil {
				t.Errorf("expected nil, got %v", ip)
			}
		})
	}
}

// TestPduInetAddressString verifies the string-typed OCTET STRING decode path
// some implementations surface for InetAddress values.
func TestPduInetAddressString(t *testing.T) {
	pdu := gsnmp.SnmpPDU{Value: "2001:db8::1"}
	ip := pduInetAddress(pdu)
	if ip == nil || ip.String() != "2001:db8::1" {
		t.Errorf("string decode = %v, want 2001:db8::1", ip)
	}
}

// Smoke test that the package's resolveVendor short-circuits on populated
// Params.Vendor without performing an SNMP GET.
func TestResolveVendorPrefersParams(t *testing.T) {
	p := snmputil.Params{Vendor: "cisco"}
	got := resolveVendor(context.Background(), p, nil) // nil client would panic if called
	if got != "cisco" {
		t.Errorf("resolveVendor = %q, want cisco", got)
	}
}

// Smoke test: vendor strings outside the known set do not panic.
func TestResolveVendorUnknownReturnsUnknown(t *testing.T) {
	p := snmputil.Params{Vendor: "obscure-vendor"}
	got := resolveVendor(context.Background(), p, nil)
	if got != "obscure-vendor" {
		t.Errorf("resolveVendor = %q, want passthrough", got)
	}
}

// Defensive: ensure the package-level OID constants didn't drift from the
// expected MIB roots. A bump here without updating the plan should fail loudly.
func TestPackageConstantsStable(t *testing.T) {
	if !strings.HasPrefix(oidBgp4V2PeerTable, "1.3.6.1.3.5") {
		t.Errorf("oidBgp4V2PeerTable = %q does not look like the IETF draft root", oidBgp4V2PeerTable)
	}
	if !strings.HasPrefix(ciscoCbgpPeer2Spec.root, "1.3.6.1.4.1.9.") {
		t.Errorf("Cisco spec root = %q does not start with the Cisco enterprise prefix", ciscoCbgpPeer2Spec.root)
	}
	if !strings.HasPrefix(juniperJnxBgpM2PeerSpec.root, "1.3.6.1.4.1.2636.") {
		t.Errorf("Juniper spec root = %q does not start with the Juniper enterprise prefix", juniperJnxBgpM2PeerSpec.root)
	}
	if !strings.HasPrefix(nokiaTBgpPeerSpec.root, "1.3.6.1.4.1.6527.") {
		t.Errorf("Nokia spec root = %q does not start with the Nokia enterprise prefix", nokiaTBgpPeerSpec.root)
	}
}
