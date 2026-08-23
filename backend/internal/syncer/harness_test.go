package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// ---------------------------------------------------------------- fixtures

// memStorage is a minimal in-memory storage.Storage, copied from the pattern
// used in internal/viewer and internal/api tests (no shared/exported fake
// exists to import across packages).
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStorage() *memStorage { return &memStorage{objects: make(map[string][]byte)} }

func (m *memStorage) SupportsSignedURL() bool { return false }

func (m *memStorage) SignedGetURL(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) SignedPutURL(ctx context.Context, key string, ttl time.Duration, size int64) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *memStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStorage) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	rc, err := m.Get(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return rc, 1, nil
}

func (m *memStorage) PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error) {
	return 0, errors.New("memStorage: conditional writes not supported")
}

func (m *memStorage) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	end := int64(len(data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func (m *memStorage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(data)), Updated: time.Now()}, nil
}

func (m *memStorage) Copy(ctx context.Context, srcKey, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	m.objects[dstKey] = data
	return nil
}

func (m *memStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStorage) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []storage.ObjectInfo
	for k, data := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.ObjectInfo{Key: k, Size: int64(len(data)), Updated: time.Now()})
		}
	}
	return out, nil
}

func (m *memStorage) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*memStorage)(nil)

func (m *memStorage) keys() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.objects))
	for k := range m.objects {
		out[k] = true
	}
	return out
}

// ------------------------------------------------------------- git helper

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
}

func addOp(path, content string) gitrepo.Op {
	return gitrepo.Op{Kind: gitrepo.OpAdd, Path: path, Data: []byte(content)}
}

// ------------------------------------------------------------ full harness

// harness wires up a Syncer against a SQLite store, a temp-dir gitrepo
// Manager, and an in-memory storage.Storage — the same shape production
// wires in cmd/thinkingface/main.go, minus webhooks (nil is fine: fireWebhook
// no-ops without a dispatcher).
type harness struct {
	t   *testing.T
	ctx context.Context

	st  *store.Store
	git *gitrepo.Manager
	obj *memStorage
	syn *Syncer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	requireGit(t)
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	git := gitrepo.NewManager(t.TempDir())
	obj := newMemStorage()
	parquet := viewer.New(obj, t.TempDir(), 0)
	indexer := experiments.NewIndexer(st, git, obj, parquet)
	syn := New(st, git, obj, parquet, indexer, nil, 1)

	return &harness{t: t, ctx: ctx, st: st, git: git, obj: obj, syn: syn}
}

// step claims and processes exactly one pending job, failing the test if the
// queue was empty or the job failed.
func (h *harness) step() {
	h.t.Helper()
	job, err := h.st.ClaimSyncJob(h.ctx)
	if err != nil {
		h.t.Fatalf("claim sync job: %v", err)
	}
	if job == nil {
		h.t.Fatal("step: no pending sync job")
	}
	jobErr := h.syn.process(h.ctx, job)
	if err := h.st.FinishSyncJob(h.ctx, job.ID, jobErr); err != nil {
		h.t.Fatalf("finish sync job: %v", err)
	}
	if jobErr != nil {
		h.t.Fatalf("process job %d (kind=%q): %v", job.ID, job.Kind, jobErr)
	}
}

func (h *harness) user(username string) *store.User {
	h.t.Helper()
	u, err := h.st.CreateUser(h.ctx, username, username+"@example.com", "x", false)
	if err != nil {
		h.t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func (h *harness) namespace(name string) *store.Namespace {
	h.t.Helper()
	ns, err := h.st.GetNamespace(h.ctx, name)
	if err != nil {
		h.t.Fatalf("get namespace %s: %v", name, err)
	}
	return ns
}
