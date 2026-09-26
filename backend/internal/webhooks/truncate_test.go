package webhooks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The 4 KiB cap on a stored response lands wherever it lands, so a reply in
// Japanese or with emoji is regularly cut inside a character. That fragment
// is what PostgreSQL refused as text, leaving the delivery unfinished and
// re-POSTed every lease period.
func TestTrimPartialRune(t *testing.T) {
	const limit = 8
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"shorter than the cap is left alone, even when invalid", "ab\xe3\x81", "ab\xe3\x81"},
		{"ascii at the cap is kept whole", "abcdefgh", "abcdefgh"},
		{"a complete character at the end is kept", "abcde" + "あ", "abcde" + "あ"},
		{"two bytes of a three-byte character are dropped", "abcdef" + "\xe3\x81", "abcdef"},
		{"one byte of a three-byte character is dropped", "abcdefg" + "\xe3", "abcdefg"},
		{"three bytes of a four-byte character are dropped", "abcde" + "\xf0\x9f\x98", "abcde"},
		{"invalid bytes that are not a cut character are left for the store", "abcdefg\x80", "abcdefg\x80"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(trimPartialRune([]byte(tt.in), limit))
			if got != tt.want {
				t.Fatalf("trimPartialRune(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// And at the real cap: a body of three-byte characters cut at 4096 bytes
	// (4096 = 3*1365 + 1) comes back as valid UTF-8.
	body := []byte(strings.Repeat("あ", 2000))[:maxResponseBodyBytes]
	if got := trimPartialRune(body, maxResponseBodyBytes); !utf8.Valid(got) || len(got) != 3*1365 {
		t.Fatalf("trimmed to %d bytes, valid=%v; want %d valid bytes", len(got), utf8.Valid(got), 3*1365)
	}
}
