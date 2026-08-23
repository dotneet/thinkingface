// In-process end-to-end tests for the tf CLI (backend/cmd/tf): the real
// Server -- SQLite store, on-disk git manager, in-memory object store -- is
// served over a loopback httptest listener and driven through
// internal/tfcli/hub (the CLI's HTTP client) and tfcli.Main itself. This is
// the one place the client and the server's HF-compatible surface meet in a
// test, so a wire-format drift on either side shows up here.

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/tfcli"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

type tfFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	obj   *memStore
	ts    *httptest.Server
	alice *store.User
	token string // alice, write scope
}

// newTFFixture is archiveFixture's sibling with a real listener: the LFS
// proxy hrefs the server hands out are absolute URLs built from
// Config.PublicURL, so PublicURL has to be the address the client can reach.
func newTFFixture(t *testing.T) *tfFixture {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	cfg := &config.Config{
		PublicURL:     ts.URL,
		WALMode:       "off",
		SessionSecret: "test-secret-test-secret",
		SignedURLTTL:  time.Hour,
	}
	obj := newMemStore()
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitrepo.NewManager(t.TempDir()),
		Storage:  obj,
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})
	handler = srv.Handler()

	f := &tfFixture{t: t, s: srv, st: st, obj: obj, ts: ts}
	hash, err := auth.HashPassword("s3cret-pass")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	f.alice, err = st.CreateUser(ctx, "alice", "alice@example.com", hash, false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	tok, tokHash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := st.CreateToken(ctx, f.alice.ID, "test", "write", tokHash); err != nil {
		t.Fatalf("create token: %v", err)
	}
	f.token = tok

	// The CLI reads/writes its config file; keep it inside the test and make
	// sure nothing from the developer's shell leaks into endpoint resolution.
	t.Setenv("TF_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	for _, k := range []string{"TF_ENDPOINT", "TF_TOKEN", "THINKINGFACE_ENDPOINT", "THINKINGFACE_TOKEN", "THINKINGFACE_API_KEY", "HF_ENDPOINT", "HF_TOKEN"} {
		t.Setenv(k, "")
	}
	return f
}

func (f *tfFixture) client() *hub.Client { return hub.New(f.ts.URL, f.token) }

// raw fetches a file through the UI raw endpoint, which answers a JSON
// envelope ({"content": ...}) for text-sized regular blobs. The decoded
// content is returned; LFS objects are compared with lfsObject instead.
func (f *tfFixture) raw(ref hub.Ref, rev, path string) (int, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/api/v1/raw/"+string(ref.Kind)+"/"+ref.ID()+"/"+rev+"/"+path, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("raw %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, b
	}
	var env struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		f.t.Fatalf("raw %s: not a JSON envelope: %s", path, b)
	}
	return resp.StatusCode, []byte(env.Content)
}

