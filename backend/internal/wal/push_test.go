package wal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// pushFixture is a bare repository standing in for the on-disk copy a hook runs
// against, plus the WAL it mirrors into.
type pushFixture struct {
	store *fakeStore
	dir   string
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	requireGit(t)
	return &pushFixture{store: newFakeStore(), dir: newBare(t, t.TempDir(), "pd.git")}
}

func (fx *pushFixture) shadow(t *testing.T, updates ...RefUpdate) error {
	t.Helper()
	return ShadowPush(context.Background(), fx.store, fx.dir, storagePath, updates)
}

func (fx *pushFixture) mustShadow(t *testing.T, updates ...RefUpdate) {
	t.Helper()
	if err := fx.shadow(t, updates...); err != nil {
		t.Fatalf("ShadowPush: %v", err)
	}
}

// rebuild materialises the WAL into a brand new directory, which is the only
// honest way to ask "does the WAL really contain this?".
func (fx *pushFixture) rebuild(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "rebuilt.git")
	if err := Materialize(context.Background(), fx.store, dst, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	return dst
}

// storedPackObjectCount reads back an uploaded entry and reports how many
// objects it carries, which is the observable effect of the exclude set.
func storedPackObjectCount(t *testing.T, f *fakeStore, rel string) uint32 {
	t.Helper()
	rc, err := f.Get(context.Background(), storage.WALKey(storagePath, rel))
	if err != nil {
		t.Fatalf("get %s: %v", rel, err)
	}
	defer rc.Close()
	count, err := packObjectCount(bufio.NewReader(rc))
	if err != nil {
		t.Fatalf("read pack header of %s: %v", rel, err)
	}
	return count
}

func countEntries(t *testing.T, f *fakeStore) int {
	t.Helper()
	return len(mustList(t, f, storage.WALEntriesPrefix(storagePath)))
}

func TestShadowPush_FirstPushCarriesTheWholeHistory(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	head := commitTo(t, fx.dir, "main", "two")

	// The WAL is empty, so even though the client only "pushed" the tip, the
	// entry has to carry both commits or nothing can be rebuilt from it.
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: zeroHash, New: head})

	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": head})
	assertHealthy(t, rebuilt)
	if got := gitRun(t, rebuilt, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2 (whole history seeded)", got)
	}
	if refTarget(t, rebuilt, first) == "" {
		t.Error("first commit missing from the rebuilt repository")
	}
}

func TestShadowPush_SecondPushIsIncremental(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: first, New: second})

	ix, _ := readIndexOrFail(t, fx.store)
	if len(ix.Entries) != 2 {
		t.Fatalf("entries = %v, want 2", ix.Entries)
	}
	if ix.Seq != 2 {
		t.Errorf("seq = %d, want 2", ix.Seq)
	}

	// The second entry holds only the new commit, tree and blob: the first
	// commit's objects came out of the exclude set. Object counts say that
	// directly; byte sizes do not, since a commit with a parent is larger.
	if got := storedPackObjectCount(t, fx.store, ix.Entries[1]); got != 3 {
		t.Errorf("second entry holds %d objects, want 3: exclude did not apply", got)
	}

	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": second})
	assertHealthy(t, rebuilt)
}

func TestShadowPush_DeletionCarriesNoEntry(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	gitRun(t, fx.dir, "update-ref", "-d", "refs/heads/main")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: head, New: zeroHash})

	ix, _ := readIndexOrFail(t, fx.store)
	if len(ix.Refs) != 0 {
		t.Errorf("refs = %v, want empty after the deletion was mirrored", ix.Refs)
	}
	if len(ix.Entries) != 1 || ix.Seq != 1 {
		t.Errorf("entries = %v seq = %d, want the deletion to add neither", ix.Entries, ix.Seq)
	}
}

func TestShadowPush_AlreadyMirroredIsATotalNoop(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	_, genBefore := readIndexOrFail(t, fx.store)
	entriesBefore := countEntries(t, fx.store)

	// Replaying the same push — a retried hook, or a repository shadowed right
	// after being seeded — must not write anything at all.
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	if _, gen := readIndexOrFail(t, fx.store); gen != genBefore {
		t.Errorf("generation moved from %d to %d on a no-op mirror", genBefore, gen)
	}
	if got := countEntries(t, fx.store); got != entriesBefore {
		t.Errorf("entry count = %d, want %d: a no-op mirror uploaded a pack", got, entriesBefore)
	}
}

