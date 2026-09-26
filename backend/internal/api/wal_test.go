package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// memStore is a minimal in-memory storage.Storage with real generation
// semantics, plus switches that fail the write paths — enough to drive
// commitThroughWAL's failure branches without an emulator.
type memStore struct {
	mu      sync.Mutex
	objects map[string]memObj
	nextGen int64

	failPut bool // Put and PutIfGeneration return errPutDown
}

type memObj struct {
	data []byte
	gen  int64
}

var errPutDown = errors.New("memStore: writes disabled")

func newMemStore() *memStore { return &memStore{objects: map[string]memObj{}, nextGen: 10} }

func (m *memStore) SupportsSignedURL() bool { return false }
func (m *memStore) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("unused")
}
func (m *memStore) SignedPutURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unused")
}

func (m *memStore) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	if m.failPut {
		return errPutDown
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextGen++
	m.objects[key] = memObj{data: data, gen: m.nextGen}
	return nil
}

func (m *memStore) PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error) {
	if m.failPut {
		return 0, errPutDown
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, exists := m.objects[key]
	switch {
	case generation == 0 && exists,
		generation != 0 && !exists,
		generation != 0 && cur.gen != generation:
		return 0, storage.ErrPreconditionFailed
	}
	m.nextGen++
	m.objects[key] = memObj{data: data, gen: m.nextGen}
	return m.nextGen, nil
}

func (m *memStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := m.GetWithGeneration(ctx, key)
	return rc, err
}

func (m *memStore) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, 0, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(obj.data)), obj.gen, nil
}

func (m *memStore) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	rc, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(rc)
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	end := int64(len(data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func (m *memStore) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(obj.data)), Generation: obj.gen, Updated: time.Now()}, nil
}

func (m *memStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	rc, err := m.Get(ctx, srcKey)
	if err != nil {
		return err
	}
	defer rc.Close()
	return m.Put(ctx, dstKey, rc, "")
}

func (m *memStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStore) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []storage.ObjectInfo
	for k, obj := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.ObjectInfo{Key: k, Size: int64(len(obj.data)), Generation: obj.gen})
		}
	}
	return out, nil
}

func (m *memStore) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*memStore)(nil)

// ---------------------------------------------------------------- fixtures

// newWALCommitFixture builds a Server wired with a real git manager and the
// in-memory store. The manager's own WAL integration (Open→EnsureLocal) is
// deliberately left off: these tests isolate commitThroughWAL's CAS/rollback
// logic; materialisation has its own suite in internal/wal.
func newWALCommitFixture(t *testing.T, mode string) (*Server, *memStore, *store.Repo, *gitrepo.Manager) {
	t.Helper()
	root := t.TempDir()
	git := gitrepo.NewManager(root)
	st := newMemStore()
	s := &Server{
		git:     git,
		storage: st,
		cfg:     &config.Config{WALMode: mode},
	}
	repo := &store.Repo{ID: 1, Kind: "dataset", Namespace: "acme", Name: "widgets", StoragePath: "datasets/acme/widgets"}
	if err := git.Init(repo.StoragePath, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s, st, repo, git
}

func commitOps(content string) gitrepo.CommitRequest {
	return gitrepo.CommitRequest{
		Branch:  "main",
		Message: "test " + content,
		Author:  gitrepo.Signature{Name: "t", Email: "t@example.com", When: time.Unix(1700000000, 0)},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "file.txt", Data: []byte(content)}},
	}
}

func headOf(t *testing.T, git *gitrepo.Manager, repo *store.Repo) string {
	t.Helper()
	r, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return r.HeadSHA()
}

// ---------------------------------------------------------------- tests

func TestCommitThroughWAL_AuthoritativeSuccessAdvancesRefAndIndex(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	newHash, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("commitThroughWAL: %v", err)
	}
	if got := headOf(t, git, repo); got != newHash.String() {
		t.Fatalf("head = %s, want %s", got, newHash)
	}
	ix, gen, err := wal.ReadIndex(context.Background(), st, repo.StoragePath)
	if err != nil || gen == 0 {
		t.Fatalf("index missing after commit: gen=%d err=%v", gen, err)
	}
	if ix.Refs["refs/heads/main"] != newHash.String() {
		t.Fatalf("index main = %s, want %s", ix.Refs["refs/heads/main"], newHash)
	}
}

// H-1 regression: a WAL outage must not leave the local ref pointing at a
// commit the WAL never accepted — that ghost would be served to readers and
// would make every later commit fail as stale forever.
func TestCommitThroughWAL_UnreachableWALRollsBackLocalRef(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	first, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	st.failPut = true
	_, _, err = s.commitThroughWAL(context.Background(), repo, commitOps("two"), true)
	if err == nil {
		t.Fatal("commit with WAL down must fail")
	}
	if errors.Is(err, errWALConflict) {
		t.Fatalf("outage must not masquerade as a conflict: %v", err)
	}
	if got := headOf(t, git, repo); got != first.String() {
		t.Fatalf("local head = %s, want rolled back to %s", got, first)
	}

	// And the branch must recover once the WAL is back (the pre-fix behaviour
	// left it permanently rejecting commits as stale).
	st.failPut = false
	again, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("three"), true)
	if err != nil {
		t.Fatalf("commit after recovery: %v", err)
	}
	if got := headOf(t, git, repo); got != again.String() {
		t.Fatalf("head after recovery = %s, want %s", got, again)
	}
}

