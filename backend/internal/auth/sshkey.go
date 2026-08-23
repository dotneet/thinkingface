package auth

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrInvalidSSHKey wraps every rejection ParseSSHPublicKey can produce, so the
// API layer can map the whole family to 400 while still relaying the specific
// message to the user.
var ErrInvalidSSHKey = errors.New("auth: invalid ssh public key")

// MinRSABits is the smallest RSA modulus accepted for a registered key.
// 1024-bit RSA is broken in practice and OpenSSH itself refuses to generate
// anything below 1024; 2048 is the floor every current client defaults past.
const MinRSABits = 2048

// MaxSSHKeyLength bounds the accepted text so a registration request cannot
// be used to store an arbitrarily large blob. A 16 kbit RSA key in
// authorized_keys form is under 4 KiB including its comment.
const MaxSSHKeyLength = 8192

// SSHPublicKey is a parsed, validated public key ready to be stored.
type SSHPublicKey struct {
	// Type is the wire algorithm name, e.g. "ssh-ed25519".
	Type string
	// Authorized is the canonical "<type> <base64>" form, with any comment
	// and any options stripped. Storing the canonical form means two
	// registrations of the same key differing only in comment collapse onto
	// the same bytes as well as the same fingerprint.
	Authorized string
	// Comment is the trailing free text OpenSSH writes into a .pub file
	// (usually user@host). Kept only to pre-fill the key's title.
	Comment string
	// Fingerprint is the OpenSSH "SHA256:<base64>" form, which is what
	// `ssh-keygen -lf` prints and what users recognise.
	Fingerprint string
}

// ParseSSHPublicKey validates one authorized_keys line and returns its
// canonical form. It is deliberately strict: exactly one key, no
// authorized_keys options (which are directives to *our* server, not data a
// user gets to supply), and only algorithms this server is willing to
// authenticate with.
func ParseSSHPublicKey(raw string) (*SSHPublicKey, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, fmt.Errorf("%w: the key is empty", ErrInvalidSSHKey)
	}
	if len(text) > MaxSSHKeyLength {
		return nil, fmt.Errorf("%w: the key is longer than %d characters", ErrInvalidSSHKey, MaxSSHKeyLength)
	}
	// Catch the most damaging paste mistake before anything else, and say so
	// plainly: a private key pasted here would be stored server-side and
	// shown back in the UI.
	if strings.HasPrefix(text, "-----BEGIN") || strings.Contains(text, "PRIVATE KEY") {
		return nil, fmt.Errorf("%w: that looks like a private key; paste the public key (the .pub file) instead", ErrInvalidSSHKey)
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, fmt.Errorf("%w: paste a single key on one line", ErrInvalidSSHKey)
	}

	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("%w: expected an OpenSSH public key such as \"ssh-ed25519 AAAA... you@example.com\"", ErrInvalidSSHKey)
	}
	if len(options) > 0 {
		// authorized_keys options (command=, environment=, ...) instruct the
		// server. Accepting them from a registration form would hand the user
		// control over how their own session is executed.
		return nil, fmt.Errorf("%w: authorized_keys options are not accepted", ErrInvalidSSHKey)
	}
	if len(strings.TrimSpace(string(rest))) > 0 {
		return nil, fmt.Errorf("%w: paste a single key on one line", ErrInvalidSSHKey)
	}

	if err := checkSSHKeyAlgorithm(pub); err != nil {
		return nil, err
	}

	return &SSHPublicKey{
		Type:        pub.Type(),
		Authorized:  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		Comment:     comment,
		Fingerprint: ssh.FingerprintSHA256(pub),
	}, nil
}

// checkSSHKeyAlgorithm allow-lists the key types this server authenticates
// with. Everything not named here is refused rather than tolerated: an
// algorithm we would not accept at login time must not look registrable.
func checkSSHKeyAlgorithm(pub ssh.PublicKey) error {
	switch pub.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256:
		return nil
	case ssh.KeyAlgoRSA:
		bits, err := rsaBits(pub)
		if err != nil {
			return err
		}
		if bits < MinRSABits {
			return fmt.Errorf("%w: RSA keys must be at least %d bits (this one is %d)",
				ErrInvalidSSHKey, MinRSABits, bits)
		}
		return nil
	// Spelled out rather than referenced: x/crypto marks its constant
	// deprecated, and this branch exists only to give DSA a better message
	// than "unsupported key type".
	case "ssh-dss":
		return fmt.Errorf("%w: DSA keys are not supported; use ssh-ed25519 or an RSA key of at least %d bits",
			ErrInvalidSSHKey, MinRSABits)
	default:
		return fmt.Errorf("%w: unsupported key type %q; use ssh-ed25519, ecdsa-sha2-* or ssh-rsa",
			ErrInvalidSSHKey, pub.Type())
	}
}

func rsaBits(pub ssh.PublicKey) (int, error) {
	crypto, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		return 0, fmt.Errorf("%w: the RSA key could not be read", ErrInvalidSSHKey)
	}
	rsaKey, ok := crypto.CryptoPublicKey().(*rsa.PublicKey)
	if !ok {
		return 0, fmt.Errorf("%w: the RSA key could not be read", ErrInvalidSSHKey)
	}
	return rsaKey.N.BitLen(), nil
}

// SSHKeyFingerprint is the fingerprint of an already-parsed key, in the same
// "SHA256:<base64>" form ParseSSHPublicKey produces. The SSH server uses it to
// look up the offered key, so both sides must agree on the spelling.
func SSHKeyFingerprint(pub ssh.PublicKey) string {
	return ssh.FingerprintSHA256(pub)
}

// SSHKeyType reports the algorithm name of a stored authorized_keys line
// without re-validating it. Storage holds keys that were valid when they were
// registered; an unreadable one degrades to "" rather than failing a listing.
func SSHKeyType(authorized string) string {
	fields := strings.Fields(authorized)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
