package sshserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// LoadOrCreateHostKey reads the server's host key, generating an ed25519 one
// the first time.
//
// The host key is the server's identity: clients pin it in known_hosts and
// warn loudly when it changes. It therefore has to outlive the container, so
// path must point at persistent storage in any deployment where the
// filesystem is ephemeral -- on a tmpfs the key is regenerated on every cold
// start and every client sees a host key mismatch.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	pemBytes, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	switch {
	case err == nil:
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse ssh host key %s: %w", path, err)
		}
		return signer, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read ssh host key %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create ssh host key directory: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ssh host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "thinkingface")
	if err != nil {
		return nil, fmt.Errorf("encode ssh host key: %w", err)
	}
	// O_EXCL so two processes racing on a cold start cannot end up disagreeing
	// about which key they wrote; the loser re-reads the winner's file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadOrCreateHostKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create ssh host key %s: %w", path, err)
	}
	if err := pem.Encode(f, block); err != nil {
		f.Close()
		return nil, fmt.Errorf("write ssh host key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write ssh host key %s: %w", path, err)
	}
	return ssh.NewSignerFromKey(priv)
}
