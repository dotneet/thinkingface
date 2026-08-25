package gitrepo

import (
	"bytes"
	"strings"
	"testing"
)

func validOID() string {
	return strings.Repeat("a", 64)
}

func TestParseLFSPointer_Valid(t *testing.T) {
	oid := validOID()
	data := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 12345\n")
	p, ok := ParseLFSPointer(data)
	if !ok {
		t.Fatalf("ParseLFSPointer(valid) ok = false, want true")
	}
	if p.OID != oid {
		t.Errorf("OID = %q, want %q", p.OID, oid)
	}
	if p.Size != 12345 {
		t.Errorf("Size = %d, want 12345", p.Size)
	}
}

func TestParseLFSPointer_RejectsBinaryWithNUL(t *testing.T) {
	data := []byte("version https://git-lfs.github.com/spec/v1\x00\noid sha256:" + validOID() + "\nsize 1\n")
	if _, ok := ParseLFSPointer(data); ok {
		t.Fatalf("ParseLFSPointer with NUL byte ok = true, want false")
	}
}

func TestParseLFSPointer_RejectsOversized(t *testing.T) {
	// Pad well past LFSPointerMaxSize with content that otherwise looks like
	// a valid pointer plus trailing garbage.
	oid := validOID()
	base := "version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 1\n"
	padded := base + strings.Repeat("x", LFSPointerMaxSize)
	if _, ok := ParseLFSPointer([]byte(padded)); ok {
		t.Fatalf("ParseLFSPointer with oversized input ok = true, want false")
	}
}

func TestParseLFSPointer_RejectsVersionMismatch(t *testing.T) {
	data := []byte("version https://git-lfs.github.com/spec/v0\noid sha256:" + validOID() + "\nsize 1\n")
	if _, ok := ParseLFSPointer(data); ok {
		t.Fatalf("ParseLFSPointer with wrong version ok = true, want false")
	}
}

func TestParseLFSPointer_RejectsBadOID(t *testing.T) {
	tests := []struct {
		name string
		oid  string
	}{
		{"too short", "abc123"},
		{"uppercase hex", strings.ToUpper(validOID())},
		{"wrong hash name", "md5:" + validOID()},
		{"non-hex chars", strings.Repeat("g", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + tt.oid + "\nsize 1\n")
			if _, ok := ParseLFSPointer(data); ok {
				t.Fatalf("ParseLFSPointer with oid %q ok = true, want false", tt.oid)
			}
		})
	}
}

func TestParseLFSPointer_RejectsMissingFields(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"no version line", "oid sha256:" + validOID() + "\nsize 1\n"},
		{"no oid line", "version https://git-lfs.github.com/spec/v1\nsize 1\n"},
		// A pointer without a size used to parse as Size=0, and the zero
		// travelled: it became the file's listed size, the X-Linked-Size
		// header, and finally a Content-Length of 0 in front of a body that
		// was not empty, which net/http truncates. size is required by the
		// spec and git-lfs always writes it, so a blob missing it is content.
		{"no size line", "version https://git-lfs.github.com/spec/v1\noid sha256:" + validOID() + "\n"},
		{"only version", "version https://git-lfs.github.com/spec/v1\n"},
		{"empty", ""},
		{"line without space", "version https://git-lfs.github.com/spec/v1\nnotakeyvalueline\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := ParseLFSPointer([]byte(tt.data)); ok {
				t.Fatalf("ParseLFSPointer(%q) ok = true, want false", tt.data)
			}
		})
	}
}

func TestFormatLFSPointer_RoundTrip(t *testing.T) {
	oid := validOID()
	var size int64 = 987654321
	formatted := FormatLFSPointer(oid, size)

	p, ok := ParseLFSPointer(formatted)
	if !ok {
		t.Fatalf("round trip: ParseLFSPointer(FormatLFSPointer(...)) ok = false")
	}
	if p.OID != oid {
		t.Errorf("round trip OID = %q, want %q", p.OID, oid)
	}
	if p.Size != size {
		t.Errorf("round trip Size = %d, want %d", p.Size, size)
	}
}

func TestHashSHA256(t *testing.T) {
	sum, n, err := HashSHA256(bytes.NewReader([]byte("hello world")))
	if err != nil {
		t.Fatalf("HashSHA256: %v", err)
	}
	const want = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if sum != want {
		t.Errorf("HashSHA256 sum = %q, want %q", sum, want)
	}
	if n != 11 {
		t.Errorf("HashSHA256 n = %d, want 11", n)
	}
}

func TestValidOID(t *testing.T) {
	if !ValidOID(validOID()) {
		t.Errorf("ValidOID(64 hex chars) = false, want true")
	}
	if ValidOID("not-hex") {
		t.Errorf("ValidOID(not-hex) = true, want false")
	}
	if ValidOID(validOID()[:63]) {
		t.Errorf("ValidOID(63 chars) = true, want false")
	}
}
