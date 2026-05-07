package snmp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	g "github.com/gosnmp/gosnmp"
)

const (
	// oidIfNameTable is the IF-MIB ifXTable.ifName column (RFC 2863 §3.1.4).
	oidIfNameTable = "1.3.6.1.2.1.31.1.1.1.1"
	// OIDIfDescr is the IF-MIB ifTable.ifDescr column (RFC 2863 §3.1.2).
	// Older devices that lack the ifXTable ifName column use this OID instead.
	OIDIfDescr = "1.3.6.1.2.1.2.2.1.2"
)

// DecodeIssue describes a decode anomaly observed while parsing SNMP table PDUs.
// It is emitted through SetDecodeIssueObserver for metrics/logging aggregation.
type DecodeIssue struct {
	Module string
	OID    string
	Reason string
	Count  int
}

// IntMapDecodeStats reports anomalies from WalkToIntMapStrict.
type IntMapDecodeStats struct {
	DecodeFailures int
	TrimFailures   int
	Samples        []string
}

var (
	decodeIssueObserverMu sync.RWMutex
	decodeIssueObserver   func(DecodeIssue)
)

// SetDecodeIssueObserver installs a process-wide callback for decode anomalies.
// Passing nil clears the observer.
func SetDecodeIssueObserver(fn func(DecodeIssue)) {
	decodeIssueObserverMu.Lock()
	defer decodeIssueObserverMu.Unlock()
	decodeIssueObserver = fn
}

func reportDecodeIssue(issue DecodeIssue) {
	decodeIssueObserverMu.RLock()
	fn := decodeIssueObserver
	decodeIssueObserverMu.RUnlock()
	if fn != nil {
		fn(issue)
	}
}

func appendDecodeSample(samples []string, sample string) []string {
	const maxSamples = 5
	if len(samples) >= maxSamples {
		return samples
	}
	return append(samples, sample)
}

// WalkToIntMapStrict walks oid and returns a map from OID suffix to integer
// value plus decode stats. Non-integer PDU values are counted as decode
// failures and excluded from the result map.
func WalkToIntMapStrict(ctx context.Context, client *g.GoSNMP, module, oid string) (map[string]int, IntMapDecodeStats, error) {
	pdus, err := BulkWalk(ctx, client, oid)
	if err != nil {
		return nil, IntMapDecodeStats{}, err
	}
	prefix := "." + oid + "."
	result := make(map[string]int, len(pdus))
	stats := IntMapDecodeStats{}
	for _, pdu := range pdus {
		key, ok := TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			stats.TrimFailures++
			stats.Samples = appendDecodeSample(stats.Samples, "trim:"+pdu.Name)
			continue
		}
		v, ok := PDUIntStrict(pdu)
		if !ok {
			stats.DecodeFailures++
			stats.Samples = appendDecodeSample(stats.Samples, fmt.Sprintf("decode:%s:%T", key, pdu.Value))
			continue
		}
		result[key] = v
	}
	if module != "" {
		if stats.DecodeFailures > 0 {
			reportDecodeIssue(DecodeIssue{
				Module: module,
				OID:    oid,
				Reason: "invalid_type",
				Count:  stats.DecodeFailures,
			})
		}
		if stats.TrimFailures > 0 {
			reportDecodeIssue(DecodeIssue{
				Module: module,
				OID:    oid,
				Reason: "invalid_oid",
				Count:  stats.TrimFailures,
			})
		}
	}
	return result, stats, nil
}

