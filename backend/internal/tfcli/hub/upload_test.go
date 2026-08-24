package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeHub is a miniature thinkingface server: enough of tree / preupload /
// LFS batch / transfer / commit for Upload to run end to end, with the state a
// second run has to see.
type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	files   map[string]fakeEntry // repo path -> committed state
	objects map[string][]byte    // lfs oid -> bytes, content-addressed and shared
	puts    int
	commits int
	lines   [][]commitLine // one entry per commit
}

type fakeEntry struct {
	blobOID string // git blob sha1 of what is stored (the pointer, for LFS)
	lfsOID  string
	size    int64
}

const fakeProxyToken = "Bearer proxy-token"

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{
		t:       t,
		files:   map[string]fakeEntry{},
		objects: map[string][]byte{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/datasets/{ns}/{name}/tree/{rev}", h.tree)
	mux.HandleFunc("POST /api/datasets/{ns}/{name}/preupload/{rev}", h.preupload)
	mux.HandleFunc("POST /api/datasets/{ns}/{name}/commit/{rev}", h.commit)
	mux.HandleFunc("POST /datasets/{ns}/{name}/info/lfs/objects/batch", h.batch)
	mux.HandleFunc("PUT /lfs/{oid}", h.put)
	mux.HandleFunc("POST /lfs/verify", h.verify)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) client() *Client {
	return New(h.srv.URL, "tf_token", WithHTTPClient(h.srv.Client()))
}

// seedRegular stores a file the way a previous commit would have.
func (h *fakeHub) seedRegular(t *testing.T, repoPath, content string) {
	t.Helper()
	oid, err := GitBlobSHA1(strings.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("seed %s: %v", repoPath, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[repoPath] = fakeEntry{blobOID: oid, size: int64(len(content))}
}

// lfsRouted is the fake's .gitattributes: anything named *.bin, plus anything
// at or above 1KiB, travels through LFS.
func lfsRouted(repoPath string, size int64) bool {
	return strings.HasSuffix(repoPath, ".bin") || size >= 1024
}

func (h *fakeHub) tree(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	type lfsInfo struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}
	type entry struct {
		Type string   `json:"type"`
		OID  string   `json:"oid"`
		Size int64    `json:"size"`
		Path string   `json:"path"`
		LFS  *lfsInfo `json:"lfs,omitempty"`
	}
	paths := make([]string, 0, len(h.files))
	for p := range h.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := []entry{}
	dirs := map[string]bool{}
	for _, p := range paths {
		// A recursive listing carries directories too; Upload must ignore them.
		if dir := path.Dir(p); dir != "." && !dirs[dir] {
			dirs[dir] = true
			out = append(out, entry{Type: "directory", Path: dir})
		}
		f := h.files[p]
		e := entry{Type: "file", OID: f.blobOID, Size: f.size, Path: p}
		if f.lfsOID != "" {
			e.LFS = &lfsInfo{OID: f.lfsOID, Size: f.size}
		}
		out = append(out, e)
	}
	writeTestJSON(h.t, w, out)
}

func (h *fakeHub) preupload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Files []struct {
			Path   string `json:"path"`
			Sample string `json:"sample"`
			Size   int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Errorf("preupload decode: %v", err)
	}
	out := make([]map[string]any, 0, len(req.Files))
	for _, f := range req.Files {
		if _, err := base64.StdEncoding.DecodeString(f.Sample); err != nil {
			h.t.Errorf("sample for %s is not base64: %v", f.Path, err)
		}
		mode := "regular"
		if lfsRouted(f.Path, f.Size) {
			mode = "lfs"
		}
		out = append(out, map[string]any{"path": f.Path, "uploadMode": mode, "shouldIgnore": false})
	}
	writeTestJSON(h.t, w, map[string]any{"files": out})
}

func (h *fakeHub) batch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Objects []struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Errorf("batch decode: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	objs := make([]map[string]any, 0, len(req.Objects))
	for _, o := range req.Objects {
		item := map[string]any{"oid": o.OID, "size": o.Size, "authenticated": true}
		if _, stored := h.objects[o.OID]; !stored {
			item["actions"] = map[string]any{
				"upload": map[string]any{
					"href":   h.srv.URL + "/lfs/" + o.OID,
					"header": map[string]string{"Authorization": fakeProxyToken},
				},
				"verify": map[string]any{
					"href":   h.srv.URL + "/lfs/verify",
					"header": map[string]string{"Authorization": fakeProxyToken},
				},
			}
		}
		objs = append(objs, item)
	}
	w.Header().Set("Content-Type", contentTypeLFS)
	writeTestJSON(h.t, w, map[string]any{"transfer": "basic", "hash_algo": "sha256", "objects": objs})
}

func (h *fakeHub) put(w http.ResponseWriter, r *http.Request) {
	oid := r.PathValue("oid")
	if got := r.Header.Get("Authorization"); got != fakeProxyToken {
		h.t.Errorf("upload Authorization = %q, want the action header", got)
	}
	if r.ContentLength <= 0 {
		h.t.Errorf("upload of %s has no Content-Length", oid)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.t.Errorf("read upload: %v", err)
	}
	if int64(len(body)) != r.ContentLength {
		h.t.Errorf("Content-Length %d but %d bytes arrived", r.ContentLength, len(body))
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != oid {
		h.t.Errorf("uploaded bytes hash to %x, declared %s", sum, oid)
	}
	h.mu.Lock()
	h.objects[oid] = body
	h.puts++
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (h *fakeHub) verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.t.Errorf("verify decode: %v", err)
	}
	h.mu.Lock()
	_, ok := h.objects[req.OID]
	h.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", contentTypeLFS)
		w.WriteHeader(http.StatusNotFound)
		writeTestJSON(h.t, w, map[string]string{"message": "object " + req.OID + " not found"})
		return
	}
	writeTestJSON(h.t, w, map[string]any{"oid": req.OID, "size": req.Size})
}

