package wal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// age backdates an object's last-modified time so the grace period of
// invariant 3 (§5) can be exercised without a test that sleeps for a day.
func (f *fakeStore) age(t testingT, key string, by time.Duration) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		t.Fatalf("age: no object at %s", key)
	}
	obj.updated = obj.updated.Add(-by)
	f.objects[key] = obj
}

func (f *fakeStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func gcOrFail(t *testing.T, f *fakeStore, minAge time.Duration) []string {
	t.Helper()
	deleted, err := GCOrphans(context.Background(), f, storagePath, minAge)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	return deleted
}

func TestGCOrphans_CollectsOnlyUnreferencedPacks(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	ix, _ := readIndexOrFail(t, fx.store)
	live := storage.WALKey(storagePath, ix.Entries[0])

	// A CAS loser: its pack was uploaded, then the index moved on without it.
	orphanEntry := storage.WALKey(storagePath, EntryName(ix.Seq+1, "0123456789ABCDEFGHJKMNPQRS"))
	orphanBase := storage.WALKey(storagePath, BaseName("0123456789ABCDEFGHJKMNPQRT"))
	for _, key := range []string{orphanEntry, orphanBase} {
		if err := fx.store.Put(context.Background(), key, stringReader("PACK"), packContentType); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		fx.store.age(t, key, 48*time.Hour)
	}
	fx.store.age(t, live, 48*time.Hour) // old, but referenced: age alone must not free it

	deleted := gcOrFail(t, fx.store, DefaultGCGracePeriod)
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want the two orphans", deleted)
	}
	for _, key := range []string{orphanEntry, orphanBase} {
		if fx.store.has(key) {
			t.Errorf("%s survived", key)
		}
	}
	if !fx.store.has(live) {
		t.Error("the referenced entry was collected")
	}
	if !fx.store.has(storage.WALIndexKey(storagePath)) {
		t.Error("the index was collected: it must never be a candidate")
	}

	// And the repository still rebuilds, which is the only test that matters.
	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": head})
	assertHealthy(t, rebuilt)
}

func TestGCOrphans_KeepsOrphansInsideTheGracePeriod(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	// A pack uploaded moments ago is exactly the case the grace period protects:
	// another instance may be about to name it, or be materialising from the
	// index that still does.
	fresh := storage.WALKey(storagePath, EntryName(9, "0123456789ABCDEFGHJKMNPQRS"))
	if err := fx.store.Put(context.Background(), fresh, stringReader("PACK"), packContentType); err != nil {
		t.Fatalf("put: %v", err)
	}

	if deleted := gcOrFail(t, fx.store, time.Hour); len(deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing inside the grace period", deleted)
	}
	if !fx.store.has(fresh) {
		t.Error("a fresh orphan was collected")
	}

	fx.store.age(t, fresh, 2*time.Hour)
	if deleted := gcOrFail(t, fx.store, time.Hour); len(deleted) != 1 || deleted[0] != fresh {
		t.Errorf("deleted = %v, want [%s] once it aged out", deleted, fresh)
	}
}

func TestGCOrphans_CollectsWhatCompactionSuperseded(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: first, New: second})

	before, _ := readIndexOrFail(t, fx.store)
	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	for _, entry := range before.Entries {
		fx.store.age(t, storage.WALKey(storagePath, entry), 48*time.Hour)
	}

	deleted := gcOrFail(t, fx.store, DefaultGCGracePeriod)
	if len(deleted) != len(before.Entries) {
		t.Fatalf("deleted = %v, want the %d folded entries", deleted, len(before.Entries))
	}

	after, _ := readIndexOrFail(t, fx.store)
	if !fx.store.has(storage.WALKey(storagePath, after.Base)) {
		t.Fatal("the new base was collected")
	}
	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": second})
	assertHealthy(t, rebuilt)
}

// A missing index used to read as "nothing is referenced", which turned the
// scheduled compaction job into the thing that finished off a lost index: it
// would sweep away every pack, and those packs are exactly what
// docs/dev/wal-index-recovery.md restores the index *from*.
func TestGCOrphans_WithoutAnIndexRefusesToSweep(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	ix, _ := readIndexOrFail(t, fx.store)
	live := storage.WALKey(storagePath, ix.Entries[0])
	fx.store.age(t, live, 48*time.Hour)

	// The §13 failure: the index is gone, the packs that reconstruct it are not.
	if err := fx.store.Delete(context.Background(), storage.WALIndexKey(storagePath)); err != nil {
		t.Fatalf("delete index: %v", err)
	}

	deleted, err := GCOrphans(context.Background(), fx.store, storagePath, DefaultGCGracePeriod)
	if !errors.Is(err, ErrIndexMissing) {
		t.Fatalf("GCOrphans err = %v, want ErrIndexMissing", err)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want nothing swept", deleted)
	}
	if !fx.store.has(live) {
		t.Error("the recovery material was deleted: the index can no longer be rebuilt")
	}
}

// The distinction the error draws: no packs at all is a repository that never
// wrote a WAL, not a lost index, and it must stay a silent no-op so the job
// does not alarm on every freshly created repository.
func TestGCOrphans_WithoutAnIndexAndWithoutPacksIsASilentNoop(t *testing.T) {
	f := newFakeStore()
	deleted, err := GCOrphans(context.Background(), f, storagePath, DefaultGCGracePeriod)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted = %v, want nothing", deleted)
	}
}

func TestGCOrphans_EmptyWALIsANoop(t *testing.T) {
	f := newFakeStore()
	if deleted := gcOrFail(t, f, DefaultGCGracePeriod); len(deleted) != 0 {
		t.Errorf("deleted = %v, want nothing", deleted)
	}
}

func TestPurge_RemovesEverythingUnderTheRepositoryPrefix(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: first, New: second})
	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// An orphan too: purge must not depend on the index to find objects.
	orphan := storage.WALKey(storagePath, EntryName(9, "0123456789ABCDEFGHJKMNPQRS"))
	if err := fx.store.Put(context.Background(), orphan, stringReader("PACK"), packContentType); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A second repository shares the prefix root and must be untouched.
	other := storage.WALIndexKey(storage.LegacyStoragePath(kind, ns, "other"))
	if err := fx.store.Put(context.Background(), other, stringReader("{}"), "application/json"); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := Purge(context.Background(), fx.store, storagePath); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if left := mustList(t, fx.store, storage.WALPrefix(storagePath)); len(left) != 0 {
		t.Errorf("objects left after purge: %v", left)
	}
	if !fx.store.has(other) {
		t.Error("purge reached into a neighbouring repository")
	}

	// A recreated repository of the same name starts from nothing, rather than
	// inheriting the deleted one's refs.
	ix, gen := readIndexOrFail(t, fx.store)
	if gen != 0 || len(ix.Refs) != 0 {
		t.Errorf("index = %+v at generation %d, want an absent index", ix, gen)
	}
}

func TestPurge_OnARepositoryWithNoWALSucceeds(t *testing.T) {
	f := newFakeStore()
	if err := Purge(context.Background(), f, storagePath); err != nil {
		t.Fatalf("Purge: %v", err)
	}
}
