// Tests for the oid half of the preupload answer (commit.go,
// handlePreupload + preuploadOID).
//
// huggingface_hub stores this oid on CommitOperationAdd._remote_oid and
// compares it with _local_oid, which it derives from the uploadMode the server
// returned in the very same response: the git blob sha1 of the content for
// "regular", the sha256 of the content for "lfs". Any addition whose two oids
// match is dropped from the commit entirely. So the tests below are really
// about one property: the oid is either from the hash space the returned
// uploadMode implies, or it is null. A value from the other space would make
// the client's comparison meaningless and, in the bad direction, silently drop
// a changed file from the commit.
//
// Driven over real HTTP against a real Server, the way revision_test.go and
// archive_test.go are.

package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

type preuploadFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	git   *gitrepo.Manager
	alice *store.User
	token string
}

func newPreuploadFixture(t *testing.T) *preuploadFixture {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(ctx, "sqlite://"+dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gitMgr := gitrepo.NewManager(t.TempDir())
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret",
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})

	f := &preuploadFixture{t: t, s: srv, st: st, git: gitMgr}
	u, err := st.CreateUser(ctx, "alice", "alice@example.com", "hash", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.alice = u

	tok, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := st.CreateToken(ctx, u.ID, "test", "write", hash, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}
	f.token = tok
	return f
}

// emptyRepo is a repository whose git directory exists but holds no commits --
// what create_repo leaves behind, and the state upload_folder hits first.
func (f *preuploadFixture) emptyRepo(name string) *store.Repo {
	f.t.Helper()
	ctx := context.Background()
	n, err := f.st.GetNamespace(ctx, "alice")
	if err != nil {
		f.t.Fatalf("namespace alice: %v", err)
	}
	sp := store.NewStoragePath()
	r, err := f.st.CreateRepo(ctx, n.ID, name, "model", "desc", "main", sp)
	if err != nil {
		f.t.Fatalf("create repo alice/%s: %v", name, err)
	}
	if err := f.git.Init(sp, "main"); err != nil {
		f.t.Fatalf("git init alice/%s: %v", name, err)
	}
	return r
}

func (f *preuploadFixture) commit(r *store.Repo, files map[string][]byte) {
	f.t.Helper()
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	ops := make([]gitrepo.Op, 0, len(files))
	for p, data := range files {
		ops = append(ops, gitrepo.Op{Kind: gitrepo.OpAdd, Path: p, Data: data})
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch:  "main",
		Message: "seed",
		Author:  gitrepo.Signature{Name: "alice", Email: "alice@example.com", When: time.Now()},
		Ops:     ops,
	}); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
}

type preuploadAnswer struct {
	Path         string  `json:"path"`
	UploadMode   string  `json:"uploadMode"`
	ShouldIgnore bool    `json:"shouldIgnore"`
	OID          *string `json:"oid"`
}