func (h *fakeHub) commit(w http.ResponseWriter, r *http.Request) {
	lines := decodeCommitBody(h.t, r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commits++
	h.lines = append(h.lines, lines)

	for _, line := range lines {
		switch line.Key {
		case "header", "": // nothing to apply
		case "file":
			var v struct{ Path, Content, Encoding string }
			if err := json.Unmarshal(line.Value, &v); err != nil {
				h.t.Errorf("file line: %v", err)
				continue
			}
			data, err := base64.StdEncoding.DecodeString(v.Content)
			if err != nil {
				h.t.Errorf("file %s content is not base64: %v", v.Path, err)
				continue
			}
			oid, err := GitBlobSHA1(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				h.t.Errorf("hash %s: %v", v.Path, err)
				continue
			}
			h.files[v.Path] = fakeEntry{blobOID: oid, size: int64(len(data))}
		case "lfsFile":
			var v struct {
				Path, Algo, OID string
				Size            int64
			}
			if err := json.Unmarshal(line.Value, &v); err != nil {
				h.t.Errorf("lfsFile line: %v", err)
				continue
			}
			if _, ok := h.objects[v.OID]; !ok {
				h.t.Errorf("commit references object %s that was never uploaded", v.OID)
				continue
			}
			pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", v.OID, v.Size)
			blob, err := GitBlobSHA1(strings.NewReader(pointer), int64(len(pointer)))
			if err != nil {
				h.t.Errorf("hash pointer: %v", err)
				continue
			}
			h.files[v.Path] = fakeEntry{blobOID: blob, lfsOID: v.OID, size: v.Size}
		case "deletedFile":
			var v struct{ Path string }
			if err := json.Unmarshal(line.Value, &v); err == nil {
				delete(h.files, v.Path)
			}
		}
	}
	oid := fmt.Sprintf("%040x", h.commits)
	writeTestJSON(h.t, w, map[string]any{
		"success": true, "commitOid": oid, "commitUrl": "http://x/commit/" + oid,
	})
}

// lastCommit returns the NDJSON lines of the most recent commit.
func (h *fakeHub) lastCommit() []commitLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.lines) == 0 {
		return nil
	}
	return h.lines[len(h.lines)-1]
}

func (h *fakeHub) counts() (puts, commits int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.puts, h.commits
}

// collector records the events an upload reports.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) report(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) paths(kind EventKind) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		if e.Kind == kind {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out
}

func (c *collector) count(kind EventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func localFile(repoPath string, content []byte) LocalFile {
	return LocalFile{RepoPath: repoPath, Size: int64(len(content)), Open: openBytes(content)}
}

func commitKeys(lines []commitLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Key)
	}
	return out
}

