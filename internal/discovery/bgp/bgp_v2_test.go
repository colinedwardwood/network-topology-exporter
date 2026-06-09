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

// Real-device capture files (snmpwalk -On -Oe), loaded directly by the vendor
// walker integration tests via snmptest.LoadCapture so the tests run against
// the exact bytes the device emitted rather than transcribed PDU slices (#59).
// Paths are relative to this package directory (internal/discovery/bgp).
const (
	ciscoCapture   = "../../../lab/cisco-iol-bgp/captures/r1_cisco_cbgpPeer2Table.txt"
	aristaCapture  = "../../../lab/arista-ceos-bgp/captures/r1_arista_bgp4v2.txt"
	juniperCapture = "../../../lab/juniper-jnxbgp/captures/r1_juniper_jnxBgpM2PeerTable.txt"
	nokiaCapture   = "../../../lab/nokia-srbgp/captures/r1_nokia_tBgpPeerNgTable.txt"
)

// --- index decoder tests -----------------------------------------------

// TestDecodeCiscoCbgpPeer2Index covers the index format used by Cisco's
// cbgpPeer2Table. Format: <addrType>.<addrLen>.<addrBytes...>
//
// Reference: real captures at
// lab/cisco-iol-bgp/captures/r{1,2}_cisco_cbgpPeer2Table.txt.
func TestDecodeCiscoCbgpPeer2Index(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		want   string
		ok     bool
	}{
		{"ipv4", "1.4.10.0.0.2", "10.0.0.2", true},
		{"ipv6", "2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2", "2001:db8::2", true},
		{"truncated", "1.4.10.0.0", "", false},
		{"length mismatch v4", "1.6.10.0.0.2", "", false},
		{"unknown family", "99.4.10.0.0.2", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, ok := decodeCiscoCbgpPeer2Index(c.suffix)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if ip.String() != c.want {
				t.Errorf("ip = %q, want %q", ip.String(), c.want)
			}
		})
	}
}

// TestDecodeAristaBgp4v2Index covers Arista's enterprise BGP4V2 index
// format. Format: <peerInstance>.<addrType>.<addrLen>.<addrBytes...>
//
// Reference: real captures at
// lab/arista-ceos-bgp/captures/r{1,2}_arista_bgp4v2.txt.
func TestDecodeAristaBgp4v2Index(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		want   string
		ok     bool
	}{
		{"ipv4 instance 1", "1.1.4.10.0.0.2", "10.0.0.2", true},
		{"ipv6 instance 1", "1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2", "2001:db8::2", true},
		{"only peer instance", "1", "", false},
		{"truncated after type", "1.1", "", false},
		{"length mismatch v4", "1.1.6.10.0.0.2", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, ok := decodeAristaBgp4v2Index(c.suffix)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if ip.String() != c.want {
				t.Errorf("ip = %q, want %q", ip.String(), c.want)
			}
		})
	}
}

// TestDecodeJuniperJnxBgpM2Index covers Juniper's jnxBgpM2PeerTable index,
// VERIFIED against a real vJunos-router (JUNOS 25.4R1.12) capture. Format:
// <instance>.<localAddrType>.<localAddr>.<remoteAddrType>.<remoteAddr>, each
// InetAddress implicit-length, peer = the remote (second) address.
//
// Suffixes are taken verbatim from
// lab/juniper-jnxbgp/captures/r1_juniper_jnxBgpM2PeerTable.txt.
func TestDecodeJuniperJnxBgpM2Index(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		want   string
		ok     bool
	}{
		{"ipv4 peer", "0.1.192.0.2.1.1.192.0.2.2", "192.0.2.2", true},
		{"ipv6 peer", "0.2.32.1.13.184.0.1.0.0.0.0.0.0.0.0.0.1.2.32.1.13.184.0.1.0.0.0.0.0.0.0.0.0.2", "2001:db8:1::2", true},
		{"local only, no remote", "0.1.192.0.2.1", "", false},
		{"truncated remote", "0.1.192.0.2.1.1.192.0", "", false},
		{"unknown local family", "0.99.1.2.3.4.1.192.0.2.2", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, ok := decodeJuniperJnxBgpM2Index(c.suffix)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if ip.String() != c.want {
				t.Errorf("ip = %q, want %q", ip.String(), c.want)
			}
		})
	}
}