// lfsObject reads an LFS object's bytes straight out of the object store.
func (f *tfFixture) lfsObject(oid string) []byte {
	f.t.Helper()
	rc, err := f.obj.Get(context.Background(), storage.LFSKey(oid))
	if err != nil {
		f.t.Fatalf("lfs object %s: %v", oid, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		f.t.Fatalf("read lfs object %s: %v", oid, err)
	}
	return b
}

// writeTree materialises files under dir; content "" means an empty file.
func writeTree(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

func localFiles(t *testing.T, dir string) []hub.LocalFile {
	t.Helper()
	var out []hub.LocalFile
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, hub.LocalFile{
			RepoPath: filepath.ToSlash(rel),
			Size:     info.Size(),
			Open:     func() (io.ReadCloser, error) { return os.Open(p) },
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func treeByPath(t *testing.T, c *hub.Client, ref hub.Ref) map[string]hub.TreeEntry {
	t.Helper()
	entries, err := c.Tree(context.Background(), ref, "main")
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	m := map[string]hub.TreeEntry{}
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

// ------------------------------------------------------------------ hub

func TestTFHub_UploadRoundTrip(t *testing.T) {
	f := newTFFixture(t)
	ctx := context.Background()
	c := f.client()

	me, err := c.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if me.Name != "alice" || me.Role != "write" {
		t.Fatalf("whoami = %+v, want alice/write", me)
	}

	ref := hub.Ref{Kind: hub.KindDataset, Namespace: "alice", Name: "imdb-ja"}
	if ok, err := c.RepoExists(ctx, ref); err != nil || ok {
		t.Fatalf("RepoExists before create = %v, %v; want false, nil", ok, err)
	}
	created, err := c.CreateRepo(ctx, ref)
	if err != nil || !created {
		t.Fatalf("CreateRepo = %v, %v; want true, nil", created, err)
	}
	created, err = c.CreateRepo(ctx, ref)
	if err != nil || created {
		t.Fatalf("second CreateRepo = %v, %v; want false, nil (409 tolerated)", created, err)
	}
	if ok, err := c.RepoExists(ctx, ref); err != nil || !ok {
		t.Fatalf("RepoExists after create = %v, %v; want true, nil", ok, err)
	}

	parquet := randomBytes(t, 1<<20+12345)
	dir := t.TempDir()
	writeTree(t, dir, map[string][]byte{
		"README.md":          []byte("---\nlicense: mit\n---\n# imdb-ja\n"),
		"data/train.parquet": parquet,
		"notes.txt":          []byte("first\n"),
	})

	var events []hub.Event
	res, err := hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir)}, func(e hub.Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.Commit == nil || res.Commit.OID == "" {
		t.Fatalf("upload result has no commit: %+v", res)
	}
	if len(res.Regular) != 2 || len(res.LFS) != 1 || res.LFS[0] != "data/train.parquet" {
		t.Fatalf("regular=%v lfs=%v; want 2 regular + data/train.parquet via lfs", res.Regular, res.LFS)
	}
	if len(res.LFSUploaded) != 1 || res.UploadedBytes != int64(len(parquet)) {
		t.Fatalf("LFSUploaded=%v uploadedBytes=%d; want 1 / %d", res.LFSUploaded, res.UploadedBytes, len(parquet))
	}
	kinds := map[hub.EventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[hub.EventPlanned] != 1 || kinds[hub.EventUploadDone] != 1 || kinds[hub.EventCommitting] != 1 {
		t.Fatalf("events = %v; want one Planned, one UploadDone, one Committing", kinds)
	}

	tree := treeByPath(t, c, ref)
	lfsEntry, ok := tree["data/train.parquet"]
	if !ok || lfsEntry.LFS == nil || lfsEntry.LFS.OID != sha256Hex(parquet) || lfsEntry.LFS.Size != int64(len(parquet)) {
		t.Fatalf("data/train.parquet tree entry = %+v; want LFS oid %s", lfsEntry, sha256Hex(parquet))
	}
	if status, b := f.raw(ref, "main", "README.md"); status != 200 || !strings.Contains(string(b), "license: mit") {
		t.Fatalf("README.md after upload: %d %q", status, b)
	}
	if got := f.lfsObject(lfsEntry.LFS.OID); !bytes.Equal(got, parquet) {
		t.Fatalf("data/train.parquet object bytes differ after upload (%d bytes)", len(got))
	}
	if _, ok := tree[".gitattributes"]; !ok {
		t.Fatalf("server-seeded .gitattributes disappeared: %v", tree)
	}

	// Same content again: nothing to do, no new commit.
	res, err = hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir)}, nil)
	if !errors.Is(err, hub.ErrNothingToDo) {
		t.Fatalf("second upload err = %v; want ErrNothingToDo", err)
	}
	if res == nil || !res.NothingToDo() || len(res.Unchanged) != 3 {
		t.Fatalf("second upload result = %+v; want 3 unchanged", res)
	}

	// One regular file changes: only it travels; the LFS file is skipped
	// without a transfer.
	writeTree(t, dir, map[string][]byte{"notes.txt": []byte("second\n")})
	res, err = hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir)}, nil)
	if err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if len(res.Regular) != 1 || res.Regular[0] != "notes.txt" || len(res.LFS) != 0 || len(res.Unchanged) != 2 {
		t.Fatalf("third upload result = %+v; want only notes.txt", res)
	}
	if status, b := f.raw(ref, "main", "notes.txt"); status != 200 || string(b) != "second\n" {
		t.Fatalf("notes.txt after third upload: %d %q", status, b)
	}

	// DryRun plans but touches nothing.
	writeTree(t, dir, map[string][]byte{"notes.txt": []byte("third\n")})
	res, err = hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir), DryRun: true}, nil)
	if err != nil || res.Commit != nil || len(res.Regular) != 1 {
		t.Fatalf("dry run = %+v, %v; want planned notes.txt and no commit", res, err)
	}
	if status, b := f.raw(ref, "main", "notes.txt"); status != 200 || string(b) != "second\n" {
		t.Fatalf("dry run wrote: %d %q", status, b)
	}

	// DeleteMissing mirrors the directory, but never touches .gitattributes.
	if err := os.RemoveAll(filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}
	res, err = hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir), DeleteMissing: true}, nil)
	if err != nil {
		t.Fatalf("delete upload: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "data/train.parquet" {
		t.Fatalf("deleted = %v; want [data/train.parquet]", res.Deleted)
	}
	tree = treeByPath(t, c, ref)
	if _, ok := tree["data/train.parquet"]; ok {
		t.Fatalf("data/train.parquet still present after delete")
	}
	if _, ok := tree[".gitattributes"]; !ok {
		t.Fatalf(".gitattributes was deleted by DeleteMissing")
	}

	// A new branch is born by its first commit.
	res, err = hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "experiment", Files: localFiles(t, dir)}, nil)
	if err != nil || res.Commit == nil {
		t.Fatalf("upload to new branch = %+v, %v", res, err)
	}
	if status, _ := f.raw(ref, "experiment", "notes.txt"); status != 200 {
		t.Fatalf("notes.txt on new branch: %d", status)
	}
}

