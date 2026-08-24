// Tests for the NDJSON commit endpoint and for the rule every write path
// shares with it: a commit goes to a branch, and only to a branch. Driven over
// real HTTP against a real Server (the archiveFixture wiring), like
// upload_test.go, because both rules live in the handlers.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// commitNDJSON posts a newline-delimited commit body, the shape
// huggingface_hub sends.
func commitNDJSON(t *testing.T, f *archiveFixture, path, token string, lines ...string) response {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(strings.Join(lines, "\n")))
	req.Header.Set("Content-Type", "application/x-ndjson")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

// seedLFSObject puts an object in the bucket and links it to the repository,
// which is the state the normal flow (preupload -> batch upload -> verify)
// leaves behind and the only state the commit endpoint accepts an oid in.
func seedLFSObject(t *testing.T, f *archiveFixture, r *store.Repo, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	ctx := context.Background()
	if err := f.obj.Put(ctx, storage.LFSKey(oid), bytes.NewReader(content), "application/octet-stream"); err != nil {
		t.Fatalf("put lfs object: %v", err)
	}
	if err := f.st.RecordLFSObject(ctx, r.ID, oid, int64(len(content)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	return oid
}

// refTargetOf returns the commit a ref points at, or "" when the ref is not
// there. Named for the ref rather than the head: wal_test.go's headOf asks the
// repository for HEAD, which is a different question.
func refTargetOf(t *testing.T, f *archiveFixture, r *store.Repo, refName string) string {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	hash, err := repo.RefTarget(refName)
	if err != nil {
		return ""
	}
	return hash.String()
}

// seedTag points refs/tags/{name} at the repository's default branch, without
// creating a branch of the same name.
func seedTag(t *testing.T, f *archiveFixture, r *store.Repo, name string) string {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	target, err := repo.Resolve(r.DefaultBranch)
	if err != nil {
		t.Fatalf("resolve %s: %v", r.DefaultBranch, err)
	}
	if err := repo.CreateRef(gitrepo.TagRef(name), target); err != nil {
		t.Fatalf("create tag %s: %v", name, err)
	}
	return target.String()
}

// ------------------------------------------------------------ lfsFile size

// A declared size that disagrees with the object is refused outright. Trusting
// it would put that number in the pointer, and resolve declares the pointer's
// size as Content-Length before streaming the object: a size of 1 hands every
// downloader a one-byte file that looks completely downloaded.
func TestCommit_RefusesAnLFSSizeThatDoesNotMatchTheObject(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	before := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	content := bytes.Repeat([]byte("w"), 4096)
	oid := seedLFSObject(t, f, r, content)

	for _, size := range []int64{1, int64(len(content)) + 1, -1} {
		t.Run(fmt.Sprintf("size %d", size), func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				`{"key":"header","value":{"summary":"lie about the size"}}`,
				fmt.Sprintf(`{"key":"lfsFile","value":{"path":"model.safetensors","algo":"sha256","oid":%q,"size":%d}}`, oid, size))
			if resp.status() != 400 {
				t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
			}
			if !strings.Contains(resp.rec.Body.String(), "does not match the uploaded object") {
				t.Errorf("body = %s, want the size mismatch message", resp.rec.Body.String())
			}
			// Nothing at all was committed: a rejected line must not leave
			// half a commit behind.
			if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != before {
				t.Errorf("head = %s, want it unmoved at %s", got, before)
			}
			if !fileMissing(t, f, r, "main", "model.safetensors") {
				t.Error("model.safetensors was committed despite the rejected size")
			}
		})
	}
}

