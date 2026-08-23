// Tests for the two write paths the browser reaches directly: the multipart
// upload endpoint and the single-file delete. Both run over real HTTP against
// a real Server (the archiveFixture wiring), because the parts that are easy
// to get wrong -- LFS routing, path validation, the archived/permission
// gates -- all live in the handler rather than in a pure function.

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// uploadPart is one file in a multipart request body.
type uploadPart struct {
	// path is sent as an explicit "path" field before the file part; empty
	// leaves the handler to fall back to the file name.
	path    string
	name    string
	content []byte
}

// multipartBody assembles a request body in the same order a browser would:
// the text fields first, then each file preceded by its path.
func multipartBody(t *testing.T, message, description string, files []uploadPart) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if message != "" {
		if err := mw.WriteField("message", message); err != nil {
			t.Fatalf("write message field: %v", err)
		}
	}
	if description != "" {
		if err := mw.WriteField("description", description); err != nil {
			t.Fatalf("write description field: %v", err)
		}
	}
	for _, f := range files {
		if f.path != "" {
			if err := mw.WriteField("path", f.path); err != nil {
				t.Fatalf("write path field: %v", err)
			}
		}
		w, err := mw.CreateFormFile("file", f.name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := w.Write(f.content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// upload POSTs a multipart body to the upload endpoint.
func upload(t *testing.T, f *archiveFixture, path, token, message string, files []uploadPart) response {
	t.Helper()
	body, contentType := multipartBody(t, message, "", files)
	req := httptest.NewRequest("POST", path, body)
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

// readFile reads a committed file straight out of git, so an assertion about
// what landed never depends on the read API under test elsewhere.
func readFile(t *testing.T, f *archiveFixture, r *store.Repo, rev, path string) []byte {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	// Generous: one test deliberately commits a file just under the LFS
	// threshold as a plain blob and then reads it back.
	data, err := repo.ReadFile(rev, path, 16<<20)
	if err != nil {
		t.Fatalf("read %s at %s: %v", path, rev, err)
	}
	return data
}

func fileMissing(t *testing.T, f *archiveFixture, r *store.Repo, rev, path string) bool {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	_, _, err = repo.Stat(rev, path)
	return err != nil
}

// seedFile commits one file directly, for tests that need something to
// delete or overwrite.
func seedFile(t *testing.T, f *archiveFixture, r *store.Repo, path string, data []byte) string {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := repo.Commit(gitrepo.CommitRequest{
		Branch:  r.DefaultBranch,
		Message: "seed " + path,
		Author:  gitrepo.Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	}); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	entry, _, err := repo.Stat(r.DefaultBranch, path)
	if err != nil {
		t.Fatalf("stat seeded %s: %v", path, err)
	}
	return entry.Hash.String()
}

// --------------------------------------------------------------- pure parts

func TestUploadSummary(t *testing.T) {
	tests := []struct {
		name        string
		paths       []string
		message     string
		description string
		want        string
	}{
		{"single file default", []string{"a.txt"}, "", "", "Upload a.txt"},
		{"several files default", []string{"a.txt", "b.txt"}, "", "", "Upload 2 files"},
		{"caller's message wins", []string{"a.txt"}, "Add data", "", "Add data"},
		{"description appended", []string{"a.txt"}, "Add data", "from the browser", "Add data\n\nfrom the browser"},
		{"description with default", []string{"a.txt", "b.txt"}, "", "why", "Upload 2 files\n\nwhy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uploadSummary(tt.paths, tt.message, tt.description); got != tt.want {
				t.Errorf("uploadSummary(%v, %q, %q) = %q, want %q", tt.paths, tt.message, tt.description, got, tt.want)
			}
		})
	}
}

func TestDeleteSummary(t *testing.T) {
	if got, want := deleteSummary("docs/a.md", "", ""), "Delete docs/a.md"; got != want {
		t.Errorf("deleteSummary default = %q, want %q", got, want)
	}
	if got, want := deleteSummary("a", "Remove it", "because"), "Remove it\n\nbecause"; got != want {
		t.Errorf("deleteSummary with message = %q, want %q", got, want)
	}
}

func TestUploadPath(t *testing.T) {
	// Paths bind to the file parts that follow them, in order.
	target, rest := uploadPath([]string{"data/a.txt", "data/b.txt"}, "a.txt")
	if target != "data/a.txt" || len(rest) != 1 {
		t.Fatalf("uploadPath = %q, rest %v; want data/a.txt with one left", target, rest)
	}
	target, rest = uploadPath(rest, "b.txt")
	if target != "data/b.txt" || len(rest) != 0 {
		t.Fatalf("uploadPath = %q, rest %v; want data/b.txt with none left", target, rest)
	}
	// With no path field left, the browser's file name is the path.
	target, rest = uploadPath(rest, "c.txt")
	if target != "c.txt" || len(rest) != 0 {
		t.Fatalf("uploadPath fallback = %q, rest %v; want c.txt", target, rest)
	}
}

func TestCleanUploadPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"/data/a.txt", "data/a.txt"},
		{"  data/a.txt  ", "data/a.txt"},
		{"data/sub/a.txt", "data/sub/a.txt"},
	}
	for _, tt := range ok {
		got, err := cleanUploadPath(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("cleanUploadPath(%q) = %q, %v; want %q, nil", tt.in, got, err, tt.want)
		}
	}
	bad := []string{"", "   ", "/", "../escape.txt", "data/../../escape.txt", ".git/config", "a/.GIT/hooks/pre-commit", "a\x00b"}
	for _, in := range bad {
		if got, err := cleanUploadPath(in); err == nil {
			t.Errorf("cleanUploadPath(%q) = %q, nil; want an error", in, got)
		}
	}
}

