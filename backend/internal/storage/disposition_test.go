package storage

import (
	"mime"
	"strings"
	"testing"
)

// The header used to be built with fmt.Sprintf("attachment; filename=%q"),
// which is Go quoting, not HTTP quoting: a non-ASCII name was signed into the
// URL as its Go escape sequence, so downloading "café.txt" saved a file
// literally called café.txt. RFC 6266's answer is both parameters -- a
// sanitised ASCII filename for anything that only understands that, and
// filename* carrying the real name.
func TestContentDisposition(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		want         string
		wantFilename string
	}{
		{
			name:         "plain ascii needs no ext-value",
			in:           "model.safetensors",
			want:         `attachment; filename="model.safetensors"`,
			wantFilename: "model.safetensors",
		},
		{
			name:         "non-ascii travels in filename*",
			in:           "café.txt",
			want:         `attachment; filename="caf_.txt"; filename*=UTF-8''caf%C3%A9.txt`,
			wantFilename: "café.txt",
		},
		{
			name:         "japanese",
			in:           "モデル.bin",
			want:         `attachment; filename="___.bin"; filename*=UTF-8''%E3%83%A2%E3%83%87%E3%83%AB.bin`,
			wantFilename: "モデル.bin",
		},
		{
			name:         "a quote cannot break out of the quoted string",
			in:           `evil".txt`,
			want:         `attachment; filename="evil_.txt"; filename*=UTF-8''evil%22.txt`,
			wantFilename: `evil".txt`,
		},
		{
			name:         "a newline cannot be injected into the header",
			in:           "a\nb.txt",
			want:         `attachment; filename="a_b.txt"; filename*=UTF-8''a%0Ab.txt`,
			wantFilename: "a\nb.txt",
		},
		{
			name:         "a name with nothing printable still yields a filename",
			in:           "…",
			want:         `attachment; filename="_"; filename*=UTF-8''%E2%80%A6`,
			wantFilename: "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContentDisposition(tt.in)
			if got != tt.want {
				t.Fatalf("ContentDisposition(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// The real assertion: a standards-compliant parser gets the
			// original name back out.
			_, params, err := mime.ParseMediaType(got)
			if err != nil {
				t.Fatalf("ParseMediaType(%q): %v", got, err)
			}
			if params["filename"] != tt.wantFilename {
				t.Errorf("parsed filename = %q, want %q", params["filename"], tt.wantFilename)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("header %q contains a line break", got)
			}
		})
	}
}