func TestShadowPush_DeletingARefTheWALNeverHadIsANoop(t *testing.T) {
	fx := newPushFixture(t)
	if err := fx.shadow(t, RefUpdate{Ref: "refs/heads/gone", Old: hashA, New: zeroHash}); err != nil {
		t.Fatalf("ShadowPush: %v", err)
	}
	if _, gen := readIndexOrFail(t, fx.store); gen != 0 {
		t.Errorf("generation = %d, want 0: an index was created for a no-op", gen)
	}
}

func TestShadowPush_IgnoresTheClientsOldValue(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")

	// The client believes main is at hashB; the on-disk repository says
	// otherwise, and in mirror mode the on-disk repository is what counts.
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: hashB, New: second})

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/heads/main"] != second {
		t.Errorf("refs[main] = %s, want %s", ix.Refs["refs/heads/main"], second)
	}
}

func TestShadowPush_SurvivesAWALThatIsAheadOfTheLocalCopy(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	// Divergence: the index names an object this copy has never seen. Without
	// the known-objects filter, pack-objects dies with "bad revision" and every
	// later push to the repository fails with it.
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/ghost"] = hashC
	})

	next := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: head, New: next})

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/heads/main"] != next {
		t.Errorf("refs[main] = %s, want %s", ix.Refs["refs/heads/main"], next)
	}
	if ix.Refs["refs/heads/ghost"] != hashC {
		t.Errorf("the divergent ref was dropped: %v", ix.Refs)
	}
}

func TestShadowPush_SeveralRefsInOnePushBecomeOneEntry(t *testing.T) {
	fx := newPushFixture(t)
	main := commitTo(t, fx.dir, "main", "one")
	side := commitTo(t, fx.dir, "side", "side one")
	tag := "refs/tags/v1.0"
	gitRun(t, fx.dir, "update-ref", tag, main)

	fx.mustShadow(t,
		RefUpdate{Ref: "refs/heads/main", Old: "", New: main},
		RefUpdate{Ref: "refs/heads/side", Old: "", New: side},
		RefUpdate{Ref: tag, Old: "", New: main},
	)

	ix, _ := readIndexOrFail(t, fx.store)
	if len(ix.Entries) != 1 {
		t.Errorf("entries = %v, want exactly one for an atomic multi-ref push", ix.Entries)
	}
	if got := countEntries(t, fx.store); got != 1 {
		t.Errorf("uploaded packs = %d, want 1", got)
	}

	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{
		"refs/heads/main": main, "refs/heads/side": side, tag: main,
	})
	assertHealthy(t, rebuilt)
}

func TestShadowPush_RefPointedAtAnExistingCommitUploadsNoPack(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	// A tag on a commit the WAL already holds: the index must change, but the
	// pack would be empty and is therefore never uploaded.
	gitRun(t, fx.dir, "update-ref", "refs/tags/v1.0", head)
	fx.mustShadow(t, RefUpdate{Ref: "refs/tags/v1.0", Old: "", New: head})

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/tags/v1.0"] != head {
		t.Errorf("refs = %v, want the tag recorded", ix.Refs)
	}
	if len(ix.Entries) != 1 || ix.Seq != 1 {
		t.Errorf("entries = %v seq = %d, want the empty pack skipped", ix.Entries, ix.Seq)
	}
	if got := countEntries(t, fx.store); got != 1 {
		t.Errorf("uploaded packs = %d, want 1: an empty pack was uploaded", got)
	}
}

func TestShadowPush_NoUpdatesIsANoop(t *testing.T) {
	fx := newPushFixture(t)
	if err := fx.shadow(t); err != nil {
		t.Fatalf("ShadowPush: %v", err)
	}
	if _, gen := readIndexOrFail(t, fx.store); gen != 0 {
		t.Errorf("generation = %d, want 0", gen)
	}
}

func TestAuthoritativePush_FastForwardSucceeds(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	first := commitTo(t, fx.dir, "main", "one")
	if err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: zeroHash, New: first}}); err != nil {
		t.Fatalf("AuthoritativePush: %v", err)
	}
	second := commitTo(t, fx.dir, "main", "two")
	if err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: first, New: second}}); err != nil {
		t.Fatalf("AuthoritativePush: %v", err)
	}

	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": second})
	assertHealthy(t, rebuilt)
}

