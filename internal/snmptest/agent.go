// Package snmptest provides an in-process SNMPv2c agent for testing.
//
// The agent listens on a random local UDP port and responds to GetRequest,
// GetNextRequest, and GetBulkRequest PDUs. It is intended for unit tests that
// exercise code paths in the snmp, lldp, and cdp packages without requiring
// a real network device.
//
// Usage:
//
//	addr := snmptest.Start(t, "public", []gsnmp.SnmpPDU{...})
//	// addr is "127.0.0.1:PORT"
package snmptest

import (
	"log/slog"
	"math"
	"net"
	"sort"
	"strconv"
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"
)

// Start launches an SNMPv2c agent on a random local UDP port.
// The agent serves the given PDUs (sorted by OID). t.Cleanup stops it.
// Returns the address as "127.0.0.1:PORT".
func Start(t *testing.T, community string, pdus []gsnmp.SnmpPDU) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("snmptest.Start: listen: %v", err)
	}

	// Sort once; GetNext and BulkWalk handlers rely on OID order.
	sorted := make([]gsnmp.SnmpPDU, len(pdus))
	copy(sorted, pdus)
	sort.Slice(sorted, func(i, j int) bool {
		return oidLess(sorted[i].Name, sorted[j].Name)
	})

	decoder := &gsnmp.GoSNMP{
		Version:   gsnmp.Version2c,
		Community: community,
	}

	done := make(chan struct{})
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})

	go func() {
		defer close(done)
		serve(conn, community, sorted, decoder)
	}()

	return conn.LocalAddr().String()
}

// ParseAddr splits a "host:port" address into a net.IP and uint16 port.
// Useful in tests that need to build snmp.Params from the address returned
// by Start.
//
// Errors from SplitHostPort or ParseUint are intentionally ignored: callers
// that pass a malformed address receive nil/0 zero values, which is the
// desired behaviour for tests that exercise error paths without panicking.
func ParseAddr(addr string) (net.IP, uint16) {
	host, portStr, _ := net.SplitHostPort(addr)
	p, _ := strconv.ParseUint(portStr, 10, 16)
	return net.ParseIP(host), uint16(p)
}

// StartMultiCommunity launches an SNMPv2c agent on a random local UDP port
// that dispatches requests based on the community string in each packet.
// The keys of communities are the accepted community strings; packets whose
// community is not in the map are silently dropped. t.Cleanup stops the agent.
// Returns the address as "127.0.0.1:PORT".
func StartMultiCommunity(t *testing.T, communities map[string][]gsnmp.SnmpPDU) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("snmptest.StartMultiCommunity: listen: %v", err)
	}

	sorted := make(map[string][]gsnmp.SnmpPDU, len(communities))
	for comm, pdus := range communities {
		s := make([]gsnmp.SnmpPDU, len(pdus))
		copy(s, pdus)
		sort.Slice(s, func(i, j int) bool {
			return oidLess(s[i].Name, s[j].Name)
		})
		sorted[comm] = s
	}

	decoder := &gsnmp.GoSNMP{
		Version: gsnmp.Version2c,
	}

	done := make(chan struct{})
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})

	go func() {
		defer close(done)
		serveMulti(conn, sorted, decoder)
	}()

	return conn.LocalAddr().String()
}

// maxUDPSize is the maximum size of a UDP datagram (IPv4 limit).
const maxUDPSize = 65535

// defaultMaxRepetitions mirrors gosnmp's default when MaxRepetitions is 0.
const defaultMaxRepetitions = 50

// maxBulkRepetitions caps the per-repeater iteration count regardless of the
// requested MaxRepetitions. A malicious or fuzzed GetBulk packet could request
// an arbitrarily large MaxRepetitions, driving maxReps × len(repeaters) of
// O(log n) sort.Search calls; clamping bounds the work the test agent performs.
const maxBulkRepetitions = 1000

func serveMulti(conn net.PacketConn, communities map[string][]gsnmp.SnmpPDU, decoder *gsnmp.GoSNMP) {
	buf := make([]byte, maxUDPSize)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		pkt, err := decoder.SnmpDecodePacket(buf[:n])
		if err != nil {
			continue
		}
		pdus, ok := communities[pkt.Community]
		if !ok {
			continue
		}
		resp := dispatchPDU(pdus, pkt)
		if resp == nil {
			continue
		}
		sendReply(conn, src, pkt.Community, pkt, resp)
	}
}

func serve(conn net.PacketConn, community string, pdus []gsnmp.SnmpPDU, decoder *gsnmp.GoSNMP) {
	buf := make([]byte, maxUDPSize)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			return // conn closed by Cleanup
		}
		pkt, err := decoder.SnmpDecodePacket(buf[:n])
		if err != nil {
			continue
		}
		if pkt.Community != community {
			continue
		}
		resp := dispatchPDU(pdus, pkt)
		if resp == nil {
			continue
		}
		sendReply(conn, src, community, pkt, resp)
	}
}

// dispatchPDU routes a decoded request packet to the appropriate handler.
// Returns nil for unsupported PDU types so the caller can skip sending a reply.
func dispatchPDU(pdus []gsnmp.SnmpPDU, pkt *gsnmp.SnmpPacket) []gsnmp.SnmpPDU {
	switch pkt.PDUType {
	case gsnmp.GetRequest:
		return handleGet(pdus, pkt.Variables)
	case gsnmp.GetNextRequest:
		return handleGetNext(pdus, pkt.Variables)
	case gsnmp.GetBulkRequest:
		return handleBulk(pdus, pkt.Variables, int(pkt.NonRepeaters), int(pkt.MaxRepetitions))
	default:
		return nil
	}
}

