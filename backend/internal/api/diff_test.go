// Tests for the commit diff endpoint (diff.go).
//
// What is being pinned down here is not "a diff comes back" but the three
// distinctions the response type exists to keep: added / modified / deleted,
// a root commit's null parent, and "no lines were counted" (binary, LFS) as
// something other than "zero lines changed". The last one is the reason
// DiffFile carries Binary / LFS / HasPatch at all -- a bare 0 in Additions
// reads as a fact, and for a binary file it is not one.
//
// The fixture is revision_test.go's, the same one repotree_test.go and
// refs_test.go drive: a real Server over real HTTP against a real git
// repository on disk.

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// commitOps commits an arbitrary batch of operations, which the fixture's own
// commit helper cannot do -- it always writes README.md.
func (f *revisionFixture) commitOps(r *store.Repo, branch, message string, ops ...gitrepo.Op) plumbing.Hash {
	f.t.Helper()
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	h, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch:  branch,
		Message: message,
		Author:  gitrepo.Signature{Name: "alice", Email: "alice@example.com", When: time.Now()},
		Ops:     ops,
	})
	if err != nil {
		f.t.Fatalf("commit %q: %v", message, err)
	}
	return h
}

func addOp(path, content string) gitrepo.Op {
	return gitrepo.Op{Kind: gitrepo.OpAdd, Path: path, Data: []byte(content)}
}

func addBytesOp(path string, data []byte) gitrepo.Op {
	return gitrepo.Op{Kind: gitrepo.OpAdd, Path: path, Data: data}
}

func delOp(path string) gitrepo.Op {
	return gitrepo.Op{Kind: gitrepo.OpDelete, Path: path}
}