func TestAuthoritativePush_StaleOldValueIsRejected(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	first := commitTo(t, fx.dir, "main", "one")
	if err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: "", New: first}}); err != nil {
		t.Fatalf("AuthoritativePush: %v", err)
	}
	// Another instance moved main while this client was preparing its push.
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/main"] = hashB
	})

	second := commitTo(t, fx.dir, "main", "two")
	err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: first, New: second}})
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("err = %v, want ErrStaleRef", err)
	}
	var stale *StaleRefError
	if !errors.As(err, &stale) || stale.Ref != "refs/heads/main" {
		t.Fatalf("err = %v, want it to name refs/heads/main", err)
	}

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/heads/main"] != hashB {
		t.Errorf("refs[main] = %s, want %s untouched: a rejected push wrote anyway",
			ix.Refs["refs/heads/main"], hashB)
	}
}

func TestAuthoritativePush_CreatingARefThatAlreadyExistsIsStale(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	head := commitTo(t, fx.dir, "main", "one")
	// Another instance created the branch first; this client still believes it
	// is absent.
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/main"] = hashA
	})

	err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: zeroHash, New: head}})
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("err = %v, want ErrStaleRef", err)
	}
}

func TestAuthoritativePush_MirrorSemanticsAreNotUsed(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	head := commitTo(t, fx.dir, "main", "one")
	if err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: "", New: head}}); err != nil {
		t.Fatalf("AuthoritativePush: %v", err)
	}
	// Same value again, but claiming absence: ShadowPush would skip this as a
	// no-op; the authoritative path must reject it, because the client's view
	// of the world is provably wrong.
	err := AuthoritativePush(ctx, fx.store, fx.dir, storagePath,
		[]RefUpdate{{Ref: "refs/heads/main", Old: "", New: head}})
	if !errors.Is(err, ErrStaleRef) {
		t.Fatalf("err = %v, want ErrStaleRef", err)
	}
}

func TestAdoptIfConverged_StampsAndMakesTheNextMaterializeANoop(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	if err := AdoptIfConverged(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("AdoptIfConverged: %v", err)
	}
	_, gen := readIndexOrFail(t, fx.store)
	if got := LocalGeneration(fx.dir); got != gen {
		t.Fatalf("local generation = %d, want %d", got, gen)
	}

	// The proof that the stamp was believed: Materialize fetches nothing.
	before := fx.store.putCalls
	if err := Materialize(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if fx.store.putCalls != before {
		t.Error("Materialize wrote after a converged adopt")
	}
	assertRefs(t, fx.dir, map[string]string{"refs/heads/main": head})
	assertHealthy(t, fx.dir)
}

func TestAdoptIfConverged_DoesNothingWhenRefsDiffer(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	// A ref only the index knows about: the copy is not the index's projection,
	// so claiming it is would hide the difference forever.
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/other"] = head
	})
	if err := AdoptIfConverged(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("AdoptIfConverged: %v", err)
	}
	if got := LocalGeneration(fx.dir); got != 0 {
		t.Errorf("local generation = %d, want 0: a diverged copy was stamped", got)
	}

	// And the next Materialize repairs it rather than trusting the copy.
	if err := Materialize(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, fx.dir, map[string]string{"refs/heads/main": head, "refs/heads/other": head})
}

func TestAdoptIfConverged_WithoutAnIndexDoesNothing(t *testing.T) {
	fx := newPushFixture(t)
	commitTo(t, fx.dir, "main", "one")
	if err := AdoptIfConverged(context.Background(), fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("AdoptIfConverged: %v", err)
	}
	if got := LocalGeneration(fx.dir); got != 0 {
		t.Errorf("local generation = %d, want 0", got)
	}
}

