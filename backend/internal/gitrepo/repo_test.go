package gitrepo

import (
	"bytes"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// requireGit skips the test when the git binary is not available, since
// Manager.Init shells out to it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
}

// newTestRepo creates a fresh bare repository under t.TempDir() and returns a
// handle plus the manager (for opening again if needed).
func newTestRepo(t *testing.T) (*Manager, *Repo) {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	mgr := NewManager(root)
	if err := mgr.Init("datasets/acme/widgets", "main"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	repo, err := mgr.Open("datasets/acme/widgets")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return mgr, repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// assertRepoHealthy execs `git fsck` and `git cat-file --batch-check` against
// the on-disk bare repository. This is the sharpest tool we have for catching
// a tree built with the wrong sort order: git itself will call it corrupt.
func assertRepoHealthy(t *testing.T, dir string) {
	t.Helper()

	fsckOut := runGit(t, dir, "fsck", "--full", "--strict")
	lower := strings.ToLower(fsckOut)
	if strings.Contains(lower, "corrupt") || strings.Contains(lower, "error") {
		t.Fatalf("git fsck reported a problem:\n%s", fsckOut)
	}

	// --batch-check over every object surfaces "missing"/"corrupt" markers
	// that fsck's exit code alone might not fail on.
	listOut := runGit(t, dir, "cat-file", "--batch-all-objects", "--batch-check")
	for _, line := range strings.Split(strings.TrimSpace(listOut), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "missing") || strings.Contains(line, "corrupt") || strings.Contains(strings.ToLower(line), "error") {
			t.Fatalf("git cat-file --batch-check reported a problem: %q\nfull output:\n%s", line, listOut)
		}
	}
}

func mustCommit(t *testing.T, repo *Repo, branch, msg string, ops ...Op) plumbing.Hash {
	t.Helper()
	newHash, _, err := repo.Commit(CommitRequest{
		Branch:  branch,
		Message: msg,
		Author:  Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     ops,
	})
	if err != nil {
		t.Fatalf("Commit(%q): %v", msg, err)
	}
	return newHash
}

func addOp(path, content string) Op {
	return Op{Kind: OpAdd, Path: path, Data: []byte(content)}
}

// ---------------------------------------------------------- tree sort order

// TestTreeSortOrder_GitCompatible is the highest-risk test in this package:
// git orders tree entries as though directory names carried a trailing
// slash, so "a" (file) < "a" (dir, compared as "a/") is NOT true in a naive
// byte-wise sort when another entry like "a.txt" or "ab.txt" sits between
// them. If dirNode.write's sortTreeEntries got this wrong, git would refuse
// to consider the object valid.
func TestTreeSortOrder_GitCompatible(t *testing.T) {
	_, repo := newTestRepo(t)

	// The classic trap: "a.txt" and "ab.txt" both sort between "a" (as file)
	// and "a/" (as directory, i.e. "a/" > "a.txt" > "a" but "a/" < "ab.txt").
	// Byte-wise: "a" < "a.txt" < "a/" < "ab.txt" (since '.' = 0x2e, '/' = 0x2f
	// < 'b' = 0x62). Git's rule (append "/" to dir names) makes "a/" sort
	// after "a.txt" and before "ab.txt" too - so in this particular set the
	// two orders agree; the real trap is names where the directory's own
	// name, compared *without* the slash, would collate differently than
	// with it. We include both classic cases to be safe.
	ops := []Op{
		addOp("a.txt", "root file a.txt"),
		addOp("ab.txt", "root file ab.txt"),
		addOp("a/b.txt", "nested under directory a"),
		// "a-b" sorts before "a/" only when comparing "a-b" vs "a/" (since
		// '-' = 0x2d < '/' = 0x2f); this is the pairing most likely to break
		// under a sort that forgets the trailing slash trick, because
		// without it "a" (bare) < "a-b" < "ab.txt" while WITH the trailing
		// slash "a/" moves after "a-b" but must still come before "ab.txt".
		addOp("a-b", "a dash b sibling file"),
	}
	mustCommit(t, repo, "main", "seed tricky sort order", ops...)

	assertRepoHealthy(t, repo.Dir())

	// Cross-check the resulting root tree against `git ls-tree`, which is
	// git's own authority on what order is "correct".
	lsOut := runGit(t, repo.Dir(), "ls-tree", "main")
	var gitNames []string
	for _, line := range strings.Split(strings.TrimSpace(lsOut), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		gitNames = append(gitNames, fields[len(fields)-1])
	}

	entries, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var ourNames []string
	for _, e := range entries {
		ourNames = append(ourNames, e.Name)
	}

	if len(gitNames) != len(ourNames) {
		t.Fatalf("entry count mismatch: git ls-tree=%v ours=%v", gitNames, ourNames)
	}
	for i := range gitNames {
		if gitNames[i] != ourNames[i] {
			t.Fatalf("order mismatch at index %d: git ls-tree=%v ours=%v", i, gitNames, ourNames)
		}
	}
}

// TestTreeSortOrder_ManyTrickyNames throws a wider net of names known to be
// sensitive to the "compare directories as if slash-terminated" rule, and
// relies on git fsck to catch any mistake.
func TestTreeSortOrder_ManyTrickyNames(t *testing.T) {
	_, repo := newTestRepo(t)

	names := []string{
		"a", "a.txt", "a/x.txt", "ab.txt", "a-b", "a+", "a.b",
		"b", "b.txt", "b/y.txt", "b-c",
		"z", "z.z", "z/inner.txt",
	}
	var ops []Op
	for _, n := range names {
		// Skip the bare "a"/"b"/"z" files here since we also create a/,b/,z/
		// directories below - a path can't be both a file and a dir in one
		// commit's op list applied sequentially, so add plain files first,
		// nested paths second; setBlob overwrites a same-named dir/file as
		// git allows only one or the other to win in the final tree state.
		if n == "a" || n == "b" || n == "z" {
			continue
		}
		ops = append(ops, addOp(n, "content of "+n))
	}
	mustCommit(t, repo, "main", "wide sort-order sweep", ops...)
	assertRepoHealthy(t, repo.Dir())

	// Verify recursive listing matches git's own recursive ls-tree, path by
	// path, in order.
	lsOut := runGit(t, repo.Dir(), "ls-tree", "-r", "--name-only", "main")
	gitPaths := strings.Split(strings.TrimSpace(lsOut), "\n")

	entries, _, err := repo.Tree("main", "", true)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var ourPaths []string
	for _, e := range entries {
		if !e.IsDir {
			ourPaths = append(ourPaths, e.Path)
		}
	}
	sort.Strings(gitPaths)
	sortedOurs := append([]string(nil), ourPaths...)
	sort.Strings(sortedOurs)
	if len(gitPaths) != len(sortedOurs) {
		t.Fatalf("path set mismatch: git=%v ours=%v", gitPaths, sortedOurs)
	}
	for i := range gitPaths {
		if gitPaths[i] != sortedOurs[i] {
			t.Fatalf("path set mismatch at %d: git=%v ours=%v", i, gitPaths[i], sortedOurs[i])
		}
	}
}

// ------------------------------------------------------------- basic commit ops

func TestCommit_NestedAddUpdateDelete(t *testing.T) {
	_, repo := newTestRepo(t)

	// A sibling file at the root keeps the repository non-empty once the
	// nested file is deleted below; see TestCommit_DeletingLastFileEverywhere
	// for the case where the repository ends up completely empty.
	mustCommit(t, repo, "main", "seed sibling", addOp("keep.txt", "k"))

	mustCommit(t, repo, "main", "add nested file", addOp("a/b/c/d.txt", "v1"))
	data, err := repo.ReadFile("main", "a/b/c/d.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "v1" {
		t.Fatalf("content = %q, want %q", data, "v1")
	}

	mustCommit(t, repo, "main", "update nested file", addOp("a/b/c/d.txt", "v2"))
	data, err = repo.ReadFile("main", "a/b/c/d.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile after update: %v", err)
	}
	if string(data) != "v2" {
		t.Fatalf("content after update = %q, want %q", data, "v2")
	}

	mustCommit(t, repo, "main", "delete nested file", Op{Kind: OpDelete, Path: "a/b/c/d.txt"})
	if _, err := repo.ReadFile("main", "a/b/c/d.txt", 0); err != ErrPathNotFound {
		t.Fatalf("ReadFile after delete: err = %v, want ErrPathNotFound", err)
	}

	assertRepoHealthy(t, repo.Dir())
}

