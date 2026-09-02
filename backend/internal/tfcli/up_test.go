package tfcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/local"
)

func TestSliceFlagSplitsOnComma(t *testing.T) {
	var s sliceFlag
	s.split = true
	if err := s.Set("a,b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("c"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(s.values) != len(want) {
		t.Fatalf("values = %v, want %v", s.values, want)
	}
	for i, v := range want {
		if s.values[i] != v {
			t.Errorf("values[%d] = %q, want %q", i, s.values[i], v)
		}
	}
}

func TestSliceFlagNoSplitKeepsCommasVerbatim(t *testing.T) {
	var s sliceFlag // split defaults to false, as used for --include/--exclude
	if err := s.Set("*.parquet"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("data/*,more/*"); err != nil {
		t.Fatal(err)
	}
	want := []string{"*.parquet", "data/*,more/*"}
	if len(s.values) != len(want) {
		t.Fatalf("values = %v, want %v", s.values, want)
	}
	for i, v := range want {
		if s.values[i] != v {
			t.Errorf("values[%d] = %q, want %q", i, s.values[i], v)
		}
	}
}

// dirWithFiles creates a temp directory containing the named files (content
// is irrelevant to these tests) and returns its path.
func dirWithFiles(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		full := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveUpTargetDefaults(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet")
	files, _, _, err := local.Scan(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ref, pinned, err := resolveUpTarget("", "", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Error("kind should not be pinned when neither --kind nor --to carries a prefix")
	}
	if ref.Namespace != "alice" {
		t.Errorf("namespace = %q, want alice (self)", ref.Namespace)
	}
	if ref.Kind != hub.KindDataset {
		t.Errorf("kind = %q, want dataset (no model files present)", ref.Kind)
	}
	wantName := filepath.Base(dir)
	if ref.Name != wantName {
		t.Errorf("name = %q, want basename of path %q", ref.Name, wantName)
	}
}

func TestResolveUpTargetToWithNamespace(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet")
	files, _, _, _ := local.Scan(dir, local.Options{})

	ref, pinned, err := resolveUpTarget("myns/myname", "", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Error("kind should not be pinned: --to had no datasets/models/ prefix")
	}
	if ref.Namespace != "myns" || ref.Name != "myname" {
		t.Errorf("ref = %+v, want myns/myname", ref)
	}
}

func TestResolveUpTargetToWithoutNamespaceUsesSelf(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet")
	files, _, _, _ := local.Scan(dir, local.Options{})

	ref, _, err := resolveUpTarget("myname", "", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "alice" || ref.Name != "myname" {
		t.Errorf("ref = %+v, want alice/myname", ref)
	}
}

func TestResolveUpTargetToPrefixPinsKind(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet") // would otherwise infer dataset anyway
	files, _, _, _ := local.Scan(dir, local.Options{})

	ref, pinned, err := resolveUpTarget("models/myns/myname", "", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Error("kind should be pinned by the models/ prefix on --to")
	}
	if ref.Kind != hub.KindModel {
		t.Errorf("kind = %q, want model", ref.Kind)
	}
}

func TestResolveUpTargetKindFlagWinsOverInference(t *testing.T) {
	dir := dirWithFiles(t, "model.safetensors") // would infer model
	files, _, _, _ := local.Scan(dir, local.Options{})

	ref, pinned, err := resolveUpTarget("", "dataset", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Error("kind should be pinned by --kind")
	}
	if ref.Kind != hub.KindDataset {
		t.Errorf("kind = %q, want dataset (--kind overrides inference)", ref.Kind)
	}
}

func TestResolveUpTargetKindFlagWinsOverToPrefix(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet")
	files, _, _, _ := local.Scan(dir, local.Options{})

	ref, pinned, err := resolveUpTarget("datasets/ns/name", "model", dir, "alice", files)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Error("kind should be pinned")
	}
	if ref.Kind != hub.KindModel {
		t.Errorf("kind = %q, want model (--kind beats the --to prefix)", ref.Kind)
	}
}

func TestResolveUpTargetInvalidToIsError(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet")
	files, _, _, _ := local.Scan(dir, local.Options{})

	if _, _, err := resolveUpTarget("a/b/c", "", dir, "alice", files); err == nil {
		t.Error("expected an error for a --to value with two slashes")
	}
}

func TestBuildUploadFilesNoCardOptionsLeavesReadmeAlone(t *testing.T) {
	dir := dirWithFiles(t, "README.md", "data.txt")
	files, _, _, err := local.Scan(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out, err := buildUploadFiles(files, local.CardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(files) {
		t.Fatalf("got %d files, want %d (untouched)", len(out), len(files))
	}
}

func TestBuildUploadFilesGeneratesReadmeWhenMissing(t *testing.T) {
	dir := dirWithFiles(t, "data.txt")
	files, _, _, err := local.Scan(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out, err := buildUploadFiles(files, local.CardOptions{License: "mit", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(files)+1 {
		t.Fatalf("got %d files, want %d (data.txt + generated README.md)", len(out), len(files)+1)
	}
	found := false
	for _, f := range out {
		if f.RepoPath == "README.md" {
			found = true
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(rc)
			rc.Close()
			if !strings.Contains(buf.String(), "mit") {
				t.Errorf("generated README should mention the license, got %q", buf.String())
			}
		}
	}
	if !found {
		t.Error("no README.md in the generated file list")
	}
}

func TestBuildUploadFilesMergesExistingReadme(t *testing.T) {
	dir := t.TempDir()
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("---\nlicense: apache-2.0\n---\n\n# Hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, _, _, err := local.Scan(dir, local.Options{})
	if err != nil {
		t.Fatal(err)
	}

	out, err := buildUploadFiles(files, local.CardOptions{Tags: []string{"nlp"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d files, want 1 (README.md replaced in place)", len(out))
	}
	rc, err := out[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(rc)
	rc.Close()
	content := buf.String()
	if !strings.Contains(content, "apache-2.0") {
		t.Errorf("merged README should keep the existing license, got %q", content)
	}
	if !strings.Contains(content, "nlp") {
		t.Errorf("merged README should add the new tag, got %q", content)
	}
	if !strings.Contains(content, "# Hello") {
		t.Errorf("merged README should keep the body, got %q", content)
	}
}

// --- end-to-end: `tf up` against a minimal fake thinkingface server --------

// fakeUpServer is just enough of the HF-compatible API for a brand new,
// all-regular-file upload: whoami, repo existence + create, an empty tree
// (unborn branch), preupload (everything regular) and commit.
type fakeUpServer struct {
	t   *testing.T
	srv *httptest.Server

	created bool
}

func newFakeUpServer(t *testing.T) *fakeUpServer {
	t.Helper()
	f := &fakeUpServer{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "alice",
			"fullname": "Alice",
			"email":    "alice@example.com",
			"orgs":     []any{},
			"auth":     map[string]any{"accessToken": map[string]any{"role": "write"}},
		})
	})

	mux.HandleFunc("GET /api/datasets/alice/x", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET /api/models/alice/x", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	mux.HandleFunc("POST /api/repos/create", func(w http.ResponseWriter, r *http.Request) {
		f.created = true
		writeJSON(t, w, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/datasets/alice/x/tree/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // unborn branch
	})

	mux.HandleFunc("POST /api/datasets/alice/x/preupload/main", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("preupload decode: %v", err)
		}
		resp := struct {
			Files []map[string]string `json:"files"`
		}{}
		for _, rf := range req.Files {
			resp.Files = append(resp.Files, map[string]string{"path": rf.Path, "uploadMode": "regular"})
		}
		writeJSON(t, w, resp)
	})

	mux.HandleFunc("POST /api/datasets/alice/x/commit/main", func(w http.ResponseWriter, r *http.Request) {
		// Body is NDJSON; we don't need to validate its shape for this test.
		writeJSON(t, w, map[string]any{
			"success":   true,
			"commitUrl": "http://example.invalid/datasets/alice/x/commit/abc1234def",
			"commitOid": "abc1234def5678",
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestUpEndToEndCreatesAndCommits(t *testing.T) {
	srv := newFakeUpServer(t)
	dir := dirWithFiles(t, "data.txt")

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--quiet",
		"--json",
	}, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("--quiet should suppress stderr progress, got %q", errOut.String())
	}
	if !srv.created {
		t.Error("server never saw POST /api/repos/create")
	}

	var jr upResultJSON
	if err := json.Unmarshal(out.Bytes(), &jr); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%s)", err, out.String())
	}
	if jr.Repo != "alice/x" {
		t.Errorf("repo = %q, want alice/x", jr.Repo)
	}
	if jr.Kind != "dataset" {
		t.Errorf("kind = %q, want dataset", jr.Kind)
	}
	if !jr.Created {
		t.Error("created should be true")
	}
	if jr.Commit == "" {
		t.Error("commit sha should be set")
	}
	if jr.Files != 1 {
		t.Errorf("files = %d, want 1", jr.Files)
	}
}

// fakeUpDeleteServer is an existing-repository counterpart to fakeUpServer:
// it has a remote tree to diff against, so `tf up --delete` has something to
// act on. deletedPaths records every "deletedFile" op the commit body carried,
// for TestUpDeleteExcludeKeepsFilesStillOnDisk to check against.
type fakeUpDeleteServer struct {
	t   *testing.T
	srv *httptest.Server

	deletedPaths []string
	filePaths    []string
	// commitAnswer replaces the body the fake commit endpoint returns.
	// Set it to reproduce a server (or a proxy) that answers 200 with
	// something other than a commit.
	commitAnswer map[string]any
}

// committedPaths is the sorted list of paths the commit actually wrote (the
// "file" / "lfsFile" ops), as opposed to the ones it deleted.
func (f *fakeUpDeleteServer) committedPaths() []string {
	out := append([]string(nil), f.filePaths...)
	sort.Strings(out)
	return out
}

func newFakeUpDeleteServer(t *testing.T, treeEntries string) *fakeUpDeleteServer {
	t.Helper()
	f := &fakeUpDeleteServer{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "alice",
			"fullname": "Alice",
			"email":    "alice@example.com",
			"orgs":     []any{},
			"auth":     map[string]any{"accessToken": map[string]any{"role": "write"}},
		})
	})

	// The repository already exists, so runUp never calls create.
	mux.HandleFunc("GET /api/datasets/alice/x", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/datasets/alice/x/tree/main", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, treeEntries)
	})

	mux.HandleFunc("POST /api/datasets/alice/x/preupload/main", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("preupload decode: %v", err)
		}
		resp := struct {
			Files []map[string]string `json:"files"`
		}{}
		for _, rf := range req.Files {
			resp.Files = append(resp.Files, map[string]string{"path": rf.Path, "uploadMode": "regular"})
		}
		writeJSON(t, w, resp)
	})

	mux.HandleFunc("POST /api/datasets/alice/x/commit/main", func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		for {
			var line struct {
				Key   string `json:"key"`
				Value struct {
					Path string `json:"path"`
				} `json:"value"`
			}
			if err := dec.Decode(&line); err != nil {
				break
			}
			switch line.Key {
			case "deletedFile":
				f.deletedPaths = append(f.deletedPaths, line.Value.Path)
			case "file", "lfsFile":
				f.filePaths = append(f.filePaths, line.Value.Path)
			}
		}
		answer := f.commitAnswer
		if answer == nil {
			answer = map[string]any{
				"success":   true,
				"commitUrl": "http://example.invalid/datasets/alice/x/commit/abc1234def",
				"commitOid": "abc1234def5678",
			}
		}
		writeJSON(t, w, answer)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// TestUpDeleteExcludeKeepsFilesStillOnDisk is the regression test for the
// data-loss bug: `tf up --exclude PATTERN --delete` must not treat a file
// that --exclude filtered out of the upload as "gone locally" and delete it
// from the remote, since it is sitting right there on disk. A remote path
// that is genuinely absent from disk must still be deleted.
func TestUpDeleteExcludeKeepsFilesStillOnDisk(t *testing.T) {
	dir := dirWithFiles(t, "data/train.parquet", "data/train.parquet.bak")

	srv := newFakeUpDeleteServer(t, `[
		{"type":"file","path":".gitattributes","oid":"a1","size":10},
		{"type":"file","path":"data/train.parquet.bak","oid":"b2","size":1},
		{"type":"file","path":"data/gone.txt","oid":"c3","size":1}
	]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--exclude", "*.bak",
		"--delete",
		"--quiet",
		"--json",
	}, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}

	var jr upResultJSON
	if err := json.Unmarshal(out.Bytes(), &jr); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%s)", err, out.String())
	}
	if jr.Deleted != 1 {
		t.Errorf("deleted count = %d, want 1", jr.Deleted)
	}
	if !equalStringSlices(srv.deletedPaths, []string{"data/gone.txt"}) {
		t.Fatalf("deletedFile ops = %v, want [data/gone.txt] (data/train.parquet.bak is excluded from upload but still on disk, and must survive)", srv.deletedPaths)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestUpDeleteKeepsSubtreeBehindSymlinkedDirectory is the regression test for
// the worst shape of the --delete data-loss bug: a directory that was
// uploaded once and has since become a symlink. The scanner will not follow
// it, so nothing under it is uploaded -- and that silence must not be read as
// "the user deleted a thousand files". The remote subtree survives, and the
// user is told on stderr why those files were not uploaded, --quiet or not.
func TestUpDeleteKeepsSubtreeBehindSymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	isolateEnv(t)

	realData := dirWithFiles(t, "train.parquet", "nested/b.csv")
	dir := dirWithFiles(t, "README.md")
	if err := os.Symlink(realData, filepath.Join(dir, "data")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	srv := newFakeUpDeleteServer(t, `[
		{"type":"file","path":".gitattributes","oid":"a1","size":10},
		{"type":"file","path":"data/train.parquet","oid":"b2","size":1},
		{"type":"file","path":"data/nested/b.csv","oid":"c3","size":1},
		{"type":"file","path":"gone.txt","oid":"d4","size":1}
	]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--delete",
		"--quiet",
		"--json",
	}, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !equalStringSlices(srv.deletedPaths, []string{"gone.txt"}) {
		t.Fatalf("deletedFile ops = %v, want [gone.txt]: nothing behind the unscanned data/ symlink may be deleted", srv.deletedPaths)
	}

	var jr upResultJSON
	if err := json.Unmarshal(out.Bytes(), &jr); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (stdout=%s)", err, out.String())
	}
	if jr.Deleted != 1 {
		t.Errorf("deleted count = %d, want 1", jr.Deleted)
	}

	warning := errOut.String()
	if !strings.Contains(warning, "data") || !strings.Contains(warning, local.ReasonSymlinkDir) {
		t.Errorf("stderr = %q, want a warning naming data and %q", warning, local.ReasonSymlinkDir)
	}
}

// TestUpWarnsAboutSkippedContentWithoutDelete covers the same silence without
// --delete: the files behind the symlink are simply not uploaded, which is a
// data gap the user has to hear about.
func TestUpWarnsAboutSkippedContentWithoutDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on windows")
	}
	isolateEnv(t)

	realData := dirWithFiles(t, "train.parquet")
	dir := dirWithFiles(t, "README.md")
	if err := os.Symlink(realData, filepath.Join(dir, "data")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	srv := newFakeUpDeleteServer(t, `[{"type":"file","path":".gitattributes","oid":"a1","size":10}]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--quiet",
	}, nil, &out, &errOut)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if len(srv.deletedPaths) != 0 {
		t.Fatalf("deletedFile ops = %v, want none without --delete", srv.deletedPaths)
	}
	if !strings.Contains(errOut.String(), "not uploading data") {
		t.Errorf("stderr = %q, want a warning that data/ was not uploaded", errOut.String())
	}
}

func TestWarnSkippedIsQuietAboutRoutineExclusions(t *testing.T) {
	var buf bytes.Buffer
	warnSkipped(&buf, []local.Skipped{
		{RepoPath: ".git", Dir: true, Reason: local.ReasonIgnoredDir},
		{RepoPath: "__pycache__", Dir: true, Reason: local.ReasonIgnoredDir},
	})
	if buf.Len() != 0 {
		t.Errorf("warnSkipped printed %q for documented exclusions, want silence", buf.String())
	}
}

func TestWarnSkippedCapsTheList(t *testing.T) {
	var skipped []local.Skipped
	for i := range maxSkipWarnings + 3 {
		skipped = append(skipped, local.Skipped{
			RepoPath: fmt.Sprintf("sock-%02d", i),
			Reason:   local.ReasonIrregular,
		})
	}
	var buf bytes.Buffer
	warnSkipped(&buf, skipped)
	lines := strings.Count(buf.String(), "\n")
	if lines != maxSkipWarnings+1 {
		t.Fatalf("warnSkipped printed %d lines, want %d warnings plus one summary:\n%s", lines, maxSkipWarnings, buf.String())
	}
	if !strings.Contains(buf.String(), "3 more path(s) skipped") {
		t.Errorf("stderr = %q, want it to count the paths it did not list", buf.String())
	}
}

// TestUpSkipsDotfilesAndSaysSo is the credential-leak regression: repositories
// here are world-readable, and `tf up ./project` used to publish a ".env"
// sitting next to the data with nothing on stderr to say it had.
func TestUpSkipsDotfilesAndSaysSo(t *testing.T) {
	isolateEnv(t)

	dir := dirWithFiles(t, "data/train.parquet", ".env", ".envrc", ".aws/credentials", ".gitignore")

	srv := newFakeUpDeleteServer(t, `[{"type":"file","path":".gitattributes","oid":"a1","size":10}]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--quiet",
		"--json",
	}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}

	committed := srv.committedPaths()
	want := []string{".gitignore", "data/train.parquet"}
	if !equalStringSlices(committed, want) {
		t.Fatalf("committed files = %v, want %v (dot-files must not be uploaded)", committed, want)
	}

	// Warnings survive --quiet, and they name the flag that opts back in.
	stderr := errOut.String()
	for _, want := range []string{".env", ".envrc", ".aws/", "--hidden"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
	if got := strings.Count(stderr, "hidden path(s)"); got != 1 {
		t.Errorf("stderr = %q, want exactly one grouped hidden-path warning, got %d", stderr, got)
	}
}

// TestUpHiddenFlagUploadsDotfiles is the opt-back-in half.
func TestUpHiddenFlagUploadsDotfiles(t *testing.T) {
	isolateEnv(t)

	dir := dirWithFiles(t, "data/train.parquet", ".env")
	srv := newFakeUpDeleteServer(t, `[{"type":"file","path":".gitattributes","oid":"a1","size":10}]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--hidden",
		"--quiet",
	}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	want := []string{".env", "data/train.parquet"}
	if !equalStringSlices(srv.committedPaths(), want) {
		t.Fatalf("committed files = %v, want %v", srv.committedPaths(), want)
	}
	if strings.Contains(errOut.String(), "hidden path(s)") {
		t.Errorf("stderr = %q, want no hidden-path warning with --hidden", errOut.String())
	}
}

// TestUpDeleteKeepsRemoteCopiesOfNowSkippedDotfiles is the dangerous half of
// the change: a repository uploaded before dot-files were skipped still has
// them on the remote, and the files are still on disk. "Not part of this
// upload" must not be read as "deleted locally".
func TestUpDeleteKeepsRemoteCopiesOfNowSkippedDotfiles(t *testing.T) {
	isolateEnv(t)

	dir := dirWithFiles(t, "data/train.parquet", ".env", ".aws/credentials")

	srv := newFakeUpDeleteServer(t, `[
		{"type":"file","path":".gitattributes","oid":"a1","size":10},
		{"type":"file","path":".env","oid":"b2","size":1},
		{"type":"file","path":".aws/credentials","oid":"c3","size":1},
		{"type":"file","path":"data/gone.txt","oid":"d4","size":1}
	]`)

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--delete",
		"--quiet",
	}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !equalStringSlices(srv.deletedPaths, []string{"data/gone.txt"}) {
		t.Fatalf("deletedFile ops = %v, want only [data/gone.txt]: a skipped dot-file is still on disk", srv.deletedPaths)
	}
}

// TestUpFailsWhenTheServerReportsNoCommit: `tf up` used to ignore the commit
// answer's success flag and an empty commitOid, printing a tick with an empty
// short oid and exiting 0 -- a script checking the exit code was told the
// upload had landed when nothing had.
func TestUpFailsWhenTheServerReportsNoCommit(t *testing.T) {
	isolateEnv(t)

	dir := dirWithFiles(t, "data/train.parquet")
	srv := newFakeUpDeleteServer(t, `[{"type":"file","path":".gitattributes","oid":"a1","size":10}]`)
	srv.commitAnswer = map[string]any{"success": false, "commitOid": ""}

	var out, errOut bytes.Buffer
	code := Main([]string{
		"up", dir,
		"--endpoint", srv.srv.URL,
		"--token", "t",
		"--to", "alice/x",
		"--json",
	}, nil, &out, &errOut)

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stderr=%s", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want no JSON result for a commit that did not happen", out.String())
	}
	if strings.Contains(errOut.String(), "✓") {
		t.Errorf("stderr = %q, want no success line", errOut.String())
	}
	if !strings.Contains(errOut.String(), "reported no commit") {
		t.Errorf("stderr = %q, want it to say the server reported no commit", errOut.String())
	}
}
