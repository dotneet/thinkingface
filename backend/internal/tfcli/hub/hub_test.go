package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func testRef() Ref { return Ref{Kind: KindDataset, Namespace: "alice", Name: "corpus"} }

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "tf_token", WithHTTPClient(srv.Client())), srv
}

func openBytes(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

func TestWhoami(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tf_token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Error("User-Agent must be set")
		}
		_, _ = io.WriteString(w, `{"name":"alice","fullname":"Alice","email":"a@example.com",
			"orgs":[{"name":"acme","fullname":"Acme","roleInOrg":"write"}],
			"auth":{"type":"access_token","accessToken":{"role":"write"}}}`)
	})
	c, _ := newTestClient(t, mux)

	user, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if user.Name != "alice" || user.Fullname != "Alice" || user.Email != "a@example.com" {
		t.Errorf("user = %+v", user)
	}
	if user.Role != "write" {
		t.Errorf("Role = %q, want write", user.Role)
	}
	if len(user.Orgs) != 1 || user.Orgs[0].Name != "acme" || user.Orgs[0].RoleInOrg != "write" {
		t.Errorf("Orgs = %+v", user.Orgs)
	}
}

func TestWhoamiUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"unauthorized","message":"authentication required"}}`)
	})
	c, _ := newTestClient(t, mux)

	_, err := c.Whoami(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("IsUnauthorized(%v) = false", err)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *Error: %v", err)
	}
	if e.Type != "unauthorized" || e.Message != "authentication required" {
		t.Errorf("error = %+v", e)
	}
	if !strings.Contains(e.Error(), "authentication required") {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestCreateRepo(t *testing.T) {
	var body map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/repos/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = io.WriteString(w, `{"url":"http://x/datasets/alice/corpus","repo_id":"alice/corpus"}`)
	})
	c, _ := newTestClient(t, mux)

	created, err := c.CreateRepo(context.Background(), testRef())
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if body["type"] != "dataset" || body["name"] != "alice/corpus" {
		t.Errorf("request body = %v", body)
	}
}

func TestCreateRepoConflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/repos/create", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		// The HF-compatible handler puts a bare string in "error" here.
		_, _ = io.WriteString(w, `{"url":"http://x/datasets/alice/corpus",
			"error":"You already created this dataset repo"}`)
	})
	c, _ := newTestClient(t, mux)

	created, err := c.CreateRepo(context.Background(), testRef())
	if err != nil {
		t.Fatalf("CreateRepo on conflict must not fail: %v", err)
	}
	if created {
		t.Error("created = true, want false")
	}
}

func TestRepoExists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/datasets/alice/corpus", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"alice/corpus"}`)
	})
	mux.HandleFunc("GET /api/datasets/alice/gone", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found","message":"no such repository"}}`)
	})
	mux.HandleFunc("GET /api/datasets/alice/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"type":"forbidden","message":"nope"}}`)
	})
	c, _ := newTestClient(t, mux)

	if ok, err := c.RepoExists(context.Background(), testRef()); err != nil || !ok {
		t.Errorf("RepoExists(corpus) = %v, %v", ok, err)
	}
	missing := Ref{Kind: KindDataset, Namespace: "alice", Name: "gone"}
	if ok, err := c.RepoExists(context.Background(), missing); err != nil || ok {
		t.Errorf("RepoExists(gone) = %v, %v", ok, err)
	}
	secret := Ref{Kind: KindDataset, Namespace: "alice", Name: "secret"}
	if _, err := c.RepoExists(context.Background(), secret); !IsForbidden(err) {
		t.Errorf("RepoExists(secret) error = %v, want 403", err)
	}
}

func TestTree(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/datasets/alice/corpus/tree/main", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recursive") != "true" {
			t.Errorf("recursive query = %q", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[
			{"type":"directory","oid":"d1","size":0,"path":"data"},
			{"type":"file","oid":"abc","size":6,"path":"README.md"},
			{"type":"file","oid":"ptr","size":2048,"path":"data/train.parquet",
			 "lfs":{"oid":"deadbeef","sha256":"deadbeef","size":2048,"pointerSize":130}}
		]`)
	})
	c, _ := newTestClient(t, mux)

	entries, err := c.Tree(context.Background(), testRef(), "main")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (directories filtered): %+v", len(entries), entries)
	}
	if entries[0].Path != "README.md" || entries[0].OID != "abc" || entries[0].LFS != nil {
		t.Errorf("entry[0] = %+v", entries[0])
	}
	if entries[1].LFS == nil || entries[1].LFS.OID != "deadbeef" || entries[1].LFS.Size != 2048 {
		t.Errorf("entry[1] = %+v", entries[1])
	}
}

func TestTreeNotFoundIsEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found","message":"read tree: not found"}}`)
	})
	c, _ := newTestClient(t, mux)

	entries, err := c.Tree(context.Background(), testRef(), "main")
	if err != nil {
		t.Fatalf("Tree on 404 must not fail: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %+v, want nil", entries)
	}
}