func TestCommitThroughWAL_UnreachableWALOnFirstCommitDeletesUnbornRef(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	st.failPut = true
	_, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err == nil {
		t.Fatal("commit with WAL down must fail")
	}
	r, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !r.IsEmpty() {
		t.Fatalf("repository must be back to unborn after rollback, head=%s", r.HeadSHA())
	}
}

// H-4 regression: a caller that already validated its own optimistic lock
// (the edit endpoint's base_oid) must get a conflict, not a silent rebuild
// on top of the concurrently moved head.
func TestCommitThroughWAL_StaleWithoutRetryIsConflictAndRollsBack(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	first, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Another instance moves the branch in the WAL behind our back.
	ix, gen, err := wal.ReadIndex(context.Background(), st, repo.StoragePath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	ix.Refs["refs/heads/main"] = strings.Repeat("a", 40)
	if _, err := wal.PutIndex(context.Background(), st, repo.StoragePath, gen, ix); err != nil {
		t.Fatalf("move index: %v", err)
	}

	_, _, err = s.commitThroughWAL(context.Background(), repo, commitOps("two"), false)
	if !errors.Is(err, errWALConflict) {
		t.Fatalf("err = %v, want errWALConflict", err)
	}
	if got := headOf(t, git, repo); got != first.String() {
		t.Fatalf("local head = %s, want rolled back to %s", got, first)
	}
}

// Two commits chained on one branch before either reached the WAL, the first
// of which hits an outage: A advances main X->A, B advances A->B, A's write
// fails for a non-stale reason (its rollback leaves main alone, since B moved
// it), and B's write is then stale because the index still says X. The steps
// below are commitThroughWAL's, one call each, in that interleaving.
//
// B's rollback used to land on its parent, A -- a commit the WAL never
// accepted -- while the index generation stayed put, so every later
// EnsureLocal was a cache hit and every later commit on main was rejected as
// stale. It must land on X, and the branch must take the next commit.
func TestCommitThroughWAL_ChainedCommitsDoNotWedgeTheBranch(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	git.EnableWAL(st, 1<<30)
	ctx := context.Background()

	x, _, err := s.commitThroughWAL(ctx, repo, commitOps("x"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	a, aParent, err := gitRepo.Commit(commitOps("a"))
	if err != nil {
		t.Fatalf("commit A: %v", err)
	}
	b, bParent, err := gitRepo.Commit(commitOps("b"))
	if err != nil {
		t.Fatalf("commit B: %v", err)
	}
	dir := git.Dir(repo.StoragePath)
	push := func(newHash, oldHash plumbing.Hash) error {
		return wal.AuthoritativePush(ctx, st, dir, repo.StoragePath, []wal.RefUpdate{{
			Ref: "refs/heads/main", Old: oldHash.String(), New: newHash.String(),
		}})
	}

	st.failPut = true
	if err := push(a, aParent); err == nil || errors.Is(err, wal.ErrStaleRef) {
		t.Fatalf("A's write = %v, want a non-stale failure", err)
	}
	st.failPut = false
	if err := gitRepo.ResetBranch("main", a, aParent); err != nil {
		t.Fatalf("roll back A: %v", err)
	}
	if err := push(b, bParent); !errors.Is(err, wal.ErrStaleRef) {
		t.Fatalf("B's write = %v, want ErrStaleRef (the index still says X)", err)
	}
	if err := gitRepo.ResetBranch("main", b, bParent); err != nil {
		t.Fatalf("roll back B: %v", err)
	}
	if got := headOf(t, git, repo); got != x.String() {
		t.Fatalf("local head = %s, want the WAL's %s (A, %s, was never accepted)", got, x, a)
	}

	// retryOnStale=false: a wedged branch fails this on the first stale.
	c, _, err := s.commitThroughWAL(ctx, repo, commitOps("c"), false)
	if err != nil {
		t.Fatalf("commit after the rollbacks: %v (the branch is wedged)", err)
	}
	ix, _, err := wal.ReadIndex(ctx, st, repo.StoragePath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if ix.Refs["refs/heads/main"] != c.String() {
		t.Fatalf("index main = %s, want %s", ix.Refs["refs/heads/main"], c)
	}
}

func TestCommitThroughWAL_ShadowFailureStillCommits(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "shadow")
	st.failPut = true
	newHash, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("shadow mode must never surface WAL failures: %v", err)
	}
	if got := headOf(t, git, repo); got != newHash.String() {
		t.Fatalf("head = %s, want %s", got, newHash)
	}
}

func TestCommitThroughWAL_OffModeNeverTouchesStorage(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "off")
	st.failPut = true // would fail loudly if anything wrote
	newHash, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("off mode: %v", err)
	}
	if got := headOf(t, git, repo); got != newHash.String() {
		t.Fatalf("head = %s, want %s", got, newHash)
	}
	if len(st.objects) != 0 {
		t.Fatalf("off mode wrote %d objects to storage", len(st.objects))
	}
}