func TestTFHub_LFSDeduplicationAcrossPaths(t *testing.T) {
	f := newTFFixture(t)
	ctx := context.Background()
	c := f.client()
	ref := hub.Ref{Kind: hub.KindModel, Namespace: "alice", Name: "dup"}
	if _, err := c.CreateRepo(ctx, ref); err != nil {
		t.Fatalf("create: %v", err)
	}
	blob := randomBytes(t, 64<<10)
	dir := t.TempDir()
	writeTree(t, dir, map[string][]byte{"a.safetensors": blob, "b/copy.safetensors": blob})

	transfers := 0
	res, err := hub.Upload(ctx, c, hub.Plan{Ref: ref, Rev: "main", Files: localFiles(t, dir)}, func(e hub.Event) {
		if e.Kind == hub.EventUploadDone {
			transfers++
		}
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Both paths are committed, but the bytes travel once.
	if len(res.LFS) != 2 || transfers != 1 || res.UploadedBytes != int64(len(blob)) {
		t.Fatalf("lfs=%v transfers=%d bytes=%d; want 2 paths, 1 transfer of %d bytes", res.LFS, transfers, res.UploadedBytes, len(blob))
	}
	tree := treeByPath(t, c, ref)
	for _, p := range []string{"a.safetensors", "b/copy.safetensors"} {
		if e := tree[p]; e.LFS == nil || e.LFS.OID != sha256Hex(blob) {
			t.Fatalf("%s = %+v; want LFS oid %s", p, e, sha256Hex(blob))
		}
	}
}

func TestTFHub_Errors(t *testing.T) {
	f := newTFFixture(t)
	ctx := context.Background()

	if _, err := hub.New(f.ts.URL, "").Whoami(ctx); !hub.IsUnauthorized(err) {
		t.Fatalf("anonymous whoami err = %v; want 401", err)
	}
	if _, err := hub.New(f.ts.URL, "tf_bogus").Whoami(ctx); !hub.IsUnauthorized(err) {
		t.Fatalf("bogus token whoami err = %v; want 401", err)
	}

	// Creating under a namespace alice cannot write to is a 400 from the
	// server (createRepo's badInput), surfaced as *hub.Error.
	_, err := f.client().CreateRepo(ctx, hub.Ref{Kind: hub.KindDataset, Namespace: "nobody", Name: "x"})
	var he *hub.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Fatalf("create in foreign namespace err = %v; want *hub.Error 400", err)
	}
}

func TestTFHub_MintAndRevokeToken(t *testing.T) {
	f := newTFFixture(t)
	ctx := context.Background()

	anon := hub.New(f.ts.URL, "")
	if _, err := anon.MintToken(ctx, "alice", "wrong", "tf-cli@test", "write"); !hub.IsUnauthorized(err) {
		t.Fatalf("mint with wrong password err = %v; want 401", err)
	}
	tok, err := anon.MintToken(ctx, "alice", "s3cret-pass", "tf-cli@test", "write")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok.Token == "" || tok.ID == 0 || tok.Scope != "write" {
		t.Fatalf("minted token = %+v", tok)
	}
	c := hub.New(f.ts.URL, tok.Token)
	me, err := c.Whoami(ctx)
	if err != nil || me.Name != "alice" || me.Role != "write" {
		t.Fatalf("whoami with minted token = %+v, %v", me, err)
	}
	if err := c.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := c.Whoami(ctx); !hub.IsUnauthorized(err) {
		t.Fatalf("whoami after revoke err = %v; want 401", err)
	}
}

// ------------------------------------------------------------------ CLI

type upJSON struct {
	Repo          string `json:"repo"`
	Kind          string `json:"kind"`
	Rev           string `json:"rev"`
	Created       bool   `json:"created"`
	Commit        string `json:"commit"`
	URL           string `json:"url"`
	Files         int    `json:"files"`
	LFSFiles      int    `json:"lfs_files"`
	Unchanged     int    `json:"unchanged"`
	Deleted       int    `json:"deleted"`
	DryRun        bool   `json:"dry_run"`
	NothingToDo   bool   `json:"nothing_to_do"`
	UploadedBytes int64  `json:"uploaded_bytes"`
}

func runTF(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = tfcli.Main(args, strings.NewReader(stdin), &out, &errb)
	t.Logf("tf %s -> %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out.String(), errb.String())
	return code, out.String(), errb.String()
}

func TestTFCLI_UpInfersKindAndName(t *testing.T) {
	f := newTFFixture(t)
	c := f.client()

	// A directory of weights with no --to / --kind: model, named after the
	// directory, under the caller's own namespace.
	dir := filepath.Join(t.TempDir(), "llm-7b")
	writeTree(t, dir, map[string][]byte{
		"model.safetensors": randomBytes(t, 4096),
		"config.json":       []byte(`{"architectures":["Llama"]}`),
	})
	code, out, stderr := runTF(t, "", "up", dir, "--endpoint", f.ts.URL, "--token", f.token, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("tf up exit %d: %s", code, stderr)
	}
	var res upJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("stdout is not JSON: %q (%v)", out, err)
	}
	if res.Repo != "alice/llm-7b" || res.Kind != "model" || !res.Created || res.Commit == "" || res.Files != 2 || res.LFSFiles != 1 {
		t.Fatalf("up result = %+v", res)
	}
	ref := hub.Ref{Kind: hub.KindModel, Namespace: "alice", Name: "llm-7b"}
	if ok, err := c.RepoExists(context.Background(), ref); err != nil || !ok {
		t.Fatalf("model repo missing after tf up: %v %v", ok, err)
	}
	if res.URL != c.WebURL(ref) {
		t.Fatalf("url = %q, want %q", res.URL, c.WebURL(ref))
	}

	// Re-running is a no-op with exit 0.
	code, out, _ = runTF(t, "", "up", dir, "--endpoint", f.ts.URL, "--token", f.token, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("second tf up exit %d", code)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || !res.NothingToDo || res.Created {
		t.Fatalf("second up result = %+v (%v); want nothing_to_do", res, err)
	}

	// --to without --kind on an existing repo of the *other* kind: the CLI
	// finds the model repository rather than creating a dataset twin.
	writeTree(t, dir, map[string][]byte{"extra.csv": []byte("a,b\n1,2\n")}) // csv alone would infer dataset
	code, out, stderr = runTF(t, "", "up", dir, "--to", "alice/llm-7b", "--include", "extra.csv", "--endpoint", f.ts.URL, "--token", f.token, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("tf up --to existing exit %d: %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || res.Kind != "model" || res.Created || res.Files != 1 {
		t.Fatalf("up --to existing result = %+v (%v); want model, not created, 1 file", res, err)
	}
	if ok, _ := c.RepoExists(context.Background(), hub.Ref{Kind: hub.KindDataset, Namespace: "alice", Name: "llm-7b"}); ok {
		t.Fatalf("a dataset twin was created")
	}
}

func TestTFCLI_UpGeneratesCardAndHonoursFlags(t *testing.T) {
	f := newTFFixture(t)
	c := f.client()

	dir := filepath.Join(t.TempDir(), "My Data Set")
	writeTree(t, dir, map[string][]byte{
		"data/train.parquet": randomBytes(t, 2048),
		"data/test.parquet":  randomBytes(t, 1024),
		"scratch/tmp.bin":    randomBytes(t, 10),
		".DS_Store":          []byte("junk"),
	})
	code, out, stderr := runTF(t, "", "up", dir,
		"--to", "alice/imdb-ja", "--kind", "dataset",
		"--license", "mit", "--tag", "nlp,ja", "--tag", "text", "--desc", "IMDB in Japanese",
		"--exclude", "scratch/**", "-m", "initial import",
		"--endpoint", f.ts.URL, "--token", f.token, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("tf up exit %d: %s", code, stderr)
	}
	var res upJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("stdout is not JSON: %q (%v)", out, err)
	}
	if res.Repo != "alice/imdb-ja" || res.Kind != "dataset" || !res.Created {
		t.Fatalf("up result = %+v", res)
	}
	ref := hub.Ref{Kind: hub.KindDataset, Namespace: "alice", Name: "imdb-ja"}
	tree := treeByPath(t, c, ref)
	for _, p := range []string{"README.md", "data/train.parquet", "data/test.parquet", ".gitattributes"} {
		if _, ok := tree[p]; !ok {
			t.Fatalf("%s missing from tree %v", p, tree)
		}
	}
	for _, p := range []string{"scratch/tmp.bin", ".DS_Store"} {
		if _, ok := tree[p]; ok {
			t.Fatalf("%s should have been excluded", p)
		}
	}
	status, readme := f.raw(ref, "main", "README.md")
	if status != 200 {
		t.Fatalf("README.md: %d", status)
	}
	for _, want := range []string{"license: mit", "- nlp", "- ja", "- text", "IMDB in Japanese"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("generated README lacks %q:\n%s", want, readme)
		}
	}

	// The commit message flag reaches the server.
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/api/v1/repos/dataset/alice/imdb-ja/commits/main", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "initial import") {
		t.Fatalf("commits: %d %s", resp.StatusCode, body)
	}

	// A dry run against a would-be-new repository creates nothing.
	dir2 := filepath.Join(t.TempDir(), "dry")
	writeTree(t, dir2, map[string][]byte{"x.csv": []byte("a\n")})
	code, out, _ = runTF(t, "", "up", dir2, "--dry-run", "--endpoint", f.ts.URL, "--token", f.token, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("dry run exit %d", code)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || !res.DryRun || res.Commit != "" {
		t.Fatalf("dry run result = %+v (%v)", res, err)
	}
	if ok, _ := c.RepoExists(context.Background(), hub.Ref{Kind: hub.KindDataset, Namespace: "alice", Name: "dry"}); ok {
		t.Fatalf("dry run created the repository")
	}
}

func TestTFCLI_UpErrors(t *testing.T) {
	f := newTFFixture(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string][]byte{"a.csv": []byte("a\n")})

	// No token anywhere: a clear failure, exit 1, not a stack of 401s.
	code, _, stderr := runTF(t, "", "up", dir, "--endpoint", f.ts.URL)
	if code != 1 || !strings.Contains(stderr, "tf login") {
		t.Fatalf("up without token: exit %d, stderr %q; want 1 and a `tf login` hint", code, stderr)
	}
	// Bad token: authentication failure surfaced as such.
	code, _, stderr = runTF(t, "", "up", dir, "--endpoint", f.ts.URL, "--token", "tf_nope")
	if code != 1 || !strings.Contains(strings.ToLower(stderr), "auth") {
		t.Fatalf("up with bad token: exit %d, stderr %q", code, stderr)
	}
	// Foreign namespace: 400 from createRepo, reported without a panic.
	code, _, _ = runTF(t, "", "up", dir, "--to", "nobody/x", "--endpoint", f.ts.URL, "--token", f.token)
	if code != 1 {
		t.Fatalf("up to foreign namespace: exit %d", code)
	}
	// gs:// is not supported yet.
	code, _, stderr = runTF(t, "", "up", "gs://bucket/prefix", "--endpoint", f.ts.URL, "--token", f.token)
	if code != 1 || !strings.Contains(stderr, "gs://") {
		t.Fatalf("up gs://: exit %d, stderr %q", code, stderr)
	}
	// Usage errors are 2.
	if code, _, _ = runTF(t, "", "up"); code != 2 {
		t.Fatalf("up without path: exit %d, want 2", code)
	}
	if code, _, _ = runTF(t, "", "frobnicate"); code != 2 {
		t.Fatalf("unknown command: exit %d, want 2", code)
	}
}