func TestPreuploadBatches(t *testing.T) {
	var batches [][]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/datasets/alice/corpus/preupload/main", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Files []struct {
				Path   string `json:"path"`
				Sample string `json:"sample"`
				Size   int64  `json:"size"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		paths := make([]string, 0, len(req.Files))
		out := make([]map[string]any, 0, len(req.Files))
		for _, f := range req.Files {
			paths = append(paths, f.Path)
			mode := "regular"
			if strings.HasSuffix(f.Path, ".bin") {
				mode = "lfs"
			}
			if _, err := base64.StdEncoding.DecodeString(f.Sample); err != nil {
				t.Errorf("sample for %s is not base64: %v", f.Path, err)
			}
			out = append(out, map[string]any{"path": f.Path, "uploadMode": mode, "shouldIgnore": false})
		}
		batches = append(batches, paths)
		writeTestJSON(t, w, map[string]any{"files": out})
	})
	c, _ := newTestClient(t, mux)

	files := make([]PreuploadFile, 0, 300)
	for i := range 300 {
		name := "f" + strconv.Itoa(i) + ".txt"
		if i == 299 {
			name = "big.bin"
		}
		files = append(files, PreuploadFile{Path: name, Size: int64(i), Sample: []byte("head")})
	}
	modes, err := c.Preupload(context.Background(), testRef(), "main", files)
	if err != nil {
		t.Fatalf("Preupload: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("got %d requests, want 2", len(batches))
	}
	if len(batches[0]) != 256 || len(batches[1]) != 44 {
		t.Errorf("batch sizes = %d, %d; want 256, 44", len(batches[0]), len(batches[1]))
	}
	if len(modes) != 300 {
		t.Errorf("got %d modes, want 300", len(modes))
	}
	if modes["big.bin"] != ModeLFS || modes["f0.txt"] != ModeRegular {
		t.Errorf("modes: big.bin=%q f0.txt=%q", modes["big.bin"], modes["f0.txt"])
	}
}

func TestLFSBatchUploadOrderAndDedup(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /datasets/alice/corpus.git/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if ct := r.Header.Get("Content-Type"); ct != contentTypeLFS {
			t.Errorf("Content-Type = %q", ct)
		}
		var req struct {
			Operation string   `json:"operation"`
			Transfers []string `json:"transfers"`
			HashAlgo  string   `json:"hash_algo"`
			Objects   []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Operation != "upload" || req.HashAlgo != "sha256" ||
			len(req.Transfers) != 1 || req.Transfers[0] != "basic" {
			t.Errorf("request = %+v", req)
		}
		// Answer in the reverse order to prove the client re-sorts.
		objs := []map[string]any{
			{"oid": "bbbb", "size": 2}, // already stored: no actions
			{"oid": "cccc", "size": 3, "error": map[string]any{"code": 422, "message": "bad oid"}},
			{"oid": "aaaa", "size": 1, "actions": map[string]any{
				"upload": map[string]any{"href": "http://up/aaaa", "header": map[string]string{"Authorization": "Bearer x"}},
				"verify": map[string]any{"href": "http://verify"},
			}},
		}
		writeTestJSON(t, w, map[string]any{"transfer": "basic", "hash_algo": "sha256", "objects": objs})
	})
	c, _ := newTestClient(t, mux)

	in := []LFSObject{{OID: "aaaa", Size: 1}, {OID: "bbbb", Size: 2}, {OID: "cccc", Size: 3}}
	out, err := c.LFSBatchUpload(context.Background(), testRef(), in)
	if err != nil {
		t.Fatalf("LFSBatchUpload: %v", err)
	}
	if gotPath != "/datasets/alice/corpus.git/info/lfs/objects/batch" {
		t.Errorf("path = %q", gotPath)
	}
	if len(out) != 3 {
		t.Fatalf("got %d results", len(out))
	}
	if out[0].OID != "aaaa" || out[0].Upload == nil || out[0].Upload.Href != "http://up/aaaa" {
		t.Errorf("result[0] = %+v", out[0])
	}
	if out[0].Upload.Header["Authorization"] != "Bearer x" {
		t.Errorf("upload header = %v", out[0].Upload.Header)
	}
	if out[0].Verify == nil || out[0].Verify.Href != "http://verify" {
		t.Errorf("verify = %+v", out[0].Verify)
	}
	if out[1].OID != "bbbb" || out[1].Upload != nil {
		t.Errorf("result[1] = %+v (deduplicated object must have no upload action)", out[1])
	}
	if out[2].Err == nil || !strings.Contains(out[2].Err.Error(), "bad oid") {
		t.Errorf("result[2].Err = %v", out[2].Err)
	}
}

func TestLFSBatchUploadModelURL(t *testing.T) {
	var path string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bob/llm.git/info/lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		writeTestJSON(t, w, map[string]any{"objects": []map[string]any{{"oid": "aaaa", "size": 1}}})
	})
	c, _ := newTestClient(t, mux)

	ref := Ref{Kind: KindModel, Namespace: "bob", Name: "llm"}
	if _, err := c.LFSBatchUpload(context.Background(), ref, []LFSObject{{OID: "aaaa", Size: 1}}); err != nil {
		t.Fatalf("LFSBatchUpload: %v", err)
	}
	if path != "/bob/llm.git/info/lfs/objects/batch" {
		t.Errorf("model batch path = %q", path)
	}
}

func TestPutLFSObject(t *testing.T) {
	content := []byte("0123456789")
	var gotLen int64
	var gotAuth, gotCT, gotBearer string
	var body []byte
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /lfs/aaaa", func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBearer = r.Header.Get("X-Token")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	c, srv := newTestClient(t, mux)

	action := LFSAction{
		Href:   srv.URL + "/lfs/aaaa",
		Header: map[string]string{"Authorization": "Bearer proxy", "X-Token": "t"},
	}
	if err := c.PutLFSObject(context.Background(), action, openBytes(content), int64(len(content))); err != nil {
		t.Fatalf("PutLFSObject: %v", err)
	}
	if gotLen != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d", gotLen, len(content))
	}
	if gotAuth != "Bearer proxy" {
		t.Errorf("Authorization = %q; the action header must win over the client token", gotAuth)
	}
	if gotBearer != "t" {
		t.Errorf("X-Token = %q", gotBearer)
	}
	if gotCT != contentTypeBinary {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if !bytes.Equal(body, content) {
		t.Errorf("body = %q", body)
	}
}

func TestPutLFSObjectRetriesOnce(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /lfs/aaaa", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	c, srv := newTestClient(t, mux)

	action := LFSAction{Href: srv.URL + "/lfs/aaaa"}
	if err := c.PutLFSObject(context.Background(), action, openBytes([]byte("hi")), 2); err != nil {
		t.Fatalf("PutLFSObject: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestPutLFSObjectDoesNotRetryClientError(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /lfs/aaaa", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", contentTypeLFS)
		w.WriteHeader(http.StatusBadRequest)
		// The LFS error shape, which is not the JSON API's.
		_, _ = io.WriteString(w, `{"message":"uploaded content hashes to bbbb"}`)
	})
	c, srv := newTestClient(t, mux)

	err := c.PutLFSObject(context.Background(), LFSAction{Href: srv.URL + "/lfs/aaaa"}, openBytes([]byte("hi")), 2)
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *Error: %v", err)
	}
	if e.Message != "uploaded content hashes to bbbb" {
		t.Errorf("message = %q (the {\"message\":...} shape must parse)", e.Message)
	}
}

func TestVerifyLFSObject(t *testing.T) {
	var got struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}
	var ct, auth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /verify", func(w http.ResponseWriter, r *http.Request) {
		ct, auth = r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		writeTestJSON(t, w, map[string]any{"oid": got.OID, "size": got.Size})
	})
	c, srv := newTestClient(t, mux)

	action := LFSAction{Href: srv.URL + "/verify", Header: map[string]string{"Authorization": "Bearer proxy"}}
	if err := c.VerifyLFSObject(context.Background(), action, LFSObject{OID: "aaaa", Size: 7}); err != nil {
		t.Fatalf("VerifyLFSObject: %v", err)
	}
	if got.OID != "aaaa" || got.Size != 7 {
		t.Errorf("body = %+v", got)
	}
	if ct != contentTypeLFS {
		t.Errorf("Content-Type = %q", ct)
	}
	if auth != "Bearer proxy" {
		t.Errorf("Authorization = %q", auth)
	}
}

// commitLine is one decoded NDJSON operation, used by the commit tests.
type commitLine struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func decodeCommitBody(t *testing.T, r io.Reader) []commitLine {
	t.Helper()
	var lines []commitLine
	dec := json.NewDecoder(r)
	for {
		var line commitLine
		if err := dec.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode ndjson: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestCommitNDJSON(t *testing.T) {
	var lines []commitLine
	var contentType string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/datasets/alice/corpus/commit/main", func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		lines = decodeCommitBody(t, r.Body)
		writeTestJSON(t, w, map[string]any{
			"success": true, "commitOid": "c0ffee", "commitUrl": "http://x/commit/c0ffee",
		})
	})
	c, _ := newTestClient(t, mux)

	content := []byte("hello \"world\"\n\x00binary")
	ops := []CommitOp{
		{Kind: OpFile, Path: `dir/a "quoted".txt`, Open: openBytes(content), Size: int64(len(content))},
		{Kind: OpLFSFile, Path: "data/train.bin", OID: "deadbeef", Size: 4096},
		{Kind: OpDeleteFile, Path: "old.txt"},
		{Kind: OpDeleteFolder, Path: "stale"},
	}
	res, err := c.Commit(context.Background(), testRef(), "main", "", "body text", ops)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.OID != "c0ffee" || res.URL != "http://x/commit/c0ffee" {
		t.Errorf("result = %+v", res)
	}
	if contentType != contentTypeNDJSON {
		t.Errorf("Content-Type = %q", contentType)
	}
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5: %+v", len(lines), lines)
	}

	var header struct{ Summary, Description string }
	if lines[0].Key != "header" {
		t.Fatalf("line 0 key = %q", lines[0].Key)
	}
	if err := json.Unmarshal(lines[0].Value, &header); err != nil {
		t.Fatalf("header: %v", err)
	}
	if header.Summary != "Upload files" || header.Description != "body text" {
		t.Errorf("header = %+v (empty summary must default)", header)
	}

	var file struct{ Path, Content, Encoding string }
	if lines[1].Key != "file" {
		t.Fatalf("line 1 key = %q", lines[1].Key)
	}
	if err := json.Unmarshal(lines[1].Value, &file); err != nil {
		t.Fatalf("file: %v", err)
	}
	if file.Path != `dir/a "quoted".txt` || file.Encoding != "base64" {
		t.Errorf("file = %+v", file)
	}
	decoded, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		t.Fatalf("content is not base64: %v", err)
	}
	if !bytes.Equal(decoded, content) {
		t.Errorf("content = %q, want %q", decoded, content)
	}

	var lfsFile struct {
		Path, Algo, OID string
		Size            int64
	}
	if lines[2].Key != "lfsFile" {
		t.Fatalf("line 2 key = %q", lines[2].Key)
	}
	if err := json.Unmarshal(lines[2].Value, &lfsFile); err != nil {
		t.Fatalf("lfsFile: %v", err)
	}
	if lfsFile.Path != "data/train.bin" || lfsFile.OID != "deadbeef" || lfsFile.Size != 4096 || lfsFile.Algo != "sha256" {
		t.Errorf("lfsFile = %+v", lfsFile)
	}

	if lines[3].Key != "deletedFile" || !strings.Contains(string(lines[3].Value), "old.txt") {
		t.Errorf("line 3 = %+v", lines[3])
	}
	if lines[4].Key != "deletedFolder" || !strings.Contains(string(lines[4].Value), "stale") {
		t.Errorf("line 4 = %+v", lines[4])
	}
}

func TestCommitConflict(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/datasets/alice/corpus/commit/main", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"type":"conflict","message":"branch changed concurrently; retry the commit"}}`)
	})
	c, _ := newTestClient(t, mux)

	ops := []CommitOp{{Kind: OpDeleteFile, Path: "x"}}
	_, err := c.Commit(context.Background(), testRef(), "main", "s", "", ops)
	if !IsConflict(err) {
		t.Fatalf("err = %v, want a 409", err)
	}
	var e *Error
	if errors.As(err, &e) && e.Type != "conflict" {
		t.Errorf("type = %q", e.Type)
	}
}