// TestDecodeNokiaTBgpPeerNgIndex covers the tBgpPeerNgTable index, VERIFIED
// against a real SR-OS 25.7.R2 capture (#57). Format:
// <vRtrID>.<addrType>.<addrLen>.<addrBytes> — explicit-length InetAddress after
// the router-instance id.
//
// The ipv4 suffix is taken verbatim from
// lab/nokia-srbgp/captures/r1_nokia_tBgpPeerNgTable.txt.
func TestDecodeNokiaTBgpPeerNgIndex(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		want   string
		ok     bool
	}{
		{"ipv4 peer (real capture)", "1.1.4.10.10.10.2", "10.10.10.2", true},
		{"ipv6 peer", "1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.2", "2001:db8::2", true},
		{"vRtrID only", "1", "", false},
		{"truncated address", "1.1.4.10.10", "", false},
		{"length mismatch v4", "1.1.6.10.10.10.2", "", false},
		{"unknown family", "1.99.4.10.10.10.2", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, ok := decodeNokiaTBgpPeerNgIndex(c.suffix)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if ip.String() != c.want {
				t.Errorf("ip = %q, want %q", ip.String(), c.want)
			}
		})
	}
}

// --- vendor walker integration tests -----------------------------------

// TestWalkVendorCisco end-to-ends the Cisco walker against a stub agent
// that mirrors real cbgpPeer2Table output (column numbers from the
// real-device captures: state=3, remoteAs=11, peer IP in the index).
func TestWalkVendorCisco(t *testing.T) {
	addr := snmptest.Start(t, "public", snmptest.LoadCapture(t, ciscoCapture))
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-cisco", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Cisco vendor walker, got %d: %+v", len(edges), edges)
	}
	if got := edges[0].DstDevice; got != "10.0.0.2" {
		t.Errorf("DstDevice = %q, want 10.0.0.2 (from cbgpPeer2 index)", got)
	}
	if got := edges[0].Metadata[metaKeyRemoteAs]; got != "65001" {
		t.Errorf("RemoteAs metadata = %q, want 65001 (from col 11)", got)
	}
}

// TestWalkVendorArista end-to-ends the Arista walker with the
// enterprise-MIB column numbers from real cEOS captures (state=13,
// remoteAs=10, peer IP in index after peerInstance).
func TestWalkVendorArista(t *testing.T) {
	addr := snmptest.Start(t, "public", snmptest.LoadCapture(t, aristaCapture))
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "arista",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-arista", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Arista vendor walker, got %d", len(edges))
	}
	if got := edges[0].DstDevice; got != "10.0.0.2" {
		t.Errorf("DstDevice = %q, want 10.0.0.2", got)
	}
	if got := edges[0].Metadata[metaKeyRemoteAs]; got != "65001" {
		t.Errorf("RemoteAs metadata = %q, want 65001 (from col 10)", got)
	}
}

// TestWalkUseV2MIBDisabledOnlyHitsRFC4273 verifies the kill-switch:
// when UseBGPV2MIB is false, the walker skips the vendor path entirely
// even on a device that would respond to it.
func TestWalkUseV2MIBDisabledOnlyHitsRFC4273(t *testing.T) {
	// Serve both: the vendor table has an IPv4 peer; RFC 4273 has the
	// same peer. With kill-switch off, only the RFC 4273 path fires.
	pdus := append(snmptest.LoadCapture(t, ciscoCapture), buildBgpAgentPDUs()...)
	addr := snmptest.Start(t, "public", pdus)
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: false, // kill-switch off
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RFC 4273, got %d", len(edges))
	}
}

