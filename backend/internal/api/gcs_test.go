// Tests for the bucket-location endpoint and the gs:// URIs the file browser
// hands out. The generated snippets are copy-pasted by users and mirrored in
// docs/dev/api-contract.md, so the script and the DuckDB call are asserted as
// whole strings rather than by fragments.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ------------------------------------------------------------- pure snippets

func TestBuildGcloudScript_Golden(t *testing.T) {
	files := []apitypes.RepoGCSFile{
		{Path: "README.md", Size: 120, URI: "gs://bucket/blobs/ab/cd/abcd"},
		{Path: "data/train-00000-of-00001.parquet", Size: 123456789, LFS: true,
			URI: "gs://bucket/lfs/ef/01/ef01"},
	}
	got := buildGcloudScript("dataset", "team", "imdb-ja", "main", files)

	want := `#!/bin/sh
# thinkingface: datasets/team/imdb-ja @ main -- 2 files, 123456909 bytes
# Objects are content-addressed; this script lays them out under DEST.
# DEST may be a local directory or a gs:// prefix.
set -eu
DEST="${DEST:-./imdb-ja}"
cp_one() {
  case "$DEST" in gs://*) ;; *) mkdir -p "$(dirname "$2")" ;; esac
  gcloud storage cp "$1" "$2"
}
cp_one 'gs://bucket/blobs/ab/cd/abcd' "$DEST"/'README.md'
cp_one 'gs://bucket/lfs/ef/01/ef01' "$DEST"/'data/train-00000-of-00001.parquet'
`
	if got != want {
		t.Fatalf("script mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A model repository with no files still yields a runnable script: the header
// reports zero, and cp_one is defined but never called.
func TestBuildGcloudScript_EmptyRevisionIsStillRunnable(t *testing.T) {
	got := buildGcloudScript("model", "alice", "tiny", "v1.0", nil)

	want := `#!/bin/sh
# thinkingface: models/alice/tiny @ v1.0 -- 0 files, 0 bytes
# Objects are content-addressed; this script lays them out under DEST.
# DEST may be a local directory or a gs:// prefix.
set -eu
DEST="${DEST:-./tiny}"
cp_one() {
  case "$DEST" in gs://*) ;; *) mkdir -p "$(dirname "$2")" ;; esac
  gcloud storage cp "$1" "$2"
}
`
	if got != want {
		t.Fatalf("script mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A quote in a path must not be able to end the single-quoted argument: the
// script is pasted into a shell, so a mis-escaped path is a command injection.
func TestBuildGcloudScript_EscapesQuotesInPaths(t *testing.T) {
	files := []apitypes.RepoGCSFile{
		{Path: "it's/a; rm -rf /.txt", Size: 1, URI: "gs://bucket/blobs/aa/bb/aabb"},
	}
	got := buildGcloudScript("dataset", "team", "quotes", "main", files)

	wantLine := `cp_one 'gs://bucket/blobs/aa/bb/aabb' "$DEST"/'it'\''s/a; rm -rf /.txt'`
	if !strings.Contains(got, wantLine+"\n") {
		t.Fatalf("escaped cp_one line missing\n--- got ---\n%s\n--- want line ---\n%s", got, wantLine)
	}
}

func TestBuildDuckDBSnippet(t *testing.T) {
	t.Run("parquet files only, in listing order", func(t *testing.T) {
		files := []apitypes.RepoGCSFile{
			{Path: "README.md", URI: "gs://bucket/blobs/ab/cd/abcd"},
			{Path: "data/a.parquet", LFS: true, URI: "gs://bucket/lfs/11/22/1122"},
			{Path: "data/b.PARQUET", LFS: true, URI: "gs://bucket/lfs/33/44/3344"},
		}
		want := `-- DuckDB: INSTALL httpfs; LOAD httpfs; then CREATE SECRET for GCS (HMAC) before running.
SELECT * FROM read_parquet([
  'gs://bucket/lfs/11/22/1122',
  'gs://bucket/lfs/33/44/3344'
]);
`
		if got := buildDuckDBSnippet(files); got != want {
			t.Fatalf("snippet mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	t.Run("no parquet means no snippet", func(t *testing.T) {
		files := []apitypes.RepoGCSFile{{Path: "README.md", URI: "gs://bucket/blobs/ab/cd/abcd"}}
		if got := buildDuckDBSnippet(files); got != "" {
			t.Fatalf("snippet = %q, want empty", got)
		}
	})
}

// ------------------------------------------------------------------ fixture

// gcsFixture is the archive fixture plus the handful of git/index helpers
// these tests need; the server wiring lives in one place.
type gcsFixture struct {
	*archiveFixture
}

func newGCSFixture(t *testing.T) *gcsFixture {
	t.Helper()
	return &gcsFixture{newArchiveFixture(t)}
}

// repo creates one of alice's repositories.
func (f *gcsFixture) repo(name, kind string) *store.Repo {
	f.t.Helper()
	return f.archiveFixture.repo("alice", name, kind)
}

// commit puts ops on main, standing in for a push. The sync worker does not
// run in these tests, so index() supplies the repo_files rows separately.
func (f *gcsFixture) commit(repo *store.Repo, ops []gitrepo.Op) string {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	newHash, _, err := g.Commit(gitrepo.CommitRequest{
		Branch:  "main",
		Message: "commit",
		Author:  gitrepo.Signature{Name: "alice", Email: "alice@example.com", When: time.Now()},
		Ops:     ops,
	})
	if err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return newHash.String()
}

func (f *gcsFixture) index(repo *store.Repo, ref string, files []store.RepoFile) {
	f.t.Helper()
	if err := f.st.ReplaceRepoFiles(context.Background(), repo.ID, ref, files); err != nil {
		f.t.Fatalf("index repo files: %v", err)
	}
}

// indexFromGit does what the sync worker does for ref: one repo_files row
// per file, carrying the real blob sha and LFS oid.
func (f *gcsFixture) indexFromGit(repo *store.Repo, ref string) {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	entries, _, err := g.Tree(ref, "", true)
	if err != nil {
		f.t.Fatalf("read tree %s: %v", ref, err)
	}
	files := []store.RepoFile{}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		row := store.RepoFile{Path: e.Path, Size: e.TargetSize(), BlobSHA: e.Hash.String()}
		if e.LFS != nil {
			oid := e.LFS.OID
			row.LFSOID = &oid
		}
		files = append(files, row)
	}
	f.index(repo, ref, files)
}

// tag writes a loose refs/tags ref straight into the bare repository: there
// is no tag-creation API on gitrepo, and pushes are what create tags in
// production.
func (f *gcsFixture) tag(repo *store.Repo, name, sha string) {
	f.t.Helper()
	path := filepath.Join(f.git.Dir(repo.StoragePath), "refs", "tags", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatalf("mkdir refs/tags: %v", err)
	}
	if err := os.WriteFile(path, []byte(sha+"\n"), 0o644); err != nil {
		f.t.Fatalf("write tag: %v", err)
	}
}

// treeEntries fetches one UI directory listing keyed by entry name.
func (f *gcsFixture) treeEntries(kind, name, rev, dir string) map[string]apitypes.TreeEntryUI {
	f.t.Helper()
	status, body := f.get("/api/v1/repos/" + kind + "/alice/" + name + "/tree/" + rev + "/" + dir)
	if status != 200 {
		f.t.Fatalf("tree %s status = %d, body = %s", rev, status, body)
	}
	var out apitypes.TreeResponseUI
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode tree: %v", err)
	}
	byName := map[string]apitypes.TreeEntryUI{}
	for _, e := range out.Entries {
		byName[e.Name] = e
	}
	return byName
}

func (f *gcsFixture) get(path string) (int, []byte) {
	f.t.Helper()
	resp := f.do("GET", path, "", nil)
	return resp.rec.Code, resp.rec.Body.Bytes()
}

func (f *gcsFixture) getGCS(kind, name, rev string) apitypes.RepoGCSResponse {
	f.t.Helper()
	status, body := f.get("/api/v1/repos/" + kind + "/alice/" + name + "/gcs/" + rev)
	if status != 200 {
		f.t.Fatalf("status = %d, body = %s", status, body)
	}
	var out apitypes.RepoGCSResponse
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

// lfsPointer renders a pointer blob, which is what an LFS-tracked path holds
// in git. gitrepo detects them by content, so no .gitattributes is needed.
func lfsPointer(oid string, size int64) []byte {
	return fmt.Appendf(nil, "version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, size)
}

const (
	testOID      = "1111111111111111111111111111111111111111111111111111111111111111"
	testOIDOther = "2222222222222222222222222222222222222222222222222222222222222222"
)

// ----------------------------------------------------------------- endpoint

func TestRepoGCS_SplitsLFSAndPlainObjects(t *testing.T) {
	f := newGCSFixture(t)
	repo := f.repo("imdb-ja", "dataset")
	f.commit(repo, []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte("hi\n")}})

	oid := testOID
	f.index(repo, "main", []store.RepoFile{
		{Path: "README.md", Size: 3, BlobSHA: "abcd0123456789"},
		{Path: "data/train.parquet", Size: 4096, BlobSHA: "ptr0000000000", LFSOID: &oid},
	})

	got := f.getGCS("dataset", "imdb-ja", "main")

	if got.Ref != "main" {
		t.Fatalf("ref = %q, want main", got.Ref)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want 2", got.Files)
	}
	// memStore renders keys as mem://{key}, so these assert the key layout.
	if got.Files[0].URI != "mem://blobs/ab/cd/abcd0123456789" || got.Files[0].LFS {
		t.Fatalf("plain file = %+v", got.Files[0])
	}
	if got.Files[1].URI != "mem://lfs/11/11/"+oid || !got.Files[1].LFS {
		t.Fatalf("lfs file = %+v", got.Files[1])
	}
	// The parquet file is LFS, so the DuckDB call must point at the lfs/
	// object and not at the pointer blob.
	if !strings.Contains(got.DuckDBSnippet, "'mem://lfs/11/11/"+oid+"'") {
		t.Fatalf("duckdb snippet = %q", got.DuckDBSnippet)
	}
	if !strings.Contains(got.GcloudScript, `DEST="${DEST:-./imdb-ja}"`) {
		t.Fatalf("gcloud script = %q", got.GcloudScript)
	}
}

// A revision git knows about but the sync worker has not indexed answers 200
// with an empty list -- `files` must be [] and not null, since the frontend
// maps over it.
func TestRepoGCS_UnindexedRefIsEmptyNotMissing(t *testing.T) {
	f := newGCSFixture(t)
	repo := f.repo("tiny", "model")
	f.commit(repo, []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "config.json", Data: []byte("{}")}})

	status, body := f.get("/api/v1/repos/model/alice/tiny/gcs/main")
	if status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(string(body), `"files":[]`) {
		t.Fatalf("files is not an empty array: %s", body)
	}
	var out apitypes.RepoGCSResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DuckDBSnippet != "" {
		t.Fatalf("duckdb snippet = %q, want empty", out.DuckDBSnippet)
	}
}

// An empty repository has no revision at all, but every revision of it is
// legitimately empty; 404 here would break the page for a fresh repository.
func TestRepoGCS_EmptyRepositoryIsNotAMissingRevision(t *testing.T) {
	f := newGCSFixture(t)
	f.repo("fresh", "dataset")

	if status, body := f.get("/api/v1/repos/dataset/alice/fresh/gcs/main"); status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

func TestRepoGCS_UnknownRevisionIs404(t *testing.T) {
	f := newGCSFixture(t)
	repo := f.repo("imdb-ja", "dataset")
	f.commit(repo, []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte("hi\n")}})

	if status, body := f.get("/api/v1/repos/dataset/alice/imdb-ja/gcs/nope"); status != 404 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

// ------------------------------------------------------ tree gcloud_command

func TestUITree_GcloudCommandDependsOnIndexing(t *testing.T) {
	f := newGCSFixture(t)
	repo := f.repo("imdb-ja", "dataset")
	oid := testOIDOther
	sha := f.commit(repo, []gitrepo.Op{
		{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte("hi\n")},
		{Kind: gitrepo.OpAdd, Path: "data/train.parquet", Data: lfsPointer(oid, 4096)},
	})
	lfsCommand := "gcloud storage cp 'mem://lfs/22/22/" + oid + "' './train.parquet'"

	// Before the sync worker has indexed main, a plain file has no promise of
	// a blobs/ object and so no command -- while the LFS entry, whose bytes
	// were uploaded before the commit existed, has one already.
	if got := f.treeEntries("dataset", "imdb-ja", "main", "")["README.md"].GcloudCommand; got != "" {
		t.Fatalf("README.md command before indexing = %q, want empty", got)
	}
	if got := f.treeEntries("dataset", "imdb-ja", "main", "data")["train.parquet"].GcloudCommand; got != lfsCommand {
		t.Fatalf("lfs command = %q, want %q", got, lfsCommand)
	}

	// Once the worker has indexed the ref, its blobs are published: plain
	// files carry a command, directories still carry none.
	f.indexFromGit(repo, "main")
	root := f.treeEntries("dataset", "imdb-ja", "main", "")
	if got := root["README.md"].GcloudCommand; !strings.HasPrefix(got, "gcloud storage cp 'mem://blobs/") || !strings.HasSuffix(got, " './README.md'") {
		t.Fatalf("README.md command on an indexed branch = %q, want a blobs/ copy", got)
	}
	if got := root["data"].GcloudCommand; got != "" {
		t.Fatalf("directory command = %q, want empty", got)
	}

	// A bare commit sha is never indexed, so plain files stay blank there
	// while the LFS entry keeps its command.
	if got := f.treeEntries("dataset", "imdb-ja", sha, "")["README.md"].GcloudCommand; got != "" {
		t.Fatalf("README.md command at a commit sha = %q, want empty", got)
	}
	if got := f.treeEntries("dataset", "imdb-ja", sha, "data")["train.parquet"].GcloudCommand; got != lfsCommand {
		t.Fatalf("lfs command at a commit sha = %q", got)
	}
}

// Tags are pushed refs too: once the worker has indexed one, it publishes
// blobs/ just like a branch, and the tree says so.
func TestUITree_GcloudCommandOnATag(t *testing.T) {
	f := newGCSFixture(t)
	repo := f.repo("tiny", "model")
	sha := f.commit(repo, []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "config.json", Data: []byte("{}")}})
	f.tag(repo, "v1.0", sha)
	f.indexFromGit(repo, "v1.0")

	entries := f.treeEntries("model", "tiny", "v1.0", "")
	if got := entries["config.json"].GcloudCommand; !strings.HasPrefix(got, "gcloud storage cp 'mem://blobs/") {
		t.Fatalf("config.json command on a tag = %q, want a blobs/ copy", got)
	}
}

// A filename with a quote in it is copy-pasted into a shell, so the command
// must come out quoted the same way the script is.
func TestGcloudCopyCommand_QuotesDestination(t *testing.T) {
	got := gcloudCopyCommand("mem://blobs/ab/cd/abcd", "./it's.txt")
	want := `gcloud storage cp 'mem://blobs/ab/cd/abcd' './it'\''s.txt'`
	if got != want {
		t.Fatalf("gcloudCopyCommand = %s, want %s", got, want)
	}
}
