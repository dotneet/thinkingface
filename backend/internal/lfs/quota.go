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
// Objects the bucket already holds are not counted, because a deduplicated
// hit transfers nothing and adds nothing to the bill -- they never reach the
// pending list this looks at.

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

// withinQuota is the gate between a batch's upload objects and the transfer
// URLs that would let them be written. It returns the actions that may still
// be minted: every one of them when the batch fits, and none at all when it
// does not -- in which case each rejected object carries the refusal instead
// (RFC 4918's 507 Insufficient Storage, in the per-object error the LFS batch
// protocol reserves for exactly this: a value the server will not act on,
// reported without failing the request itself).
//
// The refusal is all-or-nothing on purpose. Handing out URLs for the objects
// that happen to fit and refusing the rest would leave the push half
// transferred and the commit impossible, which is a worse place to be than a
// push that fails cleanly before a byte moves.
func (h *Handler) withinQuota(ctx context.Context, repoID int64, resp *BatchResponse, pending []pendingAction) ([]pendingAction, error) {
	if h.quota == nil || len(pending) == 0 {
		return pending, nil
	}
	q, err := h.quota.NamespaceQuotaForRepo(ctx, repoID, h.defaultQuota)
	if err != nil {
		return nil, fmt.Errorf("read namespace storage quota: %w", err)
	}
	limit := store.EffectiveQuota(q.QuotaBytes, h.defaultQuota)
	if limit == nil {
		return pending, nil
	}

	var add int64
	for _, p := range pending {
		add = addSaturating(add, p.obj.Size)
	}
	want := addSaturating(q.UsedBytes, add)
	if want <= *limit {
		return pending, nil
	}

	msg := quotaMessage(q.Namespace, q.UsedBytes, *limit, add, want-*limit)
	for _, p := range pending {
		resp.Objects[p.index].Error = &ObjectError{Code: http.StatusInsufficientStorage, Message: msg}
	}
	return nil, nil
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
