package wal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// materializeFixture is a source repository plus a WAL holding its history, and
// a destination path that has never been materialised.
type materializeFixture struct {
	store *fakeStore
	src   string
	dst   string
}

func newMaterializeFixture(t *testing.T) *materializeFixture {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	return &materializeFixture{
		store: newFakeStore(),
		src:   newBare(t, dir, "src.git"),
		dst:   filepath.Join(dir, "dst.git"),
	}
}

func (fx *materializeFixture) materialize(t *testing.T) error {
	t.Helper()
	return Materialize(context.Background(), fx.store, fx.dst, storagePath)
}

func (fx *materializeFixture) mustMaterialize(t *testing.T) {
	t.Helper()
	if err := fx.materialize(t); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
}

func TestMaterialize_AppliesEntriesInOrderAndProjectsRefs(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)
	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)

	fx.mustMaterialize(t)

	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
	assertHealthy(t, fx.dst)
	// The full history must be present, not just the tip: the second entry
	// excluded the first commit's objects, so this fails if entry order or
	// entry application is wrong.
	if got := gitRun(t, fx.dst, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
	if head := gitRun(t, fx.dst, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Errorf("HEAD = %s, want refs/heads/main", head)
	}
}

func TestMaterialize_SecondCallAtTheSameGenerationDoesNothing(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)
	fx.mustMaterialize(t)

	// Delete every pack from storage. A cache hit is decided by generation
	// equality alone (§4), so a second call must not touch storage at all —
	// if it did, it would now fail.
	for _, obj := range mustList(t, fx.store, storage.WALEntriesPrefix(storagePath)) {
		if err := fx.store.Delete(context.Background(), obj.Key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": head})
}

func TestMaterialize_WithoutAnIndexLeavesTheLocalCopyAlone(t *testing.T) {
	fx := newMaterializeFixture(t)
	// A repository created but never pushed through the WAL: rebuilding it
	// from an empty index would delete refs that only exist locally.
	local := newBare(t, t.TempDir(), "local.git")
	head := commitTo(t, local, "main", "one")

	if err := Materialize(context.Background(), fx.store, local, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, local, map[string]string{"refs/heads/main": head})
	if fileExists(filepath.Join(local, StateFileName)) {
		t.Error("state file written for a repository with no index")
	}
}

func TestMaterialize_PropagatesRefDeletionsAndAdditions(t *testing.T) {
	fx := newMaterializeFixture(t)
	main := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", main)
	topic := commitTo(t, fx.src, "topic", "two")
	pushToWAL(t, fx.store, fx.src, "topic", "", topic)
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": main, "refs/heads/topic": topic})

	// Another instance deletes the branch. Without the delete half of
	// writeRefs, this copy would keep serving it forever (§9).
	if err := UpdateIndex(context.Background(), fx.store, storagePath,
		[]RefUpdate{{Ref: "refs/heads/topic", Old: topic, New: zeroHash}}, ""); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": main})
	assertHealthy(t, fx.dst)
}

func TestMaterialize_RecoversFromACrashBetweenObjectsAndRefs(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)
	fx.mustMaterialize(t)

	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)

	// Reproduce the interrupted materialisation of §9: the new entry's objects
	// are already in the object database, but refs and the state file still
	// describe the previous generation.
	entries := mustList(t, fx.store, storage.WALEntriesPrefix(storagePath))
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in storage, got %d", len(entries))
	}
	if err := applyPack(context.Background(), fx.store, fx.dst, entries[1].Key); err != nil {
		t.Fatalf("applyPack (simulated crash): %v", err)
	}
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": first})

	// Re-running must converge: applying the same pack twice is a no-op for
	// git, and the refs catch up.
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
	assertHealthy(t, fx.dst)
	if got := gitRun(t, fx.dst, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}

func TestMaterialize_MissingStateFileRebuildsFromScratch(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)
	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)
	fx.mustMaterialize(t)

	if err := os.Remove(filepath.Join(fx.dst, StateFileName)); err != nil {
		t.Fatalf("remove state file: %v", err)
	}
	// Without the state file we cannot know which entries were applied, so the
	// only safe move is a full rebuild — and it must still land on the right refs.
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
	assertHealthy(t, fx.dst)
	if LocalGeneration(fx.dst) == 0 {
		t.Error("state file not restored after rebuild")
	}
}