func TestLFSRoute(t *testing.T) {
	rules := gitrepo.ParseGitAttributes([]byte(gitrepo.DefaultGitAttributes("model") + "*.csv -filter=lfs\n"))

	if forced, bySize := lfsRoute(rules, "model.safetensors"); !forced || !bySize {
		t.Errorf("lfsRoute(*.safetensors) = %v, %v; want forced", forced, bySize)
	}
	// No pattern: undecided until the reader finds out how big it is.
	if forced, bySize := lfsRoute(rules, "notes.txt"); forced || !bySize {
		t.Errorf("lfsRoute(notes.txt) = %v, %v; want size-decided", forced, bySize)
	}
	// Negated: never LFS, whatever the size.
	if forced, bySize := lfsRoute(rules, "data/rows.csv"); forced || bySize {
		t.Errorf("lfsRoute(rows.csv) = %v, %v; want never", forced, bySize)
	}
}

// --------------------------------------------------------------- upload API

func TestUpload_PlainFilesLandInOneCommit(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "Add two files", []uploadPart{
		{path: "README.md", name: "README.md", content: []byte("# hi\n")},
		{path: "docs/notes.txt", name: "notes.txt", content: []byte("notes\n")},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.UploadFilesResponse
	resp.json(t, &body)
	if len(body.Paths) != 2 || body.Paths[0] != "README.md" || body.Paths[1] != "docs/notes.txt" {
		t.Fatalf("paths = %v, want [README.md docs/notes.txt]", body.Paths)
	}
	if body.CommitOID == "" {
		t.Fatal("commit_oid is empty")
	}
	if got := string(readFile(t, f, r, "main", "README.md")); got != "# hi\n" {
		t.Errorf("README.md = %q", got)
	}
	if got := string(readFile(t, f, r, "main", "docs/notes.txt")); got != "notes\n" {
		t.Errorf("docs/notes.txt = %q", got)
	}

	// One commit, not one per file: the new head's only parent is the root.
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	commits, _, err := repo.ListCommits("main", "", plumbing.ZeroHash, 10)
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("history has %d commits, want 1 for a two-file upload", len(commits))
	}
	if commits[0].Message != "Add two files" {
		t.Errorf("commit message = %q", commits[0].Message)
	}
}

// A file name with no explicit path field is committed under its own name.
func TestUpload_FallsBackToTheFileName(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", []uploadPart{
		{name: "config.json", content: []byte("{}")},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "config.json")); got != "{}" {
		t.Errorf("config.json = %q", got)
	}
}

