// Storage quotas, enforced where the bytes are actually charged.
//
// What a namespace is charged for is its rows in repo_lfs_objects -- the links
// store.UsageByRepo sums -- so the gate belongs wherever a link is about to be
// written. This package writes them in two places, and both are gated:
//
//   - Batch, which decides a whole push before it mints a single transfer URL.
//     It is the only check that can refuse before a byte moves, which is why
//     it exists: telling a client its 80 GiB is over the limit after the
//     transfer is a far worse answer than telling it before.
//   - promotion (promoteFrom), which is what *every* upload path runs to
//     publish an object and link it: the signed-URL verify, the emulator's
//     transfer proxy (PUT /api/v1/lfs/{repoID}/{oid}) and the browser's
//     multipart upload endpoint (POST /api/v1/upload/...).
//
// The batch check used to be the only one, on the stated grounds that the
// batch API was the only place on this server authorising a write into the
// bucket. That was false: the latter two routes call PromoteStagedFrom
// directly and never pass through Batch, so a namespace pinned at its quota
// took a 507 on `git push` and then uploaded the same weights through the web
// UI's dialog -- 64 files and up to 10 GiB per request -- with usage growing
// without limit and every later batch refused for the legitimate pusher. The
// promotion check is what makes the invariant above true for all three paths
// rather than for the polite one.
//
// One writer of links used to be outside this package and outside this gate:
// store.LinkLFSObjects, as called by the syncer's post-push pipeline. It links
// *new* oids for pointer files pushed as ordinary blobs by a client that never
// spoke the LFS protocol, and nothing on that path consulted a quota. That
// hole is now closed where the link is written: the syncer consults the same
// allowance through filterLFSByQuota (syncer/quota.go) before it links, and a
// refused object is left unlinked -- the same unresolvable state a pointer for
// content nobody uploaded leaves, which is what already happens to a pointer
// naming an oid nobody uploaded.
//
// The HF-compatible commit handler's call needs no such check. Every oid it
// passes came through verifyCommitLFSFile (api/commitbody.go), which refuses
// an lfsFile op whose oid this repository is not already linked to -- so the
// call can only re-link what a gated path already charged for, which is what
// stamps committed_at. It adds no row UsageByRepo was not already summing.
//
// What is counted is the namespace's LFS footprint, the same number
// GET /api/v1/usage reports, and Batch checks it against the batch as a whole:
// one object at a time would let a push of a hundred files land a hundred
// times the remaining allowance, since every one of them fits on its own.
//
// A deduplicated hit is counted too, whenever it is about to earn this
// repository a link it does not already have. It moves no bytes, but usage is
// summed over repo_lfs_objects (store.UsageByRepo), so the link is exactly
// what the number is made of -- and an oid is public, every LFS pointer in
// every readable repository is one. Exempting dedup therefore did not exempt
// "content the namespace already pays for", it exempted "content anybody on
// the instance ever uploaded": a batch naming three existing oids added three
// gigabytes to a namespace 500 GiB past a 1 MiB quota without the check
// reading a single row. What genuinely costs nothing, and is genuinely not
// counted, is an object this repository is already linked to.
//
// Known limitation, deliberate: this is check-then-act without reserving
// anything. Usage only moves when a link is written, so two pushes racing
// each other can both read the same usage and both be admitted. The promotion
// check narrows what that costs -- each object is measured against the usage
// its predecessors have already added, so a batch admitted as a whole no
// longer lands as a whole once the namespace has filled up in between -- but
// it does not close it: concurrent transfers still overshoot by whatever is
// in flight at once, and a second replica shares nothing in-process at all.
// Closing it properly means a reservation ledger with expiry, since a batch
// that is never transferred must not hold its bytes for ever; that is a larger
// change than this file.

package lfs

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// QuotaSource is the store surface the quota check needs. *store.Store
// implements it; tests substitute a fake.
type QuotaSource interface {
	// NamespaceQuotaForRepo answers what the namespace owning this
	// repository may store, and what it is already storing. defaultBytes is
	// passed in so the store can skip aggregating usage entirely when the
	// namespace turns out to have no effective limit -- the common case on
	// an instance that configures no quotas, and one that would otherwise
	// scan the namespace on every single push.
	NamespaceQuotaForRepo(ctx context.Context, repoID int64, defaultBytes int64) (store.NamespaceQuota, error)
}

var _ QuotaSource = (*store.Store)(nil)

// EnforceNamespaceQuota switches quota enforcement on for this handler.
// defaultBytes is the instance-wide allowance
// (TF_DEFAULT_STORAGE_QUOTA_BYTES) applied to namespaces carrying no override
// of their own; zero means unlimited, so a server that never calls this, and
// one that calls it with the default unset, both behave exactly as before.
//
// It is a separate call rather than another argument to New because that is
// what makes "quotas are off" a state the type can be in: a handler with no
// source never asks the database anything on the upload path.
func (h *Handler) EnforceNamespaceQuota(src QuotaSource, defaultBytes int64) {
	h.quota = src
	h.defaultQuota = defaultBytes
}

