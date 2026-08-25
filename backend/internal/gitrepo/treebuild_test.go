package gitrepo

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// assertNoDuplicateEntries runs the check a fsck-ing client runs. A tree that
// names the same entry twice is not merely untidy: `git fsck --strict` calls
// it duplicateEntries, a clone with transfer.fsckobjects refuses the pack
// outright, and a plain clone succeeds while quietly dropping one of the two.
func assertNoDuplicateEntries(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "fsck", "--full", "--strict")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "duplicateEntries") {
		t.Fatalf("git fsck --strict reported duplicate tree entries:\n%s", out)
	}
	if err != nil {
		t.Fatalf("git fsck --strict: %v\n%s", err, out)
	}
}

// assertCloneWithFsck is the client-side half of the same check: a git client
// configured to verify what it receives must accept the repository.
func assertCloneWithFsck(t *testing.T, dir string) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "clone")
	// --no-local forces a real pack transfer; a local clone hardlinks the
	// object database and never validates anything.
	cmd := exec.Command("git", "-c", "transfer.fsckobjects=true", "clone", "--no-local", "--quiet", dir, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone with transfer.fsckobjects=true failed: %v\n%s", err, out)
	}
}

// TestCommit_FileReplacedByDirectory is the regression test for a commit that
// puts a file underneath a path that is currently a file. Before the fix the
// new subtree was added without removing the blob of the same name, so the
// commit returned success and left a permanently corrupt tree behind.
func TestCommit_FileReplacedByDirectory(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "seed",
		addOp("foo", "i am a file"),
		addOp("keep.txt", "sibling that must survive"),
	)
	mustCommit(t, repo, "main", "turn foo into a directory", addOp("foo/bar", "nested"))

	assertNoDuplicateEntries(t, repo.Dir())
	assertRepoHealthy(t, repo.Dir())
	assertCloneWithFsck(t, repo.Dir())

	entries, _, err := repo.Tree("main", "", false)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var foos int
	for _, e := range entries {
		if e.Name == "foo" {
			foos++
			if !e.IsDir {
				t.Errorf("root entry foo is a file, want the directory that replaced it")
			}
		}
	}
	if foos != 1 {
		t.Fatalf("root tree has %d entries named foo, want exactly 1: %+v", foos, entries)
	}

	// git's own view has to agree, including that the file is gone.
	lsOut := runGit(t, repo.Dir(), "ls-tree", "-r", "main")
	if !strings.Contains(lsOut, "foo/bar") {
		t.Errorf("git ls-tree -r does not list foo/bar:\n%s", lsOut)
	}
	if !strings.Contains(lsOut, "keep.txt") {
		t.Errorf("git ls-tree -r lost the untouched sibling:\n%s", lsOut)
	}
}

// TestCommit_FileReplacedByDirectoryInOneBatch covers the same collision
// arising inside a single commit, where the blob never existed on disk at all.
func TestCommit_FileReplacedByDirectoryInOneBatch(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "file and then a directory of the same name",
		addOp("x", "file first"),
		addOp("x/y", "directory second"),
	)

	assertNoDuplicateEntries(t, repo.Dir())
	assertRepoHealthy(t, repo.Dir())

	lsOut := runGit(t, repo.Dir(), "ls-tree", "-r", "main")
	if strings.Count(lsOut, "\tx") != 1 || !strings.Contains(lsOut, "x/y") {
		t.Fatalf("expected x/y and nothing else named x, got:\n%s", lsOut)
	}
}

// TestCommit_DirectoryReplacedByFile is the mirror image, which already
// worked; it is here so a future change cannot fix one direction by breaking
// the other.
func TestCommit_DirectoryReplacedByFile(t *testing.T) {
	_, repo := newTestRepo(t)

	mustCommit(t, repo, "main", "seed a directory", addOp("foo/bar", "nested"))
	mustCommit(t, repo, "main", "turn foo back into a file", addOp("foo", "i am a file again"))

	assertNoDuplicateEntries(t, repo.Dir())
	assertRepoHealthy(t, repo.Dir())

	lsOut := runGit(t, repo.Dir(), "ls-tree", "-r", "main")
	if strings.Contains(lsOut, "foo/bar") {
		t.Errorf("foo/bar survived the replacement of its parent by a file:\n%s", lsOut)
	}
	if !strings.Contains(lsOut, "\tfoo\n") {
		t.Errorf("foo is not a file in the resulting tree:\n%s", lsOut)
	}
}

// TestDirNodeWriteRefusesNameHeldByBothMaps checks the last line of defence.
// walk and setBlob are supposed to make this state unreachable, but a tree
// with a duplicate name is corruption that no later step notices, so write
// must refuse rather than encode it.
func TestDirNodeWriteRefusesNameHeldByBothMaps(t *testing.T) {
	_, repo := newTestRepo(t)

	blob, err := repo.writeBlob([]byte("content"))
	if err != nil {
		t.Fatalf("writeBlob: %v", err)
	}

	root := newDirNode()
	root.blobs["dup"] = object.TreeEntry{Name: "dup", Mode: filemode.Regular, Hash: blob}
	sub := newDirNode()
	sub.blobs["inner"] = object.TreeEntry{Name: "inner", Mode: filemode.Regular, Hash: blob}
	root.subs["dup"] = sub

	if h, err := root.write(repo.storer()); err == nil {
		t.Fatalf("write encoded a tree with a duplicated name (%s) instead of failing", h)
	} else if !strings.Contains(err.Error(), "dup") {
		t.Fatalf("write error %q does not name the offending entry", err)
	}
}

// TestWalkDropsTheShadowedBlob is the unit-level statement of the same rule,
// without going near git.
func TestWalkDropsTheShadowedBlob(t *testing.T) {
	_, repo := newTestRepo(t)

	root := newDirNode()
	root.blobs["foo"] = object.TreeEntry{Name: "foo", Mode: filemode.Regular, Hash: plumbing.ZeroHash}

	parent, name, err := root.walk(repo.storer(), "foo/bar", true)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if name != "bar" {
		t.Fatalf("walk returned name %q, want bar", name)
	}
	if parent != root.subs["foo"] {
		t.Fatalf("walk did not descend into the newly created foo subtree")
	}
	if _, still := root.blobs["foo"]; still {
		t.Fatalf("walk left the file foo in place beside the directory foo")
	}
}
