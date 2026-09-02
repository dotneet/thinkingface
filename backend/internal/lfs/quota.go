// Storage quotas, enforced where the bytes actually arrive.
//
// The batch API is the only place on this server that authorises a write into
// the bucket: a signed PUT URL (or, on the emulator, a proxy href) is minted
// here and nowhere else, and git-lfs, huggingface_hub and the web UI all pass
// through it. Checking anywhere earlier -- at commit, say -- would be
// checking after the bytes are already paid for.
//
// What is counted is the namespace's LFS footprint, the same number
// GET /api/v1/usage reports, and the check is against the batch as a whole:
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
// Known limitation, deliberate: this reads usage and compares, without
// reserving anything and without locking the namespace. Usage only moves when
// a transfer completes (verify -> promote -> link), so the window between a
// batch being allowed and its bytes being counted is as long as the transfer
// -- two concurrent pushes of 80 GiB each are both admitted under a 100 GiB
// quota. Closing it properly means a reservation ledger with expiry, since a
// batch that is never transferred must not hold its bytes for ever; that is a
// larger change than this file, and the overshoot is bounded by how much one
// client can push at once.

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
	q, err := h.quota.NamespaceQuotaForRepo(ctx, repoID, h.defaultQuota)
	if err != nil {
		return false, fmt.Errorf("read namespace storage quota: %w", err)
	}
	limit := store.EffectiveQuota(q.QuotaBytes, h.defaultQuota)
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