// The size field stays optional -- huggingface_hub sends it, `curl` by hand
// often does not -- and the object itself is the source of truth either way.
func TestCommit_LFSPointerAlwaysCarriesTheStoredSize(t *testing.T) {
	content := bytes.Repeat([]byte("w"), 4096)

	tests := []struct {
		name string
		line string
	}{
		{"size omitted", `{"key":"lfsFile","value":{"path":"model.safetensors","algo":"sha256","oid":%q}}`},
		{"size zero", `{"key":"lfsFile","value":{"path":"model.safetensors","algo":"sha256","oid":%q,"size":0}}`},
		{"size matching", `{"key":"lfsFile","value":{"path":"model.safetensors","algo":"sha256","oid":%q,"size":4096}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			r := f.repo("alice", "weights", "model")
			tok := f.token(f.alice, "write")
			oid := seedLFSObject(t, f, r, content)

			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				`{"key":"header","value":{"summary":"add weights"}}`,
				fmt.Sprintf(tt.line, oid))
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
			}
			pointer, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "model.safetensors"))
			if !ok {
				t.Fatalf("model.safetensors is not an LFS pointer: %q",
					readFile(t, f, r, "main", "model.safetensors"))
			}
			if pointer.OID != oid || pointer.Size != int64(len(content)) {
				t.Errorf("pointer = %s/%d, want %s/%d", pointer.OID, pointer.Size, oid, len(content))
			}
		})
	}
}

// --------------------------------------------------------------- branch revs

// writeRequest is one write endpoint's request against a given revision, so
// the branch rule can be asserted for all of them at once: fixing this on one
// handler and not the others is exactly how the tag collision survived.
type writeRequest struct {
	name string
	// createsBranch is false for the deletion endpoint alone: deleting a path
	// from a branch that is not there is a 404 before any branch could be
	// created, which is its own rule and not the one under test here.
	createsBranch bool
	send          func(t *testing.T, f *archiveFixture, rev, token string) response
}

func writeRequests() []writeRequest {
	return []writeRequest{
		{"hf commit", true, func(t *testing.T, f *archiveFixture, rev, token string) response {
			return commitNDJSON(t, f, "/api/models/alice/w/commit/"+rev, token,
				`{"key":"header","value":{"summary":"write"}}`,
				`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
		}},
		{"ui upload", true, func(t *testing.T, f *archiveFixture, rev, token string) response {
			return upload(t, f, "/api/v1/upload/model/alice/w/"+rev, token, "", []uploadPart{
				{path: "a.txt", name: "a.txt", content: []byte("hi\n")},
			})
		}},
		{"ui edit", true, func(t *testing.T, f *archiveFixture, rev, token string) response {
			return f.do("PUT", "/api/v1/edit/model/alice/w/"+rev+"/a.txt", token,
				apitypes.EditFileRequest{Content: "hi\n"})
		}},
		{"ui delete", false, func(t *testing.T, f *archiveFixture, rev, token string) response {
			return f.do("DELETE", "/api/v1/edit/model/alice/w/"+rev+"/README.md", token, nil)
		}},
	}
}

// Reads resolve refs/tags/{rev} before refs/heads/{rev}, so a write that
// creates refs/heads/{rev} for a rev that is a tag lands somewhere nobody
// looks: the caller is told it succeeded and then never sees the file again.
func TestWriteEndpoints_RefuseATagRevision(t *testing.T) {
	for _, tt := range writeRequests() {
		t.Run(tt.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			r := f.repo("alice", "w", "model")
			seedFile(t, f, r, "README.md", []byte("# hi\n"))
			tagged := seedTag(t, f, r, "v1")
			tok := f.token(f.alice, "write")

			resp := tt.send(t, f, "v1", tok)
			if resp.status() != 409 {
				t.Fatalf("status = %d, body = %s; want 409", resp.status(), resp.rec.Body.String())
			}
			if got := errorType(t, resp); got != "conflict" {
				t.Errorf("error type = %q, want conflict", got)
			}
			if !strings.Contains(resp.rec.Body.String(), "is a tag") {
				t.Errorf("body = %s, want it to say v1 is a tag", resp.rec.Body.String())
			}
			// The orphaned branch is the actual damage: it would shadow
			// nothing, be read by nobody, and disagree with repo_files.
			if got := refTargetOf(t, f, r, gitrepo.BranchRef("v1")); got != "" {
				t.Errorf("refs/heads/v1 = %s, want it never created", got)
			}
			if got := refTargetOf(t, f, r, gitrepo.TagRef("v1")); got != tagged {
				t.Errorf("refs/tags/v1 = %s, want it unmoved at %s", got, tagged)
			}
		})
	}
}

// The other half of the rule: a rev that names nothing is still the first
// commit on a new branch, which is how every one of these endpoints creates
// one.
func TestWriteEndpoints_StillCreateABranchThatDoesNotExistYet(t *testing.T) {
	for _, tt := range writeRequests() {
		if !tt.createsBranch {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			r := f.repo("alice", "w", "model")
			seedFile(t, f, r, "README.md", []byte("# hi\n"))
			tok := f.token(f.alice, "write")

			resp := tt.send(t, f, "draft", tok)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
			}
			if refTargetOf(t, f, r, gitrepo.BranchRef("draft")) == "" {
				t.Error("refs/heads/draft was not created")
			}
		})
	}
}