// preupload posts the request huggingface_hub would send and returns the
// answers keyed by path, plus the raw body for the null-shape assertions.
func (f *preuploadFixture) preupload(repo string, rev string, files []preuploadFile) (map[string]preuploadAnswer, string) {
	f.t.Helper()
	rec := f.preuploadRaw(repo, rev, files)
	if rec.Code != 200 {
		f.t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Files []preuploadAnswer `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		f.t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	out := make(map[string]preuploadAnswer, len(body.Files))
	for _, a := range body.Files {
		out[a.Path] = a
	}
	if len(body.Files) != len(files) {
		f.t.Fatalf("answered for %d of %d paths: %s", len(body.Files), len(files), rec.Body.String())
	}
	return out, rec.Body.String()
}

// preuploadRaw posts a preupload body and hands back the recorder untouched,
// for the cases whose subject is the status rather than the answers.
func (f *preuploadFixture) preuploadRaw(repo, rev string, files []preuploadFile) *httptest.ResponseRecorder {
	f.t.Helper()
	b, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		f.t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/models/alice/"+repo+"/preupload/"+rev, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return rec
}

// gitHashObject is `git hash-object` -- and the exact computation
// huggingface_hub's utils/sha.py:git_hash performs for a "regular" file, which
// is what makes the value we return comparable at all.
func gitHashObject(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d", len(data))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// assertOID checks one answer end to end: the mode we expected to be routed,
// shouldIgnore left alone, and the oid (nil for "no comparable value").
func assertOID(t *testing.T, got preuploadAnswer, wantMode string, wantOID *string) {
	t.Helper()
	if got.UploadMode != wantMode {
		t.Fatalf("%s: uploadMode = %q, want %q", got.Path, got.UploadMode, wantMode)
	}
	if got.ShouldIgnore {
		t.Fatalf("%s: shouldIgnore = true, want false", got.Path)
	}
	switch {
	case wantOID == nil && got.OID != nil:
		t.Fatalf("%s: oid = %q, want null", got.Path, *got.OID)
	case wantOID != nil && got.OID == nil:
		t.Fatalf("%s: oid = null, want %q", got.Path, *wantOID)
	case wantOID != nil && *got.OID != *wantOID:
		t.Fatalf("%s: oid = %q, want %q", got.Path, *got.OID, *wantOID)
	}
}

func oidPtr(s string) *string { return &s }

// ------------------------------------------------------------- happy paths

// A regular file already in the repository answers with its git blob sha1 --
// the same value `git hash-object` prints, which is the only thing
// huggingface_hub's _local_oid for a "regular" file can equal.
func TestPreupload_RegularFileReturnsGitBlobSHA1(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	content := []byte("# hello\n")
	f.commit(repo, map[string][]byte{"README.md": content})

	answers, _ := f.preupload("foo", "main", []preuploadFile{
		{Path: "README.md", Size: int64(len(content))},
	})
	assertOID(t, answers["README.md"], "regular", oidPtr(gitHashObject(content)))
}

// An LFS pointer answers with the pointer's sha256, which is the hash
// huggingface_hub compares for an "lfs" file (the sha256 of the *content*, not
// of the pointer blob).
func TestPreupload_LFSPointerReturnsSHA256(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	oid := strings.Repeat("ab", 32)
	f.commit(repo, map[string][]byte{
		"model.bin": gitrepo.FormatLFSPointer(oid, 4096),
	})

	// *.bin is in the seeded .gitattributes, so this routes to LFS.
	answers, _ := f.preupload("foo", "main", []preuploadFile{
		{Path: "model.bin", Size: 4096},
	})
	assertOID(t, answers["model.bin"], "lfs", oidPtr(oid))
}

// The blob sha1 of an LFS entry is the sha1 of the *pointer*, so it must never
// leak out as the oid: it is neither the pointer's sha256 nor the content's.
func TestPreupload_LFSPointerDoesNotReturnPointerBlobSHA1(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	oid := strings.Repeat("cd", 32)
	pointer := gitrepo.FormatLFSPointer(oid, 4096)
	f.commit(repo, map[string][]byte{"model.bin": pointer})

	answers, _ := f.preupload("foo", "main", []preuploadFile{{Path: "model.bin", Size: 4096}})
	if got := answers["model.bin"].OID; got == nil || *got == gitHashObject(pointer) {
		t.Fatalf("oid = %v, must not be the pointer blob's sha1 %s", got, gitHashObject(pointer))
	}
}

// ------------------------------------------------- nothing to compare with

// A path that is not in the tree has no remote oid at all.
func TestPreupload_MissingPathIsNull(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	f.commit(repo, map[string][]byte{"README.md": []byte("hi\n")})

	answers, body := f.preupload("foo", "main", []preuploadFile{
		{Path: "brand-new.txt", Size: 3},
		{Path: "nested/brand-new.txt", Size: 3},
	})
	assertOID(t, answers["brand-new.txt"], "regular", nil)
	assertOID(t, answers["nested/brand-new.txt"], "regular", nil)
	if !strings.Contains(body, `"oid":null`) {
		t.Fatalf("body does not spell the absent oid as null: %s", body)
	}
}

// A directory has a tree hash, which is in neither hash space the client
// computes; answering with it would be comparing a tree to a file.
func TestPreupload_DirectoryPathIsNull(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	f.commit(repo, map[string][]byte{"data/train.txt": []byte("rows\n")})

	answers, _ := f.preupload("foo", "main", []preuploadFile{{Path: "data", Size: 10}})
	assertOID(t, answers["data"], "regular", nil)
}

// The paths preupload refuses outright are the ones the commit that follows it
// would refuse too: handlePreupload now runs the same checkOpPath the commit
// body's entries go through, so a caller learns about a malformed path before
// it uploads anything against the answer rather than after.
//
// The empty path and the trailing-slash directory used to be answered with a
// null oid, which was the right value but the wrong shape of answer: it told
// the client to go ahead and upload a file it could never commit.
func TestPreupload_RefusesPathsTheCommitWouldRefuse(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	f.commit(repo, map[string][]byte{"data/train.txt": []byte("rows\n")})

	for _, path := range []string{"", "/", "data/", "../escape.txt", ".git/config"} {
		t.Run(path, func(t *testing.T) {
			rec := f.preuploadRaw("foo", "main", []preuploadFile{{Path: path, Size: 1}})
			if rec.Code != 400 {
				t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

// The element and path-length bounds paths-info has always had. Without them
// maxBatchBody alone allows on the order of 380k records in one 8 MiB body,
// each costing a .gitattributes pass and a tree lookup.
func TestPreupload_BoundsTheBatch(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	f.commit(repo, map[string][]byte{"README.md": []byte("hi\n")})

	tooMany := make([]preuploadFile, maxPathsInfoPaths+1)
	for i := range tooMany {
		tooMany[i] = preuploadFile{Path: fmt.Sprintf("f%d.txt", i), Size: 1}
	}
	if rec := f.preuploadRaw("foo", "main", tooMany); rec.Code != 400 {
		t.Errorf("status for %d files = %d, want 400", len(tooMany), rec.Code)
	}

	long := []preuploadFile{{Path: strings.Repeat("a", maxPathBytes+1), Size: 1}}
	if rec := f.preuploadRaw("foo", "main", long); rec.Code != 400 {
		t.Errorf("status for an over-long path = %d, want 400", rec.Code)
	}

	// One under each bound still answers: these are ceilings, not thresholds.
	ok := make([]preuploadFile, maxPathsInfoPaths)
	for i := range ok {
		ok[i] = preuploadFile{Path: fmt.Sprintf("f%d.txt", i), Size: 1}
	}
	if rec := f.preuploadRaw("foo", "main", ok); rec.Code != 200 {
		t.Errorf("status for %d files = %d, want 200: %s", len(ok), rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------- mismatched pairings

// The path already holds an LFS pointer, but the file being uploaded is small
// enough (and named plainly enough) to travel as a regular blob. The client
// will compute a git blob sha1 of the new content; the pointer's sha256 is from
// the other hash space and the pointer blob's own sha1 describes the pointer,
// not the file. Neither is comparable, so the answer is null.
func TestPreupload_LFSEntryAnsweredRegularIsNull(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	// notes.txt matches no LFS pattern, so a small one routes as "regular"
	// even though what is committed there is a pointer.
	f.commit(repo, map[string][]byte{
		"notes.txt": gitrepo.FormatLFSPointer(strings.Repeat("ef", 32), 4096),
	})

	answers, _ := f.preupload("foo", "main", []preuploadFile{{Path: "notes.txt", Size: 12}})
	assertOID(t, answers["notes.txt"], "regular", nil)
}

// The mirror image: the path holds an ordinary blob, but the upload routes to
// LFS, so the client will compute a sha256 of the content. The stored blob's
// sha1 is not that, so the answer is null.
func TestPreupload_RegularEntryAnsweredLFSIsNull(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	// weights.bin matches *.bin -> "lfs", but what is committed there is
	// plain text that does not parse as a pointer.
	f.commit(repo, map[string][]byte{"weights.bin": []byte("not a pointer\n")})

	answers, _ := f.preupload("foo", "main", []preuploadFile{{Path: "weights.bin", Size: 4096}})
	assertOID(t, answers["weights.bin"], "lfs", nil)

	// The same file crossing the size threshold on a path with no pattern
	// routes to LFS as well, and is just as incomparable.
	f.commit(repo, map[string][]byte{"big.dat": []byte("small for now\n")})
	answers, _ = f.preupload("foo", "main", []preuploadFile{
		{Path: "big.dat", Size: gitrepo.LFSInlineThreshold},
	})
	assertOID(t, answers["big.dat"], "lfs", nil)
}

// ------------------------------------------------ revisions that do not exist

// create_repo followed straight by upload_folder preuploads against a branch
// that has no commit yet. That must stay a 200 with null oids: the commit that
// follows is what creates the branch, so a 404 here would break the flow.
func TestPreupload_EmptyRepositoryIsNullNot500(t *testing.T) {
	f := newPreuploadFixture(t)
	f.emptyRepo("foo")

	answers, body := f.preupload("foo", "main", []preuploadFile{
		{Path: "README.md", Size: 8},
		{Path: "model.bin", Size: 4096},
	})
	assertOID(t, answers["README.md"], "regular", nil)
	assertOID(t, answers["model.bin"], "lfs", nil)
	if !strings.Contains(body, `"oid":null`) {
		t.Fatalf("body does not spell the absent oid as null: %s", body)
	}
}

// A revision that does not resolve in a repository that *does* have commits is
// the same story: preupload is a write-path step and the revision it names may
// be about to be created, so it answers with null rather than 404 or 500.
func TestPreupload_UnknownRevisionIsNullNot500(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	f.commit(repo, map[string][]byte{"README.md": []byte("hi\n")})

	for _, rev := range []string{"no-such-branch", strings.Repeat("0", 40)} {
		t.Run(rev, func(t *testing.T) {
			answers, _ := f.preupload("foo", rev, []preuploadFile{{Path: "README.md", Size: 3}})
			assertOID(t, answers["README.md"], "regular", nil)
		})
	}
}

// ------------------------------------------------------------------ batching

// One request, many paths: every path still gets its own answer, and repeats
// of the same path do not confuse the single-resolution lookup.
func TestPreupload_BatchAnswersEveryPathIndependently(t *testing.T) {
	f := newPreuploadFixture(t)
	repo := f.emptyRepo("foo")
	readme := []byte("# hello\n")
	oid := strings.Repeat("12", 32)
	f.commit(repo, map[string][]byte{
		"README.md":      readme,
		"model.bin":      gitrepo.FormatLFSPointer(oid, 4096),
		"data/train.txt": []byte("rows\n"),
	})

	answers, _ := f.preupload("foo", "main", []preuploadFile{
		{Path: "README.md", Size: int64(len(readme))},
		{Path: "README.md", Size: int64(len(readme))},
		{Path: "model.bin", Size: 4096},
		{Path: "data", Size: 1},
		{Path: "missing.txt", Size: 1},
	})
	assertOID(t, answers["README.md"], "regular", oidPtr(gitHashObject(readme)))
	assertOID(t, answers["model.bin"], "lfs", oidPtr(oid))
	assertOID(t, answers["data"], "regular", nil)
	assertOID(t, answers["missing.txt"], "regular", nil)
}
