package wal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

func seedOrFail(t *testing.T, f *fakeStore, gitDir string, force bool) bool {
	t.Helper()
	seeded, err := Seed(context.Background(), f, gitDir, storagePath, force)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return seeded
}

func TestSeed_RebuildsTheRepositoryFromTheSnapshotAlone(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	head := commitTo(t, fx.dir, "main", "two")
	side := commitTo(t, fx.dir, "side", "side one")
	tag := "refs/tags/v1.0"
	gitRun(t, fx.dir, "update-ref", tag, first)

	if !seedOrFail(t, fx.store, fx.dir, false) {
		t.Fatal("seeded = false, want true")
	}

	ix, gen := readIndexOrFail(t, fx.store)
	if gen == 0 {
		t.Fatal("no index written")
	}
	if ix.Base == "" {
		t.Error("base not set: a seed must produce a self-contained snapshot")
	}
	if len(ix.Entries) != 0 {
		t.Errorf("entries = %v, want none: a seed replays no history", ix.Entries)
	}
	want := map[string]string{"refs/heads/main": head, "refs/heads/side": side, tag: first}
	for ref, hash := range want {
		if ix.Refs[ref] != hash {
			t.Errorf("index refs[%s] = %s, want %s", ref, ix.Refs[ref], hash)
		}
	}

	rebuilt := filepath.Join(t.TempDir(), "rebuilt.git")
	if err := Materialize(context.Background(), fx.store, rebuilt, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, rebuilt, want)
	assertHealthy(t, rebuilt)
	if got := gitRun(t, rebuilt, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}

func TestSeed_AdoptsTheSourceCopySoItIsNotRebuilt(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	seedOrFail(t, fx.store, fx.dir, false)

	_, gen := readIndexOrFail(t, fx.store)
	if got := LocalGeneration(fx.dir); got != gen {
		t.Fatalf("local generation = %d, want %d: the seeded copy was not adopted", got, gen)
	}
	// The source already holds everything the index describes, so bringing it
	// up to date must not touch it.
	if err := Materialize(context.Background(), fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, fx.dir, map[string]string{"refs/heads/main": head})
	assertHealthy(t, fx.dir)
}

func TestSeed_WithoutForceRefusesAnExistingIndex(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	ixBefore, genBefore := readIndexOrFail(t, fx.store)

	// The on-disk copy has moved on, but the WAL is live: overwriting it would
	// discard whatever it accepted since.
	next := commitTo(t, fx.dir, "main", "two")
	if seedOrFail(t, fx.store, fx.dir, false) {
		t.Fatal("seeded = true, want false for an existing index without force")
	}

	ix, gen := readIndexOrFail(t, fx.store)
	if gen != genBefore {
		t.Errorf("generation moved from %d to %d", genBefore, gen)
	}
	if ix.Refs["refs/heads/main"] != ixBefore.Refs["refs/heads/main"] {
		t.Errorf("refs[main] = %s, want %s untouched (disk is at %s)",
			ix.Refs["refs/heads/main"], ixBefore.Refs["refs/heads/main"], next)
	}
}

func TestSeed_ForceReplacesTheIndexAndDropsEntries(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: first, New: second})

	// A divergence only the WAL believes in, of the kind Verify reports and
	// force-seeding repairs.
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/ghost"] = hashC
	})

	if !seedOrFail(t, fx.store, fx.dir, true) {
		t.Fatal("seeded = false, want true with force")
	}
	ix, _ := readIndexOrFail(t, fx.store)
	if len(ix.Entries) != 0 {
		t.Errorf("entries = %v, want none after a force seed", ix.Entries)
	}
	if _, ok := ix.Refs["refs/heads/ghost"]; ok {
		t.Errorf("refs = %v, want the divergent ref gone", ix.Refs)
	}
	if ix.Seq != 2 {
		t.Errorf("seq = %d, want 2: numbering must not restart and collide with orphans", ix.Seq)
	}

	rebuilt := filepath.Join(t.TempDir(), "rebuilt.git")
	if err := Materialize(context.Background(), fx.store, rebuilt, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": second})
	assertHealthy(t, rebuilt)
}

func TestSeed_EmptyRepositoryGetsAnIndexWithNoBase(t *testing.T) {
	fx := newPushFixture(t)
	if !seedOrFail(t, fx.store, fx.dir, false) {
		t.Fatal("seeded = false, want true")
	}
	ix, gen := readIndexOrFail(t, fx.store)
	if gen == 0 {
		t.Fatal("no index written for an empty repository")
	}
	if ix.Base != "" || len(ix.Entries) != 0 || len(ix.Refs) != 0 {
		t.Errorf("index = %+v, want base/entries/refs all empty", ix)
	}
	if got := len(mustList(t, fx.store, storage.WALBasePrefix(storagePath))); got != 0 {
		t.Errorf("uploaded bases = %d, want 0", got)
	}

	// A push into the seeded-but-empty repository still works from there.
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": head})
	assertHealthy(t, rebuilt)
}

func TestSeed_ThenShadowPushIsIncrementalOnTopOfTheBase(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	seedOrFail(t, fx.store, fx.dir, false)

	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: first, New: second})

	ix, _ := readIndexOrFail(t, fx.store)
	if len(ix.Entries) != 1 {
		t.Fatalf("entries = %v, want 1", ix.Entries)
	}
	if got := storedPackObjectCount(t, fx.store, ix.Entries[0]); got != 3 {
		t.Errorf("entry holds %d objects, want 3: the base was not excluded", got)
	}

	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": second})
	assertHealthy(t, rebuilt)
	if got := gitRun(t, rebuilt, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}
