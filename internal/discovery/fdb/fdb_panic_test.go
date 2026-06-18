package fdb

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	snmputil "github.com/grafana/network-topology-exporter/internal/discovery/snmp"
	"github.com/grafana/network-topology-exporter/internal/snmptest"
)

// vlanPanicAgent starts a multi-community SNMP test agent whose VLAN table
// advertises VLAN 10, so walkVlanCommunityFdbs opens a per-VLAN session and
// reaches the walkFdbTableIntoFn seam inside its per-VLAN goroutine. The
// returned Params point at the agent. Mirrors TestWalkVlanCommunityFdbDiscovery.
func vlanPanicAgent(t *testing.T) snmputil.Params {
	t.Helper()
	vlanTablePDUs := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.17.7.1.4.2.1.3.0.10", Type: gsnmp.Integer, Value: 10},
		{Name: ".1.3.6.1.2.1.17.1.4.1.2.1", Type: gsnmp.Integer, Value: 2},
		{Name: ".1.3.6.1.2.1.31.1.1.1.1.2", Type: gsnmp.OctetString, Value: []byte("GigabitEthernet0/2")},
	}
	communities := map[string][]gsnmp.SnmpPDU{
		"public":    vlanTablePDUs,
		"public@10": {}, // session opens; the panicking seam fires before any PDU matters
	}
	addr := snmptest.StartMultiCommunity(t, communities)
	ip, port := snmptest.ParseAddr(addr)
	return snmputil.Params{
		IP:        ip,
		Port:      port,
		Community: []byte("public"),
		Timeout:   3 * time.Second,
	}
}

// TestVLANWalkPanicRecovered verifies that a panic inside one per-VLAN walk
// goroutine does NOT crash the process: Walk still returns (no re-panic) and
// the injected PanicReporter is invoked once with site "fdb_vlan_walk".
func TestVLANWalkPanicRecovered(t *testing.T) {
	orig := walkFdbTableIntoFn
	t.Cleanup(func() { walkFdbTableIntoFn = orig })
	walkFdbTableIntoFn = func(_ context.Context, _ *gsnmp.GoSNMP, _ map[string]*fdbEntry) (bool, error) {
		panic("boom in per-VLAN walk")
	}

	var reported atomic.Int64
	var gotSite atomic.Value
	p := vlanPanicAgent(t)
	p.PanicReporter = func(site string) {
		reported.Add(1)
		gotSite.Store(site)
	}

	// If the per-VLAN goroutine panic were not recovered, the test process
	// would crash here; reaching the assertions proves it was contained.
	_, _, err := Walk(context.Background(), p, "sw-01", nil)
	if err != nil {
		t.Fatalf("Walk returned error after recovered per-VLAN panic: %v", err)
	}
	if got := reported.Load(); got != 1 {
		t.Fatalf("PanicReporter called %d times, want 1", got)
	}
	if site, _ := gotSite.Load().(string); site != "fdb_vlan_walk" {
		t.Fatalf("PanicReporter site = %q, want %q", site, "fdb_vlan_walk")
	}
}

// TestVLANWalkPanicNilReporter verifies the nil-tolerant seam: with no
// PanicReporter wired, a per-VLAN panic is still recovered (Walk returns)
// and nothing panics on the nil reporter.
func TestVLANWalkPanicNilReporter(t *testing.T) {
	orig := walkFdbTableIntoFn
	t.Cleanup(func() { walkFdbTableIntoFn = orig })
	walkFdbTableIntoFn = func(_ context.Context, _ *gsnmp.GoSNMP, _ map[string]*fdbEntry) (bool, error) {
		panic("boom in per-VLAN walk")
	}

	p := vlanPanicAgent(t) // PanicReporter left nil
	if _, _, err := Walk(context.Background(), p, "sw-01", nil); err != nil {
		t.Fatalf("Walk returned error with nil PanicReporter: %v", err)
	}
}
