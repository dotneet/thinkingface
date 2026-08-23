package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashPassword_CheckPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := CheckPassword(hash, "correct horse battery staple"); err != nil {
		t.Errorf("CheckPassword with correct password failed: %v", err)
	}
}

func TestCheckPassword_WrongPasswordFails(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := CheckPassword(hash, "wrong password"); err != ErrBadCredentials {
		t.Errorf("CheckPassword with wrong password: err = %v, want ErrBadCredentials", err)
	}
}

func TestNewToken_PrefixAndUniqueness(t *testing.T) {
	tok1, hash1, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	tok2, hash2, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(tok1, TokenPrefix) {
		t.Errorf("token %q does not have prefix %q", tok1, TokenPrefix)
	}
	if tok1 == tok2 {
		t.Errorf("two calls to NewToken produced the same token: %q", tok1)
	}
	if hash1 == hash2 {
		t.Errorf("two different tokens hashed to the same digest")
	}
	if hash1 != HashToken(tok1) {
		t.Errorf("NewToken's returned hash does not match HashToken(token)")
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	tok := "tf_sometoken"
	h1 := HashToken(tok)
	h2 := HashToken(tok)
	if h1 != h2 {
		t.Errorf("HashToken is not deterministic: %q != %q", h1, h2)
	}
	if HashToken("tf_different") == h1 {
		t.Errorf("HashToken produced the same digest for different tokens")
	}
}

func TestSessions_IssueVerifyRoundTrip(t *testing.T) {
	s := NewSessions("test-secret", time.Hour)
	cookie := s.Issue(42, 3)

	userID, epoch, err := s.Verify(cookie)
	if err != nil {
		t.Fatalf("Verify(valid cookie): %v", err)
	}
	if userID != 42 {
		t.Errorf("Verify returned userID = %d, want 42", userID)
	}
	if epoch != 3 {
		t.Errorf("Verify returned epoch = %d, want 3", epoch)
	}
}

func TestSessions_Verify_TamperedSignatureRejected(t *testing.T) {
	s := NewSessions("test-secret", time.Hour)
	cookie := s.Issue(42, 0)

	parts := strings.Split(cookie, ".")
	if len(parts) != 4 {
		t.Fatalf("cookie has %d parts, want 4: %q", len(parts), cookie)
	}
	// Flip the signature.
	tampered := parts[0] + "." + parts[1] + "." + parts[2] + "." + parts[3] + "x"
	if _, _, err := s.Verify(tampered); err != ErrBadSession {
		t.Errorf("Verify(tampered signature): err = %v, want ErrBadSession", err)
	}

	// Tamper with the payload (change the user id) but keep the original sig.
	tamperedPayload := "999." + parts[1] + "." + parts[2] + "." + parts[3]
	if _, _, err := s.Verify(tamperedPayload); err != ErrBadSession {
		t.Errorf("Verify(tampered payload): err = %v, want ErrBadSession", err)
	}

	// Raising the epoch is a payload change too: a cookie cannot promote
	// itself past a revocation.
	tamperedEpoch := parts[0] + ".999." + parts[2] + "." + parts[3]
	if _, _, err := s.Verify(tamperedEpoch); err != ErrBadSession {
		t.Errorf("Verify(tampered epoch): err = %v, want ErrBadSession", err)
	}
}

func TestSessions_Verify_ExpiredRejected(t *testing.T) {
	s := NewSessions("test-secret", -time.Hour) // already expired on Issue
	cookie := s.Issue(1, 0)
	if _, _, err := s.Verify(cookie); err != ErrBadSession {
		t.Errorf("Verify(expired cookie): err = %v, want ErrBadSession", err)
	}
}

func TestSessions_Verify_DifferentSecretRejected(t *testing.T) {
	s1 := NewSessions("secret-one", time.Hour)
	s2 := NewSessions("secret-two", time.Hour)
	cookie := s1.Issue(7, 0)
	if _, _, err := s2.Verify(cookie); err != ErrBadSession {
		t.Errorf("Verify with a different secret: err = %v, want ErrBadSession", err)
	}
}

func TestSessions_Verify_MalformedValue(t *testing.T) {
	s := NewSessions("secret", time.Hour)
	tests := []string{
		"", "onlyonepart", "two.parts", "a.b.c.d", "a.b.c.d.e",
		"notanumber.0.123.sig",
		// The pre-epoch three-part cookie shape is no longer accepted.
		"1.99999999999.sig",
	}
	for _, v := range tests {
		if _, _, err := s.Verify(v); err != ErrBadSession {
			t.Errorf("Verify(%q): err = %v, want ErrBadSession", v, err)
		}
	}
}

// CheckPasswordMiss must cost roughly what a real check costs; it is the only
// thing standing between an unknown username and a timing oracle.
func TestCheckPasswordMiss_AlwaysFailsAndDoesWork(t *testing.T) {
	if err := CheckPasswordMiss("anything"); err != ErrBadCredentials {
		t.Errorf("CheckPasswordMiss: err = %v, want ErrBadCredentials", err)
	}
	hash, err := HashPassword("some password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	realStart := time.Now()
	_ = CheckPassword(hash, "wrong")
	real := time.Since(realStart)

	missStart := time.Now()
	_ = CheckPasswordMiss("wrong")
	miss := time.Since(missStart)

	// Same cost class: the miss must not be an order of magnitude faster.
	if miss*8 < real {
		t.Errorf("CheckPasswordMiss took %v vs CheckPassword %v; the miss path is not doing the work", miss, real)
	}
}

func TestHashPassword_RejectsOverLongInput(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
		t.Errorf("HashPassword(%d bytes) = nil error, want bcrypt's too-long error", MaxPasswordBytes+1)
	}
}

func TestSessions_TTL(t *testing.T) {
	s := NewSessions("secret", 30*time.Minute)
	if s.TTL() != 30*time.Minute {
		t.Errorf("TTL() = %v, want 30m", s.TTL())
	}
}