// TestWalkFallsBackToRFC4273WhenVendorEmpty verifies that the
// orchestrator falls through to RFC 4273 when the vendor walker returns
// no rows (device doesn't implement the vendor table).
func TestWalkFallsBackToRFC4273WhenVendorEmpty(t *testing.T) {
	// Only RFC 4273 PDUs; vendor walker will get zero PDUs back.
	addr := snmptest.Start(t, "public", buildBgpAgentPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco", // dispatched to Cisco spec but table is empty
	}

	edges, _, err := Walk(context.Background(), p, "rtr", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from RFC 4273 fallback, got %d", len(edges))
	}
}

// TestVendorSpecForKnownVendors covers vendor → spec dispatch including
// the new arista mapping.
func TestVendorSpecForKnownVendors(t *testing.T) {
	cases := []struct {
		vendor string
		want   string
	}{
		{"cisco", "cisco-cbgpPeer2Table"},
		{"arista", "arista-bgp4v2"},
		{"juniper", "juniper-jnxBgpM2PeerTable"},
		{"nokia", "nokia-tBgpPeerNgTable"},
		{"alcatel-lucent", "nokia-tBgpPeerNgTable"},
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
				return
			}
			if spec.name != c.want {
				t.Errorf("spec.name = %q, want %q", spec.name, c.want)
			}
			if spec.decodeIndex == nil {
				t.Errorf("spec.decodeIndex is nil for %q — every spec must carry an index decoder", c.vendor)
			}
		})
	}
}

// TestBuildVendorEdgesSkipsNonEstablished covers the state filter in
// the post-walk edge-build step.
func TestBuildVendorEdgesSkipsNonEstablished(t *testing.T) {
	peers := map[string]*vendorPeer{
		"idx-established": {state: bgpStateEstablished, peerIP: net.ParseIP("2001:db8::1")},
		"idx-active":      {state: 3, peerIP: net.ParseIP("2001:db8::2")},
		"idx-idle":        {state: 1, peerIP: net.ParseIP("2001:db8::3")},
	}
	edges, _ := buildVendorEdges("rtr", peers, nil)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (established only), got %d", len(edges))
	}
	if edges[0].DstDevice != "2001:db8::1" {
		t.Errorf("DstDevice = %q, want 2001:db8::1", edges[0].DstDevice)
	}
}

// --- helper-function tests --------------------------------------------

func TestSplitOIDPartsErrors(t *testing.T) {
	cases := []string{
		"1..2",  // empty component
		"1.2.x", // non-numeric
		".1.2",  // leading dot is empty component
		"1.2.",  // trailing dot is empty component
	}
	for _, c := range cases {
		if _, err := splitOIDParts(c); err == nil {
			t.Errorf("splitOIDParts(%q) unexpectedly succeeded", c)
		}
	}
}

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
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if ip := pduInetAddress(c.pdu); ip != nil {
				t.Errorf("expected nil, got %v", ip)
			}
		})
	}
}

func TestPduInetAddressString(t *testing.T) {
	pdu := gsnmp.SnmpPDU{Value: "2001:db8::1"}
	ip := pduInetAddress(pdu)
	if ip == nil || ip.String() != "2001:db8::1" {
		t.Errorf("string decode = %v, want 2001:db8::1", ip)
	}
}

func TestResolveVendorPrefersParams(t *testing.T) {
	p := snmputil.Params{Vendor: "cisco"}
	got := resolveVendor(context.Background(), p, nil)
	if got != "cisco" {
		t.Errorf("resolveVendor = %q, want cisco", got)
	}
}

func TestResolveVendorUnknownReturnsPassthrough(t *testing.T) {
	p := snmputil.Params{Vendor: "obscure-vendor"}
	got := resolveVendor(context.Background(), p, nil)
	if got != "obscure-vendor" {
		t.Errorf("resolveVendor = %q, want passthrough", got)
	}
}

