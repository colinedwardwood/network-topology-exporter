// Package sanitize provides small, dependency-free helpers for safely
// trimming and bounding byte- and rune-encoded strings before they cross
// trust boundaries (metric labels, federation payloads, graph IDs, log
// fields). The helpers intentionally do not assume any protocol semantics
// so they can be reused outside the SNMP discovery path.
package sanitize

import "unicode/utf8"

// TruncateAtRuneBoundary returns s truncated to at most maxBytes bytes,
// retreating to the nearest UTF-8 rune boundary so the result is never
// invalid UTF-8.
//
// Background. RFC 3629 §3 defines a UTF-8 byte sequence as a leading byte
// (0xxxxxxx, 110xxxxx, 1110xxxx, or 11110xxx) followed by zero or more
// continuation bytes (10xxxxxx). Cutting a string at an arbitrary byte
// offset may land in the middle of a multi-byte sequence, producing a
// string that decodes to U+FFFD and breaks downstream consumers that
// validate UTF-8 (Prometheus label values, JSON encoders, the federation
// push validator).
//
// Algorithm. If len(s) <= maxBytes the input is returned unchanged.
// Otherwise we start at n = maxBytes and walk backwards while s[n] is a
// continuation byte (utf8.RuneStart reports false). The loop retreats at
// most three bytes — the longest possible continuation tail for a 4-byte
// rune — so the operation is O(1) per call regardless of input length.
//
// A maxBytes value <= 0 yields the empty string. The helper does not
// modify s beyond slicing it; the returned string shares the input's
// backing array.
//
// This helper consolidates three previously-duplicated truncation loops
// in NormaliseName and SanitisePortName (internal/discovery/snmp/pdu.go)
// and sanitizeLabel (internal/metrics/topology_collector.go). Each call
// site retains its own auxiliary logic (lower-casing, control-character
// filtering, etc.); this helper covers only the rune-boundary step.
func TruncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
