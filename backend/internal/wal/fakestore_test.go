package wal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// fakeStore is an in-memory storage.Storage with real generation semantics: it
// is the only way to exercise the CAS logic without an emulator, so it models
// the one behaviour that matters (a write succeeds iff the generation still
// matches) rather than approximating it.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]fakeObject
	nextGen int64

	// beforePut runs immediately before every conditional write, outside the
	// lock, so a test can slip a competing writer in between our read and our
	// CAS. It receives the attempt number, starting at 1.
	beforePut func(attempt int)
	// afterCAS runs after a conditional write that *succeeded*, outside the
	// lock, so a test can slip a competing writer into the window between a
	// CAS and whatever its caller does next (Compact's state refresh).
	afterCAS func()
	putCalls int
	casCalls int
}

type fakeObject struct {
	data       []byte
	generation int64
	updated    time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]fakeObject{}, nextGen: 100}
}

func (f *fakeStore) SupportsSignedURL() bool { return false }

func (f *fakeStore) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("fakeStore: no signed URLs")
}

func (f *fakeStore) SignedPutURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("fakeStore: no signed URLs")
}

func (f *fakeStore) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	f.store(key, data)
	return nil
}

// store must be called with the lock held.
func (f *fakeStore) store(key string, data []byte) {
	f.nextGen++
	f.objects[key] = fakeObject{data: data, generation: f.nextGen, updated: time.Now()}
}

func (f *fakeStore) PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	attempt := f.casCalls + 1
	hook := f.beforePut
	f.mu.Unlock()

	if hook != nil {
		hook(attempt)
	}

	f.mu.Lock()
	f.casCalls = attempt
	cur, exists := f.objects[key]
	switch {
	case generation == 0 && exists:
		f.mu.Unlock()
		return 0, storage.ErrPreconditionFailed
	case generation != 0 && !exists:
		f.mu.Unlock()
		return 0, storage.ErrPreconditionFailed
	case generation != 0 && cur.generation != generation:
		f.mu.Unlock()
		return 0, storage.ErrPreconditionFailed
	}
	f.store(key, data)
	newGen := f.objects[key].generation
	after := f.afterCAS
	f.mu.Unlock()
	if after != nil {
		after()
	}
	return newGen, nil
}

func (f *fakeStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := f.GetWithGeneration(ctx, key)
	return rc, err
}

func (f *fakeStore) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, 0, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(obj.data)), obj.generation, nil
}

func (f *fakeStore) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset < 0 || offset > int64(len(obj.data)) {
		return nil, errors.New("fakeStore: offset out of range")
	}
	end := int64(len(obj.data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(obj.data[offset:end])), nil
}

func (f *fakeStore) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(obj.data)), Updated: obj.updated, Generation: obj.generation}, nil
}

func (f *fakeStore) Copy(ctx context.Context, srcKey, dstKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	f.store(dstKey, obj.data)
	return nil
}

func (f *fakeStore) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *fakeStore) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storage.ObjectInfo
	for k, obj := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.ObjectInfo{Key: k, Size: int64(len(obj.data)), Updated: obj.updated, Generation: obj.generation})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeStore) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*fakeStore)(nil)

// --- helpers used by the CAS tests ---

// writeIndexUnconditionally simulates another instance winning a race: it bumps
// the generation without going through UpdateIndex.
func (f *fakeStore) writeIndexUnconditionally(t testingT, storagePath string, mutate func(ix *Index)) {
	t.Helper()
	ctx := context.Background()
	ix, _, err := ReadIndex(ctx, f, storagePath)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	mutate(ix)
	body := mustMarshalIndex(t, ix)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store(storage.WALIndexKey(storagePath), body)
}

// testingT is the slice of *testing.T these helpers need.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}
