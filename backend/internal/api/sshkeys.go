// SSH public key management. The shape follows the access token endpoints in
// auth.go: list / create / delete under the caller's own account, with
// creation and deletion requiring the write scope so a read-only token cannot
// grant itself a new credential.

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// maxSSHKeyBody has to fit a whole authorized_keys line, which maxAuthBody
// (sized for short credential strings) does not guarantee.
const maxSSHKeyBody = 32 << 10

// maxSSHKeyTitle bounds the free-text label. Long enough for "work laptop
// (macOS, 2026)", short enough that a listing stays readable.
const maxSSHKeyTitle = 100

func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListSSHKeys(r.Context(), user.ID)
	if err != nil {
		internalError(w, "list ssh keys", err)
		return
	}
	items := make([]apitypes.SSHKeyItem, 0, len(keys))
	for i := range keys {
		items = append(items, toSSHKeyItem(&keys[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.SSHKeyListResponse{Items: items})
}

func (s *Server) handleCreateSSHKey(w http.ResponseWriter, r *http.Request) {
	// Write scope, not merely authentication: a registered key is a
	// write-capable credential, so a read-only token must not be able to mint
	// one (the same reasoning as handleCreateToken).
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
		Key   string `json:"key"`
	}
	if !decodeJSON(w, r, maxSSHKeyBody, &req, "request body must be JSON with title and key") {
		return
	}

	parsed, err := auth.ParseSSHPublicKey(req.Key)
	if err != nil {
		// The wrapped text is written for the person pasting the key; only
		// the package prefix is dropped.
		badRequest(w, strings.TrimPrefix(err.Error(), "auth: invalid ssh public key: "))
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		// Fall back to the comment OpenSSH put in the .pub file, which is
		// usually user@host and exactly what the user would have typed.
		title = strings.TrimSpace(parsed.Comment)
	}
	if title == "" {
		title = parsed.Type
	}
	if len(title) > maxSSHKeyTitle {
		title = title[:maxSSHKeyTitle]
	}

	rec, err := s.store.CreateSSHKey(r.Context(), user.ID, title, parsed.Authorized, parsed.Fingerprint)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Deliberately does not say whose account holds it: the
			// fingerprint is unique instance-wide, and naming the other
			// account would be an information leak.
			conflict(w, "this key is already registered")
			return
		}
		internalError(w, "create ssh key", err)
		return
	}
	// A registered key is a write-capable credential, so adding one is an
	// auditable event. The fingerprint is safe to record -- it is a digest of
	// public key material, and it is the only handle that identifies which
	// key was added.
	slog.Info("ssh key added", "username", user.Username, "user_id", user.ID,
		"key_id", rec.ID, "fingerprint", rec.Fingerprint, "actor", "self",
		"client_ip", s.clientIP(r))
	writeJSON(w, http.StatusOK, toSSHKeyItem(rec))
}

func (s *Server) handleDeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	// Removing a credential is a state change, so a read-only token may not
	// do it.
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		badRequest(w, "ssh key id must be a number")
		return
	}
	if err := s.store.DeleteSSHKey(r.Context(), user.ID, id); err != nil {
		handleStoreError(w, "delete ssh key", err)
		return
	}
	slog.Info("ssh key removed", "username", user.Username, "user_id", user.ID,
		"key_id", id, "actor", "self", "client_ip", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// toSSHKeyItem drops the owning user id, which never leaves the server.
func toSSHKeyItem(k *store.SSHKey) apitypes.SSHKeyItem {
	return apitypes.SSHKeyItem{
		ID:          k.ID,
		Title:       k.Title,
		KeyType:     auth.SSHKeyType(k.PublicKey),
		PublicKey:   k.PublicKey,
		Fingerprint: k.Fingerprint,
		CreatedAt:   k.CreatedAt,
		LastUsedAt:  k.LastUsedAt,
	}
}
