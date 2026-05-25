package fdb

import "testing"

// FuzzParseQBridgeIndex exercises the Q-BRIDGE FDB index parser with
// arbitrary "{fdbId}.{mac1}.{mac2}.{mac3}.{mac4}.{mac5}.{mac6}" strings.
// The parser must not panic on any input; malformed input returns ok=false.
func FuzzParseQBridgeIndex(f *testing.F) {
	// Seed corpus.
	f.Add("1.0.30.226.105.137.10")                 // valid: fdbId=1, MAC=00:1e:e2:69:89:0a
	f.Add("100.255.255.255.255.255.255")           // valid: ff:ff:ff:ff:ff:ff
	f.Add("0.0.0.0.0.0.0")                         // valid: 00:00:00:00:00:00
	f.Add("")                                      // empty
	f.Add(".")                                     // single dot
	f.Add("1.2.3")                                 // too few components
	f.Add("1.2.3.4.5.6.7.8.9.10")                  // more than 7 — uses last 6
	f.Add("1.0.30.notnumber.105.137.10")           // non-numeric MAC octet
	f.Add("1.0.30.256.105.137.10")                 // MAC octet > 255
	f.Add("1.0.30.-1.105.137.10")                  // negative MAC octet
	f.Add("99999999999999999.0.30.226.105.137.10") // fdbId overflow (allowed — ignored)

	f.Fuzz(func(t *testing.T, rest string) {
		_, _, _ = parseQBridgeIndex(rest)
	})
}
