// Regression tests for an audit finding: writeResolveHeaders (resolve.go) runs
// once, ahead of every LFS/plain-blob branch that can still fail, and several
// of those failures used to answer with the success headers still attached --
// most visibly the year-long `Cache-Control: ... immutable` a commit-pinned
// revision gets, and the Content-Disposition attachment name. A CDN or shared
// cache sitting in front of this server would then store the *error* under
// that immutable policy, and a later, correct request for the same URL would
// keep getting the cached failure back -- exactly the state
// docs/dev/api-contract.md's "no headers are emitted at all" for an unlinked
// LFS oid is meant to rule out.
//
// clearResolveErrorHeaders (resolve.go) is the fix: one function, called from
// every error path reachable after writeResolveHeaders instead of the
// deletion being repeated -- or, worse, omitted -- at each call site.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// assertNoLeakedFileHeaders is the assertion every sub-test below shares: an
// error response must carry neither the immutable Cache-Control a successful,
// commit-pinned resolve gets, nor the Content-Disposition attachment name for
// the file that was actually asked for.
func assertNoLeakedFileHeaders(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, must not carry the immutable success policy on an error", cc)
	}
	if disp := rec.Header().Get("Content-Disposition"); disp != "" {
		t.Errorf("Content-Disposition = %q, want none on an error response", disp)
	}
}

// lfsPointerBody builds the pointer text a real git-lfs client would commit,
// and the .gitattributes that makes gitRepo.Stat recognise the path as LFS --
// the same pair TestResolveLFS_* in security_test.go commits.
func lfsPointerBody(oid string, size int) []byte {
	return []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(size) + "\n")
}

// sha256Hex (tfcli_test.go) computes the oid a real git-lfs client would for
// data, used here without ever calling putLFSObject -- these tests need an
// oid that *looks* legitimate to a reader of the pointer but is not actually
// backed by anything this repository (or, in one case, this instance) is
// entitled to.

// ---------------------------------------------------- lfsObjectOwned (404)

// The audit's own reproduction: a pointer naming an oid this repository never
// linked (e.g. one copied from another repository's pointer). This used to
// come back 404 with a year-long immutable Cache-Control and a
// Content-Disposition attachment still attached, which is precisely the state
// api-contract.md's "no headers are emitted at all" forbids.
func TestResolve_UnlinkedLFSObjectDoesNotLeakCacheHeaders(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "poc", "model")

	oid := sha256Hex([]byte("bytes this repository never uploaded"))
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", lfsPointerBody(oid, 9))

	// Pinned to a commit sha (rather than "main") so a bug that leaves
	// writeResolveHeaders' output in place would show the immutable form --
	// the branch-name form ("no-cache") could otherwise mask the same defect.
	head := headOf(t, f.git, repo)

	rec := f.do(secRequest{method: "GET", path: "/alice/poc/resolve/" + head + "/model.bin"})
	assertNoLeakedFileHeaders(t, rec, http.StatusNotFound)
}

// ------------------------------------------------- storage missing (404)

// The ledger says the object is linked and known, but the bytes are not in
// the bucket -- state that GC and a promotion race are supposed to prevent,
// but this is the path that answers a client anyway if it happens.
func TestResolve_LFSObjectMissingFromStorageDoesNotLeakCacheHeaders(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "poc", "model")

	data := []byte("hello lfs, but never actually stored")
	oid := sha256Hex(data)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(data)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", lfsPointerBody(oid, len(data)))

	head := headOf(t, f.git, repo)

	rec := f.do(secRequest{method: "GET", path: "/alice/poc/resolve/" + head + "/model.bin"})
	assertNoLeakedFileHeaders(t, rec, http.StatusNotFound)
}

// ------------------------------------------------------ signing failure (500)

// signErrStorage forces the one branch memStore's SupportsSignedURL()==false
// never reaches: a signed-URL redirect whose signing call itself fails.
type signErrStorage struct{ *memStore }

func (signErrStorage) SupportsSignedURL() bool { return true }

func (signErrStorage) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("signing unavailable in this test")
}

// resolveErrFixture is a standalone copy of secFixture's setup (revisionFixture
// and secFixture both build the same stack) with one field only this file
// needs: a storage.Storage that can be made to fail signing.
type resolveErrFixture struct {
	t   *testing.T
	s   *Server
	st  *store.Store
	git *gitrepo.Manager
}