// diffFile mirrors apitypes.DiffFile on the wire, so a rename of a JSON tag
// fails these tests rather than silently reading as a zero value.
type diffFile struct {
	Path           string `json:"path"`
	Status         string `json:"status"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	Binary         bool   `json:"binary"`
	LFS            bool   `json:"lfs"`
	HasPatch       bool   `json:"has_patch"`
	NoPatchReason  string `json:"no_patch_reason"`
	Patch          string `json:"patch"`
	PatchTruncated bool   `json:"patch_truncated"`
	OldSize        int64  `json:"old_size"`
	Size           int64  `json:"size"`
}

type commitDiff struct {
	Commit struct {
		OID     string `json:"oid"`
		Message string `json:"message"`
	} `json:"commit"`
	ParentOID      *string    `json:"parent_oid"`
	Files          []diffFile `json:"files"`
	NumFiles       int        `json:"num_files"`
	FilesTruncated bool       `json:"files_truncated"`
	Additions      int        `json:"additions"`
	Deletions      int        `json:"deletions"`
}

func (f *revisionFixture) diff(t *testing.T, rev string) commitDiff {
	t.Helper()
	resp := f.do("GET", "/api/v1/repos/model/alice/foo/diff/"+rev, "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var out commitDiff
	resp.json(t, &out)
	return out
}

// byPath finds one file in a diff, failing the test when it is missing --
// every assertion below is about a specific path, and a nil-map lookup that
// silently yields a zero DiffFile would pass for the wrong reason.
func byPath(t *testing.T, d commitDiff, path string) diffFile {
	t.Helper()
	for _, f := range d.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no entry for %q in %+v", path, d.Files)
	return diffFile{}
}

// ------------------------------------------------------- added / modified / deleted

// One commit that does all three things at once, so the statuses cannot be
// right only because each was tested in isolation.
func TestRepoDiff_AddModifyDelete(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed",
		addOp("keep.txt", "one\ntwo\nthree\n"),
		addOp("gone.txt", "bye\nbye again\n"))
	f.commitOps(repo, "main", "second",
		addOp("keep.txt", "one\nTWO\nthree\nfour\n"),
		addOp("new.txt", "hello\nworld\n"),
		delOp("gone.txt"))

	d := f.diff(t, "main")
	if d.Commit.Message != "second" {
		t.Fatalf("commit = %+v, want the second commit", d.Commit)
	}
	if d.ParentOID == nil {
		t.Fatal("parent_oid = null, want the first commit")
	}
	if d.NumFiles != 3 || len(d.Files) != 3 || d.FilesTruncated {
		t.Fatalf("num_files = %d, files = %d, truncated = %v; want 3, 3, false",
			d.NumFiles, len(d.Files), d.FilesTruncated)
	}

	added := byPath(t, d, "new.txt")
	if added.Status != "added" {
		t.Fatalf("new.txt status = %q, want added", added.Status)
	}
	if added.Additions != 2 || added.Deletions != 0 {
		t.Fatalf("new.txt +%d/-%d, want +2/-0", added.Additions, added.Deletions)
	}
	if added.OldSize != 0 || added.Size == 0 {
		t.Fatalf("new.txt old_size = %d, size = %d; want 0 and non-zero", added.OldSize, added.Size)
	}
	if !added.HasPatch || !strings.Contains(added.Patch, "+hello") {
		t.Fatalf("new.txt patch = %q (has_patch = %v), want the added lines", added.Patch, added.HasPatch)
	}

	modified := byPath(t, d, "keep.txt")
	if modified.Status != "modified" {
		t.Fatalf("keep.txt status = %q, want modified", modified.Status)
	}
	// "two" became "TWO" (one line each way) and "four" was appended.
	if modified.Additions != 2 || modified.Deletions != 1 {
		t.Fatalf("keep.txt +%d/-%d, want +2/-1", modified.Additions, modified.Deletions)
	}
	if modified.OldSize == 0 || modified.Size == 0 {
		t.Fatalf("keep.txt old_size = %d, size = %d; want both non-zero", modified.OldSize, modified.Size)
	}
	if !modified.HasPatch || !strings.HasPrefix(modified.Patch, "@@ ") {
		t.Fatalf("keep.txt patch = %q, want hunks with no `diff --git` preamble", modified.Patch)
	}

	deleted := byPath(t, d, "gone.txt")
	if deleted.Status != "deleted" {
		t.Fatalf("gone.txt status = %q, want deleted", deleted.Status)
	}
	if deleted.Additions != 0 || deleted.Deletions != 2 {
		t.Fatalf("gone.txt +%d/-%d, want +0/-2", deleted.Additions, deleted.Deletions)
	}
	if deleted.OldSize == 0 || deleted.Size != 0 {
		t.Fatalf("gone.txt old_size = %d, size = %d; want non-zero and 0", deleted.OldSize, deleted.Size)
	}
	if !deleted.HasPatch || !strings.Contains(deleted.Patch, "-bye") {
		t.Fatalf("gone.txt patch = %q, want the removed lines", deleted.Patch)
	}

	if d.Additions != 4 || d.Deletions != 3 {
		t.Fatalf("totals +%d/-%d, want +4/-3", d.Additions, d.Deletions)
	}
}

// ------------------------------------------------------------------ root commit

// The root commit has no parent to diff against, so parent_oid is null and
// every path in it is added. Reporting the zero hash instead would make the
// UI offer a link to a commit that does not exist.
func TestRepoDiff_RootCommitHasNoParentAndAddsEverything(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed",
		addOp("a.txt", "a\n"),
		addOp("dir/b.txt", "b\nb\n"))

	d := f.diff(t, "main")
	if d.ParentOID != nil {
		t.Fatalf("parent_oid = %q, want null for the root commit", *d.ParentOID)
	}
	if d.NumFiles != 2 || len(d.Files) != 2 {
		t.Fatalf("files = %+v, want both paths", d.Files)
	}
	for _, file := range d.Files {
		if file.Status != "added" {
			t.Fatalf("%s status = %q, want added", file.Path, file.Status)
		}
		if file.OldSize != 0 {
			t.Fatalf("%s old_size = %d, want 0 -- it did not exist before", file.Path, file.OldSize)
		}
	}
	if d.Additions != 3 || d.Deletions != 0 {
		t.Fatalf("totals +%d/-%d, want +3/-0", d.Additions, d.Deletions)
	}
}

// A file with no patch says *why* it has none, rather than leaving the reader
// to infer it from Binary and LFS being false. An empty file is neither, and
// inferring "then it was too big" from that reported a 0-byte file as too
// large to diff.
func TestRepoDiff_EmptyFileIsNotReportedAsTooLarge(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed", addOp("readme.txt", "text\n"))
	f.commitOps(repo, "main", "add an empty file", addOp("empty.txt", ""))

	file := byPath(t, f.diff(t, "main"), "empty.txt")
	if file.HasPatch {
		t.Fatalf("empty.txt has_patch = true, want false: there are no lines to render")
	}
	if file.Binary || file.LFS {
		t.Fatalf("empty.txt = %+v, want neither binary nor lfs", file)
	}
	if file.NoPatchReason != "no_text_change" {
		t.Fatalf("empty.txt no_patch_reason = %q, want %q", file.NoPatchReason, "no_text_change")
	}
	if file.Size != 0 {
		t.Fatalf("empty.txt size = %d, want 0", file.Size)
	}
}

// Every branch that withholds a patch names itself, so no caller has to guess.
func TestRepoDiff_NoPatchReasonIsSetWheneverThereIsNoPatch(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed", addOp("readme.txt", "text\n"))
	f.commitOps(repo, "main", "mixed",
		addOp("empty.txt", ""),
		addBytesOp("weights.bin", []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe}),
		addOp("plain.txt", "one\n"))

	for _, tc := range []struct{ path, want string }{
		{"empty.txt", "no_text_change"},
		{"weights.bin", "binary"},
		{"plain.txt", ""},
	} {
		file := byPath(t, f.diff(t, "main"), tc.path)
		if file.NoPatchReason != tc.want {
			t.Errorf("%s no_patch_reason = %q, want %q", tc.path, file.NoPatchReason, tc.want)
		}
		if (file.NoPatchReason == "") != file.HasPatch {
			t.Errorf("%s: no_patch_reason %q and has_patch %v disagree",
				tc.path, file.NoPatchReason, file.HasPatch)
		}
	}
}

// ---------------------------------------------------------------- binary and LFS

// A binary file is flagged rather than diffed, and its line counts stay 0
// *because nothing was counted*. has_patch is what tells the two apart, and it
// is the field a client has to read before showing "+0 -0".
func TestRepoDiff_BinaryFileIsFlaggedWithNoPatch(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed", addOp("readme.txt", "text\n"))
	f.commitOps(repo, "main", "add a binary",
		addBytesOp("weights.bin", []byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe}))

	file := byPath(t, f.diff(t, "main"), "weights.bin")
	if !file.Binary {
		t.Fatalf("weights.bin = %+v, want binary", file)
	}
	if file.HasPatch || file.Patch != "" {
		t.Fatalf("weights.bin has_patch = %v, patch = %q; want no patch", file.HasPatch, file.Patch)
	}
	if file.Additions != 0 || file.Deletions != 0 {
		t.Fatalf("weights.bin +%d/-%d, want the counts left at 0", file.Additions, file.Deletions)
	}
	if file.Size != 6 {
		t.Fatalf("weights.bin size = %d, want 6", file.Size)
	}
}

// Bytes that hold no NUL but are not valid UTF-8 are binary too: the patch
// travels inside a JSON string, so those bytes could only arrive as U+FFFD.
func TestRepoDiff_InvalidUTF8IsBinary(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	f.commitOps(repo, "main", "seed", addOp("readme.txt", "text\n"))
	f.commitOps(repo, "main", "latin-1", addBytesOp("latin1.txt", []byte("caf\xe9\n")))

	file := byPath(t, f.diff(t, "main"), "latin1.txt")
	if !file.Binary || file.HasPatch {
		t.Fatalf("latin1.txt = %+v, want binary with no patch", file)
	}
}

// An LFS pointer is text, so it would diff cleanly -- and the diff would show
// an oid changing, which says nothing about the file. It is reported as LFS
// instead, with the same "counts were not taken" rule as a binary file.
func TestRepoDiff_LFSPointerIsFlaggedInsteadOfDiffed(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	oid := strings.Repeat("a", 64)
	f.commitOps(repo, "main", "seed", addOp("readme.txt", "text\n"))
	f.commitOps(repo, "main", "add a big file",
		addBytesOp("model.safetensors", gitrepo.FormatLFSPointer(oid, 4<<30)))

	file := byPath(t, f.diff(t, "main"), "model.safetensors")
	if !file.LFS {
		t.Fatalf("model.safetensors = %+v, want lfs", file)
	}
	if file.HasPatch || file.Patch != "" {
		t.Fatalf("model.safetensors has_patch = %v, patch = %q; want no patch", file.HasPatch, file.Patch)
	}
	if file.Additions != 0 || file.Deletions != 0 {
		t.Fatalf("model.safetensors +%d/-%d, want the counts left at 0", file.Additions, file.Deletions)
	}
	// The pointer's own size, not the 4 GiB it points at: that is what
	// old_size/size mean everywhere else in this response.
	if file.Size != int64(len(gitrepo.FormatLFSPointer(oid, 4<<30))) {
		t.Fatalf("model.safetensors size = %d, want the pointer's size", file.Size)
	}
}

// -------------------------------------------------------------- bad revisions

// An unknown revision is a 404 carrying X-Error-Code: RevisionNotFound, the
// same answer every other revision-taking endpoint gives. Answering 200 with
// an empty file list would say the commit exists and changed nothing.
func TestRepoDiff_UnknownRevisionIsRevisionNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.do("GET", "/api/v1/repos/model/alice/foo/diff/does-not-exist", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// A repository with no commits is a 404 here rather than the empty 200 the
// tree and commit-list endpoints answer with: this response describes one
// commit, and there is no commit to describe.
func TestRepoDiff_EmptyRepositoryIsRevisionNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.emptyRepo("alice", "foo")

	resp := f.do("GET", "/api/v1/repos/model/alice/foo/diff/main", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// A commit SHA works as the revision, not just a branch name -- the commit
// list links to individual commits, and every one of them is a SHA.
func TestRepoDiff_AcceptsACommitSHA(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.emptyRepo("alice", "foo")
	first := f.commitOps(repo, "main", "seed", addOp("a.txt", "a\n"))
	f.commitOps(repo, "main", "second", addOp("a.txt", "a\nb\n"))

	d := f.diff(t, first.String())
	if d.Commit.OID != first.String() {
		t.Fatalf("commit oid = %q, want %q", d.Commit.OID, first)
	}
	if d.ParentOID != nil {
		t.Fatalf("parent_oid = %q, want null -- the first commit is the root", *d.ParentOID)
	}
}
