package gitrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// LFSPointerMaxSize bounds how much of a blob we will consider when sniffing
// for a pointer. Real pointers are ~130 bytes; the spec caps them at 1 KiB.
const LFSPointerMaxSize = 1024

const lfsPointerVersion = "https://git-lfs.github.com/spec/v1"

var oidRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

type LFSPointer struct {
	OID  string // sha256 hex, without the "sha256:" prefix
	Size int64
}

// ParseLFSPointer returns the pointer encoded in data, or ok=false when the
// bytes are ordinary file content.
func ParseLFSPointer(data []byte) (LFSPointer, bool) {
	if len(data) == 0 || len(data) > LFSPointerMaxSize {
		return LFSPointer{}, false
	}
	// Pointer files are strictly text; a NUL byte means binary content.
	for _, b := range data {
		if b == 0 {
			return LFSPointer{}, false
		}
	}
	var p LFSPointer
	seenVersion, seenSize := false, false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			return LFSPointer{}, false
		}
		switch key {
		case "version":
			if value != lfsPointerVersion {
				return LFSPointer{}, false
			}
			seenVersion = true
		case "oid":
			hash, hex, ok := strings.Cut(value, ":")
			if !ok || hash != "sha256" || !oidRe.MatchString(hex) {
				return LFSPointer{}, false
			}
			p.OID = hex
		case "size":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return LFSPointer{}, false
			}
			p.Size = n
			seenSize = true
		}
	}
	// All three fields are required by the spec and git-lfs always writes
	// them; a blob missing one is not a pointer. Accepting a sizeless one and
	// calling it zero bytes is worse than treating it as ordinary content,
	// because the zero travels: it becomes the file's advertised size, the
	// X-Linked-Size header, and finally a Content-Length of 0 in front of a
	// body that is not empty, which net/http truncates.
	if !seenVersion || p.OID == "" || !seenSize {
		return LFSPointer{}, false
	}
	return p, true
}

// FormatLFSPointer renders the canonical pointer file for an object.
func FormatLFSPointer(oid string, size int64) []byte {
	return []byte(fmt.Sprintf("version %s\noid sha256:%s\nsize %d\n", lfsPointerVersion, oid, size))
}

// HashSHA256 streams r and returns its lowercase hex digest along with the
// number of bytes read.
func HashSHA256(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func ValidOID(oid string) bool { return oidRe.MatchString(oid) }
