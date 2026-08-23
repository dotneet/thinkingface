// Package lfs implements the Git LFS batch API. Object bytes never pass
// through this process in a real GCS deployment: clients get V4 signed URLs
// and transfer directly with the bucket.
package lfs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ContentType is the media type the LFS spec mandates on batch requests and
// responses.
const ContentType = "application/vnd.git-lfs+json"

// ErrUnsupportedOperation reports a batch naming something other than upload
// or download, which is a malformed request rather than a server fault.
var ErrUnsupportedOperation = errors.New("lfs: unsupported operation")

// oidRe matches the only object id this server accepts. Every oid reaches
// storage.LFSKey, which splices it straight into an object key, so an
// unchecked value from a batch request would let a client name a key outside
// the content-addressed lfs/ prefix.
var oidRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidOID reports whether oid is a lowercase sha256 hex digest.
func ValidOID(oid string) bool { return oidRe.MatchString(oid) }

type BatchRequest struct {
	Operation string      `json:"operation"`
	Transfers []string    `json:"transfers"`
	Ref       *Ref        `json:"ref"`
	Objects   []ObjectRef `json:"objects"`
	HashAlgo  string      `json:"hash_algo"`
}

type Ref struct {
	Name string `json:"name"`
}

type ObjectRef struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

type ObjectResponse struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type BatchResponse struct {
	Transfer string           `json:"transfer"`
	Objects  []ObjectResponse `json:"objects"`
	HashAlgo string           `json:"hash_algo"`
}

// lfsRecorder is the store surface the batch/verify paths need. *store.Store
// implements it; tests substitute a fake so they can drive ErrLFSObjectGone
// without Postgres.
type lfsRecorder interface {
	RecordLFSObject(ctx context.Context, repoID int64, oid string, size int64, confirmPresent func(key string) (bool, error)) error
	// RepoHasLFSObject answers whether the repository is entitled to an
	// object at all. Objects are stored by content hash with no repository
	// in the key, so this is the only thing separating "this repository has
	// this file" from "somebody on this instance uploaded these bytes once".
	RepoHasLFSObject(ctx context.Context, repoID int64, oid string) (bool, error)
}

type Handler struct {
	store     lfsRecorder
	storage   storage.Storage
	ttl       time.Duration
	publicURL string
	secret    []byte
}

func New(st *store.Store, obj storage.Storage, ttl time.Duration, publicURL, secret string) *Handler {
	return &Handler{store: st, storage: obj, ttl: ttl, publicURL: publicURL, secret: []byte(secret)}
}

var _ lfsRecorder = (*store.Store)(nil)

