// Tests for how the handlers read revisions after gitrepo.Resolve stopped
// using go-git's ResolveRevision: refs before hash prefixes, branches before
// tags, peeled tag targets, and "empty" meaning no refs at all rather than an
// unborn HEAD.

package api

import (
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