// TestCommit_DeletingLastFileEverywhere covers the case where a commit empties
// the repository. dirNode.write reports an empty directory as the zero hash so
// a parent can drop the entry, but the root has no parent: the commit must
// point at git's canonical empty tree instead, or the commit is unreadable and
// `git fsck` reports a broken link.
func TestCommit_DeletingLastFileEverywhere(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "seed", addOp("only.txt", "x"))
	newHash, _, err := repo.Commit(CommitRequest{
		Branch:  "main",
		Message: "delete the only file",
		Author:  Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []Op{{Kind: OpDelete, Path: "only.txt"}},
	})
	if err != nil {
		t.Fatalf("Commit(delete last file): %v", err)
	}

	commitObj, err := repo.CommitObject(newHash)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	const emptyTreeSHA1 = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if got := commitObj.TreeHash.String(); got != emptyTreeSHA1 {
		t.Fatalf("TreeHash = %s, want git's empty tree %s", got, emptyTreeSHA1)
	}

	entries, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree after emptying the repository: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Tree returned %d entries, want 0: %+v", len(entries), entries)
	}
	assertRepoHealthy(t, repo.Dir())
}

func TestCommit_EmptyDirectoryDropsFromTree(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "add two files under dir",
		addOp("dir/only.txt", "x"), addOp("keep.txt", "y"))

	// Directory should be visible before deletion.
	if _, _, err := repo.Stat("main", "dir"); err != nil {
		t.Fatalf("Stat(dir) before delete: %v", err)
	}

	mustCommit(t, repo, "main", "delete the only file in dir", Op{Kind: OpDelete, Path: "dir/only.txt"})

	// Git does not allow empty trees inside a tree; "dir" must be gone.
	if _, _, err := repo.Stat("main", "dir"); err != ErrPathNotFound {
		t.Fatalf("Stat(dir) after emptying: err = %v, want ErrPathNotFound", err)
	}
	entries, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	for _, e := range entries {
		if e.Name == "dir" {
			t.Fatalf("empty directory %q still present in root tree: %+v", e.Name, entries)
		}
	}

	assertRepoHealthy(t, repo.Dir())
}