// WalkToIntMap walks oid and returns a map from OID suffix to integer value.
// The suffix is the instance index portion of the OID (everything after the
// column prefix). Returns nil on walk error.
func WalkToIntMap(ctx context.Context, client *g.GoSNMP, oid string) (map[string]int, error) {
	m, _, err := WalkToIntMapStrict(ctx, client, "", oid)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// WalkIfNames walks the IF-MIB ifXTable.ifName column (RFC 2863 §3.1.4) and
// returns a map of ifIndex → ifName. Multiple discovery modules (CDP, FDB)
// require this mapping to translate bridge/CDP port numbers to interface names.
func WalkIfNames(ctx context.Context, client *g.GoSNMP) (map[int]string, error) {
	return walkIntIndexedStrings(ctx, client, oidIfNameTable)
}

// WalkIfDescr walks the IF-MIB ifTable.ifDescr column (RFC 2863 §3.1.2) and
// returns a map of ifIndex → ifDescr string.
func WalkIfDescr(ctx context.Context, client *g.GoSNMP) (map[int]string, error) {
	return walkIntIndexedStrings(ctx, client, OIDIfDescr)
}

func walkIntIndexedStrings(ctx context.Context, client *g.GoSNMP, oid string) (map[int]string, error) {
	pdus, err := BulkWalk(ctx, client, oid)
	if err != nil {
		return nil, err
	}
	prefix := "." + oid + "."
	names := make(map[int]string, len(pdus))
	for _, pdu := range pdus {
		idxStr, ok := TrimOIDPrefix(pdu.Name, prefix)
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		names[idx] = PDUString(pdu)
	}
	return names, nil
}

// PDUString extracts a string value from a PDU. It handles both string and
// []byte value types, returning "" for any other type.
func PDUString(pdu g.SnmpPDU) string {
	switch v := pdu.Value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

// PDUInt extracts an integer value from a PDU. It handles int, int32, uint,
// and uint32 value types, returning 0 for any other type.
func PDUInt(pdu g.SnmpPDU) int {
	v, ok := PDUIntStrict(pdu)
	if !ok {
		return 0
	}
	return v
}

// PDUIntStrict extracts an integer value from a PDU and reports whether
// extraction succeeded.
func PDUIntStrict(pdu g.SnmpPDU) (int, bool) {
	switch v := pdu.Value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case uint:
		return int(v), true
	case uint32:
		return int(v), true
	case int64:
		return int(v), true
	case uint64:
		return int(v), true
	}
	return 0, false
}

// PDUBytes extracts a byte slice from a PDU. It handles []byte and string
// value types, returning nil for any other type. The returned slice is always
// a fresh copy.
func PDUBytes(pdu g.SnmpPDU) []byte {
	switch v := pdu.Value.(type) {
	case []byte:
		return append([]byte(nil), v...)
	case string:
		return []byte(v)
	}
	return nil
}

// PDUIPv4 extracts an IPv4 address from an SNMP PDU. gosnmp may decode
// the value as a dotted-decimal string or as a raw 4-byte slice.
func PDUIPv4(pdu g.SnmpPDU) net.IP {
	switch v := pdu.Value.(type) {
	case string:
		if ip := net.ParseIP(v); ip != nil {
			return ip.To4()
		}
	case []byte:
		if len(v) == 4 {
			return net.IP(append([]byte(nil), v...))
		}
	}
	return nil
}

// SplitOIDComponent splits the leading numeric component from an OID suffix
// string of the form "col.rest" or "col" (last component). Returns the integer
// value, the remaining string after the dot, and true on success.
func SplitOIDComponent(s string) (int, string, bool) {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		// last component
		v, err := strconv.Atoi(s)
		return v, "", err == nil
	}
	v, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, "", false
	}
	return v, s[i+1:], true
}

// TrimOIDPrefix strips prefix from s and returns the remainder. Returns
// ("", false) when s does not start with prefix or the remainder is empty.
func TrimOIDPrefix(s, prefix string) (string, bool) {
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	rest := s[len(prefix):]
	if rest == "" {
		return "", false
	}
	return rest, true
}

// IPInNets reports whether ip is contained in any of the given networks.
func IPInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// NormaliseName trims surrounding whitespace and lowercases s. Used to
// normalise sysName / device-ID values from SNMP PDUs consistently across
// LLDP, CDP, and the SYSTEM group walker.
func NormaliseName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ParseCIDRs parses a slice of CIDR strings and returns the corresponding
// IPNet values. Entries that fail to parse are silently skipped.
func ParseCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}