func TestMaterialize_CorruptOrImplausibleStateRebuilds(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":          "{ not json",
		"empty file":            "",
		"zero generation":       `{"generation":0,"base":"","applied":[]}`,
		"negative generation":   `{"generation":-5,"base":"","applied":[]}`,
		"wrong type for fields": `{"generation":"seven","applied":"nope"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newMaterializeFixture(t)
			head := commitTo(t, fx.src, "main", "one")
			pushToWAL(t, fx.store, fx.src, "main", "", head)
			fx.mustMaterialize(t)

			if err := os.WriteFile(filepath.Join(fx.dst, StateFileName), []byte(body), 0o644); err != nil {
				t.Fatalf("write state: %v", err)
			}
			fx.mustMaterialize(t)
			assertRefs(t, fx.dst, map[string]string{"refs/heads/main": head})
			assertHealthy(t, fx.dst)
			if LocalGeneration(fx.dst) == 0 {
				t.Error("state file not rewritten")
			}
		})
	}
}

func TestMaterialize_StateClaimingUnknownEntriesRebuilds(t *testing.T) {
	fx := newMaterializeFixture(t)
	first := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", first)
	fx.mustMaterialize(t)

	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", first, second)

	// Applied is no longer a prefix of the index entries: the local copy is
	// describing a history that never happened, so incremental application
	// would silently skip real entries.
	if err := os.WriteFile(filepath.Join(fx.dst, StateFileName),
		[]byte(`{"generation":1,"base":"","applied":["entries/000001-GHOST.pack","entries/000002-GHOST.pack"]}`), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
	assertHealthy(t, fx.dst)
}

func TestMaterialize_MissingDirectoryIsCreated(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)

	// The tmpfs-eviction case of §13: the whole directory is gone.
	fx.mustMaterialize(t)
	if err := os.RemoveAll(fx.dst); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": head})
	assertHealthy(t, fx.dst)
}

func TestMaterialize_CorruptPackFailsLoudlyAndDoesNotClaimSuccess(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)

	entries := mustList(t, fx.store, storage.WALEntriesPrefix(storagePath))
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	// Keep a valid pack header but rot the body: --strict must catch it.
	corrupt := append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x03"), []byte(strings.Repeat("\x00", 64))...)
	if err := fx.store.Put(context.Background(), entries[0].Key, strings.NewReader(string(corrupt)), packContentType); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := fx.materialize(t); err == nil {
		t.Fatal("Materialize accepted a corrupt pack")
	}
	if LocalGeneration(fx.dst) != 0 {
		t.Error("state file written despite a failed materialisation")
	}
}

func TestMaterialize_HEADFallsBackToTheFirstBranchWhenMainIsAbsent(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "develop", "one")
	pushToWAL(t, fx.store, fx.src, "develop", "", head)

	fx.mustMaterialize(t)
	if got := gitRun(t, fx.dst, "symbolic-ref", "HEAD"); got != "refs/heads/develop" {
		t.Errorf("HEAD = %s, want refs/heads/develop", got)
	}

	// Once main exists, HEAD stays where it is: it still points at a branch
	// that exists, and moving it would surprise clones.
	other := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", "", other)
	fx.mustMaterialize(t)
	if got := gitRun(t, fx.dst, "symbolic-ref", "HEAD"); got != "refs/heads/develop" {
		t.Errorf("HEAD = %s, want it to stay on refs/heads/develop", got)
	}
}

func TestMaterialize_HEADMovesWhenItsBranchDisappears(t *testing.T) {
	fx := newMaterializeFixture(t)
	develop := commitTo(t, fx.src, "develop", "one")
	pushToWAL(t, fx.store, fx.src, "develop", "", develop)
	main := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", "", main)
	fx.mustMaterialize(t)

	if err := UpdateIndex(context.Background(), fx.store, storagePath,
		[]RefUpdate{{Ref: "refs/heads/develop", Old: develop, New: ""}}, ""); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	fx.mustMaterialize(t)
	if got := gitRun(t, fx.dst, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("HEAD = %s, want refs/heads/main after develop was deleted", got)
	}
}

func TestMaterialize_TagsAreProjectedToo(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)
	if err := UpdateIndex(context.Background(), fx.store, storagePath,
		[]RefUpdate{{Ref: "refs/tags/v1.0", Old: "", New: head}}, ""); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}

	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": head, "refs/tags/v1.0": head})
	assertHealthy(t, fx.dst)
}

func TestMaterialize_LocalRefsNotInTheIndexAreRemoved(t *testing.T) {
	fx := newMaterializeFixture(t)
	head := commitTo(t, fx.src, "main", "one")
	pushToWAL(t, fx.store, fx.src, "main", "", head)
	fx.mustMaterialize(t)

	// Something wrote a ref directly to the cache, which invariant 5 forbids.
	// The next materialisation at a new generation must sweep it away.
	gitRun(t, fx.dst, "update-ref", "refs/heads/rogue", head)
	second := commitTo(t, fx.src, "main", "two")
	pushToWAL(t, fx.store, fx.src, "main", head, second)

	fx.mustMaterialize(t)
	assertRefs(t, fx.dst, map[string]string{"refs/heads/main": second})
}

func mustList(t *testing.T, f *fakeStore, prefix string) []storage.ObjectInfo {
	t.Helper()
	objs, err := f.List(context.Background(), prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return objs
}