func TestCommit_OpDeleteDir(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "seed",
		addOp("dir/a.txt", "a"), addOp("dir/sub/b.txt", "b"), addOp("keep.txt", "k"))

	mustCommit(t, repo, "main", "delete whole dir", Op{Kind: OpDeleteDir, Path: "dir"})

	if _, _, err := repo.Stat("main", "dir"); err != ErrPathNotFound {
		t.Fatalf("Stat(dir) after OpDeleteDir: err = %v, want ErrPathNotFound", err)
	}
	if _, err := repo.ReadFile("main", "keep.txt", 0); err != nil {
		t.Fatalf("keep.txt should survive OpDeleteDir: %v", err)
	}

	assertRepoHealthy(t, repo.Dir())
}

func TestCommit_NoopReturnsExistingHead(t *testing.T) {
	_, repo := newTestRepo(t)

	first := mustCommit(t, repo, "main", "seed", addOp("f.txt", "same"))

	// Re-apply the identical content; AllowNoop defaults to false so no new
	// commit should be created.
	second, oldHash, err := repo.Commit(CommitRequest{
		Branch:  "main",
		Message: "no-op",
		Author:  Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []Op{addOp("f.txt", "same")},
	})
	if err != nil {
		t.Fatalf("Commit (noop): %v", err)
	}
	if second != first {
		t.Fatalf("noop commit produced a new hash: got %s, want existing head %s", second, first)
	}
	if oldHash != first {
		t.Fatalf("noop oldHash = %s, want %s", oldHash, first)
	}

	head := repo.HeadSHA()
	if head != first.String() {
		t.Fatalf("HEAD after noop = %s, want unchanged %s", head, first)
	}
}

