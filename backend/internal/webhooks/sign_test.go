package webhooks

import "testing"

func TestSignIsDeterministicAndPrefixed(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"event":"repo.push"}`)

	sig := Sign(secret, body)
	if got, want := sig[:7], "sha256="; got != want {
		t.Fatalf("signature prefix = %q, want %q", got, want)
	}
	if sig2 := Sign(secret, body); sig != sig2 {
		t.Fatalf("Sign is not deterministic: %q != %q", sig, sig2)
	}
}

func TestSignDiffersOnSecretOrBody(t *testing.T) {
	base := Sign([]byte("secret-a"), []byte("body"))
	if got := Sign([]byte("secret-b"), []byte("body")); got == base {
		t.Fatal("signature must depend on the secret")
	}
	if got := Sign([]byte("secret-a"), []byte("other body")); got == base {
		t.Fatal("signature must depend on the body")
	}
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"ok":true}`)
	sig := Sign(secret, body)

	if !VerifySignature(secret, body, sig) {
		t.Fatal("VerifySignature rejected a signature Sign produced")
	}
	if VerifySignature(secret, body, sig+"tampered") {
		t.Fatal("VerifySignature accepted a tampered signature")
	}
	if VerifySignature([]byte("wrong-secret"), body, sig) {
		t.Fatal("VerifySignature accepted the wrong secret")
	}
	if VerifySignature(secret, []byte("different body"), sig) {
		t.Fatal("VerifySignature accepted a mismatched body")
	}
}
