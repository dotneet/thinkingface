package lfs

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// fakeQuota is the store's quota surface with the answer written down in
// advance, so the enforcement can be driven without a database.
type fakeQuota struct {
	q     store.NamespaceQuota
	err   error
	calls int
}

func (f *fakeQuota) NamespaceQuotaForRepo(context.Context, int64, int64) (store.NamespaceQuota, error) {
	f.calls++
	return f.q, f.err
}

var _ QuotaSource = (*fakeQuota)(nil)

// quotaHandler is testHandler with enforcement switched on.
func quotaHandler(rec lfsRecorder, st storage.Storage, q *fakeQuota, defaultBytes int64) *Handler {
	h := testHandler(rec, st)
	h.EnforceNamespaceQuota(q, defaultBytes)
	return h
}

// Two oids that are not each other, both well-formed. The bytes behind them
// are never read here -- the quota check happens before any transfer -- so
// only their distinctness matters.
var (
	oidA = oidOf([]byte("object a"))
	oidB = oidOf([]byte("object b"))
	oidC = oidOf([]byte("object c"))
)

func uploadBatch(objs ...ObjectRef) *BatchRequest {
	return &BatchRequest{Operation: "upload", Objects: objs}
}

// The whole point of checking the batch rather than the object: three files
// that each fit in what is left would, one at a time, land three times the
// remaining allowance. git-lfs pushes exactly like that.
func TestBatchRefusesAnUploadWhoseTotalExceedsTheQuota(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{} // nothing is in the bucket, so every object transfers
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(100), UsedBytes: 40,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: 30},
		ObjectRef{OID: oidB, Size: 30},
		ObjectRef{OID: oidC, Size: 30},
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if len(resp.Objects) != 3 {
		t.Fatalf("Batch returned %d objects, want 3", len(resp.Objects))
	}
	for i, obj := range resp.Objects {
		if obj.Actions != nil {
			t.Errorf("object %d was handed transfer actions despite the quota", i)
		}
		if obj.Error == nil {
			t.Fatalf("object %d carries no error, want the quota refusal", i)
		}
		if obj.Error.Code != http.StatusInsufficientStorage {
			t.Errorf("object %d error code = %d, want %d", i, obj.Error.Code, http.StatusInsufficientStorage)
		}
	}
	// The message has to answer "whose quota, how much of it is gone, and how
	// much do I have to free" without a second trip to the usage page.
	msg := resp.Objects[0].Error.Message
	for _, want := range []string{`"acme"`, "40", "100", "90", "30"} {
		if !strings.Contains(msg, want) {
			t.Errorf("quota message %q does not mention %q", msg, want)
		}
	}
}

// Each of those objects on its own is inside the allowance -- the refusal
// above is not just "the quota is tiny".
func TestBatchAcceptsAnUploadThatFitsTheQuota(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(100), UsedBytes: 40,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: 30},
		ObjectRef{OID: oidB, Size: 30},
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	for i, obj := range resp.Objects {
		if obj.Error != nil {
			t.Fatalf("object %d refused (%d %s), want it accepted", i, obj.Error.Code, obj.Error.Message)
		}
		if obj.Actions["upload"].Href == "" {
			t.Errorf("object %d got no upload action", i)
		}
	}
	// Exactly at the limit is inside it: 40 + 60 = 100.
	// One read for one batch whatever its size. What must not grow with the
	// batch is the count, not the constant.
	if q.calls != 1 {
		t.Errorf("the quota was read %d times for one batch, want 1", q.calls)
	}
}

// An object this repository is *already linked to* costs nothing: re-pushing
// content the namespace is already charged for adds no bytes to the number
// UsageByRepo produces, so a repository at its limit must still be able to
// push what it already holds.
func TestBatchDoesNotCountObjectsTheRepositoryAlreadyHolds(t *testing.T) {
	rec := &fakeRecorder{owned: ownedBy(1, oidA)}
	// The presence check for oidA hits; everything after that misses, so oidB
	// really does have to be uploaded.
	st := &stubStorage{presentFor: 1, size: 10}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(55), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: 10}, // already linked to this repository
		ObjectRef{OID: oidB, Size: 50}, // has to be transferred
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	// 50 of the 55 are needed. Had the already-held 10 been counted too, the
	// batch would be 5 bytes over and both objects refused.
	for i, obj := range resp.Objects {
		if obj.Error != nil {
			t.Fatalf("object %d refused (%d %s), want it accepted", i, obj.Error.Code, obj.Error.Message)
		}
	}
	if resp.Objects[0].Actions != nil {
		t.Errorf("the object the repository already holds was handed transfer actions")
	}
	if resp.Objects[1].Actions["upload"].Href == "" {
		t.Errorf("the new object got no upload action")
	}
}