func TestCommit_AllowNoopCreatesEmptyCommit(t *testing.T) {
	_, repo := newTestRepo(t)

	first := mustCommit(t, repo, "main", "seed", addOp("f.txt", "same"))

	newHash, oldHash, err := repo.Commit(CommitRequest{
		Branch:    "main",
		Message:   "explicit noop",
		Author:    Signature{Name: "tester", Email: "tester@example.com"},
		Ops:       []Op{addOp("f.txt", "same")},
		AllowNoop: true,
	})
	if err != nil {
		t.Fatalf("Commit (AllowNoop): %v", err)
	}
	if newHash == first {
		t.Fatalf("AllowNoop=true should create a new commit even with identical tree, got same hash %s", newHash)
	}
	if oldHash != first {
		t.Fatalf("oldHash = %s, want %s", oldHash, first)
	}
}

func TestCommit_UnchangedSubtreeReusesHash(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "seed",
		addOp("untouched/x.txt", "x"), addOp("untouched/y.txt", "y"), addOp("touched.txt", "1"))

	entriesBefore, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree before: %v", err)
	}
	var untouchedHashBefore plumbing.Hash
	for _, e := range entriesBefore {
		if e.Name == "untouched" {
			untouchedHashBefore = e.Hash
		}
	}
	if untouchedHashBefore.IsZero() {
		t.Fatalf("did not find 'untouched' directory in tree: %+v", entriesBefore)
	}

	mustCommit(t, repo, "main", "touch unrelated file", addOp("touched.txt", "2"))

	entriesAfter, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree after: %v", err)
	}
	var untouchedHashAfter plumbing.Hash
	for _, e := range entriesAfter {
		if e.Name == "untouched" {
			untouchedHashAfter = e.Hash
		}
	}
	if untouchedHashAfter.IsZero() {
		t.Fatalf("did not find 'untouched' directory in tree after: %+v", entriesAfter)
	}
	if untouchedHashBefore != untouchedHashAfter {
		t.Fatalf("untouched subtree hash changed: before=%s after=%s (dirNode should have reused the hash unmodified)",
			untouchedHashBefore, untouchedHashAfter)
	}
}

func TestCommit_FirstCommitNoParentAndHeadPointsAtBranch(t *testing.T) {
	_, repo := newTestRepo(t)

	if !repo.IsEmpty() {
		t.Fatalf("freshly initialised repo should be empty")
	}

	h := mustCommit(t, repo, "main", "first commit", addOp("README.md", "hello"))

	commitObj, err := repo.CommitObject(h)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	if commitObj.NumParents() != 0 {
		t.Fatalf("first commit should have no parents, got %d", commitObj.NumParents())
	}

	head := repo.HeadSHA()
	if head != h.String() {
		t.Fatalf("HeadSHA() = %s, want %s", head, h)
	}
	branches, err := repo.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	found := false
	for _, b := range branches {
		if b == "main" {
			found = true
		}
	}
	if !found {
		t.Fatalf("branches = %v, want to contain %q", branches, "main")
	}

	// HEAD must be a symbolic ref pointing at refs/heads/main so a plain
	// `git clone` lands on a real branch.
	out := runGit(t, repo.Dir(), "symbolic-ref", "HEAD")
	if strings.TrimSpace(out) != "refs/heads/main" {
		t.Fatalf("HEAD symbolic-ref = %q, want refs/heads/main", strings.TrimSpace(out))
	}

	assertRepoHealthy(t, repo.Dir())
}

