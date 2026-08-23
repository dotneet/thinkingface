// Tests for how the repo-detail and directory-tree endpoints report a
// README.md that exists but is too large to render: the response must carry
// readme_too_large=true rather than let the file look absent, which the
// frontend would otherwise render as an empty-state "no README" card.

package api

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// commitReadme writes README.md (or dir/README.md) directly to the repo's
// git storage, bypassing the HTTP edit endpoint so an over-the-limit commit
// can't be rejected by any edit-specific size guard.
func commitReadme(t *testing.T, f *archiveFixture, r *store.Repo, path string, size int) {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	content := make([]byte, size)
	for i := range content {
		content[i] = 'a'
	}
	_, _, err = repo.Commit(gitrepo.CommitRequest{
		Branch:  r.DefaultBranch,
		Message: "add readme",
		Author:  gitrepo.Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: content}},
	})
	if err != nil {
		t.Fatalf("commit %s: %v", path, err)
	}
}

func TestRepoDetail_ReadmeTooLarge(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "big-readme", "model")
	commitReadme(t, f, r, "README.md", maxReadmeBytes+1)

	resp := f.do("GET", "/api/v1/repos/model/alice/big-readme", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if !body.Repo.ReadmeTooLarge {
		t.Fatalf("readme_too_large = false, want true")
	}
	if body.Repo.Readme != "" {
		t.Fatalf("readme = %q, want empty when too large", body.Repo.Readme)
	}
}

func TestRepoDetail_ReadmeWithinLimitIsNotFlagged(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "small-readme", "model")
	commitReadme(t, f, r, "README.md", 10)

	resp := f.do("GET", "/api/v1/repos/model/alice/small-readme", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.ReadmeTooLarge {
		t.Fatalf("readme_too_large = true, want false for a small README")
	}
}

func TestUITree_ReadmeTooLarge(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "big-readme-tree", "model")
	commitReadme(t, f, r, "README.md", maxReadmeBytes+1)

	resp := f.do("GET", "/api/v1/repos/model/alice/big-readme-tree/tree/main", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.TreeResponseUI
	resp.json(t, &body)
	if !body.ReadmeTooLarge {
		t.Fatalf("readme_too_large = false, want true")
	}
	if body.Readme != nil {
		t.Fatalf("readme = %v, want nil when too large", body.Readme)
	}
}
