// Tests for how the handlers read revisions after gitrepo.Resolve stopped
// using go-git's ResolveRevision: refs before hash prefixes, branches before
// tags, peeled tag targets, and "empty" meaning no refs at all rather than an
// unborn HEAD.

package api

import (
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// The Web UI's revision picker shows and links each tag's commit, so an
// annotated tag has to be peeled there too, the same as HF /refs.
func TestUIRefs_AnnotatedTagTargetIsTheCommit(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "dataset")
	tok := f.token(f.alice, "write")
	branches, _ := f.refs("dataset", "alice", "foo")
	head := branches["main"]

	if got := f.do("POST", "/api/datasets/alice/foo/tag/main", tok,
		map[string]any{"tag": "v1", "message": "annotated"}).status(); got != 201 {
		t.Fatalf("create annotated tag status = %d", got)
	}

	resp := f.do("GET", "/api/v1/repos/dataset/alice/foo/refs", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RefsResponseUI
	resp.json(t, &body)
	if len(body.Tags) != 1 || body.Tags[0].Name != "v1" {
		t.Fatalf("tags = %+v, want just v1", body.Tags)
	}
	if body.Tags[0].TargetOID != head {
		t.Fatalf("v1 target_oid = %s, want the tagged commit %s", body.Tags[0].TargetOID, head)
	}
}

// git allows tagging a tree (or a blob), and a push can carry such a tag. It
// does not peel to a commit, so both listings fall back to the raw object the
// ref names -- dropping Resolve's error used to list it as forty zeros, which
// names no object at all.
func TestRefs_TagOfATreeListsTheTree(t *testing.T) {
	f := newRefsFixture(t)
	r := f.repo("alice", "foo", "dataset")
	tok := f.token(f.alice, "write")
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := gitRepo.Resolve("main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	commit, err := gitRepo.CommitObject(head)
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	tree := commit.TreeHash.String()
	if err := gitRepo.CreateRef(gitrepo.TagRef("tree-tag"), commit.TreeHash); err != nil {
		t.Fatalf("create tag of a tree: %v", err)
	}

	if _, tags := f.refs("dataset", "alice", "foo"); tags["tree-tag"] != tree {
		t.Errorf("HF refs tree-tag targetCommit = %q, want the tree %s", tags["tree-tag"], tree)
	}

	resp := f.do("GET", "/api/v1/repos/dataset/alice/foo/refs", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RefsResponseUI
	resp.json(t, &body)
	if len(body.Tags) != 1 || body.Tags[0].TargetOID != tree {
		t.Errorf("UI refs tags = %+v, want tree-tag -> %s", body.Tags, tree)
	}
}

// A branch or tag named like a full commit id could never be read by that
// name (Resolve puts a full id ahead of every ref), so creating one is a 400
// on every API path that creates a ref. Deleting one -- git push can still
// make it -- stays possible.
func TestRefs_CreatingAFullHexNameIsRefused(t *testing.T) {
	f := newRefsFixture(t)
	r := f.repo("alice", "foo", "dataset")
	tok := f.token(f.alice, "write")
	branches, _ := f.refs("dataset", "alice", "foo")
	hexName := strings.ToUpper(branches["main"])

	for what, resp := range map[string]response{
		"branch": f.do("POST", "/api/datasets/alice/foo/branch/"+hexName, tok, nil),
		"tag":    f.do("POST", "/api/datasets/alice/foo/tag/main", tok, map[string]any{"tag": hexName}),
	} {
		if resp.status() != 400 || !strings.Contains(resp.rec.Body.String(), "40 hex digits") {
			t.Errorf("create %s %s = %d %s, want 400 naming the 40-hex rule",
				what, hexName, resp.status(), resp.rec.Body.String())
		}
	}

	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, _ := gitRepo.Resolve("main")
	if err := gitRepo.CreateRef(gitrepo.BranchRef(hexName), head); err != nil {
		t.Fatalf("seed a hex-named branch the way a push would: %v", err)
	}
	if got := f.do("DELETE", "/api/datasets/alice/foo/branch/"+hexName, tok, nil).status(); got != 200 {
		t.Errorf("delete branch %s status = %d, want 200", hexName, got)
	}
}

// A repository whose only history is on a branch that is not the default one
// is not empty. It used to be read as empty (IsEmpty asked HEAD), so a typo'd
// revision there answered 200 with nothing in it. The default branch itself is
// still unborn, though, and naming it keeps the empty-repository answer.
func TestHFReadEndpoints_HistoryOnlyOnANonDefaultBranch(t *testing.T) {
	f := newRevisionFixture(t)
	r := f.emptyRepo("alice", "foo")
	f.commit(r, "dev", "Add README")

	t.Run("unknown revision is RevisionNotFound", func(t *testing.T) {
		resp := f.do("GET", "/api/models/alice/foo/tree/typo", "", nil)
		if resp.status() != 404 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
			t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
		}
	})

	t.Run("the unborn default branch is still empty", func(t *testing.T) {
		resp := f.do("GET", "/api/models/alice/foo/tree/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var entries []any
		resp.json(t, &entries)
		if len(entries) != 0 {
			t.Fatalf("entries = %+v, want none", entries)
		}
	})

	t.Run("the branch with history lists it", func(t *testing.T) {
		resp := f.do("GET", "/api/models/alice/foo/tree/dev", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var entries []struct {
			Path string `json:"path"`
		}
		resp.json(t, &entries)
		if len(entries) != 1 || entries[0].Path != "README.md" {
			t.Fatalf("entries = %+v, want README.md", entries)
		}
	})
}

// A commit to a new branch whose name is a hex prefix of an existing commit
// ("c", "cafe", "2024") used to be refused with 409 "not a branch": Resolve
// matched the name as a hash prefix, so ensureBranchRev saw something that
// resolved and was not a branch. Short names are refs or nothing now.
func TestCommit_ToANewBranchNamedLikeAHashPrefix(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	head := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	branch := head[:1]
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/"+branch, tok,
		`{"key":"header","value":{"summary":"first on a hex-named branch"}}`,
		`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, branch, "a.txt")); got != "hi\n" {
		t.Errorf("a.txt on %s = %q, want hi", branch, got)
	}
	if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != head {
		t.Errorf("main moved to %s, want it left at %s", got, head)
	}
}