// ------------------------------------------------------------- validatePath

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"a.txt", false},
		{"dir/a.txt", false},
		{"dir/sub/a.txt", false},
		{"../escape.txt", true},
		{"dir/../escape.txt", true},
		{"dir/..", true},
		{".git/config", true},
		{".git", true},
		{".GIT/config", true},    // git matches .git case-insensitively
		{"dir/.git/hooks", true}, // and at any depth, not just the root
		{"dir/.Git", true},
		{"dir/a\x00.txt", true}, // NUL is never valid in a path
		{"dir//a.txt", true},    // empty segment
		{"", true},              // empty overall (handled before validatePath but check directly too)
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestCommit_RejectsInvalidBranchName(t *testing.T) {
	// The branch comes from a URL segment and becomes a path under refs/, so
	// a name git itself would reject must never reach the reference store.
	for _, branch := range []string{"..", "bad~name", "with space", "-leading-dash"} {
		_, repo := newTestRepo(t)
		_, _, err := repo.Commit(CommitRequest{
			Branch:  branch,
			Message: "attempt bad ref",
			Author:  Signature{Name: "tester", Email: "tester@example.com"},
			Ops:     []Op{addOp("a.txt", "x")},
		})
		if err == nil {
			t.Errorf("Commit to branch %q should have failed", branch)
		}
	}
}

func TestCommit_RejectsDotDotPath(t *testing.T) {
	_, repo := newTestRepo(t)
	_, _, err := repo.Commit(CommitRequest{
		Branch:  "main",
		Message: "attempt escape",
		Author:  Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []Op{addOp("../escape.txt", "x")},
	})
	if err == nil {
		t.Fatalf("Commit with '..' in path should have failed")
	}
}

func TestCommit_RejectsDotGitPath(t *testing.T) {
	_, repo := newTestRepo(t)
	_, _, err := repo.Commit(CommitRequest{
		Branch:  "main",
		Message: "attempt .git write",
		Author:  Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []Op{addOp(".git/hooks/pre-commit", "x")},
	})
	if err == nil {
		t.Fatalf("Commit writing inside .git/ should have failed")
	}
}

// -------------------------------------------------------------- Tree/Stat/ReadFile

func TestTree_RecursiveAndStat(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "seed",
		addOp("top.txt", "t"), addOp("dir/mid.txt", "m"), addOp("dir/sub/deep.txt", "d"))

	// Non-recursive at root only lists direct children.
	entries, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree non-recursive: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("root non-recursive entries = %d, want 2 (top.txt, dir): %+v", len(entries), entries)
	}

	// Recursive lists everything, including nested dirs and files.
	all, _, err := repo.Tree("main", "", true)
	if err != nil {
		t.Fatalf("Tree recursive: %v", err)
	}
	wantPaths := map[string]bool{"top.txt": true, "dir": true, "dir/mid.txt": true, "dir/sub": true, "dir/sub/deep.txt": true}
	if len(all) != len(wantPaths) {
		t.Fatalf("recursive entries = %d, want %d: %+v", len(all), len(wantPaths), all)
	}
	for _, e := range all {
		if !wantPaths[e.Path] {
			t.Errorf("unexpected path in recursive tree: %q", e.Path)
		}
	}

	// Stat on a directory.
	dirEntry, _, err := repo.Stat("main", "dir")
	if err != nil {
		t.Fatalf("Stat(dir): %v", err)
	}
	if !dirEntry.IsDir {
		t.Fatalf("Stat(dir).IsDir = false, want true")
	}

	// Stat on a file.
	fileEntry, _, err := repo.Stat("main", "dir/mid.txt")
	if err != nil {
		t.Fatalf("Stat(dir/mid.txt): %v", err)
	}
	if fileEntry.IsDir {
		t.Fatalf("Stat(dir/mid.txt).IsDir = true, want false")
	}
	if fileEntry.Size != 1 {
		t.Fatalf("Stat(dir/mid.txt).Size = %d, want 1", fileEntry.Size)
	}
}

