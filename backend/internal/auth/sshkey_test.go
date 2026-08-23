package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// authorizedLine renders a generated key the way an OpenSSH .pub file does.
func authorizedLine(t *testing.T, key any, comment string) string {
	t.Helper()
	pub, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatalf("wrap public key: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

func ed25519Line(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	return authorizedLine(t, pub, comment)
}

func rsaLine(t *testing.T, bits int) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	return authorizedLine(t, &key.PublicKey, "")
}

func TestParseSSHPublicKeyAcceptsSupportedAlgorithms(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}

	cases := map[string]struct {
		line     string
		wantType string
	}{
		"ed25519": {ed25519Line(t, "alice@laptop"), ssh.KeyAlgoED25519},
		"rsa2048": {rsaLine(t, 2048), ssh.KeyAlgoRSA},
		"ecdsa":   {authorizedLine(t, &ecKey.PublicKey, ""), ssh.KeyAlgoECDSA256},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseSSHPublicKey(tc.line)
			if err != nil {
				t.Fatalf("ParseSSHPublicKey: %v", err)
			}
			if got.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tc.wantType)
			}
			if !strings.HasPrefix(got.Fingerprint, "SHA256:") {
				t.Errorf("Fingerprint = %q, want a SHA256: prefix", got.Fingerprint)
			}
			// The canonical form drops the comment, so the same key pasted
			// with a different comment stores identically.
			if strings.Fields(got.Authorized)[0] != tc.wantType || len(strings.Fields(got.Authorized)) != 2 {
				t.Errorf("Authorized = %q, want exactly \"<type> <base64>\"", got.Authorized)
			}
		})
	}
}

func TestParseSSHPublicKeyKeepsComment(t *testing.T) {
	got, err := ParseSSHPublicKey(ed25519Line(t, "alice@laptop"))
	if err != nil {
		t.Fatalf("ParseSSHPublicKey: %v", err)
	}
	if got.Comment != "alice@laptop" {
		t.Errorf("Comment = %q, want alice@laptop", got.Comment)
	}
}

func TestParseSSHPublicKeyFingerprintIsStable(t *testing.T) {
	line := ed25519Line(t, "alice@laptop")
	first, err := ParseSSHPublicKey(line)
	if err != nil {
		t.Fatalf("ParseSSHPublicKey: %v", err)
	}
	// Same key, different comment: the fingerprint (which is the uniqueness
	// key in the database) must not move.
	second, err := ParseSSHPublicKey(strings.TrimSuffix(line, " alice@laptop") + " bob@desktop")
	if err != nil {
		t.Fatalf("ParseSSHPublicKey: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprint changed with the comment: %q vs %q", first.Fingerprint, second.Fingerprint)
	}
	if first.Authorized != second.Authorized {
		t.Errorf("canonical form changed with the comment: %q vs %q", first.Authorized, second.Authorized)
	}
}

func TestParseSSHPublicKeyMatchesServerSideFingerprint(t *testing.T) {
	// The registration path and the SSH auth path must spell the fingerprint
	// the same way, or no registered key would ever authenticate.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	parsed, err := ParseSSHPublicKey(authorizedLine(t, pub, ""))
	if err != nil {
		t.Fatalf("ParseSSHPublicKey: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap public key: %v", err)
	}
	if got := SSHKeyFingerprint(sshPub); got != parsed.Fingerprint {
		t.Errorf("SSHKeyFingerprint = %q, ParseSSHPublicKey = %q", got, parsed.Fingerprint)
	}
}

func TestParseSSHPublicKeyRejections(t *testing.T) {
	validEd25519 := ed25519Line(t, "alice@laptop")

	cases := map[string]struct {
		input       string
		wantMessage string
	}{
		"empty":        {"   ", "empty"},
		"garbage":      {"not a key at all", "OpenSSH public key"},
		"privateKey":   {"-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----", "private key"},
		"pemFragment":  {"MIIEpAIBAAKCAQEA PRIVATE KEY material", "private key"},
		"twoKeys":      {validEd25519 + "\n" + ed25519Line(t, ""), "single key"},
		"withOptions":  {`command="/bin/false" ` + validEd25519, "options"},
		"rsaTooSmall":  {rsaLine(t, 1024), "at least 2048 bits"},
		"tooLong":      {validEd25519 + " " + strings.Repeat("x", MaxSSHKeyLength), "longer than"},
		"emptyComment": {"ssh-ed25519", "OpenSSH public key"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseSSHPublicKey(tc.input)
			if err == nil {
				t.Fatalf("ParseSSHPublicKey accepted %q -> %+v", tc.input, got)
			}
			if !errors.Is(err, ErrInvalidSSHKey) {
				t.Errorf("error %v does not wrap ErrInvalidSSHKey", err)
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantMessage)
			}
		})
	}
}

func TestSSHKeyType(t *testing.T) {
	if got := SSHKeyType("ssh-ed25519 AAAAC3Nz"); got != "ssh-ed25519" {
		t.Errorf("SSHKeyType = %q, want ssh-ed25519", got)
	}
	if got := SSHKeyType("   "); got != "" {
		t.Errorf("SSHKeyType of blank = %q, want empty", got)
	}
}
