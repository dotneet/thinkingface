package wal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

func TestCompact_FoldsEntriesIntoASingleBase(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)
	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)
	tag := "refs/tags/v1.0"
	if err := UpdateIndex(context.Background(), fx.store, storagePath,
		[]RefUpdate{{Ref: tag, Old: "", New: first}}, ""); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Base == "" {
		t.Fatal("base not set after compaction")
	}
	if len(ix.Entries) != 0 {
		t.Errorf("entries = %v, want empty after compaction", ix.Entries)
	}
	if ix.Seq != 2 {
		t.Errorf("seq = %d, want 2: numbering continues so orphaned entries keep distinct names", ix.Seq)
	}
	if ix.Refs["refs/heads/main"] != second || ix.Refs[tag] != first {
		t.Errorf("refs = %v, want them carried over untouched", ix.Refs)
	}

	// The snapshot alone must rebuild the repository: a fresh cache has no
	// entries left to fall back on.
	fresh := filepath.Join(t.TempDir(), "fresh.git")
	if err := Materialize(context.Background(), fx.store, fresh, storagePath); err != nil {
		t.Fatalf("Materialize after compaction: %v", err)
	}
	assertRefs(t, fresh, map[string]string{"refs/heads/main": second, tag: first})
	assertHealthy(t, fresh)
	if got := gitRun(t, fresh, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}

func TestCompact_KeepsSupersededPacksInStorage(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)
	before := mustList(t, fx.store, storage.WALEntriesPrefix(storagePath))

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Invariant 3 of §5: another instance may still be materialising from the
	// index we just replaced. Deletion is age-based GC's job, not ours.
	after := mustList(t, fx.store, storage.WALEntriesPrefix(storagePath))
	if len(after) != len(before) {
		t.Errorf("entry packs in storage: %d before, %d after; compaction must not delete", len(before), len(after))
	}
}

func TestCompact_LosesToAConcurrentPushWithoutRetrying(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)

	// A push lands after the snapshot was built but before its CAS.
	fx.store.beforePut = func(int) {
		fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
			ix.Refs["refs/heads/main"] = hashC
			ix.Entries = append(ix.Entries, "entries/000002-LATER.pack")
			ix.Seq = 2
		})
	}

	casesBefore := fx.store.casCalls
	work := filepath.Join(t.TempDir(), "work.git")
	err := Compact(context.Background(), fx.store, work, storagePath)
	if !errors.Is(err, ErrCompactionRaced) {
		t.Fatalf("Compact error = %v, want ErrCompactionRaced", err)
	}
	if got := fx.store.casCalls - casesBefore; got != 1 {
		t.Errorf("CAS attempts during compaction = %d, want exactly 1: compaction must not retry (§10)", got)
	}

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Base != "" {
		t.Errorf("base = %q, want empty: the losing snapshot must not be published", ix.Base)
	}
	if ix.Refs["refs/heads/main"] != hashC || len(ix.Entries) != 2 {
		t.Errorf("index = %+v, want the concurrent push intact", ix)
	}
}

func TestCompact_WithoutAnIndexIsANoop(t *testing.T) {
	requireGit(t)
	f := newFakeStore()
	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), f, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if objs := mustList(t, f, storage.WALPrefix(storagePath)); len(objs) != 0 {
		t.Errorf("objects written for a repository with no WAL: %v", objs)
	}
}

func TestCompact_EmptyRepositoryWritesNoBase(t *testing.T) {
	requireGit(t)
	f := newFakeStore()
	// An index exists but carries nothing: a create with no refs yet.
	if _, err := PutIndex(context.Background(), f, storagePath, 0, NewIndex()); err != nil {
		t.Fatalf("PutIndex: %v", err)
	}

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), f, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if objs := mustList(t, f, storage.WALBasePrefix(storagePath)); len(objs) != 0 {
		t.Errorf("base written for an empty repository: %v", objs)
	}
}

func TestCompact_TwiceInARowIsStable(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	firstBase, _ := readIndexOrFail(t, fx.store)

	// The scratch copy is reused: compaction must work from a directory that
	// already reflects the base it just published (state refreshed in place).
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Base == "" || ix.Base == firstBase.Base {
		t.Errorf("base = %q (previous %q), want a new snapshot", ix.Base, firstBase.Base)
	}
	if ix.Refs["refs/heads/main"] != head {
		t.Errorf("refs = %v, want main=%s", ix.Refs, head)
	}

	fresh := filepath.Join(t.TempDir(), "fresh.git")
	if err := Materialize(context.Background(), fx.store, fresh, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, fresh, map[string]string{"refs/heads/main": head})
	assertHealthy(t, fresh)
}

func TestCompact_ThenPushContinuesFromTheNewBase(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)

	// A cache that was materialised before compaction must notice the changed
	// base and rebuild rather than apply the new entry onto stale objects.
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
	assertHealthy(t, fx.dst)
	if got := gitRun(t, fx.dst, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}

// Regression: a push that lands in the window between compaction's successful
// CAS and its local-state refresh must not poison the scratch copy. A state
// file stamped with the interleaved push's generation would make the next
// Materialize treat the stale copy as current, and the compaction after that
// would anchor its CAS on that claim and publish an index that rolls the push
// back — data loss.
func TestCompact_InterleavedPushAfterCASKeepsLocalStateHonest(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)

	// Arm the trap: the moment compaction's CAS succeeds, a second push lands
	// (a real one, so the later materialisation has a real pack to apply).
	var second string
	fx.store.afterCAS = func() {
		fx.store.afterCAS = nil // one shot; pushToWAL below does CAS too
		second = commitTo(t, fx.src, "main", "two")
		pushToWAL(t, fx.store, fx.src, "main", first, second)
	}

	work := filepath.Join(t.TempDir(), "work.git")
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	fx.store.afterCAS = nil

	// The scratch copy must not claim the post-push generation while missing
	// the push: after a materialisation it has to converge on the new commit.
	if err := Materialize(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("Materialize after compact: %v", err)
	}
	if got := refTarget(t, work, "refs/heads/main"); got != second {
		t.Fatalf("after materialize, main = %s, want the interleaved push %s", got, second)
	}
	assertHealthy(t, work)

	// And a second compaction must fold the push in, not roll it back.
	if err := Compact(context.Background(), fx.store, work, storagePath); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/heads/main"] != second {
		t.Fatalf("after second compact, index main = %s, want %s (the push must survive)", ix.Refs["refs/heads/main"], second)
	}
	fresh := filepath.Join(t.TempDir(), "fresh.git")
	if err := Materialize(context.Background(), fx.store, fresh, storagePath); err != nil {
		t.Fatalf("Materialize fresh: %v", err)
	}
	if got := refTarget(t, fresh, "refs/heads/main"); got != second {
		t.Fatalf("fresh copy main = %s, want %s", got, second)
	}
	assertHealthy(t, fresh)
}