// A .gitattributes pattern routes a file to LFS however small it is: git gets
// a pointer, the object store gets the bytes, and the repository gets a link
// entitling it to read them back.
func TestUpload_GitattributesPatternGoesToLFS(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")
	content := []byte("not really tensors")

	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", []uploadPart{
		{path: "model.safetensors", name: "model.safetensors", content: content},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	pointer, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "model.safetensors"))
	if !ok {
		t.Fatalf("model.safetensors is not an LFS pointer: %q", readFile(t, f, r, "main", "model.safetensors"))
	}
	if pointer.Size != int64(len(content)) {
		t.Errorf("pointer size = %d, want %d", pointer.Size, len(content))
	}

	stored, err := f.obj.Get(context.Background(), storage.LFSKey(pointer.OID))
	if err != nil {
		t.Fatalf("lfs object not in storage: %v", err)
	}
	defer stored.Close()
	got, _ := io.ReadAll(stored)
	if !bytes.Equal(got, content) {
		t.Errorf("stored bytes = %q, want %q", got, content)
	}

	owned, err := f.st.RepoHasLFSObject(context.Background(), r.ID, pointer.OID)
	if err != nil || !owned {
		t.Fatalf("RepoHasLFSObject = %v, %v; want true, nil", owned, err)
	}

	// Nothing is left behind under the scratch prefix.
	scratch, err := f.obj.List(context.Background(), "tmp/uploads/")
	if err != nil {
		t.Fatalf("list scratch: %v", err)
	}
	if len(scratch) != 0 {
		t.Errorf("scratch objects left behind: %v", scratch)
	}
}

// No pattern matches, so size alone decides -- and the handler only learns
// the size by reading, which is the case the streaming split exists for.
func TestUpload_OverThresholdGoesToLFSBySize(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	big := bytes.Repeat([]byte("x"), gitrepo.LFSInlineThreshold+1)
	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", []uploadPart{
		{path: "big.unknown", name: "big.unknown", content: big},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	pointer, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "big.unknown"))
	if !ok {
		t.Fatal("big.unknown was committed inline, want an LFS pointer")
	}
	if pointer.Size != int64(len(big)) {
		t.Errorf("pointer size = %d, want %d", pointer.Size, len(big))
	}

	// Just under the threshold stays a plain blob.
	small := bytes.Repeat([]byte("x"), gitrepo.LFSInlineThreshold-1)
	resp = upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", []uploadPart{
		{path: "small.unknown", name: "small.unknown", content: small},
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if _, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "small.unknown")); ok {
		t.Error("small.unknown became an LFS pointer, want an inline blob")
	}
}

func TestUpload_RejectsPathEscapes(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	for _, bad := range []string{"../escape.txt", "docs/../../escape.txt", ".git/config", ".GIT/hooks/pre-commit"} {
		resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", []uploadPart{
			{path: bad, name: "x.txt", content: []byte("x")},
		})
		if resp.status() != 400 {
			t.Errorf("upload to %q: status = %d, want 400 (body %s)", bad, resp.status(), resp.rec.Body.String())
		}
	}
}

func TestUpload_RejectsTooManyFiles(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	files := make([]uploadPart, maxUploadFiles+1)
	for i := range files {
		name := fmt.Sprintf("f%d.txt", i)
		files[i] = uploadPart{path: name, name: name, content: []byte("x")}
	}
	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "", files)
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "at most") {
		t.Errorf("body = %s, want it to name the limit", resp.rec.Body.String())
	}
}