// proxyHref builds a self-authenticating URL for the emulator transfer path.
// git-lfs and huggingface_hub both assume an upload href is pre-signed and
// send no Authorization header with the transfer itself, so the credential has
// to live in the URL exactly as it does for a real GCS signed URL.
func (h *Handler) proxyHref(op string, repoID int64, oid string) string {
	exp := time.Now().Add(h.ttl).Unix()
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

// Batch answers an LFS batch request for one repository. authToken is echoed
// back to the client in the action headers so its follow-up proxy requests
// authenticate; it is empty in signed-URL mode, where the URL carries its own
// credentials.
func (h *Handler) Batch(ctx context.Context, repoID int64, req *BatchRequest, authToken string) (*BatchResponse, error) {
	resp := &BatchResponse{Transfer: "basic", HashAlgo: "sha256", Objects: make([]ObjectResponse, 0, len(req.Objects))}

	if req.Operation != "upload" && req.Operation != "download" {
		return nil, fmt.Errorf("%w %q", ErrUnsupportedOperation, req.Operation)
	}

	for _, obj := range req.Objects {
		item := ObjectResponse{OID: obj.OID, Size: obj.Size, Authenticated: true}

		// A per-object error is how the LFS spec reports a value it will not
		// act on, so one bad entry does not fail the whole batch.
		if !ValidOID(obj.OID) || obj.Size < 0 {
			item.Error = &ObjectError{Code: 422, Message: "oid must be a sha256 hex digest and size must not be negative"}
			resp.Objects = append(resp.Objects, item)
			continue
		}

		verifyExp := time.Now().Add(h.ttl).Unix()

		switch req.Operation {
		case "upload":
			exists, err := h.stored(ctx, obj.OID, obj.Size)
			if err != nil {
				return nil, err
			}
			if exists {
				// Deduplicated: the client uploads nothing and the commit still
				// references the same content. RecordLFSObject re-checks
				// storage under the row lock; if a concurrent GC already
				// deleted the bytes, ErrLFSObjectGone means we must ask
				// the client to upload rather than treat the oid as present.
				err := h.store.RecordLFSObject(ctx, repoID, obj.OID, obj.Size,
					func(k string) (bool, error) { return h.storedAt(ctx, k, obj.Size) })
				if err != nil && !errors.Is(err, store.ErrLFSObjectGone) {
					return nil, err
				}
				if err == nil {
					resp.Objects = append(resp.Objects, item)
					continue
				}
			}
			upload, err := h.uploadAction(ctx, repoID, obj, authToken)
			if err != nil {
				return nil, err
			}
			item.Actions = map[string]Action{
				"upload": upload,
				"verify": {
					Href: fmt.Sprintf("%s/api/v1/lfs/%d/verify?op=verify&exp=%d&sig=%s",
						h.publicURL, repoID, verifyExp, h.sign("verify", repoID, "", verifyExp)),
					Header: authHeader(authToken),
				},
			}

		case "download":
			// Membership first, and with the same answer as a genuinely
			// absent object: the caller has already proved it may read *this*
			// repository, but an oid it merely knows about must not become a
			// download URL for bytes that live in a repository it was not
			// given. Every legitimate route into this repository records
			// the link before a commit can reference the object (upload dedup
			// and Verify below, the emulator proxy upload, and the post-push
			// indexer's LinkLFSObjects).
			owned, err := h.store.RepoHasLFSObject(ctx, repoID, obj.OID)
			if err != nil {
				return nil, fmt.Errorf("check lfs object ownership: %w", err)
			}
			exists := false
			if owned {
				if exists, err = h.stored(ctx, obj.OID, obj.Size); err != nil {
					return nil, err
				}
			}
			if !exists {
				item.Error = &ObjectError{Code: 404, Message: "object " + obj.OID + " not found"}
				break
			}
			download, err := h.downloadAction(ctx, repoID, obj, storage.LFSKey(obj.OID), authToken)
			if err != nil {
				return nil, err
			}
			item.Actions = map[string]Action{"download": download}
		}

		resp.Objects = append(resp.Objects, item)
	}
	return resp, nil
}

// stored reports whether the object's bytes are really in the bucket. The key
// is derived from the oid alone -- lfs/ is the one and only home, immutable
// and shared across repositories -- so this asks the bucket, not the ledger.
//
// It deliberately carries no authorisation: an object being present says
// nothing about who may read it. The download path answers that separately,
// with RepoHasLFSObject, before it ever gets here.
func (h *Handler) stored(ctx context.Context, oid string, size int64) (bool, error) {
	return h.storedAt(ctx, storage.LFSKey(oid), size)
}

// storedAt reports whether key holds size bytes right now. It takes the key
// rather than an oid, so it can be used on a path where the ledger cannot
// answer yet -- Verify runs before the row exists.
func (h *Handler) storedAt(ctx context.Context, key string, size int64) (bool, error) {
	info, err := h.storage.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat lfs object: %w", err)
	}
	if size > 0 && info.Size != size {
		return false, nil
	}
	return true, nil
}

// uploadAction writes to the content-addressed lfs/ key, which is where the
// object stays forever: a batch request carries no path, and no path would
// change the key if it did.
func (h *Handler) uploadAction(ctx context.Context, repoID int64, obj ObjectRef, authToken string) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedPutURL(ctx, storage.LFSKey(obj.OID), h.ttl, obj.Size)
		if err != nil {
			return Action{}, fmt.Errorf("sign upload url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(h.ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("upload", repoID, obj.OID),
		Header:    authHeader(authToken),
		ExpiresIn: int(h.ttl.Seconds()),
	}, nil
}

// downloadAction signs the object's content-addressed key. It takes the key
// rather than deriving it so the caller's authorisation check and the URL it
// hands out are visibly about the same object.
func (h *Handler) downloadAction(ctx context.Context, repoID int64, obj ObjectRef, key, authToken string) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedGetURL(ctx, key, h.ttl, "")
		if err != nil {
			return Action{}, fmt.Errorf("sign download url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(h.ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("download", repoID, obj.OID),
		Header:    authHeader(authToken),
		ExpiresIn: int(h.ttl.Seconds()),
	}, nil
}

func authHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": token}
}

// Verify confirms an uploaded object landed in the bucket at the promised size
// and records it against the repository.
// Storage and database faults are logged rather than described to the client:
// their text carries bucket names and connection detail.
func (h *Handler) Verify(ctx context.Context, repoID int64, oid string, size int64) error {
	if !ValidOID(oid) {
		return errors.New("oid must be a sha256 hex digest")
	}
	info, err := h.storage.Stat(ctx, storage.LFSKey(oid))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("object %s was not uploaded", oid)
		}
		slog.Error("lfs verify: stat object", "oid", oid, "repo_id", repoID, "error", err)
		return fmt.Errorf("object %s could not be verified", oid)
	}
	if size > 0 && info.Size != size {
		return fmt.Errorf("object %s is %d bytes, expected %d", oid, info.Size, size)
	}
	if err := h.store.RecordLFSObject(ctx, repoID, oid, info.Size, func(k string) (bool, error) {
		return h.storedAt(ctx, k, info.Size)
	}); err != nil {
		if errors.Is(err, store.ErrLFSObjectGone) {
			return fmt.Errorf("object %s was not uploaded", oid)
		}
		slog.Error("lfs verify: record object", "oid", oid, "repo_id", repoID, "error", err)
		return fmt.Errorf("object %s could not be recorded", oid)
	}
	return nil
}
