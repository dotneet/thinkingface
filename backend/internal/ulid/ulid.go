// Package ulid generates 26-character Crockford base32 ULIDs: 48 bits of
// millisecond timestamp followed by 80 random bits. Implemented here rather
// than pulled in as a dependency because the only property callers need is
// "unique key suffix", with lexical ordering as a debugging convenience.
// The WAL uses it for pack names and the store for repository storage paths.
package ulid

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a fresh ULID.
func New() string {
	var b [16]byte
	ms := uint64(time.Now().UTC().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// crypto/rand.Read never returns an error on any supported platform; a
	// failure here would be unrecoverable anyway, so panic rather than hand
	// back a predictable key.
	if _, err := rand.Read(b[6:]); err != nil {
		panic("ulid: crypto/rand failed: " + err.Error())
	}
	return encode(b)
}

func encode(b [16]byte) string {
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[b[15]&0x1f]
		shiftRight5(&b)
	}
	return string(out)
}

// shiftRight5 divides the 128-bit big-endian value by 32.
func shiftRight5(b *[16]byte) {
	for i := 15; i > 0; i-- {
		b[i] = b[i]>>5 | b[i-1]<<3
	}
	b[0] >>= 5
}