// ReadBlob/ReadFile must let callers tell "too large" apart from "missing"
// or any other failure, so a caller can degrade gracefully instead of
// reporting a file that exists as if it didn't.
func TestReadFile_TooLargeIsDistinguishableFromMissing(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "seed", addOp("big.txt", strings.Repeat("x", 100)))

	entry, _, err := repo.Stat("main", "big.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	// Over the limit: ErrBlobTooLarge, not just any error.
	if _, err := repo.ReadBlob(entry.Hash, 10); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("ReadBlob over limit: err = %v, want ErrBlobTooLarge", err)
	}
	if _, err := repo.ReadFile("main", "big.txt", 10); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("ReadFile over limit: err = %v, want ErrBlobTooLarge", err)
	}

	// Within the limit: no error at all.
	if _, err := repo.ReadFile("main", "big.txt", 1000); err != nil {
		t.Fatalf("ReadFile within limit: %v", err)
	}

	// A genuinely missing file must not be ErrBlobTooLarge.
	if _, err := repo.ReadFile("main", "missing.txt", 10); err == nil || errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("ReadFile missing: err = %v, want a non-ErrBlobTooLarge error", err)
	}
}

func TestTree_LFSPointerPopulatesEntry(t *testing.T) {
	_, repo := newTestRepo(t)
	pointer := FormatLFSPointer("a"+strings.Repeat("b", 63), 123456)
	mustCommit(t, repo, "main", "add lfs pointer", Op{Kind: OpAdd, Path: "big.bin", Data: pointer})

	entry, _, err := repo.Stat("main", "big.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.LFS == nil {
		t.Fatalf("entry.LFS is nil, want populated pointer")
	}
	if entry.LFS.Size != 123456 {
		t.Fatalf("entry.LFS.Size = %d, want 123456", entry.LFS.Size)
	}
	if entry.TargetSize() != 123456 {
		t.Fatalf("entry.TargetSize() = %d, want 123456 (the real object size, not the pointer's own %d bytes)",
			entry.TargetSize(), entry.Size)
	}
	if entry.Size == entry.TargetSize() {
		t.Fatalf("pointer's own blob size (%d) should differ from the target size (%d) for this test to be meaningful",
			entry.Size, entry.TargetSize())
	}
}

func TestStat_NonexistentPath(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "seed", addOp("f.txt", "x"))

	if _, _, err := repo.Stat("main", "does/not/exist.txt"); err != ErrPathNotFound {
		t.Fatalf("Stat on missing path: err = %v, want ErrPathNotFound", err)
	}
}

// ----------------------------------------------------------------------- Diff

func TestDiff_AddModifyDelete(t *testing.T) {
	_, repo := newTestRepo(t)

	h1 := mustCommit(t, repo, "main", "seed",
		addOp("keep.txt", "k"), addOp("change.txt", "v1"), addOp("gone.txt", "bye"))

	h2 := mustCommit(t, repo, "main", "mutate",
		addOp("change.txt", "v2"), addOp("new.txt", "n"), Op{Kind: OpDelete, Path: "gone.txt"})

	changes, err := repo.Diff(h1, h2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	byPath := map[string]ChangeKind{}
	for _, c := range changes {
		byPath[c.Path] = c.Kind
	}
	want := map[string]ChangeKind{
		"change.txt": ChangeModify,
		"new.txt":    ChangeAdd,
		"gone.txt":   ChangeDelete,
	}
	if len(byPath) != len(want) {
		t.Fatalf("Diff produced %d changes, want %d: %+v", len(byPath), len(want), changes)
	}
	for p, wantKind := range want {
		gotKind, ok := byPath[p]
		if !ok {
			t.Errorf("Diff missing expected change for %q", p)
			continue
		}
		if gotKind != wantKind {
			t.Errorf("Diff(%q) kind = %v, want %v", p, gotKind, wantKind)
		}
	}
	if _, ok := byPath["keep.txt"]; ok {
		t.Errorf("Diff reported a change for untouched file %q", "keep.txt")
	}
}

func TestDiff_ZeroOldHashMeansEverythingIsNew(t *testing.T) {
	_, repo := newTestRepo(t)
	h := mustCommit(t, repo, "main", "seed", addOp("a.txt", "a"), addOp("dir/b.txt", "b"))

	changes, err := repo.Diff(plumbing.ZeroHash, h)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("Diff from zero hash produced %d changes, want 2: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.Kind != ChangeAdd {
			t.Errorf("Diff(zero, h) change %q kind = %v, want ChangeAdd", c.Path, c.Kind)
		}
	}
}

