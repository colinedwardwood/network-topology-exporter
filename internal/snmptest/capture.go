package snmptest

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"
)

// LoadCapture reads an snmpwalk capture file (numeric OIDs and numeric enums,
// i.e. `snmpwalk -On -Oe`, the format emitted by lab/*/capture.sh) and returns
// the PDUs it describes, ready to hand to Start. Comment lines (`#`) and blank
// lines are skipped. It fails the test on any parse error, mirroring Start's
// fail-fast helper style.
//
// Loading captures directly — instead of transcribing them into hand-built PDU
// slices — means the vendor walker tests run against the exact bytes a real
// device emitted, so a capture refresh that drifts from the walker's column
// assumptions is caught rather than silently ignored (issue #59).
func LoadCapture(t *testing.T, path string) []gsnmp.SnmpPDU {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: a test helper that opens a caller-supplied capture path is the whole point.
	if err != nil {
		t.Fatalf("snmptest.LoadCapture: open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	pdus, err := parseCapture(f)
	if err != nil {
		t.Fatalf("snmptest.LoadCapture: parse %s: %v", path, err)
	}
	return pdus
}

// parseCapture is the testable core of LoadCapture: it turns the capture text
// into PDUs or returns an error. Kept separate so the parser can be unit-tested
// against in-memory input without touching the filesystem.
func parseCapture(r io.Reader) ([]gsnmp.SnmpPDU, error) {
	var pdus []gsnmp.SnmpPDU
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.Index(line, " = ")
		if eq < 0 {
			// A line with no ' = ' is a wrapped continuation of the previous
			// Hex-STRING value (snmpwalk folds long octet strings across lines).
			if appended, err := appendHexContinuation(pdus, trimmed); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			} else if appended {
				continue
			}
			return nil, fmt.Errorf("line %d: no ' = ' separator: %q", lineNo, line)
		}
		oid := strings.TrimSpace(line[:eq])
		rest := strings.TrimSpace(line[eq+3:])
		pdu, err := parseValue(oid, rest)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		pdus = append(pdus, pdu)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return pdus, nil
}

// appendHexContinuation folds a wrapped hex line into the preceding Hex-STRING
// PDU. Returns false (not an error) when the line isn't a plausible hex
// continuation, so the caller can report it as a malformed line instead.
func appendHexContinuation(pdus []gsnmp.SnmpPDU, line string) (bool, error) {
	n := len(pdus)
	if n == 0 || pdus[n-1].Type != gsnmp.OctetString || !looksHex(line) {
		return false, nil
	}
	b, err := parseHexBytes(line)
	if err != nil {
		return false, fmt.Errorf("continuation hex: %w", err)
	}
	prev, _ := pdus[n-1].Value.([]byte)
	merged := make([]byte, 0, len(prev)+len(b))
	merged = append(merged, prev...)
	merged = append(merged, b...)
	pdus[n-1].Value = merged
	return true, nil
}

// parseValue turns "<TYPE>: <value>" (or a bare quoted string) into a PDU.
func parseValue(oid, rest string) (gsnmp.SnmpPDU, error) {
	pdu := gsnmp.SnmpPDU{Name: oid}

	if colon := strings.Index(rest, ":"); colon > 0 {
		typ := rest[:colon]
		val := strings.TrimSpace(rest[colon+1:])
		// A bare token before the colon (no spaces, type-shaped) is an SNMP
		// type keyword: parse it if known, but reject an unknown one so that
		// capture-format drift surfaces rather than being silently swallowed
		// into an OctetString. A quoted or space-containing left side (e.g.
		// the value itself happens to contain a colon) is not a type prefix.
		if looksLikeTypeToken(typ) {
			if !isTypeToken(typ) {
				return pdu, fmt.Errorf("unsupported SNMP type %q", typ)
			}
			return typedValue(pdu, typ, val)
		}
	}

	// No type prefix — a bare string value such as `""` (an empty OctetString,
	// common for unset columns) or a quoted literal.
	pdu.Type = gsnmp.OctetString
	pdu.Value = []byte(strings.Trim(rest, `"`))
	return pdu, nil
}

func typedValue(pdu gsnmp.SnmpPDU, typ, val string) (gsnmp.SnmpPDU, error) {
	switch typ {
	case "INTEGER":
		n, err := strconv.Atoi(val)
		if err != nil {
			return pdu, fmt.Errorf("INTEGER %q: %w", val, err)
		}
		pdu.Type, pdu.Value = gsnmp.Integer, n
	case "Gauge32", "Gauge", "Unsigned32":
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return pdu, fmt.Errorf("%s %q: %w", typ, val, err)
		}
		pdu.Type, pdu.Value = gsnmp.Gauge32, uint(n)
	case "Counter32", "Counter":
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return pdu, fmt.Errorf("%s %q: %w", typ, val, err)
		}
		pdu.Type, pdu.Value = gsnmp.Counter32, uint(n)
	case "Counter64":
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return pdu, fmt.Errorf("Counter64 %q: %w", val, err)
		}
		pdu.Type, pdu.Value = gsnmp.Counter64, n
	case "Timeticks", "Timeticks32":
		// snmpwalk prints `(12345) 0:02:03.45`; the ticks are the first integer.
		n, ok := firstUint(val)
		if !ok {
			return pdu, fmt.Errorf("timeticks %q: no numeric ticks", val)
		}
		if n > math.MaxUint32 {
			return pdu, fmt.Errorf("timeticks %q: exceeds 32 bits", val)
		}
		pdu.Type, pdu.Value = gsnmp.TimeTicks, uint32(n)
	case "IpAddress":
		pdu.Type, pdu.Value = gsnmp.IPAddress, val
	case "Hex-STRING":
		b, err := parseHexBytes(val)
		if err != nil {
			return pdu, fmt.Errorf("Hex-STRING %q: %w", val, err)
		}
		pdu.Type, pdu.Value = gsnmp.OctetString, b
	case "STRING":
		pdu.Type, pdu.Value = gsnmp.OctetString, []byte(strings.Trim(val, `"`))
	case "OID":
		pdu.Type, pdu.Value = gsnmp.ObjectIdentifier, val
	default:
		return pdu, fmt.Errorf("unsupported SNMP type %q", typ)
	}
	return pdu, nil
}

