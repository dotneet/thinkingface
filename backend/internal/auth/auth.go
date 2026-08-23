// Package auth handles password hashing, personal access tokens, and the
// signed session cookie the web UI uses.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// TokenPrefix marks personal access tokens so they are recognisable in logs
// and secret scanners.
const TokenPrefix = "tf_"

const SessionCookieName = "tf_session"

var (
	ErrBadCredentials = errors.New("auth: invalid credentials")
	ErrBadSession     = errors.New("auth: invalid session")
)

// MaxPasswordBytes is bcrypt's hard input limit. Anything longer makes
// GenerateFromPassword return ErrPasswordTooLong, so callers must reject it
// as bad input rather than let it surface as a 500.
const MaxPasswordBytes = 72

func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

func CheckPassword(hash, password string) error {
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrBadCredentials
	}
	return nil
}

// dummyHash is a valid bcrypt digest of a value nobody can supply. It exists
// so an unknown username costs the same as a known one: without it the login
// handler returns before any bcrypt work and the response time alone
// enumerates accounts. Generated once at init because the cost of a
// GenerateFromPassword is exactly what has to be paid on the miss path.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("thinkingface-nonexistent-account"), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt only fails on an over-long input, which this is not.
		panic("auth: generate dummy hash: " + err.Error())
	}
	return string(h)
}()

// CheckPasswordMiss burns the same bcrypt work CheckPassword would have, for
// the case where no user matched. It always reports failure.
func CheckPasswordMiss(password string) error {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
	return ErrBadCredentials
}

// NewToken mints a personal access token and returns both the value shown once
// to the user and the digest stored in the database.
func NewToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken digests a token for storage and lookup. Tokens are high-entropy
// random values, so a plain SHA-256 is the right tool: no stretching needed,
// and lookups stay a single indexed query.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Sessions issues and verifies the signed cookie value.
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

func NewSessions(secret string, ttl time.Duration) *Sessions {
	return &Sessions{secret: []byte(secret), ttl: ttl}
}

// Issue returns a cookie value of the form
// "<userID>.<epoch>.<expiryUnix>.<hmac>". epoch is the user's session_epoch
// at the time of issue: the caller compares it against the stored value on
// every request, which is what lets logout and a password change revoke a
// cookie that has not expired yet.
func (s *Sessions) Issue(userID, epoch int64) string {
	exp := time.Now().Add(s.ttl).Unix()
	payload := fmt.Sprintf("%d.%d.%d", userID, epoch, exp)
	return payload + "." + s.sign(payload)
}

func (s *Sessions) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify returns the user ID and session epoch carried by a cookie, rejecting
// tampered or expired values. The epoch still has to be checked against the
// user row; this function only proves the value is one this server signed.
func (s *Sessions) Verify(value string) (userID, epoch int64, err error) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		// Includes the pre-epoch three-part form, which is deliberately no
		// longer accepted: those cookies predate revocation.
		return 0, 0, ErrBadSession
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2]
	if subtle.ConstantTimeCompare([]byte(s.sign(payload)), []byte(parts[3])) != 1 {
		return 0, 0, ErrBadSession
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, 0, ErrBadSession
	}
	userID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, ErrBadSession
	}
	epoch, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, ErrBadSession
	}
	return userID, epoch, nil
}

func (s *Sessions) TTL() time.Duration { return s.ttl }
