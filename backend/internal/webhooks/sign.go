package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Sign computes the HMAC-SHA256 signature thinkingface sends with every
// delivery, in the X-Thinkingface-Signature header. The value carries the
// digest name so a future algorithm change stays self-describing, mirroring
// how GitHub and Stripe shape their webhook signature headers.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature reports whether sig is the signature Sign would have
// produced for body under secret. Comparison is constant-time so a receiver
// implementing this check cannot be timed into leaking the secret.
func VerifySignature(secret, body []byte, sig string) bool {
	return hmac.Equal([]byte(sig), []byte(Sign(secret, body)))
}
