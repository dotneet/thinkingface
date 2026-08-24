package main

import (
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func ptr(s string) *string { return &s }

func TestParseRepoSelector_AcceptsBothForms(t *testing.T) {
	cases := []struct {
		raw  string
		want repoSelector
	}{
		{"admin/foo", repoSelector{namespace: "admin", name: "foo"}},
		{"/admin/foo/", repoSelector{namespace: "admin", name: "foo"}},
		{"model/admin/foo", repoSelector{kind: "model", namespace: "admin", name: "foo"}},
		{"models/admin/foo", repoSelector{kind: "model", namespace: "admin", name: "foo"}},
		{"datasets/admin/foo", repoSelector{kind: "dataset", namespace: "admin", name: "foo"}},
	}
	for _, c := range cases {
		got, err := parseRepoSelector(c.raw)
		if err != nil {
			t.Fatalf("parseRepoSelector(%q): %v", c.raw, err)
		}
		if got != c.want {
			t.Fatalf("parseRepoSelector(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestParseRepoSelector_RejectsGarbage(t *testing.T) {
	for _, raw := range []string{"foo", "a/b/c/d", "space/admin/foo", "admin/"} {
		if _, err := parseRepoSelector(raw); err == nil {
			t.Fatalf("parseRepoSelector(%q) = nil error, want a rejection", raw)
		}
	}
}

func TestRepoSelector_EmptyMatchesEverything(t *testing.T) {
	sel, err := parseRepoSelector("")
	if err != nil {
		t.Fatalf("parseRepoSelector: %v", err)
	}
	if !sel.matches(store.RepoRef{Kind: "model", Namespace: "admin", Name: "foo"}) {
		t.Fatal("the empty selector must match every repository")
	}
}

// A name is only unique per kind, so a selector without one has to match both
// repositories that carry it -- and a selector with one must not.
func TestRepoSelector_KindNarrowsAnAmbiguousName(t *testing.T) {
	model := store.RepoRef{Kind: "model", Namespace: "admin", Name: "foo"}
	dataset := store.RepoRef{Kind: "dataset", Namespace: "admin", Name: "foo"}

	anyKind, _ := parseRepoSelector("admin/foo")
	if !anyKind.matches(model) || !anyKind.matches(dataset) {
		t.Fatal("a selector without a kind must match both kinds")
	}

	onlyModel, _ := parseRepoSelector("model/admin/foo")
	if !onlyModel.matches(model) || onlyModel.matches(dataset) {
		t.Fatal("a selector with a kind must match that kind only")
	}
	if onlyModel.matches(store.RepoRef{Kind: "model", Namespace: "other", Name: "foo"}) {
		t.Fatal("a selector must not match another namespace")
	}
}

func TestStoredSet_KeysByContentIdentifier(t *testing.T) {
	sha := "aaaabbbbccccddddeeeeffff00001111222233334444"
	oid := "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	set := storedSet([]storage.ObjectInfo{
		{Key: storage.BlobKey(sha), Updated: time.Now()},
		{Key: storage.LFSKey(oid)},
	})
	if !set[sha] || !set[oid] {
		t.Fatalf("storedSet = %v, want it keyed by the content identifier", set)
	}
}

func TestMissingObjects_SeparatesBlobsFromLFS(t *testing.T) {
	stored := storedObjects{
		blobs: map[string]bool{"present-sha": true},
		lfs:   map[string]bool{"present-oid": true},
	}
	files := []store.RepoFile{
		{Path: "here.txt", BlobSHA: "present-sha"},
		{Path: "gone.txt", BlobSHA: "absent-sha"},
		{Path: "here.bin", BlobSHA: "pointer-sha", LFSOID: ptr("present-oid")},
		{Path: "gone.bin", BlobSHA: "pointer-sha2", LFSOID: ptr("absent-oid")},
	}

	got := missingObjects(files, stored)
	want := []missingObject{
		{Path: "gone.txt", ID: "absent-sha"},
		{Path: "gone.bin", ID: "absent-oid", LFS: true},
	}
	if len(got) != len(want) {
		t.Fatalf("missingObjects = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missingObjects[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An LFS file's pointer blob is not published to blobs/, so its blob sha must
// never be looked up there: doing so would report every LFS file on the
// instance as a missing blob.
func TestMissingObjects_IgnoresThePointerBlobOfAnLFSFile(t *testing.T) {
	stored := storedObjects{blobs: map[string]bool{}, lfs: map[string]bool{"oid": true}}
	files := []store.RepoFile{{Path: "big.bin", BlobSHA: "pointer-sha", LFSOID: ptr("oid")}}

	if got := missingObjects(files, stored); len(got) != 0 {
		t.Fatalf("missingObjects = %+v, want none", got)
	}
}

func TestDiffIndex_AgreementIsEmpty(t *testing.T) {
	files := []store.RepoFile{
		{Path: "a.txt", Size: 3, BlobSHA: "sha-a"},
		{Path: "b.bin", Size: 99, BlobSHA: "pointer", LFSOID: ptr("oid-b")},
	}
	// A separate slice with equal contents: the comparison must be by value,
	// not by identity of the pointer fields.
	indexed := []store.RepoFile{
		{Path: "a.txt", Size: 3, BlobSHA: "sha-a"},
		{Path: "b.bin", Size: 99, BlobSHA: "pointer", LFSOID: ptr("oid-b")},
	}
	if d := diffIndex(files, indexed); d.total() != 0 {
		t.Fatalf("diffIndex = %+v, want no drift", d)
	}
}

func TestDiffIndex_ReportsEachKindOfDrift(t *testing.T) {
	tree := []store.RepoFile{
		{Path: "kept.txt", BlobSHA: "sha-kept"},
		{Path: "added.txt", BlobSHA: "sha-added"},
		{Path: "edited.txt", BlobSHA: "sha-new"},
	}
	indexed := []store.RepoFile{
		{Path: "kept.txt", BlobSHA: "sha-kept"},
		{Path: "edited.txt", BlobSHA: "sha-old"},
		{Path: "removed.txt", BlobSHA: "sha-removed"},
	}

	d := diffIndex(tree, indexed)
	if len(d.Missing) != 1 || d.Missing[0] != "added.txt" {
		t.Fatalf("Missing = %v, want [added.txt]", d.Missing)
	}
	if len(d.Extra) != 1 || d.Extra[0] != "removed.txt" {
		t.Fatalf("Extra = %v, want [removed.txt]", d.Extra)
	}
	if len(d.Changed) != 1 || d.Changed[0] != "edited.txt" {
		t.Fatalf("Changed = %v, want [edited.txt]", d.Changed)
	}
	if d.total() != 3 {
		t.Fatalf("total = %d, want 3", d.total())
	}
}

// A file converted to LFS keeps neither identity: the tree's pointer blob and
// the index's plain blob are different content, and the reverse -- an index
// row that still carries an oid for a file that is plain again -- is too.
func TestDiffIndex_NoticesAnLFSConversionInEitherDirection(t *testing.T) {
	plain := []store.RepoFile{{Path: "f.bin", BlobSHA: "sha"}}
	lfsSide := []store.RepoFile{{Path: "f.bin", BlobSHA: "sha", LFSOID: ptr("oid")}}

	if d := diffIndex(lfsSide, plain); len(d.Changed) != 1 {
		t.Fatalf("plain index vs lfs tree: Changed = %v, want [f.bin]", d.Changed)
	}
	if d := diffIndex(plain, lfsSide); len(d.Changed) != 1 {
		t.Fatalf("lfs index vs plain tree: Changed = %v, want [f.bin]", d.Changed)
	}
	other := []store.RepoFile{{Path: "f.bin", BlobSHA: "sha", LFSOID: ptr("other-oid")}}
	if d := diffIndex(lfsSide, other); len(d.Changed) != 1 {
		t.Fatalf("differing oids: Changed = %v, want [f.bin]", d.Changed)
	}
}

// The exit verdict: dry run leaves everything it found, and an execute run
// leaves only what nothing can regenerate -- which is what the missing LFS
// object is there to prove.
func TestResyncStats_UnrepairedCountsWhatIsLeftBehind(t *testing.T) {
	reported := resyncStats{missingBlobs: 2, missingLFS: 1, staleRefs: 1}
	if got := reported.unrepaired(); got != 4 {
		t.Fatalf("dry-run unrepaired = %d, want 4", got)
	}
	repaired := resyncStats{missingBlobs: 2, missingLFS: 1, staleRefs: 1, republished: 2, reenqueued: 1}
	if got := repaired.unrepaired(); got != 1 {
		t.Fatalf("repaired unrepaired = %d, want 1 (the lfs object)", got)
	}
	clean := resyncStats{repos: 3, refs: 5}
	if got := clean.unrepaired(); got != 0 {
		t.Fatalf("clean unrepaired = %d, want 0", got)
	}
}