func sendReply(conn net.PacketConn, src net.Addr, community string, pkt *gsnmp.SnmpPacket, resp []gsnmp.SnmpPDU) {
	reply := &gsnmp.SnmpPacket{
		Version:   gsnmp.Version2c,
		Community: community,
		PDUType:   gsnmp.GetResponse,
		RequestID: pkt.RequestID,
		Variables: resp,
	}
	raw, err := reply.MarshalMsg()
	if err != nil {
		return
	}
	if _, err := conn.WriteTo(raw, src); err != nil {
		slog.Debug("snmptest: sendReply WriteTo failed", "err", err)
	}
}

func handleGet(pdus []gsnmp.SnmpPDU, vars []gsnmp.SnmpPDU) []gsnmp.SnmpPDU {
	resp := make([]gsnmp.SnmpPDU, 0, len(vars))
	for _, v := range vars {
		if pdu, ok := exactLookup(pdus, v.Name); ok {
			resp = append(resp, pdu)
		} else {
			resp = append(resp, gsnmp.SnmpPDU{Name: v.Name, Type: gsnmp.NoSuchObject})
		}
	}
	return resp
}

func handleGetNext(pdus []gsnmp.SnmpPDU, vars []gsnmp.SnmpPDU) []gsnmp.SnmpPDU {
	resp := make([]gsnmp.SnmpPDU, 0, len(vars))
	for _, v := range vars {
		if pdu, ok := nextLookup(pdus, v.Name); ok {
			resp = append(resp, pdu)
		} else {
			resp = append(resp, gsnmp.SnmpPDU{Name: v.Name, Type: gsnmp.EndOfMibView})
		}
	}
	return resp
}

func handleBulk(pdus []gsnmp.SnmpPDU, vars []gsnmp.SnmpPDU, nonRepeaters, maxReps int) []gsnmp.SnmpPDU {
	if maxReps == 0 {
		maxReps = defaultMaxRepetitions
	}
	// Clamp to an upper bound so an arbitrarily large requested MaxRepetitions
	// (or a negative value decoded from a crafted packet) cannot drive an
	// unbounded number of iterations.
	if maxReps < 0 || maxReps > maxBulkRepetitions {
		maxReps = maxBulkRepetitions
	}
	var resp []gsnmp.SnmpPDU

	// Non-repeaters: treat like GetNext.
	nr := nonRepeaters
	if nr > len(vars) {
		nr = len(vars)
	}
	for _, v := range vars[:nr] {
		if pdu, ok := nextLookup(pdus, v.Name); ok {
			resp = append(resp, pdu)
		} else {
			resp = append(resp, gsnmp.SnmpPDU{Name: v.Name, Type: gsnmp.EndOfMibView})
		}
	}

	// Repeaters: up to maxReps successive GetNext for each repeater variable.
	for _, v := range vars[nr:] {
		cur := v.Name
		for i := 0; i < maxReps; i++ {
			pdu, ok := nextLookup(pdus, cur)
			if !ok {
				resp = append(resp, gsnmp.SnmpPDU{Name: cur, Type: gsnmp.EndOfMibView})
				break
			}
			resp = append(resp, pdu)
			cur = pdu.Name
		}
	}
	return resp
}

// exactLookup finds the PDU with exactly the given OID name.
func exactLookup(pdus []gsnmp.SnmpPDU, name string) (gsnmp.SnmpPDU, bool) {
	i := sort.Search(len(pdus), func(i int) bool {
		return !oidLess(pdus[i].Name, name)
	})
	if i < len(pdus) && pdus[i].Name == name {
		return pdus[i], true
	}
	return gsnmp.SnmpPDU{}, false
}

// nextLookup returns the first PDU whose OID strictly follows name.
func nextLookup(pdus []gsnmp.SnmpPDU, name string) (gsnmp.SnmpPDU, bool) {
	i := sort.Search(len(pdus), func(i int) bool {
		return oidLess(name, pdus[i].Name)
	})
	if i < len(pdus) {
		return pdus[i], true
	}
	return gsnmp.SnmpPDU{}, false
}

// oidLess compares two dotted-decimal OID strings component by component.
func oidLess(a, b string) bool {
	// strip leading dot for uniform comparison
	if len(a) > 0 && a[0] == '.' {
		a = a[1:]
	}
	if len(b) > 0 && b[0] == '.' {
		b = b[1:]
	}
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		av, an := nextComponent(a, ai)
		bv, bn := nextComponent(b, bi)
		if av != bv {
			return av < bv
		}
		ai = an
		bi = bn
	}
	return bi < len(b)
}

// componentOverflow is the sentinel value returned for an OID component whose
// numeric value would overflow int. Real SNMP OID sub-identifiers are 32-bit,
// so any input large enough to reach this is malformed; clamping keeps oidLess
// deterministic instead of letting v*10 silently wrap negative.
const componentOverflow = math.MaxInt

// nextComponent parses the next integer component from s starting at pos.
// Returns (value, next_pos_after_dot_or_end).
//
// Accumulation is overflow-safe: once the running value would exceed int range
// it is clamped to componentOverflow and further digits in the component are
// consumed without altering the value. Non-digit, non-dot bytes are skipped so
// a malformed OID cannot corrupt ordering or panic.
func nextComponent(s string, pos int) (int, int) {
	v := 0
	overflow := false
	for pos < len(s) && s[pos] != '.' {
		c := s[pos]
		pos++
		if c < '0' || c > '9' {
			// Skip any non-digit byte rather than computing a garbage value.
			continue
		}
		if overflow {
			continue
		}
		d := int(c - '0')
		// Guard before multiplying/adding so v never wraps negative.
		if v > (math.MaxInt-d)/10 {
			v = componentOverflow
			overflow = true
			continue
		}
		v = v*10 + d
	}
	if pos < len(s) && s[pos] == '.' {
		pos++
	}
	return v, pos
}
