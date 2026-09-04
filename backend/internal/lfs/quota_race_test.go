package lfs

import (
	"context"
	"sync"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// concurrentQuota is a thread-safe QuotaSource for the concurrency test
// below: the quota gate itself holds no lock (see the "Known limitation"
// note in quota.go), so the fake has to be safe to read from many
// goroutines at once.
type concurrentQuota struct {
	mu    sync.Mutex
	q     store.NamespaceQuota
	calls int
}

func (c *concurrentQuota) NamespaceQuotaForRepo(context.Context, int64, int64) (store.NamespaceQuota, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.q, nil
}

var _ QuotaSource = (*concurrentQuota)(nil)

// The quota gate is check-then-act without a reservation ledger (see the
// "Known limitation" note in quota.go): concurrent decisions may all rest on
// the same usage and all be admitted. This test asserts only what is
// documented -- that concurrent decisions complete without crashing and each
// costs one quota read -- never that they serialise.
func TestWithinQuotaConcurrentDecisionsDoNotCrash(t *testing.T) {
	q := &concurrentQuota{q: store.NamespaceQuota{
		Namespace: "acme", QuotaBytes: ptrInt64(100), UsedBytes: 0,
	}}
	h := testHandler(&fakeRecorder{}, &stubStorage{})
	h.EnforceNamespaceQuota(q, 0)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	oks := make([]bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := &BatchResponse{Objects: []ObjectResponse{{OID: oidA, Size: 60}}}
			ok, err := h.withinQuota(context.Background(), 1, resp,
				[]pendingAction{{index: 0, op: "upload", obj: ObjectRef{OID: oidA, Size: 60}}}, nil)
			oks[i] = ok
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d withinQuota: %v", i, errs[i])
		}
	}
	q.mu.Lock()
	calls := q.calls
	q.mu.Unlock()
	if calls != workers {
		t.Errorf("quota reads = %d, want %d (one per decision, no re-read)", calls, workers)
	}
}
