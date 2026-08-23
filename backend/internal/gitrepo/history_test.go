package gitrepo

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestLastCommits_AttributesEachEntry(t *testing.T) {
	_, repo := newTestRepo(t)

	c1 := mustCommit(t, repo, "main", "add a and dir/b",
		addOp("a.txt", "one"), addOp("dir/b.txt", "b"))
	c2 := mustCommit(t, repo, "main", "update a\n\nwith a body", addOp("a.txt", "two"))
	c3 := mustCommit(t, repo, "main", "add c", addOp("c.txt", "c"))

	got, latest, err := repo.LastCommits("main", "")
	if err != nil {
		t.Fatalf("LastCommits: %v", err)
	}
	if latest == nil || latest.Hash != c3 {
		t.Fatalf("latest = %+v, want hash %s", latest, c3)
	}
	want := map[string]plumbing.Hash{"a.txt": c2, "c.txt": c3, "dir": c1}
	for name, wantHash := range want {
		meta, ok := got[name]
		if !ok {
			t.Fatalf("no attribution for %q (got %v)", name, got)
		}
		if meta.Hash != wantHash {
			t.Errorf("%q attributed to %s, want %s", name, meta.Hash, wantHash)
		}
	}
	// The subject line is extracted without the body.
	if got["a.txt"].Message != "update a" {
		t.Errorf("subject = %q, want %q", got["a.txt"].Message, "update a")
	}
	if got["a.txt"].Author != "tester" {
		t.Errorf("author = %q, want tester", got["a.txt"].Author)
	}

	// A subdirectory listing attributes its own children.
	sub, _, err := repo.LastCommits("main", "dir")
	if err != nil {
		t.Fatalf("LastCommits(dir): %v", err)
	}
	if sub["b.txt"].Hash != c1 {
		t.Errorf("dir/b.txt attributed to %s, want %s", sub["b.txt"].Hash, c1)
	}
}

func TestLastCommits_EmptyRepo(t *testing.T) {
	_, repo := newTestRepo(t)
	got, latest, err := repo.LastCommits("main", "")
	if err != nil || got != nil || latest != nil {
		t.Fatalf("empty repo: got %v, latest %v, err %v; want all nil", got, latest, err)
	}
}

func TestListCommits_Pagination(t *testing.T) {
	_, repo := newTestRepo(t)

	c1 := mustCommit(t, repo, "main", "first", addOp("a.txt", "1"))
	c2 := mustCommit(t, repo, "main", "second", addOp("a.txt", "2"))
	c3 := mustCommit(t, repo, "main", "third", addOp("a.txt", "3"))

	page1, next, err := repo.ListCommits("main", "", plumbing.ZeroHash, 2)
	if err != nil {
		t.Fatalf("ListCommits page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Hash != c3 || page1[1].Hash != c2 {
		t.Fatalf("page 1 = %v, want [%s %s]", page1, c3, c2)
	}
	if next != c2 {
		t.Fatalf("next = %s, want %s", next, c2)
	}

	page2, next, err := repo.ListCommits("main", "", next, 2)
	if err != nil {
		t.Fatalf("ListCommits page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].Hash != c1 {
		t.Fatalf("page 2 = %v, want [%s]", page2, c1)
	}
	if !next.IsZero() {
		t.Fatalf("next after root = %s, want zero", next)
	}

	// Paging past the root commit yields an empty page.
	tail, next, err := repo.ListCommits("main", "", c1, 2)
	if err != nil || len(tail) != 0 || !next.IsZero() {
		t.Fatalf("past root: %v next %s err %v; want empty, zero, nil", tail, next, err)
	}

	// The full page ending exactly at the root still reports no cursor.
	all, next, err := repo.ListCommits("main", "", plumbing.ZeroHash, 3)
	if err != nil || len(all) != 3 || !next.IsZero() {
		t.Fatalf("full history: %d commits, next %s, err %v; want 3, zero, nil", len(all), next, err)
	}
}

func TestListCommits_PathFilter(t *testing.T) {
	_, repo := newTestRepo(t)

	c1 := mustCommit(t, repo, "main", "add a and dir/b",
		addOp("a.txt", "one"), addOp("dir/b.txt", "b"))
	mustCommit(t, repo, "main", "unrelated", addOp("other.txt", "x"))
	c3 := mustCommit(t, repo, "main", "update a", addOp("a.txt", "two"))
	c4 := mustCommit(t, repo, "main", "delete a", Op{Kind: OpDelete, Path: "a.txt"})

	// A file's history includes its deletion, skips unrelated commits, and
	// ends at the commit that introduced it.
	got, next, err := repo.ListCommits("main", "a.txt", plumbing.ZeroHash, 10)
	if err != nil {
		t.Fatalf("ListCommits(a.txt): %v", err)
	}
	if len(got) != 3 || got[0].Hash != c4 || got[1].Hash != c3 || got[2].Hash != c1 {
		t.Fatalf("a.txt history = %v, want [%s %s %s]", got, c4, c3, c1)
	}
	if !next.IsZero() {
		t.Fatalf("next = %s, want zero", next)
	}

	// A directory's history follows the subtree hash.
	got, _, err = repo.ListCommits("main", "dir", plumbing.ZeroHash, 10)
	if err != nil {
		t.Fatalf("ListCommits(dir): %v", err)
	}
	if len(got) != 1 || got[0].Hash != c1 {
		t.Fatalf("dir history = %v, want [%s]", got, c1)
	}

	// Filtered pagination: the cursor resumes mid-scan.
	page1, next, err := repo.ListCommits("main", "a.txt", plumbing.ZeroHash, 2)
	if err != nil || len(page1) != 2 || next.IsZero() {
		t.Fatalf("page 1 = %v next %s err %v; want 2 commits and a cursor", page1, next, err)
	}
	page2, next, err := repo.ListCommits("main", "a.txt", next, 2)
	if err != nil || len(page2) != 1 || page2[0].Hash != c1 || !next.IsZero() {
		t.Fatalf("page 2 = %v next %s err %v; want [%s], zero", page2, next, err, c1)
	}

	// A path that never existed yields an empty history.
	got, next, err = repo.ListCommits("main", "missing.txt", plumbing.ZeroHash, 10)
	if err != nil || len(got) != 0 || !next.IsZero() {
		t.Fatalf("missing path: %v next %s err %v; want empty, zero, nil", got, next, err)
	}
}

func TestListCommits_EmptyRepo(t *testing.T) {
	_, repo := newTestRepo(t)
	commits, next, err := repo.ListCommits("main", "", plumbing.ZeroHash, 10)
	if err != nil || len(commits) != 0 || !next.IsZero() {
		t.Fatalf("empty repo: %v next %s err %v; want empty, zero, nil", commits, next, err)
	}
}
