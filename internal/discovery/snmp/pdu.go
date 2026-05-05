package snmp

import (
	"net"
	"strconv"
	"strings"

	g "github.com/gosnmp/gosnmp"
)

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
	switch v := pdu.Value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case uint:
		return int(v)
	case uint32:
		return int(v)
	}
	return 0
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
