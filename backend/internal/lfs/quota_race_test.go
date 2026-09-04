package lfs

import (
	"context"
	"errors"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// shiftingQuota answers a different usage on each call: the first read sees
// room left, and every read after it sees a namespace that has since filled
// up. It is what two promotions racing each other look like to the second one.
type shiftingQuota struct {
	namespace string
	limit     int64
	uses      []int64
	calls     int
}

func (s *shiftingQuota) NamespaceQuotaForRepo(context.Context, int64, int64) (store.NamespaceQuota, error) {
	s.calls++
	used := s.uses[len(s.uses)-1]
	if s.calls <= len(s.uses) {
		used = s.uses[s.calls-1]
	}
	return store.NamespaceQuota{Namespace: s.namespace, QuotaBytes: &s.limit, UsedBytes: used}, nil
}

var _ QuotaSource = (*shiftingQuota)(nil)

// The promotion-side decision must rest on a fresh reading, not on the usage
// as it was when the check started: a namespace that filled up in between
// still refuses the object.
func TestChargeQuotaDecidesOnAFreshReading(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{presentFor: 4, size: goodSize, content: goodBody}
	q := &shiftingQuota{namespace: "acme", limit: 100, uses: []int64{0, 100}}
	h := testHandler(rec, st)
	h.EnforceNamespaceQuota(q, 0)

	err := h.PromoteStagedFrom(context.Background(), 1, goodOID, goodSize, "tmp/uploads/1/abc")
	var overQuota *QuotaExceededError
	if !errors.As(err, &overQuota) {
		t.Fatalf("PromoteStagedFrom error = %v, want a QuotaExceededError decided on the second reading", err)
	}
	if q.calls != 2 {
		t.Errorf("quota reads = %d, want 2 (one to learn the namespace, one under the stripe)", q.calls)
	}
	if rec.calls != 0 {
		t.Errorf("RecordLFSObject calls = %d, want 0 -- a refused upload must not be linked", rec.calls)
	}
}

// The same re-read on the batch side: the whole-batch decision is taken while
// holding the namespace's stripe, against the usage as it is then.
func TestWithinQuotaDecidesOnAFreshReading(t *testing.T) {
	rec := &fakeRecorder{}
	st := &stubStorage{}
	q := &shiftingQuota{namespace: "acme", limit: 100, uses: []int64{0, 100}}
	h := testHandler(rec, st)
	h.EnforceNamespaceQuota(q, 0)

	resp := &BatchResponse{Objects: []ObjectResponse{{OID: oidA, Size: 60}}}
	ok, err := h.withinQuota(context.Background(), 1, resp,
		[]pendingAction{{index: 0, op: "upload", obj: ObjectRef{OID: oidA, Size: 60}}}, nil)
	if err != nil {
		t.Fatalf("withinQuota: %v", err)
	}
	if ok {
		t.Fatal("withinQuota admitted a batch the fresh reading has no room for")
	}
	if resp.Objects[0].Error == nil {
		t.Fatal("the refused object carries no per-object error")
	}
	if q.calls != 2 {
		t.Errorf("quota reads = %d, want 2 (one to learn the namespace, one under the stripe)", q.calls)
	}
}

// Stripes are stable: one namespace always lands on the same stripe, so two
// decisions for it genuinely serialise rather than hashing apart.
func TestQuotaStripeIsStablePerNamespace(t *testing.T) {
	//nolint:staticcheck // intentional self-comparison: stability means same input, same stripe
	if quotaStripe("acme") != quotaStripe("acme") {
		t.Fatal("quotaStripe returned different stripes for one namespace")
	}
}