// isTypeToken reports whether s is a recognised snmpwalk type keyword. Type
// keywords are a single token of letters, digits and hyphens (e.g. Hex-STRING),
// which lets parseValue tell `STRING: "x"` (typed) from a bare `"x"` value.
func isTypeToken(s string) bool {
	switch s {
	case "INTEGER", "Gauge32", "Gauge", "Unsigned32", "Counter32", "Counter",
		"Counter64", "Timeticks", "Timeticks32", "IpAddress", "Hex-STRING",
		"STRING", "OID":
		return true
	}
	return false
}

// looksLikeTypeToken reports whether s has the shape of an snmpwalk type
// keyword: a leading letter followed by letters, digits or hyphens, with no
// spaces (e.g. INTEGER, Gauge32, Hex-STRING).
func looksLikeTypeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case i > 0 && ((c >= '0' && c <= '9') || c == '-'):
		default:
			return false
		}
	}
	return true
}

// parseHexBytes decodes space-separated hex pairs ("0A 00 00 01") into bytes.
func parseHexBytes(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return []byte{}, nil
	}
	return hex.DecodeString(s)
}

// looksHex reports whether s is composed solely of hex digits and spaces, used
// to recognise a wrapped Hex-STRING continuation line.
func looksHex(s string) bool {
	seen := false
	for _, c := range s {
		switch {
		case c == ' ':
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
			seen = true
		default:
			return false
		}
	}
	return seen
}

// firstUint returns the first run of decimal digits in s as a uint64.
func firstUint(s string) (uint64, bool) {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			n, err := strconv.ParseUint(s[start:i], 10, 64)
			return n, err == nil
		}
	}
	if start >= 0 {
		n, err := strconv.ParseUint(s[start:], 10, 64)
		return n, err == nil
	}
	return 0, false
}