func TestTFCLI_LoginWhoamiLogout(t *testing.T) {
	f := newTFFixture(t)

	code, out, stderr := runTF(t, "s3cret-pass\n", "login", f.ts.URL, "--username", "alice", "--password-stdin")
	if code != 0 {
		t.Fatalf("login exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("login output %q lacks the user name", out)
	}
	cfgBytes, err := os.ReadFile(os.Getenv("TF_CONFIG"))
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	var cfg struct {
		Default     string `json:"default_endpoint"`
		Credentials map[string]struct {
			Token    string `json:"token"`
			TokenID  int64  `json:"token_id"`
			Username string `json:"username"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("config is not JSON: %s", cfgBytes)
	}
	cred, ok := cfg.Credentials[f.ts.URL]
	if cfg.Default != f.ts.URL || !ok || !strings.HasPrefix(cred.Token, "tf_") || cred.TokenID == 0 || cred.Username != "alice" {
		t.Fatalf("saved config = %s", cfgBytes)
	}
	if info, err := os.Stat(os.Getenv("TF_CONFIG")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", info.Mode(), err)
	}

	// whoami with nothing but the saved config.
	code, out, stderr = runTF(t, "", "whoami")
	if code != 0 || !strings.Contains(out, "alice") || !strings.Contains(out, "write") {
		t.Fatalf("whoami exit %d, out %q, err %q", code, out, stderr)
	}

	// up with nothing but the saved config.
	dir := filepath.Join(t.TempDir(), "ds")
	writeTree(t, dir, map[string][]byte{"a.csv": []byte("a\n")})
	code, _, stderr = runTF(t, "", "up", dir, "--quiet")
	if code != 0 {
		t.Fatalf("up via saved login exit %d: %s", code, stderr)
	}

	// logout revokes the minted token and forgets it.
	minted := cred.Token
	code, _, stderr = runTF(t, "", "logout")
	if code != 0 {
		t.Fatalf("logout exit %d: %s", code, stderr)
	}
	if _, err := hub.New(f.ts.URL, minted).Whoami(context.Background()); !hub.IsUnauthorized(err) {
		t.Fatalf("minted token still valid after logout: %v", err)
	}
	if code, _, _ = runTF(t, "", "whoami"); code != 1 {
		t.Fatalf("whoami after logout exit %d, want 1", code)
	}

	// login with a pasted token (stdin) instead of a password.
	code, _, stderr = runTF(t, f.token+"\n", "login", f.ts.URL, "--token", "-")
	if code != 0 {
		t.Fatalf("login --token - exit %d: %s", code, stderr)
	}
	if code, out, _ = runTF(t, "", "whoami"); code != 0 || !strings.Contains(out, "alice") {
		t.Fatalf("whoami after token login exit %d: %q", code, out)
	}
}

func TestTFCLI_StatusAndAPIKey(t *testing.T) {
	f := newTFFixture(t)

	// Nothing configured: status says so, exits 1, and still prints JSON.
	code, out, _ := runTF(t, "", "status", "--json")
	var st struct {
		Endpoint string   `json:"endpoint"`
		LoggedIn bool     `json:"logged_in"`
		Error    string   `json:"error"`
		PushTo   []string `json:"push_to"`
		User     *struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		} `json:"user"`
		TokenSource string `json:"token_source"`
	}
	if code != 1 {
		t.Fatalf("status with nothing configured: exit %d, want 1", code)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil || st.LoggedIn || st.Error == "" {
		t.Fatalf("status json = %q (%v); want logged_in=false with an error", out, err)
	}

	// An API key in the environment is a full login: no config file, no
	// `tf login`, and every command just works.
	t.Setenv("THINKINGFACE_API_KEY", f.token)
	t.Setenv("THINKINGFACE_ENDPOINT", f.ts.URL)
	code, out, stderr := runTF(t, "", "status")
	if code != 0 {
		t.Fatalf("status with API key: exit %d, stderr %q, out %q", code, stderr, out)
	}
	for _, want := range []string{"logged in:  yes", "alice", "env THINKINGFACE_API_KEY", "env THINKINGFACE_ENDPOINT", "push to:    alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, f.token) {
		t.Fatalf("status printed the full token:\n%s", out)
	}
	code, out, _ = runTF(t, "", "status", "--json")
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil || code != 0 || !st.LoggedIn ||
		st.User == nil || st.User.Name != "alice" || st.User.Scope != "write" || st.TokenSource != "env THINKINGFACE_API_KEY" {
		t.Fatalf("status --json with API key = %q (exit %d, %v)", out, code, err)
	}

	dir := filepath.Join(t.TempDir(), "keyed")
	writeTree(t, dir, map[string][]byte{"a.csv": []byte("a\n")})
	code, out, stderr = runTF(t, "", "up", dir, "--quiet", "--json")
	if code != 0 {
		t.Fatalf("up with API key: exit %d: %s", code, stderr)
	}
	var res upJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || res.Repo != "alice/keyed" || !res.Created {
		t.Fatalf("up with API key result = %+v (%v)", res, err)
	}

	// --api-key on the command line is the same thing, and a wrong key is
	// reported as "not logged in" rather than a crash.
	if code, out, _ = runTF(t, "", "whoami", "--api-key", f.token, "--endpoint", f.ts.URL); code != 0 || !strings.Contains(out, "alice") {
		t.Fatalf("whoami --api-key: exit %d, out %q", code, out)
	}
	t.Setenv("THINKINGFACE_API_KEY", "tf_wrong")
	code, out, _ = runTF(t, "", "status")
	if code != 1 || !strings.Contains(out, "logged in:  no") {
		t.Fatalf("status with a bad key: exit %d, out %q", code, out)
	}
}
