package bgp

// Real-device validation harness for the vendor_cisco walker against
// Cisco IOS-XE, fulfilling the IOS-XE half of issue #1's acceptance
// criteria.
//
// Scope: this file is the post-capture landing pad. Today it ships
// with placeholder PDU values and the test below calls t.Skip until
// real captures land at lab/cisco-iosxe-bgp/captures/. Once captures
// exist, the workflow is:
//
//   1. Inspect lab/cisco-iosxe-bgp/captures/<host>__1_3_6_1_4_1_9_9_187_1_2_5.txt.
//   2. Locate the rows: column 3 = bgpPeer2State (Integer),
//      column 11 = bgpPeer2RemoteAs (Gauge32). The index suffix after
//      ".1.3.6.1.4.1.9.9.187.1.2.5.1.<col>." is the peer-address
//      encoding (<addrType>.<addrLen>.<bytes...>).
//   3. Replace the TODO values in buildCiscoCbgpPeer2IOSXERealPDUs
//      below with the captured values.
//   4. Delete the t.Skip line in TestWalkVendorCiscoFromIOSXECapture.
//   5. `go test ./internal/discovery/bgp/ -run TestWalkVendorCiscoFromIOSXECapture`.
//
// If the test passes with values byte-identical to
// buildCiscoCbgpPeer2RealPDUs (the IOL-derived helper in
// bgp_v2_test.go), the IOS-XE and IOL row shapes match — strong
// cross-confirmation that the walker's column numbers and index
// decoder are correct. Note that fact in the closing PR for issue #1
// and consider deleting this file in favor of a one-line comment
// update on the IOL helper.
//
// If the test fails, the walker code in bgp_vendor.go has IOS-XE
// drift — file a follow-up issue with the divergence and land the
// column-number correction in the same PR.

import (
	"context"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/colinedwardwood/network-topology-exporter/internal/discovery/snmp"
	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// buildCiscoCbgpPeer2IOSXERealPDUs returns a PDU set modelled on real
// cbgpPeer2Table output captured from a Cisco IOS-XE router (CSR1000v
// or c8000v) via the DevNet Sandbox flow documented in
// lab/cisco-iosxe-bgp/README.md.
//
// Placeholder values below assume Option A (CML two-router iBGP,
// AS 65001, peer at 10.0.0.2). For Option B (single CSR + external
// bird speaker), swap the remote-AS to 65002 and the peer IP/index
// to whatever bird's router-id resolves to.
//
// TODO(#1): replace placeholder values with values from
// lab/cisco-iosxe-bgp/captures/<r1-host>__1_3_6_1_4_1_9_9_187_1_2_5.txt
// once captures land. Numeric OID form is mandatory.
func buildCiscoCbgpPeer2IOSXERealPDUs() []gsnmp.SnmpPDU {
	const base = ".1.3.6.1.4.1.9.9.187.1.2.5.1."
	// TODO(#1): confirm index encoding matches IOL — expected format
	// from MIB documentation: <addrType=1=ipv4>.<addrLen=4>.<addrBytes>.
	// Replace with the actual index suffix observed in the capture.
	const idx = "1.4.10.0.0.2"
	return []gsnmp.SnmpPDU{
		// TODO(#1): col 3 = bgpPeer2State; expect Integer(6)=established
		// when BGP is up. Read the value from the capture line that
		// matches the OID prefix and column number.
		{Name: base + "3." + idx, Type: gsnmp.Integer, Value: bgpStateEstablished},
		// TODO(#1): col 11 = bgpPeer2RemoteAs; expect Gauge32(65001) for
		// Option A iBGP, Gauge32(65002) for Option B eBGP-to-bird.
		{Name: base + "11." + idx, Type: gsnmp.Gauge32, Value: uint(65001)},
	}
}

// TestWalkVendorCiscoFromIOSXECapture validates that the vendor_cisco
// walker decodes a real IOS-XE cbgpPeer2Table response into exactly
// one Established edge with the expected peer IP and remote-AS.
//
// SKIPPED until lab/cisco-iosxe-bgp/captures/ has real walks and the
// placeholders in buildCiscoCbgpPeer2IOSXERealPDUs are filled in.
// See file-level comment for the post-capture workflow.
func TestWalkVendorCiscoFromIOSXECapture(t *testing.T) {
	t.Skip("issue #1: awaiting Cisco IOS-XE captures in lab/cisco-iosxe-bgp/captures/; " +
		"fill in buildCiscoCbgpPeer2IOSXERealPDUs() and remove this skip")

	addr := snmptest.Start(t, "public", buildCiscoCbgpPeer2IOSXERealPDUs())
	ip, port := snmptest.ParseAddr(addr)

	p := snmputil.Params{
		IP:          ip,
		Port:        port,
		Community:   []byte("public"),
		Timeout:     3 * time.Second,
		UseBGPV2MIB: true,
		Vendor:      "cisco",
	}

	edges, _, err := Walk(context.Background(), p, "rtr-iosxe", nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge from IOS-XE vendor walker, got %d: %+v", len(edges), edges)
	}
	// TODO(#1): assert the expected DstDevice — 10.0.0.2 for Option A,
	// or the bird router-id for Option B.
	if got := edges[0].DstDevice; got != "10.0.0.2" {
		t.Errorf("DstDevice = %q, want 10.0.0.2 (from IOS-XE cbgpPeer2 index)", got)
	}
	// TODO(#1): assert remote-AS — 65001 for Option A iBGP, 65002 for Option B eBGP.
	if got := edges[0].Metadata[metaKeyRemoteAs]; got != "65001" {
		t.Errorf("RemoteAs metadata = %q, want 65001 (from IOS-XE col 11)", got)
	}
}