func TestPackageConstantsStable(t *testing.T) {
	if !strings.HasPrefix(ciscoCbgpPeer2Spec.root, "1.3.6.1.4.1.9.") {
		t.Errorf("Cisco spec root = %q does not start with the Cisco enterprise prefix", ciscoCbgpPeer2Spec.root)
	}
	if !strings.HasPrefix(aristaBgp4v2Spec.root, "1.3.6.1.4.1.30065.") {
		t.Errorf("Arista spec root = %q does not start with the Arista enterprise prefix", aristaBgp4v2Spec.root)
	}
	if !strings.HasPrefix(juniperJnxBgpM2PeerSpec.root, "1.3.6.1.4.1.2636.") {
		t.Errorf("Juniper spec root = %q does not start with the Juniper enterprise prefix", juniperJnxBgpM2PeerSpec.root)
	}
	if !strings.HasPrefix(nokiaTBgpPeerSpec.root, "1.3.6.1.4.1.6527.") {
		t.Errorf("Nokia spec root = %q does not start with the Nokia enterprise prefix", nokiaTBgpPeerSpec.root)
	}
	// Verified specs must carry verified=true; unverified must not.
	if !ciscoCbgpPeer2Spec.verified {
		t.Error("ciscoCbgpPeer2Spec.verified must be true (lab/cisco-iol-bgp/captures/)")
	}
	if !aristaBgp4v2Spec.verified {
		t.Error("aristaBgp4v2Spec.verified must be true (lab/arista-ceos-bgp/captures/)")
	}
	if !juniperJnxBgpM2PeerSpec.verified {
		t.Error("juniperJnxBgpM2PeerSpec.verified must be true (lab/juniper-jnxbgp/captures/)")
	}
	if !nokiaTBgpPeerSpec.verified {
		t.Error("nokiaTBgpPeerSpec.verified must be true (lab/nokia-srbgp/captures/, #57)")
	}
}

// TestWalkVendorJuniper end-to-ends the Juniper walker against the real
// jnxBgpM2PeerTable captured from a vJunos-router (JUNOS 25.4R1.12, #56). The
// capture has two established eBGP peers — 192.0.2.2 (IPv4) and 2001:db8:1::2
// (IPv6) — both with remote AS 65002 (state col 2, remoteAs col 13).
func TestWalkVendorJuniper(t *testing.T) {
	addr := snmptest.Start(t, "public", snmptest.LoadCapture(t, juniperCapture))
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "juniper",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-juniper", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges from Juniper vendor walker (one per peer), got %d: %+v", len(edges), edges)
	}
	got := map[string]string{}
	for _, e := range edges {
		got[e.DstDevice] = e.Metadata[metaKeyRemoteAs]
	}
	for _, peer := range []string{"192.0.2.2", "2001:db8:1::2"} {
		as, ok := got[peer]
		if !ok {
			t.Errorf("no edge to peer %s; got %+v", peer, got)
			continue
		}
		if as != "65002" {
			t.Errorf("peer %s RemoteAs = %q, want 65002 (from col 13)", peer, as)
		}
	}
}

// TestWalkVendorNokia end-to-ends the Nokia walker against the real
// tBgpPeerNgTable captured from an SR-OS 25.7.R2 device (#57). One established
// eBGP peer 10.10.10.2 with remote AS 64512 (state col 59, remote-AS col 66 =
// tBgpPeerNgPeerAS4Byte; the index is vRtrID + explicit-length InetAddress).
func TestWalkVendorNokia(t *testing.T) {
	addr := snmptest.Start(t, "public", snmptest.LoadCapture(t, nokiaCapture))
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "nokia",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-nokia", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from Nokia vendor walker, got %d: %+v", len(edges), edges)
	}
	if got := edges[0].DstDevice; got != "10.10.10.2" {
		t.Errorf("DstDevice = %q, want 10.10.10.2 (from tBgpPeerNgTable index)", got)
	}
	if got := edges[0].Metadata[metaKeyRemoteAs]; got != "64512" {
		t.Errorf("RemoteAs = %q, want 64512 (col 66 tBgpPeerNgPeerAS4Byte)", got)
	}
}
