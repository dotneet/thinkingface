package gitrepo

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"
)

// submoduleSHA is the commit a gitlink points at. It is deliberately an object
// this repository has never stored -- which is the whole situation: a
// submodule's commits live in the submodule's own object database, so nothing
// here can load it, as a blob or as anything else.
const submoduleSHA = "0123456789abcdef0123456789abcdef01234567"

// runGitStdin is runGit with an input stream and an identity, for the plumbing
// commands that need either.
func runGitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// seedSubmodule adds a `vendor` gitlink alongside whatever head already
// carries and moves refs/heads/main onto the result -- the shape a repository
// takes the moment anyone runs `git submodule add` and pushes.
func seedSubmodule(t *testing.T, repo *Repo, head string) {
	t.Helper()
	dir := repo.Dir()
	listing := runGitStdin(t, dir, "", "ls-tree", head)
	entries := listing + "160000 commit " + submoduleSHA + "\tvendor\n"
	// --missing because the gitlink names a commit that is, correctly, not
	// here; git's own `git submodule add` writes exactly such an entry into a
	// superproject that has never fetched the submodule.
	tree := strings.TrimSpace(runGitStdin(t, dir, entries, "mktree", "--missing"))
	commit := strings.TrimSpace(runGitStdin(t, dir, "", "commit-tree", tree, "-p", head, "-m", "add a submodule"))
	runGitStdin(t, dir, "", "update-ref", "refs/heads/main", commit)
}

// TestTreeListsASubmoduleInsteadOfFailing is the regression test for a
// repository that a single submodule used to destroy.
//
// listTree sent everything that was not mode 040000 to blobEntry, and
// blobEntry asks the object database for a blob. A gitlink's hash is a commit
// in another repository, so the lookup returned "object not found" -- and
// because listTree propagates that, the failure was not confined to the one
// entry: the entire listing failed. That took out the HF-compatible tree API
// and the file browser with a 500, and failed the post-push index job on every
// retry until it parked as 'failed', which stops repo_files and blobs/
// publishing for the whole repository.
func TestTreeListsASubmoduleInsteadOfFailing(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first",
		addOp("README.md", "hello"), addOp("data/rows.txt", "1\n"))
	seedSubmodule(t, repo, head.String())

	for _, recursive := range []bool{false, true} {
		entries, _, err := repo.Tree("main", "", recursive)
		if err != nil {
			t.Fatalf("Tree(recursive=%v) over a repository with a submodule: %v", recursive, err)
		}
		byPath := map[string]Entry{}
		for _, e := range entries {
			byPath[e.Path] = e
		}
		if _, ok := byPath["README.md"]; !ok {
			t.Errorf("Tree(recursive=%v) lost the ordinary files: %v", recursive, byPath)
		}
		vendor, ok := byPath["vendor"]
		if !ok {
			t.Fatalf("Tree(recursive=%v) did not list the submodule at all: %v", recursive, byPath)
		}
		assertSubmoduleEntry(t, vendor)
		// A gitlink has no children in this repository, so a recursive walk
		// must stop at it rather than try to read a tree that is not here.
		for p := range byPath {
			if strings.HasPrefix(p, "vendor/") {
				t.Errorf("Tree(recursive=%v) descended into the submodule and produced %q", recursive, p)
			}
		}
	}
}

// TestStatOnASubmoduleReportsIt covers the single-path lookups that sit behind
// resolve and the paths-info endpoints; they went through the same blobEntry
// and failed the same way.
func TestStatOnASubmoduleReportsIt(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))
	seedSubmodule(t, repo, head.String())

	e, _, err := repo.Stat("main", "vendor")
	if err != nil {
		t.Fatalf("Stat on a submodule: %v", err)
	}
	assertSubmoduleEntry(t, e)

	many, _, err := repo.StatMany("main", []string{"vendor", "README.md"})
	if err != nil {
		t.Fatalf("StatMany over a submodule: %v", err)
	}
	if _, ok := many["README.md"]; !ok {
		t.Errorf("StatMany dropped the ordinary file: %v", many)
	}
	vendor, ok := many["vendor"]
	if !ok {
		t.Fatalf("StatMany dropped the submodule: %v", many)
	}
	assertSubmoduleEntry(t, vendor)

	// ReadFile has nothing to hand back for a gitlink, and must say so the
	// way it says it for a directory rather than by failing to load a blob.
	if _, err := repo.ReadFile("main", "vendor", 1<<20); err == nil {
		t.Errorf("ReadFile on a submodule returned content")
	}
}

func assertSubmoduleEntry(t *testing.T, e Entry) {
	t.Helper()
	if !e.IsSubmodule() {
		t.Errorf("entry %+v is not reported as a submodule", e)
	}
	if e.Mode != filemode.Submodule {
		t.Errorf("entry mode = %v, want %v", e.Mode, filemode.Submodule)
	}
	// IsDir is what every caller of a tree listing tests to mean "nothing to
	// read, index or publish here", and that is exactly true of a gitlink --
	// syncer's publishBlobs would otherwise try to put its hash in blobs/ and
	// fail the job just as the listing used to.
	if !e.IsDir {
		t.Errorf("a submodule entry came back as a readable file: %+v", e)
	}
	if e.Hash.String() != submoduleSHA {
		t.Errorf("entry hash = %s, want the recorded submodule commit %s", e.Hash, submoduleSHA)
	}
	if e.LFS != nil || e.Size != 0 {
		t.Errorf("a submodule entry carries blob metadata: %+v", e)
	}
}

// TestDiffAcrossASubmoduleChange checks the other reader of a tree: adding the
// gitlink must produce a row rather than an error, and the row must not claim
// to have read anything.
func TestDiffAcrossASubmoduleChange(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))
	seedSubmodule(t, repo, head.String())

	after, err := repo.RefTarget(BranchRef("main"))
	if err != nil {
		t.Fatalf("RefTarget: %v", err)
	}
	diff, err := repo.CommitDiff(after)
	if err != nil {
		t.Fatalf("CommitDiff over a submodule addition: %v", err)
	}
	var found bool
	for _, f := range diff.Files {
		if f.Path == "vendor" {
			found = true
			if f.HasPatch {
				t.Errorf("the submodule row claims to carry a patch: %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("the submodule addition produced no row: %+v", diff.Files)
	}
}