func TestCommit_PathPreconditionMatches(t *testing.T) {
	_, r := newTestRepo(t)
	_, _, err := r.Commit(CommitRequest{Branch: "main", Message: "one", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops: []Op{addOp("file.txt", "v1")}})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	entry, _, err := r.Stat("main", "file.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	_, _, err = r.Commit(CommitRequest{Branch: "main", Message: "two", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops:           []Op{addOp("file.txt", "v2")},
		Preconditions: []PathPrecondition{{Path: "file.txt", OID: entry.Hash.String()}}})
	if err != nil {
		t.Fatalf("commit with matching precondition: %v", err)
	}
}

// The Bugbot regression: a writer that lands between a caller's stale check
// and its Commit must be caught here, under the parent-selection mutex —
// nowhere else is the optimistic lock sound.
func TestCommit_PathPreconditionCatchesInterleavedWrite(t *testing.T) {
	_, r := newTestRepo(t)
	_, _, err := r.Commit(CommitRequest{Branch: "main", Message: "one", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops: []Op{addOp("file.txt", "v1")}})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	staleEntry, _, _ := r.Stat("main", "file.txt") // what a slow caller observed

	// Another writer gets there first.
	if _, _, err := r.Commit(CommitRequest{Branch: "main", Message: "interleaved", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops: []Op{addOp("file.txt", "v2")}}); err != nil {
		t.Fatalf("interleaved commit: %v", err)
	}

	_, _, err = r.Commit(CommitRequest{Branch: "main", Message: "stale", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops:           []Op{addOp("file.txt", "v3")},
		Preconditions: []PathPrecondition{{Path: "file.txt", OID: staleEntry.Hash.String()}}})
	var stale *StalePathError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StalePathError", err)
	}
	if stale.Path != "file.txt" {
		t.Fatalf("stale.Path = %q", stale.Path)
	}
	// And the interleaved content must have survived.
	data, err := r.ReadFile("main", "file.txt", 1<<10)
	if err != nil || string(data) != "v2" {
		t.Fatalf("file = %q err=%v, want the interleaved v2 intact", data, err)
	}
}

func TestCommit_PathPreconditionAbsentMeansCreateOnly(t *testing.T) {
	_, r := newTestRepo(t)
	// Empty OID on an unborn branch: create passes.
	_, _, err := r.Commit(CommitRequest{Branch: "main", Message: "one", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops:           []Op{addOp("file.txt", "v1")},
		Preconditions: []PathPrecondition{{Path: "file.txt", OID: ""}}})
	if err != nil {
		t.Fatalf("create with absent precondition: %v", err)
	}
	// Empty OID once the file exists: rejected.
	_, _, err = r.Commit(CommitRequest{Branch: "main", Message: "two", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops:           []Op{addOp("file.txt", "v2")},
		Preconditions: []PathPrecondition{{Path: "file.txt", OID: ""}}})
	var stale *StalePathError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StalePathError for create-over-existing", err)
	}
}

func TestCommit_PathPreconditionOnUnbornBranchWithOIDIsStale(t *testing.T) {
	_, r := newTestRepo(t)
	_, _, err := r.Commit(CommitRequest{Branch: "main", Message: "one", Author: Signature{Name: "tester", Email: "tester@example.com"},
		Ops:           []Op{addOp("file.txt", "v1")},
		Preconditions: []PathPrecondition{{Path: "file.txt", OID: strings.Repeat("a", 40)}}})
	var stale *StalePathError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want StalePathError on unborn branch", err)
	}
}
