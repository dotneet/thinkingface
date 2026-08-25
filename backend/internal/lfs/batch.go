package lfs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Batch answers an LFS batch request for one repository. authToken is echoed
// back to the client in the action headers so its follow-up proxy requests
// authenticate; it is empty in signed-URL mode, where the URL carries its own
// credentials.
func (h *Handler) Batch(ctx context.Context, repoID int64, req *BatchRequest, authToken string) (*BatchResponse, error) {
	resp := &BatchResponse{Transfer: "basic", HashAlgo: "sha256", Objects: make([]ObjectResponse, 0, len(req.Objects))}

	if req.Operation != "upload" && req.Operation != "download" {
		return nil, fmt.Errorf("%w %q", ErrUnsupportedOperation, req.Operation)
	}

	// Actions are decided here but minted below, because their lifetime
	// depends on the size of the *whole* batch: the client transfers these
	// objects one after another over a single connection, so the URL for the
	// last one has to still be valid after every earlier one has gone across.
	var (
		pending    []pendingAction
		totalBytes int64
	)

	for _, obj := range req.Objects {
		item := ObjectResponse{OID: obj.OID, Size: obj.Size, Authenticated: true}

		// A per-object error is how the LFS spec reports a value it will not
		// act on, so one bad entry does not fail the whole batch.
		if !ValidOID(obj.OID) || obj.Size < 0 {
			item.Error = &ObjectError{Code: 422, Message: "oid must be a sha256 hex digest and size must not be negative"}
			resp.Objects = append(resp.Objects, item)
			continue
		}

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
				err := h.link(ctx, repoID, obj.OID, obj.Size)
				if err != nil && !errors.Is(err, store.ErrLFSObjectGone) {
					return nil, err
				}
				if err == nil {
					resp.Objects = append(resp.Objects, item)
					continue
				}
			}
			// Only objects that actually get transferred count towards the
			// batch's byte total; a deduplicated hit costs no wall clock.
			pending = append(pending, pendingAction{index: len(resp.Objects), op: "upload", obj: obj})
			totalBytes += obj.Size

		case "download":
			// Membership first, and with the same answer as a genuinely
			// absent object: the caller has already proved it may read *this*
			// repository, but an oid it merely knows about must not become a
			// download URL for bytes that live in a repository it was not
			// given. Every legitimate route into this repository records
			// the link before a commit can reference the object (upload dedup
			// and Verify below, the emulator proxy upload, and LinkLFSObjects
			// from both the HF-compatible commit handler and the syncer's
			// post-push pipeline -- the latter being what covers a pointer
			// pushed as a plain blob, which never reaches this API at all).
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
			pending = append(pending, pendingAction{index: len(resp.Objects), op: "download", obj: obj})
			totalBytes += obj.Size
		}

		resp.Objects = append(resp.Objects, item)
	}

	// Nothing has been authorised yet -- the loop above only decided what it
	// would authorise -- so this is the last point at which the whole batch's
	// appetite is known and no URL has been handed out. Downloads cost the
	// namespace nothing and are never gated.
	if req.Operation == "upload" {
		var err error
		if pending, err = h.withinQuota(ctx, repoID, resp, pending); err != nil {
			return nil, err
		}
	}

	ttl := TTLFor(h.ttl, h.maxTTL, totalBytes)
	for _, p := range pending {
		switch p.op {
		case "upload":
			upload, err := h.uploadAction(ctx, repoID, p.obj, authToken, ttl)
			if err != nil {
				return nil, err
			}
			// The verify call comes after the last byte of the last object,
			// so its signature gets the batch-wide lifetime too.
			verifyExp := time.Now().Add(ttl).Unix()
			resp.Objects[p.index].Actions = map[string]Action{
				"upload": upload,
				"verify": {
					Href: fmt.Sprintf("%s/api/v1/lfs/%d/verify?op=verify&exp=%d&sig=%s",
						h.publicURL, repoID, verifyExp, h.sign("verify", repoID, "", verifyExp)),
					Header: authHeader(authToken),
				},
			}
		case "download":
			download, err := h.downloadAction(ctx, repoID, p.obj, storage.LFSKey(p.obj.OID), authToken, ttl)
			if err != nil {
				return nil, err
			}
			resp.Objects[p.index].Actions = map[string]Action{"download": download}
		}
	}
	return resp, nil
}

// pendingAction is an object the batch decided to hand transfer actions for,
// held back until the batch's total transfer size (and so the URL lifetime) is
// known.
type pendingAction struct {
	index int
	op    string
	obj   ObjectRef
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

// storedAt reports whether key holds exactly size bytes right now. It takes
// the key rather than an oid, so it can be used on a path where the ledger
// cannot answer yet -- Verify runs before the row exists.
//
// The comparison is exact, zero included. It used to be skipped for size <= 0
// ("the client did not tell us"), which handed dedup to anyone able to name an
// oid -- and oids are public, every LFS pointer in every readable repository
// is one. Dedup's whole premise is that declaring the oid *and* its size is
// evidence of holding the content; a request declaring zero asserted only the
// oid, and got the object linked to a repository the caller controls. From
// there RepoHasLFSObject answers yes and the download half of Batch, resolve
// and the transfer proxy all hand over somebody else's bytes. It is the same
// leniency promoteFrom removed on the promotion side, left behind on this one.
//
// A genuinely empty object is unaffected: it declares zero and it is zero, so
// the sizes agree like any other pair and it deduplicates normally.
func (h *Handler) storedAt(ctx context.Context, key string, size int64) (bool, error) {
	info, err := h.storage.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat lfs object: %w", err)
	}
	if info.Size != size {
		return false, nil
	}
	return true, nil
}

// uploadAction signs a write to the *staging* key, never to the
// content-addressed lfs/ key. What arrives over a signed URL is unverified by
// construction -- the bytes never pass through this process, so nothing checks
// their digest until verify -- and lfs/{oid} is immutable, shared by every
// repository on the instance and treated as authoritative by dedup. Letting a
// truncated or mislabelled transfer land there would corrupt the object for
// every repository referencing it. Verify promotes the staged bytes instead.
func (h *Handler) uploadAction(ctx context.Context, repoID int64, obj ObjectRef, authToken string, ttl time.Duration) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedPutURL(ctx, storage.LFSStagingKey(repoID, obj.OID), ttl)
		if err != nil {
			return Action{}, fmt.Errorf("sign upload url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("upload", repoID, obj.OID, ttl),
		Header:    authHeader(authToken),
		ExpiresIn: int(ttl.Seconds()),
	}, nil
}

// downloadAction signs the object's content-addressed key. It takes the key
// rather than deriving it so the caller's authorisation check and the URL it
// hands out are visibly about the same object.
func (h *Handler) downloadAction(ctx context.Context, repoID int64, obj ObjectRef, key, authToken string, ttl time.Duration) (Action, error) {
	if h.storage.SupportsSignedURL() {
		url, err := h.storage.SignedGetURL(ctx, key, ttl, "")
		if err != nil {
			return Action{}, fmt.Errorf("sign download url: %w", err)
		}
		return Action{Href: url, ExpiresIn: int(ttl.Seconds())}, nil
	}
	return Action{
		Href:      h.proxyHref("download", repoID, obj.OID, ttl),
		Header:    authHeader(authToken),
		ExpiresIn: int(ttl.Seconds()),
	}, nil
}

func authHeader(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": token}
}
