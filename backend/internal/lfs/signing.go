package lfs

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
)

// minTransferBytesPerSecond is the throughput a signed URL's lifetime is
// budgeted against: 1 MiB/s. It is not a prediction of how fast clients are,
// it is the slowest link we are willing to let a transfer fail on. Pushing a
// 10 GiB dataset over a home uplink of a few MiB/s is an ordinary thing to do
// here, and a URL that dies mid-PUT costs the whole object -- git-lfs restarts
// the transfer from zero -- while a URL that outlives the transfer costs
// nothing but a slightly wider window on a key that only ever accepts one
// specific object. The asymmetry is why this number is pessimistic.
const minTransferBytesPerSecond = 1 << 20

// maxTTLSeconds is the largest whole-second count time.Duration can hold.
const maxTTLSeconds = int64(math.MaxInt64) / int64(time.Second)

// signingLimit is GCS's own ceiling on a V4 signed URL's lifetime: 7 days.
// Asking for more does not produce a long-lived URL, it produces an error at
// signing time -- i.e. a failed push rather than a slow one. It is enforced
// here, not left to the operator, because TF_SIGNED_URL_MAX_TTL is allowed to
// be zero ("no ceiling") and a batch large enough to reach 7 days at the
// assumed floor throughput is roughly 600 GiB, which is not an absurd dataset
// for this hub.
const signingLimit = 7 * 24 * time.Hour

// MaxSignedURLTTL returns the longest lifetime TTLFor can hand out for a given
// configured ceiling: the ceiling itself, or signingLimit when there is none
// (max <= 0) or when the configured one is above what GCS will sign.
//
// It exists so that nothing has to re-derive that answer from the config value
// alone. `thinkingface gc` needs it -- its staging window has to outlast every
// URL still in flight -- and reading TF_SIGNED_URL_MAX_TTL as if it were the
// effective maximum gets the no-ceiling case exactly backwards: zero means
// URLs live *longer* (up to 7 days), not shorter.
func MaxSignedURLTTL(max time.Duration) time.Duration {
	if max <= 0 || max > signingLimit {
		return signingLimit
	}
	return max
}

// TTLFor returns how long a signed URL for a transfer of n bytes must live:
// the base lifetime plus the time n bytes take at minTransferBytesPerSecond,
// clamped to max.
//
// A batch hands out every URL at once but the client uses them one at a time,
// so the last object's URL is first touched after every earlier object has
// finished transferring. Sizing the lifetime off a single object's size (or
// off a fixed hour) is what makes a 100-object push of 1 GiB files fail with
// 403s two thirds of the way through.
//
// max is a hard ceiling, even below base: it is the operator's statement of
// how long a leaked URL stays useful. A max <= 0 means "no ceiling", which
// still leaves signingLimit. n <= 0 (unknown size) gets base.
func TTLFor(base, max time.Duration, n int64) time.Duration {
	ttl := base
	if n > 0 {
		// Seconds are computed in integer bytes first: n * time.Second
		// overflows int64 nanoseconds at about 9.2 GB, which is squarely
		// inside the range this function exists for.
		secs := n / minTransferBytesPerSecond
		if secs >= maxTTLSeconds {
			ttl = time.Duration(math.MaxInt64)
		} else if transfer := time.Duration(secs) * time.Second; base > time.Duration(math.MaxInt64)-transfer {
			ttl = time.Duration(math.MaxInt64)
		} else {
			ttl = base + transfer
		}
	}
	if max > 0 && ttl > max {
		ttl = max
	}
	if ttl > signingLimit {
		ttl = signingLimit
	}
	return ttl
}

// proxyHref builds a self-authenticating URL for the emulator transfer path.
// git-lfs and huggingface_hub both assume an upload href is pre-signed and
// send no Authorization header with the transfer itself, so the credential has
// to live in the URL exactly as it does for a real GCS signed URL.
func (h *Handler) proxyHref(op string, repoID int64, oid string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%s/api/v1/lfs/%d/%s?op=%s&exp=%d&sig=%s",
		h.publicURL, repoID, url.PathEscape(oid), op, exp, h.sign(op, repoID, oid, exp))
}

func (h *Handler) sign(op string, repoID int64, oid string, exp int64) string {
	mac := hmac.New(sha256.New, h.secret)
	fmt.Fprintf(mac, "%s\n%d\n%s\n%d", op, repoID, oid, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyProxySignature reports whether a proxy request carries a signature this
// server issued and that has not expired.
func (h *Handler) VerifyProxySignature(op string, repoID int64, oid, expRaw, sig string) bool {
	if sig == "" || expRaw == "" {
		return false
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h.sign(op, repoID, oid, exp)), []byte(sig)) == 1
}
