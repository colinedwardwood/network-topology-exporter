package snmp

import (
	"testing"

	g "github.com/gosnmp/gosnmp"
)

// pduOf builds an SnmpPDU whose Value field exercises one of the type
// branches in the helper functions. typeSelector picks the dynamic type;
// the same fuzz []byte / int feeds into whichever branch is selected.
// This lets the native fuzzer reach every coercion path via just two
// fuzz inputs.
func pduOf(typeSelector uint8, raw []byte, num int) g.SnmpPDU {
	pdu := g.SnmpPDU{Name: ".1.3.6.1"}
	switch typeSelector % 9 {
	case 0:
		pdu.Value = string(raw)
	case 1:
		pdu.Value = append([]byte(nil), raw...)
	case 2:
		pdu.Value = num
	case 3:
		pdu.Value = int32(num) //nolint:gosec
	case 4:
		pdu.Value = uint(num) //nolint:gosec
	case 5:
		pdu.Value = uint32(num) //nolint:gosec
	case 6:
		pdu.Value = int64(num)
	case 7:
		pdu.Value = uint64(num) //nolint:gosec
	case 8:
		pdu.Value = nil // unhandled type → helpers return zero values
	}
	return pdu
}

// FuzzPDUString exercises the string-coercion helper. The function trims
// trailing NULs from string and []byte values, and returns "" for any
// other type. Must not panic on any combination of raw bytes / type
// selector.
func FuzzPDUString(f *testing.F) {
	f.Add(uint8(0), []byte("hello"), 0)
	f.Add(uint8(0), []byte("with\x00nul"), 0)
	f.Add(uint8(1), []byte{0xff, 0xfe, 0xfd}, 0)
	f.Add(uint8(8), []byte{}, 0) // nil value branch
	f.Add(uint8(0), []byte{}, 0)
	f.Add(uint8(0), []byte("\x00\x00\x00"), 0) // all NULs

	f.Fuzz(func(t *testing.T, selector uint8, raw []byte, num int) {
		_ = PDUString(pduOf(selector, raw, num))
	})
}

// FuzzPDUBytes exercises the bytes-coercion helper. Must not panic.
func FuzzPDUBytes(f *testing.F) {
	f.Add(uint8(0), []byte("hello"), 0)
	f.Add(uint8(1), []byte{0x00, 0xff, 0x80}, 0)
	f.Add(uint8(8), []byte{}, 0)
	f.Add(uint8(0), []byte{}, 0)

	f.Fuzz(func(t *testing.T, selector uint8, raw []byte, num int) {
		_ = PDUBytes(pduOf(selector, raw, num))
	})
}

// FuzzPDUInt exercises the integer-coercion helper across every integer
// PDU type gosnmp may surface. Must not panic.
func FuzzPDUInt(f *testing.F) {
	f.Add(uint8(2), []byte{}, 0)
	f.Add(uint8(3), []byte{}, -1)
	f.Add(uint8(4), []byte{}, 1<<30)
	f.Add(uint8(5), []byte{}, 0)
	f.Add(uint8(6), []byte{}, 1<<60)
	f.Add(uint8(7), []byte{}, -1) // uint64 from negative int — exercises gosec-suppressed path
	f.Add(uint8(0), []byte("notanum"), 0)
	f.Add(uint8(8), []byte{}, 0)

	f.Fuzz(func(t *testing.T, selector uint8, raw []byte, num int) {
		_ = PDUInt(pduOf(selector, raw, num))
	})
}

// FuzzPDUIntStrict — same as PDUInt but reports ok bool. Must not panic.
func FuzzPDUIntStrict(f *testing.F) {
	f.Add(uint8(2), []byte{}, 0)
	f.Add(uint8(0), []byte("notanum"), 0) // string branch → ok=false
	f.Add(uint8(8), []byte{}, 0)

	f.Fuzz(func(t *testing.T, selector uint8, raw []byte, num int) {
		_, _ = PDUIntStrict(pduOf(selector, raw, num))
	})
}

// FuzzPDUIPv4 exercises the IPv4-coercion helper. Accepts dotted-decimal
// strings or 4-byte slices; rejects anything else. Must not panic.
func FuzzPDUIPv4(f *testing.F) {
	f.Add(uint8(0), []byte("10.0.0.1"), 0)
	f.Add(uint8(0), []byte("notanip"), 0)
	f.Add(uint8(1), []byte{10, 0, 0, 1}, 0)
	f.Add(uint8(1), []byte{10, 0, 0}, 0) // wrong length
	f.Add(uint8(1), []byte{}, 0)
	f.Add(uint8(8), []byte{}, 0)

	f.Fuzz(func(t *testing.T, selector uint8, raw []byte, num int) {
		_ = PDUIPv4(pduOf(selector, raw, num))
	})
}
