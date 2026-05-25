package bgp

import "testing"

// FuzzDecodeCiscoCbgpPeer2Index exercises the Cisco vendor index decoder
// with arbitrary OID suffix strings. The function must not panic on any
// input; legitimate parse failures return ok=false.
func FuzzDecodeCiscoCbgpPeer2Index(f *testing.F) {
	// Seed corpus: valid + edge cases.
	f.Add("1.4.10.0.0.2")                                 // valid IPv4 (addrType=1, addrLen=4)
	f.Add("2.16.32.1.13.184.133.163.0.0.0.0.0.0.0.0.0.1") // valid IPv6 (addrType=2, addrLen=16)
	f.Add("")                                             // empty
	f.Add("1.4")                                          // truncated — no body
	f.Add("1.5.1.2.3.4.5")                                // addrType=IPv4 but addrLen=5 (wrong)
	f.Add("3.0")                                          // unknown addrType
	f.Add("0.0")                                          // zero addrType
	f.Add("1.4.999.0.0.0")                                // byte out of range
	f.Add(".")                                            // single dot
	f.Add("1.4.-1.0.0.0")                                 // negative byte

	f.Fuzz(func(t *testing.T, suffix string) {
		_, _ = decodeCiscoCbgpPeer2Index(suffix)
	})
}

// FuzzDecodeAristaBgp4v2Index exercises Arista's index decoder, which has a
// peerInstance prefix before the InetAddress triplet.
func FuzzDecodeAristaBgp4v2Index(f *testing.F) {
	f.Add("1.1.4.10.0.0.2")                                  // valid IPv4 with instance=1
	f.Add("99.2.16.32.1.13.184.133.163.0.0.0.0.0.0.0.0.0.1") // IPv6 with instance=99
	f.Add("")
	f.Add("1")               // only instance, no body
	f.Add("1.1.4")           // instance + addrType + addrLen, no bytes
	f.Add("-1.1.4.10.0.0.2") // negative instance (decoder skips it; should still try parse)
	f.Add("1.0.0")           // instance + zero addrType
	f.Add(".")

	f.Fuzz(func(t *testing.T, suffix string) {
		_, _ = decodeAristaBgp4v2Index(suffix)
	})
}

// FuzzDecodeBgp4v2InstanceIndex covers the Juniper / Nokia path. Currently
// aliases Arista's decoder, but worth fuzzing under its own name so a future
// vendor-specific decoder is regression-tested before swap-in.
func FuzzDecodeBgp4v2InstanceIndex(f *testing.F) {
	f.Add("1.1.4.10.0.0.2")
	f.Add("0.1.4.10.0.0.2")
	f.Add("")
	f.Add("1")

	f.Fuzz(func(t *testing.T, suffix string) {
		_, _ = decodeBgp4v2InstanceIndex(suffix)
	})
}

// FuzzSplitOIDParts fuzzes the generic OID-suffix splitter. Empty input is
// valid (returns empty slice, no error). The function must not panic on any
// input.
func FuzzSplitOIDParts(f *testing.F) {
	f.Add("1.2.3.4")
	f.Add("")
	f.Add(".")                              // empty leading component
	f.Add("1.")                             // trailing dot
	f.Add(".1")                             // leading dot
	f.Add("1..2")                           // double dot
	f.Add("notanumber")                     // non-numeric
	f.Add("1.2.notanum.4")                  // mixed
	f.Add("999999999999999999999999999999") // overflow strconv.Atoi
	f.Add("-1.2.3.4")                       // negative
	f.Add("1.-2.3.4")                       // negative in middle

	f.Fuzz(func(t *testing.T, s string) {
		_, _ = splitOIDParts(s)
	})
}

// FuzzReadInetAddrAt fuzzes the RFC 4001 InetAddress decoder by feeding it
// a byte slice (treated as a series of small ints) and a position offset.
// This is the highest-risk target in this package: addrLen is attacker-
// controlled and flows into make([]byte, addrLen). Bounds-checks must hold.
func FuzzReadInetAddrAt(f *testing.F) {
	f.Add([]byte{1, 4, 10, 0, 0, 2}, 0)                                     // valid IPv4
	f.Add([]byte{2, 16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, 0) // valid IPv6
	f.Add([]byte{}, 0)                                                      // empty parts
	f.Add([]byte{1, 4}, 0)                                                  // truncated
	f.Add([]byte{1, 4, 10, 0, 0, 2}, 100)                                   // pos out of range
	f.Add([]byte{1, 255, 10, 0, 0, 2}, 0)                                   // addrLen=255 (length mismatch)
	f.Add([]byte{0, 0}, 0)                                                  // zero type, zero length

	f.Fuzz(func(t *testing.T, raw []byte, pos int) {
		parts := make([]int, len(raw))
		for i, b := range raw {
			parts[i] = int(b)
		}
		_, _, _ = readInetAddrAt(parts, pos)
	})
}
