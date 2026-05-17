package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateAtRuneBoundary(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{
			name:     "empty input",
			in:       "",
			maxBytes: 16,
			want:     "",
		},
		{
			name:     "ascii short passthrough",
			in:       "hello",
			maxBytes: 16,
			want:     "hello",
		},
		{
			name:     "ascii exactly at cap",
			in:       strings.Repeat("a", 8),
			maxBytes: 8,
			want:     strings.Repeat("a", 8),
		},
		{
			name:     "ascii one byte over cap",
			in:       strings.Repeat("a", 9),
			maxBytes: 8,
			want:     strings.Repeat("a", 8),
		},
		{
			// 254 ASCII bytes + a 3-byte rune (€ = 0xE2 0x82 0xAC) gives length 257.
			// Cap at 255: byte index 255 is the second continuation byte (0x82),
			// so the loop must retreat to 254 — chopping the partial rune entirely.
			name:     "ascii then 3-byte rune across boundary",
			in:       strings.Repeat("a", 254) + "€",
			maxBytes: 255,
			want:     strings.Repeat("a", 254),
		},
		{
			// 'é' = 0xC3 0xA9 — two bytes. Repeated four times = 8 bytes.
			// Cap at 7: byte 7 is the second byte of the 4th 'é' (0xA9, a
			// continuation byte). Loop retreats to byte 6, dropping the
			// partial rune.
			name:     "2-byte rune straddling boundary",
			in:       strings.Repeat("é", 4),
			maxBytes: 7,
			want:     strings.Repeat("é", 3),
		},
		{
			// 4-byte runes (U+1F600 grinning face = F0 9F 98 80). Three of
			// them = 12 bytes. Cap at 10 lands inside the 3rd rune; loop
			// must retreat to byte 8 (boundary after the 2nd rune).
			name:     "4-byte runes truncate on rune boundary",
			in:       strings.Repeat("\U0001F600", 3),
			maxBytes: 10,
			want:     strings.Repeat("\U0001F600", 2),
		},
		{
			name:     "negative maxBytes yields empty",
			in:       "hello",
			maxBytes: -1,
			want:     "",
		},
		{
			name:     "zero maxBytes yields empty",
			in:       "hello",
			maxBytes: 0,
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateAtRuneBoundary(tc.in, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("TruncateAtRuneBoundary(%q, %d) = %q (len %d), want %q (len %d)",
					tc.in, tc.maxBytes, got, len(got), tc.want, len(tc.want))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
			if len(got) > tc.maxBytes && tc.maxBytes > 0 {
				t.Fatalf("result length %d exceeds maxBytes %d", len(got), tc.maxBytes)
			}
		})
	}
}