func TestAdoptIfConverged_StampedCopyStillAppliesLaterEntriesIncrementally(t *testing.T) {
	fx := newPushFixture(t)
	ctx := context.Background()
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	if err := AdoptIfConverged(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("AdoptIfConverged: %v", err)
	}

	// Another instance pushes a branch this copy has never seen. The stamp is
	// now stale, and Materialize must apply the difference on top rather than
	// either trusting it or rebuilding from scratch.
	other := newBare(t, t.TempDir(), "other.git")
	gitRun(t, other, "fetch", fx.dir, "refs/heads/main:refs/heads/main")
	side := commitTo(t, other, "side", "side one")
	if err := ShadowPush(ctx, fx.store, other, storagePath,
		[]RefUpdate{{Ref: "refs/heads/side", Old: "", New: side}}); err != nil {
		t.Fatalf("ShadowPush from the other instance: %v", err)
	}

	if err := Materialize(ctx, fx.store, fx.dir, storagePath); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertRefs(t, fx.dir, map[string]string{"refs/heads/main": first, "refs/heads/side": side})
	assertHealthy(t, fx.dir)
	if got := gitRun(t, fx.dir, "rev-list", "--count", "refs/heads/side"); got != "1" {
		t.Errorf("side commit count = %s, want 1", got)
	}
}

// commitInQuarantine builds a commit whose objects land in quarantineDir
// instead of the repository, reproducing what receive-pack does before it runs
// the pre-receive hook.
func commitInQuarantine(t *testing.T, gitDir, quarantineDir, parent, content string) string {
	t.Helper()
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatalf("mkdir quarantine: %v", err)
	}
	run := func(stdin string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = gitDir
		cmd.Env = append(testGitEnv(),
			"GIT_DIR="+gitDir,
			"GIT_OBJECT_DIRECTORY="+quarantineDir,
			"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(gitDir, "objects"),
		)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out))
	}
	blob := run(content, "hash-object", "-w", "--stdin")
	tree := run(fmt.Sprintf("100644 blob %s\tfile.txt\n", blob), "mktree")
	args := []string{"commit-tree", tree, "-m", "quarantined " + content}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return run("", args...)
}

func TestShadowPush_SeesObjectsStillInQuarantine(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	quarantine := filepath.Join(t.TempDir(), "quarantine")
	pushed := commitInQuarantine(t, fx.dir, quarantine, head, "two")
	// rev-parse would happily echo a raw hash back, so ask whether the object
	// actually resolves. It must not, or the fixture is not testing quarantine.
	if known, err := knownObjects(context.Background(), fx.dir, []string{pushed}); err != nil {
		t.Fatalf("knownObjects: %v", err)
	} else if len(known) != 0 {
		t.Fatal("the quarantined commit resolves without the quarantine environment; the fixture is wrong")
	}

	// A pre-receive hook inherits these from receive-pack. gitEnv shuts the
	// ambient environment out, so without the passthrough pack-objects would
	// reject the pushed commit as a bad revision and every push would fail.
	t.Setenv("GIT_QUARANTINE_PATH", quarantine)
	t.Setenv("GIT_OBJECT_DIRECTORY", quarantine)
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(fx.dir, "objects"))

	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: head, New: pushed})

	ix, _ := readIndexOrFail(t, fx.store)
	if ix.Refs["refs/heads/main"] != pushed {
		t.Fatalf("refs[main] = %s, want %s", ix.Refs["refs/heads/main"], pushed)
	}
	if got := storedPackObjectCount(t, fx.store, ix.Entries[1]); got != 3 {
		t.Errorf("entry holds %d objects, want 3: the exclude set was not resolvable either", got)
	}

	// Leave the hook's environment before rebuilding: git refuses ref updates
	// inside a quarantine, which is precisely why the passthrough is scoped to
	// object lookups and Materialize never runs from a hook.
	for _, k := range quarantineVars {
		os.Unsetenv(k)
	}
	rebuilt := fx.rebuild(t)
	assertRefs(t, rebuilt, map[string]string{"refs/heads/main": pushed})
	assertHealthy(t, rebuilt)
	if got := gitRun(t, rebuilt, "rev-list", "--count", "refs/heads/main"); got != "2" {
		t.Errorf("commit count = %s, want 2", got)
	}
}

func TestKnownObjects_DropsWhatTheRepositoryCannotResolve(t *testing.T) {
	requireGit(t)
	dir := newBare(t, t.TempDir(), "pd.git")
	head := commitTo(t, dir, "main", "one")

	got, err := knownObjects(context.Background(), dir, []string{head, hashC, head, "", zeroHash})
	if err != nil {
		t.Fatalf("knownObjects: %v", err)
	}
	if len(got) != 1 || got[0] != head {
		t.Errorf("knownObjects = %v, want [%s]", got, head)
	}
	if got, err := knownObjects(context.Background(), dir, nil); err != nil || got != nil {
		t.Errorf("knownObjects(nil) = %v, %v, want nil, nil", got, err)
	}
}
