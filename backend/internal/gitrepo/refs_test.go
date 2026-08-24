package gitrepo

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// ------------------------------------------------------- ValidateRefName

func TestValidateRefName(t *testing.T) {
	tests := []struct {
		name  string
		short string
		valid bool
	}{
		{"simple", "experiment", true},
		{"with slash", "feature/new-tokenizer", true},
		{"tag-ish", "v1.0.0", true},
		{"digits and dashes", "release-2026-08", true},
		{"underscore", "my_branch", true},

		{"empty", "", false},
		{"double dot", "a..b", false},
		{"leading slash", "/main", false},
		{"trailing slash", "main/", false},
		{"empty component", "a//b", false},
		{"HEAD", "HEAD", false},
		{"space", "my branch", false},
		{"tab", "my\tbranch", false},
		{"newline", "my\nbranch", false},
		{"control character", "my\x01branch", false},
		{"del character", "my\x7fbranch", false},
		{"tilde", "main~1", false},
		{"caret", "main^", false},
		{"colon", "refs:heads", false},
		{"question mark", "what?", false},
		{"asterisk", "star*", false},
		{"open bracket", "a[b", false},
		{"backslash", `a\b`, false},
		{"reflog syntax", "main@{1}", false},
		{"at sign alone", "@", false},
		{"lock suffix", "main.lock", false},
		{"component starting with dot", "feature/.hidden", false},
		{"trailing dot", "main.", false},
		{"too long", strings.Repeat("a", maxRefNameBytes+1), false},
		{"at the length limit", strings.Repeat("a", maxRefNameBytes), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRefName(tt.short)
			if tt.valid && err != nil {
				t.Fatalf("ValidateRefName(%q) = %v, want nil", tt.short, err)
			}
			if !tt.valid {
				if err == nil {
					t.Fatalf("ValidateRefName(%q) = nil, want an error", tt.short)
				}
				if !errors.Is(err, ErrInvalidRefName) {
					t.Fatalf("ValidateRefName(%q) error does not wrap ErrInvalidRefName: %v", tt.short, err)
				}
			}
		})
	}
}

// A name that escapes refs/heads/ is the whole reason this validator exists:
// the short name becomes a path under refs/, so "../../config" would name a
// file inside the bare repository itself.
func TestValidateRefName_RejectsPathEscape(t *testing.T) {
	for _, name := range []string{"../config", "../../refs/heads/main", "a/../../b"} {
		if err := ValidateRefName(name); err == nil {
			t.Fatalf("ValidateRefName(%q) = nil, want an error", name)
		}
	}
}

// ------------------------------------------------------- Create / Delete

func TestCreateRef_PointsAtTargetAndRefusesDuplicates(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))

	if err := repo.CreateRef(BranchRef("experiment"), head); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	got, err := repo.RefTarget(BranchRef("experiment"))
	if err != nil {
		t.Fatalf("RefTarget: %v", err)
	}
	if got != head {
		t.Fatalf("refs/heads/experiment = %s, want %s", got, head)
	}
	branches, err := repo.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %v, want main and experiment", branches)
	}

	// Creation never forces: a second create is a conflict, not a move.
	second := mustCommit(t, repo, "main", "second", addOp("a.txt", "a"))
	if err := repo.CreateRef(BranchRef("experiment"), second); !errors.Is(err, ErrRefExists) {
		t.Fatalf("CreateRef on an existing ref = %v, want ErrRefExists", err)
	}
	got, _ = repo.RefTarget(BranchRef("experiment"))
	if got != head {
		t.Fatalf("refs/heads/experiment moved to %s after a refused create", got)
	}

	assertRepoHealthy(t, repo.Dir())
}

func TestCreateRef_RejectsInvalidNameAndZeroTarget(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))

	if err := repo.CreateRef("refs/heads/a..b", head); !errors.Is(err, ErrInvalidRefName) {
		t.Fatalf("CreateRef with an invalid name = %v, want ErrInvalidRefName", err)
	}
	if err := repo.CreateRef(BranchRef("empty"), plumbing.ZeroHash); err == nil {
		t.Fatalf("CreateRef with the zero hash = nil, want an error")
	}
}

func TestDeleteRef_ReturnsOldTargetAndReportsMissing(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))

	if err := repo.CreateRef(TagRef("v1.0"), head); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	old, err := repo.DeleteRef(TagRef("v1.0"))
	if err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if old != head {
		t.Fatalf("DeleteRef returned %s, want the removed target %s", old, head)
	}
	tags, err := repo.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("tags = %v, want none", tags)
	}

	if _, err := repo.DeleteRef(TagRef("v1.0")); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("DeleteRef on a missing ref = %v, want ErrRefNotFound", err)
	}
	assertRepoHealthy(t, repo.Dir())
}

// ------------------------------------------------------- annotated tags

func TestWriteTagObject_ResolvesToTheTaggedCommit(t *testing.T) {
	_, repo := newTestRepo(t)
	head := mustCommit(t, repo, "main", "first", addOp("README.md", "hello"))

	tagObj, err := repo.WriteTagObject("v1.0", head, "the first release",
		Signature{Name: "tester", Email: "tester@example.com"})
	if err != nil {
		t.Fatalf("WriteTagObject: %v", err)
	}
	if tagObj == head {
		t.Fatalf("annotated tag object hash equals the commit hash")
	}
	if err := repo.CreateRef(TagRef("v1.0"), tagObj); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}

	// The ref names the tag object, but every revision lookup peels it.
	target, err := repo.RefTarget(TagRef("v1.0"))
	if err != nil {
		t.Fatalf("RefTarget: %v", err)
	}
	if target != tagObj {
		t.Fatalf("refs/tags/v1.0 = %s, want the tag object %s", target, tagObj)
	}
	resolved, err := repo.Resolve("v1.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != head {
		t.Fatalf("Resolve(v1.0) = %s, want the tagged commit %s", resolved, head)
	}

	// git itself has to accept the object, which is the point of the test.
	assertRepoHealthy(t, repo.Dir())
	out := runGit(t, repo.Dir(), "cat-file", "-t", tagObj.String())
	if strings.TrimSpace(out) != "tag" {
		t.Fatalf("cat-file -t = %q, want \"tag\"", strings.TrimSpace(out))
	}
}

// ---------------------------------------------------------- commit body

// metaOf splits a commit message the way huggingface_hub's GitCommitInfo does:
// subject into Message, everything after it into Body.
func TestListCommits_SplitsSubjectFromBody(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "Add the tokenizer\n\nIt reads the vocab from\ndisk at load time.\n",
		addOp("tokenizer.json", "{}"))

	metas, _, err := repo.ListCommits("main", "", plumbing.ZeroHash, 10)
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("len(commits) = %d, want 1", len(metas))
	}
	if metas[0].Message != "Add the tokenizer" {
		t.Fatalf("Message = %q, want the subject line only", metas[0].Message)
	}
	if metas[0].Body != "It reads the vocab from\ndisk at load time." {
		t.Fatalf("Body = %q, want the message after the subject", metas[0].Body)
	}
}

func TestListCommits_EmptyBodyForASingleLineMessage(t *testing.T) {
	_, repo := newTestRepo(t)
	mustCommit(t, repo, "main", "Add README", addOp("README.md", "hello"))

	metas, _, err := repo.ListCommits("main", "", plumbing.ZeroHash, 10)
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	if metas[0].Body != "" {
		t.Fatalf("Body = %q, want empty", metas[0].Body)
	}
}