func TestUploadEndToEnd(t *testing.T) {
	hub := newFakeHub(t)
	// Every repository is created with a .gitattributes; the client never
	// sends one and must never delete it.
	hub.seedRegular(t, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	c := hub.client()
	ctx := context.Background()

	readme := []byte("hello\n")
	blob := bytes.Repeat([]byte("x"), 2000)
	files := []LocalFile{localFile("README.md", readme), localFile("data/train.bin", blob)}

	// ---------------------------------------------------------- first upload
	events := &collector{}
	res, err := Upload(ctx, c, Plan{Ref: testRef(), Rev: "main", Files: files}, events.report)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if want := []string{"README.md"}; !equalStrings(res.Regular, want) {
		t.Errorf("Regular = %v, want %v", res.Regular, want)
	}
	if want := []string{"data/train.bin"}; !equalStrings(res.LFS, want) {
		t.Errorf("LFS = %v, want %v", res.LFS, want)
	}
	if !equalStrings(res.LFSUploaded, []string{"data/train.bin"}) {
		t.Errorf("LFSUploaded = %v", res.LFSUploaded)
	}
	if res.Bytes != int64(len(readme)+len(blob)) || res.LFSBytes != int64(len(blob)) {
		t.Errorf("Bytes = %d, LFSBytes = %d", res.Bytes, res.LFSBytes)
	}
	if res.UploadedBytes != int64(len(blob)) {
		t.Errorf("UploadedBytes = %d, want %d", res.UploadedBytes, len(blob))
	}
	if res.Commit == nil || res.Commit.OID == "" {
		t.Fatalf("Commit = %+v", res.Commit)
	}
	if puts, commits := hub.counts(); puts != 1 || commits != 1 {
		t.Errorf("puts = %d, commits = %d; want 1, 1", puts, commits)
	}
	if got := commitKeys(hub.lastCommit()); !equalStrings(got, []string{"header", "file", "lfsFile"}) {
		t.Errorf("commit lines = %v", got)
	}
	if n := events.count(EventPlanned); n != 1 {
		t.Errorf("EventPlanned fired %d times", n)
	}
	if got := events.paths(EventHashing); !equalStrings(got, []string{"README.md", "data/train.bin"}) {
		t.Errorf("hashed = %v", got)
	}
	if got := events.paths(EventUploadStart); !equalStrings(got, []string{"data/train.bin"}) {
		t.Errorf("upload started for %v", got)
	}
	if events.count(EventUploadDone) != 1 || events.count(EventCommitting) != 1 {
		t.Errorf("done = %d, committing = %d", events.count(EventUploadDone), events.count(EventCommitting))
	}

	// -------------------------------------------------------- nothing to do
	events = &collector{}
	res, err = Upload(ctx, c, Plan{Ref: testRef(), Rev: "main", Files: files}, events.report)
	if !errors.Is(err, ErrNothingToDo) {
		t.Fatalf("second upload err = %v, want ErrNothingToDo", err)
	}
	if res == nil {
		t.Fatal("Result must still be returned alongside ErrNothingToDo")
	}
	if got := res.Unchanged; !equalStrings(got, []string{"README.md", "data/train.bin"}) {
		t.Errorf("Unchanged = %v", got)
	}
	if res.Commit != nil {
		t.Errorf("Commit = %+v, want nil", res.Commit)
	}
	if got := events.paths(EventSkipped); !equalStrings(got, []string{"README.md", "data/train.bin"}) {
		t.Errorf("skipped = %v", got)
	}
	if puts, commits := hub.counts(); puts != 1 || commits != 1 {
		t.Errorf("a no-op upload touched the server: puts = %d, commits = %d", puts, commits)
	}

	// ------------------------------------------------------ one file changed
	changed := []LocalFile{localFile("README.md", []byte("goodbye\n")), localFile("data/train.bin", blob)}
	res, err = Upload(ctx, c, Plan{Ref: testRef(), Rev: "main", Files: changed}, nil)
	if err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if !equalStrings(res.Regular, []string{"README.md"}) || len(res.LFS) != 0 {
		t.Errorf("Regular = %v, LFS = %v", res.Regular, res.LFS)
	}
	if !equalStrings(res.Unchanged, []string{"data/train.bin"}) {
		t.Errorf("Unchanged = %v", res.Unchanged)
	}
	if puts, commits := hub.counts(); puts != 1 || commits != 2 {
		t.Errorf("puts = %d, commits = %d; want 1, 2", puts, commits)
	}

	// ---------------------------------------------------------- delete stale
	only := []LocalFile{localFile("README.md", []byte("goodbye\n"))}
	res, err = Upload(ctx, c, Plan{
		Ref: testRef(), Rev: "main", Files: only, DeleteMissing: true,
	}, nil)
	if err != nil {
		t.Fatalf("delete upload: %v", err)
	}
	if !equalStrings(res.Deleted, []string{"data/train.bin"}) {
		t.Errorf("Deleted = %v, want [data/train.bin] (.gitattributes must survive)", res.Deleted)
	}
	if got := commitKeys(hub.lastCommit()); !equalStrings(got, []string{"header", "deletedFile"}) {
		t.Errorf("commit lines = %v", got)
	}
	hub.mu.Lock()
	_, stillThere := hub.files[".gitattributes"]
	hub.mu.Unlock()
	if !stillThere {
		t.Error(".gitattributes was deleted from the repository")
	}
}

func TestUploadDeleteMissingKeepsGitattributesAndReadme(t *testing.T) {
	hub := newFakeHub(t)
	hub.seedRegular(t, ".gitattributes", "*.bin filter=lfs\n")
	hub.seedRegular(t, "README.md", "---\nlicense: mit\n---\n")
	hub.seedRegular(t, "stale.txt", "old\n")
	hub.seedRegular(t, "docs/README.md", "nested readmes are ordinary files\n")
	c := hub.client()

	res, err := Upload(context.Background(), c, Plan{
		Ref:           testRef(),
		Rev:           "main",
		Files:         []LocalFile{localFile("data.csv", []byte("a\n"))},
		DeleteMissing: true,
	}, nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !equalStrings(res.Deleted, []string{"docs/README.md", "stale.txt"}) {
		t.Fatalf("Deleted = %v, want [docs/README.md stale.txt] (root .gitattributes and README.md must survive)", res.Deleted)
	}
	hub.mu.Lock()
	_, attrs := hub.files[".gitattributes"]
	_, readme := hub.files["README.md"]
	hub.mu.Unlock()
	if !attrs || !readme {
		t.Errorf("root .gitattributes present = %v, README.md present = %v; want both kept", attrs, readme)
	}
}

// TestUploadDeleteMissingRespectsLocalPaths is the regression test for the
// `tf up --exclude ... --delete` data-loss bug: a remote path that
// --include/--exclude filtered out of Files must not be deleted as long as it
// is reported present via LocalPaths (the unfiltered disk listing), while a
// remote path absent from both Files and LocalPaths -- truly gone locally --
// is still deleted as before.
func TestUploadDeleteMissingRespectsLocalPaths(t *testing.T) {
	hub := newFakeHub(t)
	hub.seedRegular(t, ".gitattributes", "*.bin filter=lfs\n")
	hub.seedRegular(t, "data/train.parquet.bak", "old backup, still on disk\n")
	hub.seedRegular(t, "data/gone.txt", "no longer on disk\n")
	c := hub.client()

	res, err := Upload(context.Background(), c, Plan{
		Ref:   testRef(),
		Rev:   "main",
		Files: []LocalFile{localFile("data/train.parquet", []byte("new data\n"))},
		// LocalPaths is the full disk listing: train.parquet.bak is on disk
		// even though --exclude kept it out of Files.
		LocalPaths:    []string{"data/train.parquet", "data/train.parquet.bak"},
		DeleteMissing: true,
	}, nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !equalStrings(res.Deleted, []string{"data/gone.txt"}) {
		t.Fatalf("Deleted = %v, want [data/gone.txt] (data/train.parquet.bak is excluded but on disk, and must survive)", res.Deleted)
	}
	hub.mu.Lock()
	_, bakStillThere := hub.files["data/train.parquet.bak"]
	_, goneStillThere := hub.files["data/gone.txt"]
	hub.mu.Unlock()
	if !bakStillThere {
		t.Error("data/train.parquet.bak was deleted despite being present in LocalPaths")
	}
	if goneStillThere {
		t.Error("data/gone.txt should have been deleted: it is absent from both Files and LocalPaths")
	}
}

func TestUploadDryRun(t *testing.T) {
	hub := newFakeHub(t)
	c := hub.client()

	blob := bytes.Repeat([]byte("y"), 4096)
	events := &collector{}
	res, err := Upload(context.Background(), c, Plan{
		Ref:    testRef(),
		Rev:    "main",
		Files:  []LocalFile{localFile("README.md", []byte("hi\n")), localFile("big.bin", blob)},
		DryRun: true,
	}, events.report)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !equalStrings(res.Regular, []string{"README.md"}) || !equalStrings(res.LFS, []string{"big.bin"}) {
		t.Errorf("Regular = %v, LFS = %v", res.Regular, res.LFS)
	}
	if res.Commit != nil || len(res.LFSUploaded) != 0 || res.UploadedBytes != 0 {
		t.Errorf("a dry run reported transfers: %+v", res)
	}
	if puts, commits := hub.counts(); puts != 0 || commits != 0 {
		t.Errorf("a dry run touched the server: puts = %d, commits = %d", puts, commits)
	}
	if events.count(EventPlanned) != 1 {
		t.Error("a dry run must still report the plan")
	}
	if events.count(EventUploadStart) != 0 {
		t.Error("a dry run must not start transfers")
	}
}

func TestUploadDeduplicatesByOID(t *testing.T) {
	hub := newFakeHub(t)
	c := hub.client()
	ctx := context.Background()

	blob := bytes.Repeat([]byte("z"), 2048)
	files := []LocalFile{localFile("a.bin", blob), localFile("copy/b.bin", blob)}

	events := &collector{}
	res, err := Upload(ctx, c, Plan{Ref: testRef(), Rev: "main", Files: files, Workers: 2}, events.report)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if puts, _ := hub.counts(); puts != 1 {
		t.Errorf("puts = %d; identical content must travel once", puts)
	}
	if !equalStrings(res.LFS, []string{"a.bin", "copy/b.bin"}) {
		t.Errorf("LFS = %v", res.LFS)
	}
	if !equalStrings(res.LFSUploaded, []string{"a.bin", "copy/b.bin"}) {
		t.Errorf("LFSUploaded = %v", res.LFSUploaded)
	}
	if res.UploadedBytes != int64(len(blob)) {
		t.Errorf("UploadedBytes = %d; the shared object must be counted once", res.UploadedBytes)
	}
	if got := commitKeys(hub.lastCommit()); !equalStrings(got, []string{"header", "lfsFile", "lfsFile"}) {
		t.Errorf("commit lines = %v; each path needs its own lfsFile", got)
	}

	// A third path with the same bytes: the server already has the object, so
	// the batch answer carries no upload action.
	events = &collector{}
	third := []LocalFile{localFile("a.bin", blob), localFile("copy/b.bin", blob), localFile("c.bin", blob)}
	if _, err := Upload(ctx, c, Plan{Ref: testRef(), Rev: "main", Files: third}, events.report); err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if puts, _ := hub.counts(); puts != 1 {
		t.Errorf("puts = %d; a stored object must not travel again", puts)
	}
	if got := events.paths(EventDeduplicated); !equalStrings(got, []string{"c.bin"}) {
		t.Errorf("deduplicated = %v", got)
	}
}

func TestUploadFirstCommitOnEmptyRepo(t *testing.T) {
	// An unborn branch answers 404 on tree; the upload must proceed anyway.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/datasets/alice/corpus/tree/main", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"type":"not_found","message":"read tree: not found"}}`)
	})
	var summary string
	mux.HandleFunc("POST /api/datasets/alice/corpus/preupload/main", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		out := make([]map[string]any, 0, len(req.Files))
		for _, f := range req.Files {
			out = append(out, map[string]any{"path": f.Path, "uploadMode": "regular"})
		}
		writeTestJSON(t, w, map[string]any{"files": out})
	})
	mux.HandleFunc("POST /api/datasets/alice/corpus/commit/main", func(w http.ResponseWriter, r *http.Request) {
		lines := decodeCommitBody(t, r.Body)
		var header struct{ Summary string }
		if err := json.Unmarshal(lines[0].Value, &header); err != nil {
			t.Errorf("header: %v", err)
		}
		summary = header.Summary
		writeTestJSON(t, w, map[string]any{"success": true, "commitOid": "abc", "commitUrl": "u"})
	})
	c, _ := newTestClient(t, mux)

	files := []LocalFile{localFile("a.txt", []byte("a")), localFile("b.txt", []byte("b"))}
	res, err := Upload(context.Background(), c, Plan{Ref: testRef(), Rev: "main", Files: files}, nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.Commit == nil || res.Commit.OID != "abc" {
		t.Errorf("Commit = %+v", res.Commit)
	}
	if summary != "Upload 2 files with tf" {
		t.Errorf("default summary = %q", summary)
	}
}

func TestUploadNothingToDoOnEmptyPlan(t *testing.T) {
	hub := newFakeHub(t)
	res, err := Upload(context.Background(), hub.client(), Plan{Ref: testRef(), Rev: "main"}, nil)
	if !errors.Is(err, ErrNothingToDo) {
		t.Fatalf("err = %v, want ErrNothingToDo", err)
	}
	if res == nil || !res.NothingToDo() {
		t.Errorf("result = %+v", res)
	}
	if puts, commits := hub.counts(); puts != 0 || commits != 0 {
		t.Errorf("puts = %d, commits = %d", puts, commits)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