// A deduplicated hit on an object this repository does *not* hold is a new
// link, and a new link is new usage: it is exactly what UsageByRepo sums, so
// it has to pass the same gate a transfer does.
//
// The hole this closes: an oid is public (every LFS pointer in every readable
// repository is one), the old code linked such an object and `continue`d
// before the pending list existed, and withinQuota returned early on an empty
// pending list -- so a namespace hundreds of gigabytes past its quota could
// keep adding content by naming oids it had merely read somewhere, without the
// quota being read even once.
func TestBatchChargesDedupHitsTheRepositoryDoesNotHold(t *testing.T) {
	rec := &fakeRecorder{}
	// Both objects are already in the bucket, and neither is linked here.
	st := &stubStorage{presentFor: 8, size: 10}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(15), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: 10},
		ObjectRef{OID: oidB, Size: 10},
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if q.calls != 1 {
		t.Fatalf("the quota was consulted %d times, want 1 -- dedup must not skip the gate", q.calls)
	}
	for i, obj := range resp.Objects {
		if obj.Error == nil || obj.Error.Code != http.StatusInsufficientStorage {
			t.Errorf("object %d error = %+v, want a 507", i, obj.Error)
		}
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0 -- nothing may be linked once the batch is refused", rec.calls)
	}
	if len(rec.owned) != 0 {
		t.Errorf("owned = %v, want nothing linked", rec.owned)
	}
}

// The other half of the same rule: when the dedup hits do fit, they are linked
// and no bytes are asked for.
func TestBatchLinksDedupHitsThatFitTheQuota(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 8, size: 10}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(100), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: 10},
		ObjectRef{OID: oidB, Size: 10},
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	for i, obj := range resp.Objects {
		if obj.Error != nil {
			t.Fatalf("object %d refused (%d %s), want it accepted", i, obj.Error.Code, obj.Error.Message)
		}
		if obj.Actions != nil {
			t.Errorf("object %d was handed transfer actions for content already in the bucket", i)
		}
	}
	if rec.calls != 2 {
		t.Errorf("RecordLFSObject calls = %d, want both dedup hits linked", rec.calls)
	}
}

// Zero is a quota, and it is the one an override exists to express: a
// namespace that may hold repositories but must not upload a byte. It also
// has to beat a permissive instance default, which is what would happen if
// anything treated the stored zero as "unset".
func TestBatchTreatsAZeroOverrideAsARealQuota(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(0), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 1<<40)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(ObjectRef{OID: oidA, Size: 1}), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error == nil || resp.Objects[0].Error.Code != http.StatusInsufficientStorage {
		t.Fatalf("object = %+v, want a 507 refusal", resp.Objects[0])
	}
}

// The mirror image: no override at all with the instance default unset is
// unlimited, so an instance that never configures quotas refuses nothing.
func TestBatchWithNoOverrideAndNoDefaultIsUnlimited(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{q: store.NamespaceQuota{Namespace: "acme", UsedBytes: 1 << 50}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(ObjectRef{OID: oidA, Size: 1 << 40}), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error != nil {
		t.Fatalf("object refused (%+v), want no quota at all", resp.Objects[0].Error)
	}
	if resp.Objects[0].Actions["upload"].Href == "" {
		t.Error("no upload action was issued")
	}
}

// A namespace with no override of its own is held to the instance default.
func TestBatchAppliesTheInstanceDefaultWhenThereIsNoOverride(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{q: store.NamespaceQuota{Namespace: "acme", UsedBytes: 90}}
	h := quotaHandler(rec, st, q, 100)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(ObjectRef{OID: oidA, Size: 11}), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error == nil || resp.Objects[0].Error.Code != http.StatusInsufficientStorage {
		t.Fatalf("object = %+v, want a 507 refusal against the instance default", resp.Objects[0])
	}
	if !strings.Contains(resp.Objects[0].Error.Message, "100") {
		t.Errorf("message %q does not state the effective limit", resp.Objects[0].Error.Message)
	}
}

// Sizes come from the client. A batch declaring a couple of objects near
// math.MaxInt64 must not wrap around into a negative total that compares
// below every quota -- that would make the check its own bypass.
func TestBatchQuotaDoesNotOverflowOnAbsurdSizes(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(1 << 30), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(
		ObjectRef{OID: oidA, Size: math.MaxInt64},
		ObjectRef{OID: oidB, Size: math.MaxInt64},
	), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	for i, obj := range resp.Objects {
		if obj.Error == nil || obj.Error.Code != http.StatusInsufficientStorage {
			t.Fatalf("object %d = %+v, want a 507 refusal", i, obj)
		}
	}
}

// Reading a repository costs the namespace nothing, so a namespace sitting
// over its quota can still be cloned -- and the check never runs on that
// path.
func TestBatchDownloadIsNotGatedByTheQuota(t *testing.T) {
	rec := &fakeRecorder{owned: ownedBy(1, oidA)}
	st := &stubStorage{presentFor: 1, size: 10}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(0), UsedBytes: 5000,
	}}
	h := quotaHandler(rec, st, q, 0)

	resp, err := h.Batch(context.Background(), 1, &BatchRequest{
		Operation: "download",
		Objects:   []ObjectRef{{OID: oidA, Size: 10}},
	}, "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error != nil {
		t.Fatalf("download refused (%+v), want it served", resp.Objects[0].Error)
	}
	if q.calls != 0 {
		t.Errorf("the quota was read %d times on a download, want 0", q.calls)
	}
}