// withinQuota is the gate between a batch's upload objects and everything
// that would add to the namespace's footprint: the transfer URLs for objects
// that must be uploaded (transfer), and the links for objects the bucket
// already holds (dedup). It reports whether the batch as a whole fits. When
// it does not, nothing at all is authorised and each charged object carries
// the refusal instead (RFC 4918's 507 Insufficient Storage, in the per-object
// error the LFS batch protocol reserves for exactly this: a value the server
// will not act on, reported without failing the request itself).
//
// The refusal is all-or-nothing on purpose. Handing out URLs for the objects
// that happen to fit and refusing the rest would leave the push half
// transferred and the commit impossible, which is a worse place to be than a
// push that fails cleanly before a byte moves.
func (h *Handler) withinQuota(ctx context.Context, repoID int64, resp *BatchResponse, transfer, dedup []pendingAction) (bool, error) {
	if h.quota == nil || len(transfer)+len(dedup) == 0 {
		return true, nil
	}
	q, limit, err := h.effectiveQuota(ctx, repoID)
	if err != nil {
		return false, err
	}
	if limit == nil {
		return true, nil
	}

	var add int64
	for _, p := range transfer {
		add = addSaturating(add, p.obj.Size)
	}
	for _, p := range dedup {
		add = addSaturating(add, p.obj.Size)
	}
	want := addSaturating(q.UsedBytes, add)
	if want <= *limit {
		return true, nil
	}

	msg := quotaMessage(q.Namespace, q.UsedBytes, *limit, add, want-*limit)
	for _, p := range transfer {
		resp.Objects[p.index].Error = &ObjectError{Code: http.StatusInsufficientStorage, Message: msg}
	}
	for _, p := range dedup {
		resp.Objects[p.index].Error = &ObjectError{Code: http.StatusInsufficientStorage, Message: msg}
	}
	return false, nil
}

// effectiveQuota reads the allowance for the namespace owning repoID. A nil
// limit means nothing is enforced -- enforcement switched off, or a namespace
// with no ceiling once the instance default is folded in -- and it is the
// common case, so it is answered before any caller does further work on the
// strength of a limit that does not exist.
func (h *Handler) effectiveQuota(ctx context.Context, repoID int64) (store.NamespaceQuota, *int64, error) {
	if h.quota == nil {
		return store.NamespaceQuota{}, nil, nil
	}
	q, err := h.quota.NamespaceQuotaForRepo(ctx, repoID, h.defaultQuota)
	if err != nil {
		return store.NamespaceQuota{}, nil, fmt.Errorf("read namespace storage quota: %w", err)
	}
	return q, store.EffectiveQuota(q.QuotaBytes, h.defaultQuota), nil
}

// QuotaExceededError reports a write refused because the namespace has no room
// for it. It is a distinct type rather than a plain error because the three
// upload handlers have to answer it with 507 Insufficient Storage and its
// sentence verbatim -- everything else promotion can fail with is either the
// client's own bytes being wrong (400) or the server's business (500).
type QuotaExceededError struct {
	Namespace string
	Message   string
}

func (e *QuotaExceededError) Error() string { return e.Message }

// chargeQuota gates the one link a promotion is about to write. It is the
// promotion-side half of the rule stated at the top of this file, and it runs
// on every path -- including the batch/verify one, where it is a second look
// at a decision Batch already made. That repetition is deliberate: making the
// check a property of "a link is being written" rather than of "the client
// asked politely via Batch" is the only shape that cannot be routed around,
// and the extra cost is one row read per object on an instance that enforces
// quotas at all.
//
// An object this repository is already linked to is free, exactly as it is in
// Batch: relinking adds nothing to what UsageByRepo sums. The ownership lookup
// is deliberately after the limit read, so a namespace with no ceiling -- the
// common case -- pays for neither.
func (h *Handler) chargeQuota(ctx context.Context, repoID int64, oid string, size int64) error {
	q, limit, err := h.effectiveQuota(ctx, repoID)
	if err != nil {
		return err
	}
	if limit == nil {
		return nil
	}
	linked, err := h.store.RepoHasLFSObject(ctx, repoID, oid)
	if err != nil {
		return fmt.Errorf("check lfs object ownership: %w", err)
	}
	if linked {
		return nil
	}
	want := addSaturating(q.UsedBytes, size)
	if want <= *limit {
		return nil
	}
	return &QuotaExceededError{
		Namespace: q.Namespace,
		Message:   quotaMessage(q.Namespace, q.UsedBytes, *limit, size, want-*limit),
	}
}

// quotaMessage is what the person running `git push` reads. It names the
// namespace (theirs is rarely the only one they can write to), both sides of
// the ratio, and the shortfall -- so the answer to "how much do I have to
// delete" is in the message rather than in a second trip to the usage page.
func quotaMessage(namespace string, used, limit, add, short int64) string {
	return fmt.Sprintf(
		"storage quota exceeded for namespace %q: %d of %d bytes used, "+
			"and this upload of %d bytes is %d bytes over the limit",
		namespace, used, limit, add, short)
}

// addSaturating adds two non-negative byte counts without wrapping. Sizes
// come from the client, and a batch declaring a few objects of nearly
// math.MaxInt64 would otherwise sum to a negative number that compares below
// every quota -- turning the check into its own bypass.
func addSaturating(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
