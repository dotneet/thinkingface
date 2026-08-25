package syncer

import (
	"bytes"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// seedLFSObject puts an object's bytes at their content-addressed key and
// records the row, linked to owner. It is the state every legitimate upload
// path leaves behind, and the starting point for the pointer-push cases below:
// the bytes and the lfs_objects row already exist, and the only question is
// which repositories are entitled to them.
func seedLFSObject(t *testing.T, h *harness, owner int64, oid string, content []byte) {
	t.Helper()
	if err := h.obj.Put(h.ctx, storage.LFSKey(oid), bytes.NewReader(content), "application/octet-stream"); err != nil {
		t.Fatalf("seed lfs object bytes: %v", err)
	}
	if err := h.st.RecordLFSObject(h.ctx, owner, oid, int64(len(content)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
}

// spareRepo is a second repository in the same harness, used as the original
// owner of an object so it can be deleted out from under the first one.
func spareRepo(t *testing.T, h *harness, name string) *store.Repo {
	t.Helper()
	ns := h.namespace("alice")
	repo, err := h.st.CreateRepo(h.ctx, ns.ID, name, "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo %s: %v", name, err)
	}
	return repo
}

const pointerOID = "1111111111111111111111111111111111111111111111111111111111111111"

// TestPush_LinksLFSObjectsNamedByPointerBlobs covers the push that never goes
// near the LFS batch API: a pointer file committed as an ordinary blob, which
// is what a client without git-lfs installed (or one that cloned with
// GIT_LFS_SKIP_SMUDGE=1 and pushed the pointers back out) sends. The syncer
// recognises the pointer by sniffing the blob, so repo_files gets the oid --
// and the link has to follow it, or the repository holds a file the download
// paths refuse to resolve.
func TestPush_LinksLFSObjectsNamedByPointerBlobs(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("large file bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	f.push("main",
		addOp("README.md", "# foo\n"),
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)

	files, err := f.st.ListRepoFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("list repo files: %v", err)
	}
	var indexed bool
	for _, file := range files {
		if file.Path == "model.bin" && file.LFSOID != nil && *file.LFSOID == pointerOID {
			indexed = true
		}
	}
	if !indexed {
		t.Fatalf("repo_files does not name the oid for model.bin: %+v", files)
	}

	// The predicate every download path asks (resolve, the LFS batch's
	// download branch, the transfer proxy). An indexed pointer whose oid is
	// not linked is a file the repository can list but nobody can fetch.
	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !owned {
		t.Error("the pushed pointer's oid is not linked to the repository; downloads of model.bin would 404")
	}
}

// TestPush_PointerPushKeepsTheObjectOutOfTheOrphanSet is the same hole seen
// from the collector's side, and the expensive half: `thinkingface gc` counts
// references through repo_lfs_objects, so an oid that only repo_files names is
// indistinguishable from one nobody wants. Once the repository that uploaded
// it is deleted, the bytes a live pointer still points at become a deletion
// candidate.
func TestPush_PointerPushKeepsTheObjectOutOfTheOrphanSet(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("large file bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	f.push("main", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
		Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))})

	// The uploader goes away; only the pointer push keeps the object alive.
	if err := f.st.DeleteRepo(f.ctx, other.ID); err != nil {
		t.Fatalf("delete origin repo: %v", err)
	}

	all, err := f.st.ListLFSObjects(f.ctx)
	if err != nil {
		t.Fatalf("list lfs objects: %v", err)
	}
	referenced, err := f.st.ListReferencedLFSOIDs(f.ctx)
	if err != nil {
		t.Fatalf("list referenced lfs oids: %v", err)
	}
	for _, o := range store.OrphanedLFSObjects(all, referenced) {
		if o.OID == pointerOID {
			t.Fatal("gc would collect an object a repo_files row still names: the pushed pointer's bytes")
		}
	}
}

// TestPush_DoesNotLinkAnOIDThatWasNeverUploaded is the limit of the link:
// pointer text is just text, and committing one for content nobody ever sent
// must not manufacture a claim. LinkLFSObjects only links oids lfs_objects
// already knows, so the row stays absent and the download paths keep saying
// the object does not exist.
func TestPush_DoesNotLinkAnOIDThatWasNeverUploaded(t *testing.T) {
	f := newPushFixture(t)
	const ghost = "2222222222222222222222222222222222222222222222222222222222222222"

	f.push("main", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "ghost.bin",
		Data: gitrepo.FormatLFSPointer(ghost, 1234)})

	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, ghost)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if owned {
		t.Error("a pointer for content that was never uploaded produced a link")
	}
}

// TestPush_DoesNotLinkAPointerThatLiesAboutTheSize is the other limit of the
// link, and the one that matters: the object exists, so the previous test's
// "lfs_objects has never heard of it" guard does not apply, and the only thing
// standing between a writer and somebody else's bytes is whether the pointer
// declares their real length.
//
// An oid is public -- every pointer in every readable repository is one -- so
// naming one is not evidence of anything. Declaring its size is. That is the
// rule the LFS batch's dedup enforces (lfs.storedAt, which stopped accepting a
// declared zero for exactly this reason), and a push must not be the way
// around it.
func TestPush_DoesNotLinkAPointerThatLiesAboutTheSize(t *testing.T) {
	content := []byte("somebody else's bytes")

	for _, tc := range []struct {
		name     string
		declared int64
		want     bool
	}{
		{"a guessed size", 1, false},
		// Zero is the guess that used to work on the batch side.
		{"zero", 0, false},
		{"the real size", int64(len(content)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushFixture(t)
			other := spareRepo(t, f.harness, "origin")
			seedLFSObject(t, f.harness, other.ID, pointerOID, content)

			f.push("main", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
				Data: gitrepo.FormatLFSPointer(pointerOID, tc.declared)})

			owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
			if err != nil {
				t.Fatalf("RepoHasLFSObject: %v", err)
			}
			if owned != tc.want {
				t.Errorf("a pointer declaring %d bytes for a %d byte object: linked = %v, want %v",
					tc.declared, len(content), owned, tc.want)
			}
		})
	}
}

// TestPush_AWrongPointerDoesNotSpeakForARightOne is the push-path statement of
// what LinkLFSObjects' (oid, size) keying is for. One revision can name an
// object from two paths -- one pointer written by git-lfs, one hand-made and
// wrong -- and if only the first declaration survived the walk, a bad pointer
// arriving earlier in tree order would cost the good file its link and take
// the object out of gc's reference set. Deduplicating in the syncer by oid
// alone put that back after the store stopped doing it.
func TestPush_AWrongPointerDoesNotSpeakForARightOne(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("somebody else's bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	// "a.bin" sorts before "model.bin", so the lie is what a first-wins
	// deduplication would keep.
	f.push("main",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "a.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, 1)},
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)

	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !owned {
		t.Error("a pointer lying about the size suppressed the link a correct one earned; model.bin would 404")
	}
}
