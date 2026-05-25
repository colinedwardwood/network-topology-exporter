package mpls

import "testing"

// FuzzParseTunnelSuffix exercises the MPLS-TE tunnel-suffix parser.
// Valid suffixes have exactly 10 dot-separated components:
// tunnelIdx.tunnelInstance.ig0.ig1.ig2.ig3.eg0.eg1.eg2.eg3
// The parser must not panic on any input.
func FuzzParseTunnelSuffix(f *testing.F) {
	f.Add("1.0.10.0.0.1.10.0.0.2")        // valid
	f.Add(".1.0.10.0.0.1.10.0.0.2")       // valid with leading dot (trimmed)
	f.Add("")                              // empty
	f.Add(".")                             // single dot
	f.Add("1.0.10.0.0.1")                  // too few components
	f.Add("1.0.10.0.0.1.10.0.0.2.extra")   // too many
	f.Add("notnum.0.10.0.0.1.10.0.0.2")    // non-numeric tunnel index
	f.Add("0.0.10.0.0.1.10.0.0.2")         // zero tunnel index (rejected)
	f.Add("-1.0.10.0.0.1.10.0.0.2")        // negative tunnel index
	f.Add("1.0.10.0.0.1.10.0.0.notnum")    // non-numeric egress octet
	f.Add("1.0.10.0.0.1.256.0.0.2")        // egress octet > 255

	f.Fuzz(func(t *testing.T, suffix string) {
		_, _, _ = parseTunnelSuffix(suffix)
	})
}

// FuzzParseIPFromParts exercises the 4-octet IPv4 parser. Inputs are
// dot-joined into a single fuzz string and split inside the fuzz function
// because Go fuzz can't carry a []string directly.
func FuzzParseIPFromParts(f *testing.F) {
	// Each seed is a dot-joined 4-octet (or pathological) string.
	f.Add("10.0.0.1")
	f.Add("255.255.255.255")
	f.Add("0.0.0.0")
	f.Add("")
	f.Add(".")
	f.Add("1.2.3")        // too few
	f.Add("1.2.3.4.5")    // too many
	f.Add("256.0.0.0")    // octet > 255
	f.Add("-1.0.0.0")     // negative
	f.Add("notnum.0.0.0") // non-numeric

	f.Fuzz(func(t *testing.T, joined string) {
		parts := splitDots(joined)
		_, _ = parseIPFromParts(parts)
	})
}

// splitDots is a fuzz-helper that splits on '.' the same way the parser
// expects. Lives next to the fuzz target so the helper is colocated.
func splitDots(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
