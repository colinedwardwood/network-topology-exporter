package lldp

import "testing"

// FuzzDecodePortID exercises LLDP port-ID decoding across all subtypes.
// raw bytes can be anything; the decoder must not panic. Output is a
// human-readable string; this fuzz target asserts only no-panic, not
// content correctness.
func FuzzDecodePortID(f *testing.F) {
	f.Add(7, []byte("Ethernet1/1"))                       // interfaceName subtype
	f.Add(3, []byte{0x00, 0x1e, 0xe2, 0x69, 0x89, 0x0a})  // MAC subtype, 6 bytes
	f.Add(4, []byte{1, 10, 0, 0, 1})                      // networkAddress IPv4
	f.Add(4, []byte{2, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}) // IPv6
	f.Add(0, []byte{})                                    // empty raw
	f.Add(0, []byte("name with \x00 nul"))                // trailing/embedded NUL
	f.Add(99, []byte{0xff, 0xfe, 0xfd})                   // unknown subtype, opaque bytes
	f.Add(7, []byte{0x80, 0x81, 0x82})                    // non-UTF8 default-branch
	f.Add(3, []byte{})                                    // MAC subtype with empty bytes
	f.Add(4, []byte{1, 10, 0, 0})                         // networkAddress IPv4 truncated
	f.Add(4, []byte{99})                                  // unknown address family byte

	f.Fuzz(func(t *testing.T, subtype int, raw []byte) {
		_ = decodePortID(subtype, raw)
	})
}

// FuzzDecodeChassisID exercises LLDP chassis-ID decoding across all subtypes.
// Same shape as FuzzDecodePortID; no-panic is the property under test.
func FuzzDecodeChassisID(f *testing.F) {
	f.Add(4, []byte("hostname.example.com"))              // interfaceAlias subtype
	f.Add(4, []byte{0x00, 0x1e, 0xe2, 0x69, 0x89, 0x0a})  // MAC subtype, 6 bytes
	f.Add(5, []byte{1, 10, 0, 0, 1})                      // networkAddress IPv4
	f.Add(0, []byte{})                                    // empty
	f.Add(7, []byte{0xff})                                // single byte unknown subtype
	f.Add(-1, []byte{1, 2, 3})                            // negative subtype
	f.Add(99, []byte("\x00\x00\x00"))                     // all-NUL

	f.Fuzz(func(t *testing.T, subtype int, raw []byte) {
		_ = decodeChassisID(subtype, raw)
	})
}