func TestMintTokenAndRevoke(t *testing.T) {
	var loginCalls, tokenCalls, deleteCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		if r.Header.Get("Origin") != "" {
			t.Error("MintToken must not send an Origin header")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("login must not carry the caller's bearer token")
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req["username"] != "alice" || req["password"] != "hunter2" {
			t.Errorf("credentials = %v", req)
		}
		http.SetCookie(w, &http.Cookie{Name: "tf_session", Value: "sess", Path: "/"})
		writeTestJSON(t, w, map[string]any{"user": map[string]any{"username": "alice"}})
	})
	mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		if _, err := r.Cookie("tf_session"); err != nil {
			t.Errorf("token request must carry the session cookie: %v", err)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req["name"] != "laptop" || req["scope"] != "write" {
			t.Errorf("token request = %v", req)
		}
		writeTestJSON(t, w, map[string]any{
			"id": 42, "name": "laptop", "scope": "write", "token": "tf_secret",
		})
	})
	mux.HandleFunc("DELETE /api/v1/tokens/42", func(w http.ResponseWriter, r *http.Request) {
		deleteCalls++
		if r.Header.Get("Authorization") != "Bearer tf_token" {
			t.Errorf("revoke Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c, _ := newTestClient(t, mux)

	tok, err := c.MintToken(context.Background(), "alice", "hunter2", "laptop", "write")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if tok.ID != 42 || tok.Token != "tf_secret" || tok.Scope != "write" || tok.Name != "laptop" {
		t.Errorf("token = %+v", tok)
	}
	if loginCalls != 1 || tokenCalls != 1 {
		t.Errorf("calls: login=%d tokens=%d", loginCalls, tokenCalls)
	}

	if err := c.RevokeToken(context.Background(), 42); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if deleteCalls != 1 {
		t.Errorf("delete calls = %d", deleteCalls)
	}
}

func TestMintTokenBadPassword(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"unauthorized","message":"username or password is incorrect"}}`)
	})
	c, _ := newTestClient(t, mux)

	if _, err := c.MintToken(context.Background(), "alice", "wrong", "x", "write"); !IsUnauthorized(err) {
		t.Fatalf("err = %v, want 401", err)
	}
}

func TestErrorFallsBackToRawBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", 500))
	})
	c, _ := newTestClient(t, mux)

	_, err := c.Whoami(context.Background())
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *Error: %v", err)
	}
	if e.Status != http.StatusBadGateway {
		t.Errorf("status = %d", e.Status)
	}
	if len(e.Message) > errorMessageLimit+3 {
		t.Errorf("message not truncated: %d bytes", len(e.Message))
	}
}

func TestParseKindAndRef(t *testing.T) {
	for _, s := range []string{"dataset", "datasets", "DataSet"} {
		if k, err := ParseKind(s); err != nil || k != KindDataset {
			t.Errorf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	for _, s := range []string{"model", "models", " MODEL "} {
		if k, err := ParseKind(s); err != nil || k != KindModel {
			t.Errorf("ParseKind(%q) = %q, %v", s, k, err)
		}
	}
	if _, err := ParseKind("space"); err == nil {
		t.Error("ParseKind(space) must fail")
	}

	c := New("http://h/", "")
	if c.Endpoint() != "http://h" {
		t.Errorf("Endpoint = %q", c.Endpoint())
	}
	if got := c.WebURL(testRef()); got != "http://h/datasets/alice/corpus" {
		t.Errorf("dataset WebURL = %q", got)
	}
	model := Ref{Kind: KindModel, Namespace: "bob", Name: "llm"}
	if got := c.WebURL(model); got != "http://h/models/bob/llm" {
		t.Errorf("model WebURL = %q", got)
	}
	if got := c.CommitURL(model, "abc"); got != "http://h/models/bob/llm/commit/abc" {
		t.Errorf("CommitURL = %q", got)
	}
	if testRef().String() != "datasets/alice/corpus" {
		t.Errorf("Ref.String = %q", testRef().String())
	}
}

func TestGitBlobSHA1(t *testing.T) {
	// Values from `git hash-object`.
	cases := []struct {
		content string
		want    string
	}{
		{"", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{"hello\n", "ce013625030ba8dba906f756967f9e9ca394464a"},
	}
	for _, tc := range cases {
		got, err := GitBlobSHA1(strings.NewReader(tc.content), int64(len(tc.content)))
		if err != nil {
			t.Fatalf("GitBlobSHA1(%q): %v", tc.content, err)
		}
		if got != tc.want {
			t.Errorf("GitBlobSHA1(%q) = %s, want %s", tc.content, got, tc.want)
		}
	}
	if _, err := GitBlobSHA1(strings.NewReader("hello"), 99); err == nil {
		t.Error("a size that does not match the stream must be an error")
	}
}

func TestSHA256Hex(t *testing.T) {
	// sha256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got, n, err := SHA256Hex(strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("SHA256Hex: %v", err)
	}
	if got != want || n != 3 {
		t.Errorf("SHA256Hex = %s, %d", got, n)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("write response: %v", err)
	}
}
