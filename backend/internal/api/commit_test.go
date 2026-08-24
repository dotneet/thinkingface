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

// -------------------------------------------------------------- unknown keys

// The switch over `key` had no default, so a line naming an operation this
// server does not implement was skipped in silence. Mixed with one it does,
// the commit applied half of what was sent and answered 200 `success` -- the
// caller had no way to learn the rest never happened.
func TestCommit_RefusesAnUnknownOperationKey(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	before := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"half a commit"}}`,
		`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`,
		`{"key":"movedFile","value":{"path":"b.txt","srcPath":"a.txt"}}`)

	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "movedFile") {
		t.Errorf("body = %s, want it to name the unsupported operation", resp.rec.Body.String())
	}
	if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != before {
		t.Errorf("head = %s, want it unmoved at %s", got, before)
	}
	if !fileMissing(t, f, r, "main", "a.txt") {
		t.Error("the add alongside the unknown operation was committed anyway")
	}
}

// A header that will not parse takes the parentCommit lock down with it, so it
// is an error rather than a header that was never there.
func TestCommit_RefusesAMalformedHeader(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":"just a string"}`,
		`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
	if !fileMissing(t, f, r, "main", "a.txt") {
		t.Error("the commit was applied despite the unreadable header")
	}
}

// ------------------------------------------------------------------ copyFile

// huggingface_hub's CommitOperationCopy. The mixed commit is the point: a
// copy that was dropped from a commit that also added a file used to answer
// 200 with the copy simply missing.
func TestCommit_CopyFileDuplicatesAFileAlongsideAnAdd(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "src.txt", []byte("payload\n"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"copy and add"}}`,
		`{"key":"copyFile","value":{"path":"copy.txt","srcPath":"src.txt","srcRevision":null}}`,
		`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "copy.txt")); got != "payload\n" {
		t.Errorf("copy.txt = %q, want the source's content", got)
	}
	if got := string(readFile(t, f, r, "main", "a.txt")); got != "hi\n" {
		t.Errorf("a.txt = %q, want hi", got)
	}
	if got := string(readFile(t, f, r, "main", "src.txt")); got != "payload\n" {
		t.Errorf("src.txt = %q, want the source left alone", got)
	}
}

// A copy on its own is a commit: it used to fall through to "commit contains
// no file operations" and 400.
func TestCommit_CopyFileOnItsOwnIsACommit(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "src.txt", []byte("payload\n"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"copy only"}}`,
		`{"key":"copyFile","value":{"path":"copy.txt","srcPath":"src.txt"}}`)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "copy.txt")); got != "payload\n" {
		t.Errorf("copy.txt = %q, want the source's content", got)
	}
}

// The Hub's own copy operation is documented for LFS files, so this is the
// case that matters most: what git holds is the pointer, and copying the
// pointer is what makes the copy resolve to the same object.
func TestCommit_CopyFilePreservesAnLFSPointer(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	tok := f.token(f.alice, "write")
	content := bytes.Repeat([]byte("w"), 4096)
	oid := seedLFSObject(t, f, r, content)

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"add weights"}}`,
		fmt.Sprintf(`{"key":"lfsFile","value":{"path":"model.safetensors","algo":"sha256","oid":%q}}`, oid))
	if resp.status() != 200 {
		t.Fatalf("seed commit status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	resp = commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"copy the weights"}}`,
		`{"key":"copyFile","value":{"path":"model-copy.safetensors","srcPath":"model.safetensors"}}`)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
	pointer, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "model-copy.safetensors"))
	if !ok {
		t.Fatalf("the copy is not an LFS pointer: %q", readFile(t, f, r, "main", "model-copy.safetensors"))
	}
	if pointer.OID != oid || pointer.Size != int64(len(content)) {
		t.Errorf("pointer = %s/%d, want %s/%d", pointer.OID, pointer.Size, oid, len(content))
	}
}