// A handler that was never given a quota source asks nothing and refuses
// nothing: enforcement being off is a state, not a quota of infinity that
// still costs a query per push.
func TestBatchWithoutAQuotaSourceEnforcesNothing(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	h := testHandler(rec, st)

	resp, err := h.Batch(context.Background(), 1, uploadBatch(ObjectRef{OID: oidA, Size: 1 << 40}), "")
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.Objects[0].Error != nil {
		t.Fatalf("object refused (%+v), want no enforcement at all", resp.Objects[0].Error)
	}
}

// A quota that cannot be read is not a quota of zero and not a quota of
// infinity: the batch fails rather than guessing in either direction.
func TestBatchFailsWhenTheQuotaCannotBeRead(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &fakeQuota{err: errors.New("database is down")}
	h := quotaHandler(rec, st, q, 0)

	if _, err := h.Batch(context.Background(), 1, uploadBatch(ObjectRef{OID: oidA, Size: 1}), ""); err == nil {
		t.Fatal("Batch succeeded with an unreadable quota, want an error")
	}
}

func ptrInt64(v int64) *int64 { return &v }

// ------------------------------------------------- the promotion-side check

// The finding this closes: withinQuota was reached only from Batch, while
// PromoteStagedFrom is reached from the browser upload endpoint and the
// emulator's transfer proxy without passing through Batch at all. A namespace
// at its limit took a 507 on `git push` and then uploaded the same weights
// through the web UI.
func TestPromoteRefusesAnObjectTheNamespaceHasNoRoomFor(t *testing.T) {
	rec := &fakeRecorder{}
	// The staged object is present and is the length the caller declares, so
	// nothing but the quota can refuse this.
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(1), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, "tmp/uploads/1/abc")
	var overQuota *QuotaExceededError
	if !errors.As(err, &overQuota) {
		t.Fatalf("PromoteStagedFrom error = %v, want a QuotaExceededError", err)
	}
	if !strings.Contains(overQuota.Error(), `"acme"`) {
		t.Errorf("message %q does not name the namespace", overQuota.Error())
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0 -- a refused upload must not be linked", rec.calls)
	}
}

// The same gate on the signed-URL path. Verify passes the refusal through
// rather than folding it into "object could not be verified", so `git push`
// reports something the operator can act on.
func TestVerifyRefusesAnObjectTheNamespaceHasNoRoomFor(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(1), UsedBytes: 0,
	}}
	h := quotaHandler(rec, st, q, 0)

	err := h.Verify(context.Background(), 1, goodOID, goodSize)
	var overQuota *QuotaExceededError
	if !errors.As(err, &overQuota) {
		t.Fatalf("Verify error = %v, want a QuotaExceededError", err)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0", rec.calls)
	}
}

// Re-publishing an object this repository is already linked to adds nothing to
// what UsageByRepo sums, so it stays possible at any quota -- the same rule
// Batch applies to a dedup hit. Without it a repository at its limit could not
// re-upload a file it already holds.
func TestPromoteDoesNotChargeAnObjectTheRepositoryAlreadyHolds(t *testing.T) {
	rec := &fakeRecorder{owned: ownedBy(1, goodOID)}
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	q := &fakeQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(0), UsedBytes: 1 << 40,
	}}
	h := quotaHandler(rec, st, q, 0)

	if err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, "tmp/uploads/1/abc"); err != nil {
		t.Fatalf("PromoteStagedFrom: %v", err)
	}
}

// An unlimited namespace pays for the quota read and nothing else: no
// ownership lookup, since there is no ceiling for it to inform.
func TestPromoteSkipsTheOwnershipLookupWhenNothingIsEnforced(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	q := &fakeQuota{q: store.NamespaceQuota{Namespace: "acme", UsedBytes: 1 << 40}}
	h := quotaHandler(rec, st, q, 0)

	if err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, "tmp/uploads/1/abc"); err != nil {
		t.Fatalf("PromoteStagedFrom: %v", err)
	}
	// RepoHasLFSObject is called once by promoteAlreadyDone's sibling paths
	// only; here the object is staged, so the charge is the only thing that
	// would have asked -- and it did not.
	if rec.ownCalls != 0 {
		t.Errorf("RepoHasLFSObject calls = %d, want 0 for a namespace with no limit", rec.ownCalls)
	}
}

// Enforcement switched off asks the database nothing at all on this path
// either, which is the state an instance that configures no quotas is in.
func TestPromoteWithoutAQuotaSourceEnforcesNothing(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	h := testHandler(rec, st)

	if err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, "tmp/uploads/1/abc"); err != nil {
		t.Fatalf("PromoteStagedFrom: %v", err)
	}
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want the object linked", rec.calls)
	}
}
