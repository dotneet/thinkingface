package storage

import (
	"fmt"
	"strings"
)

// ContentDisposition builds an RFC 6266 attachment header for a file name
// that may be anything a git tree can hold.
//
// It is exported because it is the *only* implementation in this codebase:
// the LFS signed-URL path asks GCS for the header it returns
// (GCS.SignedGetURL) and the API's own download paths set the same string on
// the response themselves (api/resolve.go, api/git.go). Two spellings of
// this header is how one of them ends up unparseable -- which is exactly
// what happened while a second copy built filename* with url.PathEscape,
// leaving `=`, `:`, `@`, `'`, `(` and `)` unescaped, so a name as ordinary
// as PyTorch Lightning's default "epoch=12-step=500.ckpt" produced a header
// no RFC 2231 parser would accept.
//
// This used to be fmt.Sprintf("attachment; filename=%q", name), which is
// wrong twice over for a non-ASCII name. Go's %q escapes non-ASCII runes as
// Go source escapes, so "café.txt" was signed into the URL as the literal
// eleven characters caf\u00e9.txt and the browser saved a file called that;
// and even the raw UTF-8 bytes would have been out of spec, since a quoted
// filename parameter is limited to ISO-8859-1 and browsers disagree about
// what to do with anything else. %q would also happily emit a bare newline
// escape into a signed header value.
//
// The interoperable form is both parameters: a sanitised ASCII `filename` for
// anything that only understands that, and `filename*` carrying the real name
// as an RFC 5987 ext-value, which every current browser prefers. The ASCII
// fallback is only emitted with characters that need no quoting, so the
// header cannot be broken by a quote, a backslash or a control character in a
// path.
func ContentDisposition(name string) string {
	ascii := asciiFallbackName(name)
	if ascii == name {
		return `attachment; filename="` + ascii + `"`
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + rfc5987Escape(name)
}

// asciiFallbackName reduces a file name to printable ASCII that is safe
// inside a quoted-string: everything else becomes '_'. It is a fallback, not
// a transliteration -- the real name travels in filename*.
func asciiFallbackName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

// rfc5987Escape percent-encodes a UTF-8 string as an ext-value's value-chars:
// attr-char (RFC 5987 §3.2.1) is kept, every other byte is escaped. It is not
// url.QueryEscape, which encodes a space as '+' and leaves out several
// attr-chars.
func rfc5987Escape(s string) string {
	const attrExtra = "!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(attrExtra, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