// srcRevision names where the copy is taken from, which is the whole reason
// the operation exists: restoring a file as it was at an earlier commit.
func TestCommit_CopyFileFromAnEarlierRevision(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "notes.txt", []byte("v1\n"))
	v1 := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	seedFile(t, f, r, "notes.txt", []byte("v2\n"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"restore v1 alongside v2"}}`,
		fmt.Sprintf(`{"key":"copyFile","value":{"path":"notes-v1.txt","srcPath":"notes.txt","srcRevision":%q}}`, v1))
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "notes-v1.txt")); got != "v1\n" {
		t.Errorf("notes-v1.txt = %q, want the content at %s", got, v1)
	}
	if got := string(readFile(t, f, r, "main", "notes.txt")); got != "v2\n" {
		t.Errorf("notes.txt = %q, want v2 untouched", got)
	}
}

// Every way a copy source can fail to name one file, and the answer for each.
// The X-Error-Code matters: it is what makes huggingface_hub raise
// EntryNotFoundError / RevisionNotFoundError rather than a bare HTTP error.
func TestCommit_CopyFileRejectsSourcesItCannotResolve(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		status    int
		errorCode string
		message   string
	}{
		{"missing path", `{"path":"copy.txt","srcPath":"nope.txt"}`, 404, "EntryNotFound", "does not exist"},
		{"unknown revision", `{"path":"copy.txt","srcPath":"src.txt","srcRevision":"no-such-branch"}`,
			404, "RevisionNotFound", "source revision"},
		{"directory", `{"path":"copy","srcPath":"dir"}`, 400, "", "is a directory"},
		{"no srcPath", `{"path":"copy.txt"}`, 400, "", "srcPath"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newArchiveFixture(t)
			r := f.repo("alice", "weights", "model")
			seedFile(t, f, r, "src.txt", []byte("payload\n"))
			seedFile(t, f, r, "dir/inner.txt", []byte("inner\n"))
			before := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
			tok := f.token(f.alice, "write")

			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				`{"key":"header","value":{"summary":"bad copy"}}`,
				`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`,
				`{"key":"copyFile","value":`+tt.value+`}`)

			if resp.status() != tt.status {
				t.Fatalf("status = %d, body = %s; want %d", resp.status(), resp.rec.Body.String(), tt.status)
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != tt.errorCode {
				t.Errorf("X-Error-Code = %q, want %q", got, tt.errorCode)
			}
			if !strings.Contains(resp.rec.Body.String(), tt.message) {
				t.Errorf("body = %s, want it to mention %q", resp.rec.Body.String(), tt.message)
			}
			// The rest of the commit must not survive the refused line.
			if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != before {
				t.Errorf("head = %s, want it unmoved at %s", got, before)
			}
			if !fileMissing(t, f, r, "main", "a.txt") {
				t.Error("the add alongside the refused copy was committed")
			}
		})
	}
}

// -------------------------------------------------------------- parentCommit

// create_commit(parent_commit=...) is an optimistic lock, and the one thing it
// must never do is answer 200 for a branch that moved: the caller asked
// precisely not to write on top of somebody else's push.
func TestCommit_ParentCommitLetsAMatchingParentThrough(t *testing.T) {
	for _, shorten := range []bool{false, true} {
		name := "full hash"
		if shorten {
			name = "shorthand"
		}
		t.Run(name, func(t *testing.T) {
			f := newArchiveFixture(t)
			r := f.repo("alice", "weights", "model")
			seedFile(t, f, r, "README.md", []byte("# hi\n"))
			head := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
			parent := head
			if shorten {
				parent = head[:7]
			}
			tok := f.token(f.alice, "write")

			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				fmt.Sprintf(`{"key":"header","value":{"summary":"on top of the head I saw","parentCommit":%q}}`, parent),
				`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
			}
			if got := string(readFile(t, f, r, "main", "a.txt")); got != "hi\n" {
				t.Errorf("a.txt = %q, want hi", got)
			}
		})
	}
}

// 412 rather than the 409 the WAL uses for contention: a stale parent cannot
// be fixed by sending the identical request again, so it must not read as the
// retryable conflict.
func TestCommit_ParentCommitRefusesABranchThatMoved(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	stale := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	// Somebody else pushes between the caller reading the head and committing.
	seedFile(t, f, r, "other.txt", []byte("theirs\n"))
	head := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		fmt.Sprintf(`{"key":"header","value":{"summary":"blind write","parentCommit":%q}}`, stale),
		`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)

	if resp.status() != 412 {
		t.Fatalf("status = %d, body = %s; want 412", resp.status(), resp.rec.Body.String())
	}
	if got := errorType(t, resp); got != "stale_parent" {
		t.Errorf("error type = %q, want stale_parent (distinct from the WAL's conflict)", got)
	}
	if !strings.Contains(resp.rec.Body.String(), head) {
		t.Errorf("body = %s, want it to name the head the branch is actually at", resp.rec.Body.String())
	}
	if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != head {
		t.Errorf("head = %s, want it unmoved at %s", got, head)
	}
	if !fileMissing(t, f, r, "main", "a.txt") {
		t.Error("the commit landed despite the stale parentCommit")
	}
}

// A branch that does not exist yet is not "any parent": committing to it with
// a parentCommit means the caller believes it is there.
func TestCommit_ParentCommitRefusesAnUnbornBranch(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	head := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	for _, parent := range []string{head, strings.Repeat("0", 40)} {
		resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/draft", tok,
			fmt.Sprintf(`{"key":"header","value":{"summary":"new branch","parentCommit":%q}}`, parent),
			`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
		if resp.status() != 412 {
			t.Fatalf("parentCommit %s: status = %d, body = %s; want 412",
				parent, resp.status(), resp.rec.Body.String())
		}
		if got := refTargetOf(t, f, r, gitrepo.BranchRef("draft")); got != "" {
			t.Errorf("refs/heads/draft = %s, want it never created", got)
		}
	}
}

// A parentCommit that is not a hash at all is the caller's typo, not a branch
// that moved -- 400, and a different sentence.
func TestCommit_ParentCommitMustLookLikeAHash(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	for _, parent := range []string{"main", "abc", "not-a-hash-at-all", "ZZZZZZZ"} {
		resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
			fmt.Sprintf(`{"key":"header","value":{"summary":"typo","parentCommit":%q}}`, parent),
			`{"key":"file","value":{"path":"a.txt","content":"aGkK","encoding":"base64"}}`)
		if resp.status() != 400 {
			t.Fatalf("parentCommit %q: status = %d, body = %s; want 400",
				parent, resp.status(), resp.rec.Body.String())
		}
		if !fileMissing(t, f, r, "main", "a.txt") {
			t.Fatalf("parentCommit %q: the commit landed anyway", parent)
		}
	}
}