func newResolveErrFixture(t *testing.T, objStore storage.Storage) *resolveErrFixture {
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

	gitMgr := gitrepo.NewManager(t.TempDir())
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret-test-secret",
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  objStore,
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})
	return &resolveErrFixture{t: t, s: srv, st: st, git: gitMgr}
}

func (f *resolveErrFixture) repo(ns, name string) *store.Repo {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.st.CreateUser(ctx, ns, ns+"@example.com", "hash", false); err != nil {
		f.t.Fatalf("create user %s: %v", ns, err)
	}
	n, err := f.st.GetNamespace(ctx, ns)
	if err != nil {
		f.t.Fatalf("namespace %s: %v", ns, err)
	}
	sp := store.NewStoragePath()
	r, err := f.st.CreateRepo(ctx, n.ID, name, "model", "desc", "main", sp)
	if err != nil {
		f.t.Fatalf("create repo %s/%s: %v", ns, name, err)
	}
	if err := f.git.Init(sp, "main"); err != nil {
		f.t.Fatalf("git init: %v", err)
	}
	return r
}

func (f *resolveErrFixture) writeFile(repo *store.Repo, path string, data []byte) {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	_, _, err = g.Commit(gitrepo.CommitRequest{
		Branch:  "main",
		Message: "add " + path,
		Author:  gitrepo.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	})
	if err != nil {
		f.t.Fatalf("commit %s: %v", path, err)
	}
}

func (f *resolveErrFixture) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// A signing failure sits behind lfsObjectOwned and lfsObjectSize both
// succeeding -- the object is legitimately linked and its size is known --
// and behind the ETag / conditional-request headers being set, so this is
// the branch most likely to be missed by a fix that only special-cases the
// two ownership checks.
func TestResolve_SigningFailureDoesNotLeakCacheHeaders(t *testing.T) {
	objStore := signErrStorage{newMemStore()}
	f := newResolveErrFixture(t, objStore)
	repo := f.repo("alice", "poc")

	data := []byte("hello lfs, signed download")
	oid := sha256Hex(data)
	if err := objStore.Put(context.Background(), storage.LFSKey(oid),
		strings.NewReader(string(data)), "application/octet-stream"); err != nil {
		t.Fatalf("put lfs object: %v", err)
	}
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(data)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", lfsPointerBody(oid, len(data)))

	head := headOf(t, f.git, repo)

	rec := f.get("/alice/poc/resolve/" + head + "/model.bin")
	assertNoLeakedFileHeaders(t, rec, http.StatusInternalServerError)
}

// --------------------------------------------------- range not satisfiable

// writeRangeNotSatisfiable is the one error path that already deleted
// Content-Disposition before this fix -- but left the immutable Cache-Control
// standing. Folded into clearResolveErrorHeaders so both are gone together.
func TestResolve_RangeNotSatisfiableDoesNotLeakCacheHeaders(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte("a,b\n1,2\n"))

	head := headOf(t, f.git, repo)

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/datasets/alice/data/resolve/" + head + "/rows.csv",
		headers: map[string]string{"Range": "bytes=1000-2000"},
	})
	assertNoLeakedFileHeaders(t, rec, http.StatusRequestedRangeNotSatisfiable)
}

// --------------------------------------------- lfsObjectSize (unit-level)

// The ledger having no row for an oid a repository links to should not
// happen -- store.LinkLFSObjects / RecordLFSObject only ever create the two
// rows together -- but lfsObjectSize is the code that answers if it ever
// does, and it is reachable after writeResolveHeaders has already run. Driven
// directly (same package) rather than through a contrived database state,
// since reaching it via HTTP would mean breaking the very foreign key that is
// supposed to make it impossible.
func TestLFSObjectSize_ClearsResolveHeadersOnUnknownOID(t *testing.T) {
	f := newSecFixture(t)

	w := httptest.NewRecorder()
	// Simulates writeResolveHeaders having already run for a commit-pinned
	// revision, the state every real caller of lfsObjectSize is in.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Disposition", `attachment; filename="model.bin"`)
	r := httptest.NewRequest("GET", "/alice/poc/resolve/deadbeef/model.bin", nil)

	size, ok := f.s.lfsObjectSize(w, r, "oid-the-ledger-has-never-heard-of")
	if ok {
		t.Fatalf("ok = true, size = %d, want the lookup to fail", size)
	}
	assertNoLeakedFileHeaders(t, w, http.StatusNotFound)
}

// headOf (wal_test.go) resolves a repository's current main-branch commit as
// a hex string, for a URL pinned to a commit sha rather than a branch name --
// the form that actually gets the immutable Cache-Control a leak would be
// most visible in.