func TestUpload_RejectsNonMultipartAndShaRevisions(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/upload/model/alice/up/main", tok, map[string]any{"file": "x"})
	if resp.status() != 400 {
		t.Errorf("json body: status = %d, want 400", resp.status())
	}

	sha := strings.Repeat("a", 40)
	resp = upload(t, f, "/api/v1/upload/model/alice/up/"+sha, tok, "", []uploadPart{
		{path: "a.txt", name: "a.txt", content: []byte("x")},
	})
	if resp.status() != 400 {
		t.Errorf("sha revision: status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

func TestUpload_RejectsEmptyBody(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "up", "model")
	tok := f.token(f.alice, "write")

	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", tok, "just a message", nil)
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

func TestUpload_PermissionGates(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "up", "model")
	write := f.token(f.alice, "write")
	files := []uploadPart{{path: "a.txt", name: "a.txt", content: []byte("x")}}

	// Signed out.
	if resp := upload(t, f, "/api/v1/upload/model/alice/up/main", "", "", files); resp.status() != 401 {
		t.Errorf("anonymous: status = %d, want 401", resp.status())
	}
	// Someone else's repository.
	if resp := upload(t, f, "/api/v1/upload/model/alice/up/main", f.token(f.bob, "write"), "", files); resp.status() != 403 {
		t.Errorf("other user: status = %d, want 403", resp.status())
	}
	// The owner, but holding a read-only token.
	if resp := upload(t, f, "/api/v1/upload/model/alice/up/main", f.token(f.alice, "read"), "", files); resp.status() != 403 {
		t.Errorf("read token: status = %d, want 403", resp.status())
	}
	// Archived: read-only for everyone, owner included.
	f.archive("model", "alice", "up", write)
	resp := upload(t, f, "/api/v1/upload/model/alice/up/main", write, "", files)
	if resp.status() != 403 {
		t.Fatalf("archived: status = %d, want 403", resp.status())
	}
	if got := errorType(t, resp); got != "repository_archived" {
		t.Errorf("archived error type = %q, want repository_archived", got)
	}
}

// --------------------------------------------------------------- delete API

func TestDeleteFile_RemovesTheFile(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "del", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	seedFile(t, f, r, "docs/notes.txt", []byte("notes\n"))
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/v1/edit/model/alice/del/main/docs/notes.txt", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.EditFileResponse
	resp.json(t, &body)
	if body.Path != "docs/notes.txt" || body.CommitOID == "" {
		t.Fatalf("response = %+v, want the path and a commit oid", body)
	}
	if !fileMissing(t, f, r, "main", "docs/notes.txt") {
		t.Error("docs/notes.txt is still there after the delete")
	}
	if got := string(readFile(t, f, r, "main", "README.md")); got != "# hi\n" {
		t.Errorf("README.md was disturbed: %q", got)
	}
}

// The one deliberate difference from the editor: an LFS-tracked file can be
// deleted. Only the pointer leaves the tree -- the object stays in the
// bucket for `thinkingface gc` to reclaim once nothing references it.
func TestDeleteFile_AllowsLFSPointersAndKeepsTheObject(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "del", "model")
	tok := f.token(f.alice, "write")

	resp := upload(t, f, "/api/v1/upload/model/alice/del/main", tok, "", []uploadPart{
		{path: "model.safetensors", name: "model.safetensors", content: []byte("weights")},
	})
	if resp.status() != 200 {
		t.Fatalf("upload status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	pointer, ok := gitrepo.ParseLFSPointer(readFile(t, f, r, "main", "model.safetensors"))
	if !ok {
		t.Fatal("uploaded file is not an LFS pointer")
	}

	resp = f.do("DELETE", "/api/v1/edit/model/alice/del/main/model.safetensors", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("delete status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if !fileMissing(t, f, r, "main", "model.safetensors") {
		t.Error("the pointer is still in the tree")
	}
	if _, err := f.obj.Stat(context.Background(), storage.LFSKey(pointer.OID)); err != nil {
		t.Errorf("the LFS object was removed from storage (%v); deletion must only drop the reference", err)
	}
}

func TestDeleteFile_BaseOIDIsAnOptimisticLock(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "del", "model")
	oid := seedFile(t, f, r, "a.txt", []byte("one\n"))
	tok := f.token(f.alice, "write")

	// Stale: someone else changed the file since the caller looked.
	resp := f.do("DELETE", "/api/v1/edit/model/alice/del/main/a.txt", tok,
		apitypes.DeleteFileRequest{BaseOID: strings.Repeat("0", 40)})
	if resp.status() != 409 {
		t.Fatalf("stale base_oid: status = %d, want 409 (body %s)", resp.status(), resp.rec.Body.String())
	}
	if fileMissing(t, f, r, "main", "a.txt") {
		t.Fatal("a.txt was deleted despite the conflict")
	}

	// Current: the delete goes through, with the caller's own message.
	resp = f.do("DELETE", "/api/v1/edit/model/alice/del/main/a.txt", tok,
		apitypes.DeleteFileRequest{BaseOID: oid, Message: "Remove a.txt"})
	if resp.status() != 200 {
		t.Fatalf("matching base_oid: status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if !fileMissing(t, f, r, "main", "a.txt") {
		t.Error("a.txt survived the delete")
	}
}

func TestDeleteFile_MissingPathAndDirectory(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "del", "model")
	seedFile(t, f, r, "docs/notes.txt", []byte("notes\n"))
	tok := f.token(f.alice, "write")

	if resp := f.do("DELETE", "/api/v1/edit/model/alice/del/main/nope.txt", tok, nil); resp.status() != 404 {
		t.Errorf("missing path: status = %d, want 404", resp.status())
	}
	if resp := f.do("DELETE", "/api/v1/edit/model/alice/del/main/docs", tok, nil); resp.status() != 400 {
		t.Errorf("directory: status = %d, want 400", resp.status())
	}
	sha := strings.Repeat("a", 40)
	if resp := f.do("DELETE", "/api/v1/edit/model/alice/del/"+sha+"/docs/notes.txt", tok, nil); resp.status() != 400 {
		t.Errorf("sha revision: status = %d, want 400", resp.status())
	}
}

func TestDeleteFile_PermissionGates(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "del", "model")
	seedFile(t, f, r, "a.txt", []byte("one\n"))
	write := f.token(f.alice, "write")
	const path = "/api/v1/edit/model/alice/del/main/a.txt"

	if resp := f.do("DELETE", path, "", nil); resp.status() != 401 {
		t.Errorf("anonymous: status = %d, want 401", resp.status())
	}
	if resp := f.do("DELETE", path, f.token(f.bob, "write"), nil); resp.status() != 403 {
		t.Errorf("other user: status = %d, want 403", resp.status())
	}
	if resp := f.do("DELETE", path, f.token(f.alice, "read"), nil); resp.status() != 403 {
		t.Errorf("read token: status = %d, want 403", resp.status())
	}
	f.archive("model", "alice", "del", write)
	resp := f.do("DELETE", path, write, nil)
	if resp.status() != 403 {
		t.Fatalf("archived: status = %d, want 403", resp.status())
	}
	if got := errorType(t, resp); got != "repository_archived" {
		t.Errorf("archived error type = %q, want repository_archived", got)
	}
	if fileMissing(t, f, r, "main", "a.txt") {
		t.Error("a.txt was deleted through one of the refused calls")
	}
}

// ------------------------------------------------------ inline memory budget

// Non-LFS parts are held in memory until the commit is built, so the handler
// carries a budget for them across the whole request. Exercised directly:
// filling it over HTTP would mean posting 128MiB.
func TestReadUploadPart_RefusesAnInlineFileOverTheRemainingBudget(t *testing.T) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	w, err := mw.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 100)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	read := func(budget int64) (gitrepo.Op, error) {
		mr := multipart.NewReader(bytes.NewReader(body.Bytes()), mw.Boundary())
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		// No .gitattributes rules and well under the threshold, so this part
		// never reaches storage and the Server's dependencies stay unused.
		return (&Server{}).readUploadPart(httptest.NewRequest("POST", "/", nil), &store.Repo{},
			gitrepo.ParseGitAttributes(nil), "notes.txt", part, budget)
	}

	if _, err := read(10); !errors.Is(err, errUploadTooLarge) {
		t.Errorf("with 10 bytes of budget left: err = %v, want errUploadTooLarge", err)
	}
	op, err := read(maxUploadInlineTotalBytes)
	if err != nil {
		t.Fatalf("with a full budget: %v", err)
	}
	if len(op.Data) != 100 {
		t.Errorf("committed %d bytes, want 100", len(op.Data))
	}
}
