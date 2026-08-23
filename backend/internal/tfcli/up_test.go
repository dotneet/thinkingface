package tfcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	files, err := local.Scan(dir, local.Options{})
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
	files, _ := local.Scan(dir, local.Options{})

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
	files, _ := local.Scan(dir, local.Options{})

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
	files, _ := local.Scan(dir, local.Options{})

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
	files, _ := local.Scan(dir, local.Options{})

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
	files, _ := local.Scan(dir, local.Options{})

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
	files, _ := local.Scan(dir, local.Options{})

	if _, _, err := resolveUpTarget("a/b/c", "", dir, "alice", files); err == nil {
		t.Error("expected an error for a --to value with two slashes")
	}
}

func TestBuildUploadFilesNoCardOptionsLeavesReadmeAlone(t *testing.T) {
	dir := dirWithFiles(t, "README.md", "data.txt")
	files, err := local.Scan(dir, local.Options{})
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
	files, err := local.Scan(dir, local.Options{})
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
	files, err := local.Scan(dir, local.Options{})
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
