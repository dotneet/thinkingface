package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// Project notes are {project}/NOTES.md on the default branch, read and
// committed through the web editor's commit path. Driven over real HTTP
// against a real Server with a real bare repository (archiveFixture).

const notesURL = "/api/v1/experiments/alice/exp/p1/notes"

func getNotes(t *testing.T, f *archiveFixture, url, tok string) apitypes.ExpNotesResponse {
	t.Helper()
	resp := f.do("GET", url, tok, nil)
	if resp.status() != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", url, resp.status(), resp.rec.Body.String())
	}
	var body apitypes.ExpNotesResponse
	resp.json(t, &body)
	return body
}

func TestExperimentNotes_GetMissingIsExistsFalse(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "read")

	// An empty repository: no branch head at all.
	got := getNotes(t, f, notesURL, tok)
	if got.Exists || got.Content != "" || got.BlobSHA != "" || got.CommitSHA != "" {
		t.Fatalf("empty repo notes = %+v, want exists=false and nothing else", got)
	}
	if got.Path != "p1/NOTES.md" {
		t.Fatalf("path = %q, want p1/NOTES.md", got.Path)
	}

	// A repository with metrics but no notes still answers exists=false,
	// now with the head it looked at.
	seedFile(t, f, r, "p1/metrics.parquet", []byte("not really parquet"))
	got = getNotes(t, f, notesURL, tok)
	if got.Exists || got.CommitSHA == "" {
		t.Fatalf("notes = %+v, want exists=false with a commit_sha", got)
	}
}

func TestExperimentNotes_PutCreatesThenGetReadsBack(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	seedFile(t, f, r, "p1/metrics.parquet", []byte("x"))
	rec := &recordingEnqueuer{}
	f.s.sync = rec
	tok := f.token(f.alice, "write")
	before := commitCount(t, f, r, "main")

	content := "# Plan\n\n- try lr=3e-4, see [baseline](run:base-1)\n"
	resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: content, BaseSHA: strPtr("")})
	if resp.status() != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var put apitypes.ExpNotesResponse
	resp.json(t, &put)
	if !put.Exists || put.BlobSHA == "" || put.CommitSHA == "" || put.Content != content || put.Path != "p1/NOTES.md" {
		t.Fatalf("PUT response = %+v", put)
	}

	if got := string(readFile(t, f, r, "main", "p1/NOTES.md")); got != content {
		t.Fatalf("committed content = %q, want %q", got, content)
	}
	if after := commitCount(t, f, r, "main"); after != before+1 {
		t.Fatalf("commit count %d -> %d, want exactly one new commit", before, after)
	}

	// The commit is attributed to the caller and carries the default message.
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	commits, _, err := gitRepo.ListCommits("main", "", plumbing.ZeroHash, 1)
	if err != nil || len(commits) != 1 {
		t.Fatalf("list commits: %v (%d)", err, len(commits))
	}
	if commits[0].Hash.String() != put.CommitSHA {
		t.Fatalf("head = %s, want the PUT's commit %s", commits[0].Hash, put.CommitSHA)
	}
	if commits[0].Message != "docs(experiments): update notes for p1" {
		t.Fatalf("commit message = %q", commits[0].Message)
	}
	if !strings.Contains(commits[0].Author, "alice") {
		t.Fatalf("commit author = %q, want alice", commits[0].Author)
	}

	// Like any other server-side commit, it schedules the post-push sync
	// (which is what fires repo.push and re-indexes the experiment).
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0].Ref != "main" || calls[0].NewSHA != put.CommitSHA || calls[0].RepoID != r.ID {
		t.Fatalf("sync calls = %+v, want one for main at %s", calls, put.CommitSHA)
	}

	got := getNotes(t, f, notesURL, f.token(f.alice, "read"))
	if !got.Exists || got.Content != content || got.BlobSHA != put.BlobSHA || got.CommitSHA != put.CommitSHA {
		t.Fatalf("GET after PUT = %+v, want %+v", got, put)
	}

	// Saving the same content again is not an empty commit.
	resp = f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: content, BaseSHA: strPtr(put.BlobSHA)})
	if resp.status() != http.StatusOK {
		t.Fatalf("no-op PUT status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if after := commitCount(t, f, r, "main"); after != before+1 {
		t.Fatalf("a no-op save created a commit (%d -> %d)", before, after)
	}
}

func TestExperimentNotes_CustomMessage(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "hi\n", Message: "  record the sweep plan  "})
	if resp.status() != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	gitRepo, _ := f.git.Open(r.StoragePath)
	commits, _, _ := gitRepo.ListCommits("main", "", plumbing.ZeroHash, 1)
	if len(commits) != 1 || commits[0].Message != "record the sweep plan" {
		t.Fatalf("commits = %+v, want the trimmed custom message", commits)
	}

	for _, msg := range []string{"two\nlines", strings.Repeat("m", 201)} {
		resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "x", Message: msg})
		if resp.status() != http.StatusBadRequest {
			t.Errorf("message %.20q: status = %d, want 400; body = %s", msg, resp.status(), resp.rec.Body.String())
		}
	}
}

func TestExperimentNotes_StaleBaseSHAIs409(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	oid := seedFile(t, f, r, "p1/NOTES.md", []byte("v1\n"))

	cases := []struct {
		name string
		base string
	}{
		{"wrong blob", "0000000000000000000000000000000000000000"},
		{"claims absent but exists", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "v2\n", BaseSHA: strPtr(tc.base)})
			if resp.status() != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := string(readFile(t, f, r, "main", "p1/NOTES.md")); got != "v1\n" {
				t.Fatalf("content = %q, a refused write changed it", got)
			}
		})
	}

	// The current blob wins, and an unrelated commit in between -- the
	// metrics flusher writing parquet -- does not make it stale: the lock is
	// on the notes path, not the branch.
	seedFile(t, f, r, "p1/metrics.parquet", []byte("x"))
	resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "v2\n", BaseSHA: strPtr(oid)})
	if resp.status() != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "p1/NOTES.md")); got != "v2\n" {
		t.Fatalf("content = %q, want v2", got)
	}

	// Without base_sha the write overwrites.
	resp = f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "v3\n"})
	if resp.status() != http.StatusOK {
		t.Fatalf("overwrite status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "p1/NOTES.md")); got != "v3\n" {
		t.Fatalf("content = %q, want v3", got)
	}
}

func TestExperimentNotes_WriteRequiresWriteAccess(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	body := apitypes.ExpNotesUpdateRequest{Content: "x"}

	if resp := f.do("PUT", notesURL, "", body); resp.status() != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401; body = %s", resp.status(), resp.rec.Body.String())
	}
	if resp := f.do("PUT", notesURL, f.token(f.alice, "read"), body); resp.status() != http.StatusForbidden {
		t.Errorf("read token: status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if resp := f.do("PUT", notesURL, f.token(f.bob, "write"), body); resp.status() != http.StatusForbidden {
		t.Errorf("non-member: status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if !fileMissing(t, f, r, "main", "p1/NOTES.md") {
		t.Fatal("a refused write created the notes")
	}
}

func TestExperimentNotes_ArchivedRepoRefusesWrites(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	seedFile(t, f, r, "p1/NOTES.md", []byte("frozen\n"))
	tok := f.token(f.alice, "write")
	f.archive("dataset", "alice", "exp", tok)

	resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: "thawed\n"})
	if resp.status() != http.StatusForbidden || errorType(t, resp) != "repository_archived" {
		t.Fatalf("status = %d, body = %s; want 403 repository_archived", resp.status(), resp.rec.Body.String())
	}
	// Reads keep working.
	if got := getNotes(t, f, notesURL, tok); got.Content != "frozen\n" {
		t.Fatalf("GET on archived repo = %+v", got)
	}
}

func TestExperimentNotes_InvalidProjectIs400(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	for _, project := range []string{".git", "..", "%2E%2E", "a%2F..%2F..%2Fx"} {
		url := "/api/v1/experiments/alice/exp/" + project + "/notes"
		if resp := f.do("GET", url, tok, nil); resp.status() != http.StatusBadRequest {
			t.Errorf("GET project %q: status = %d, want 400; body = %s", project, resp.status(), resp.rec.Body.String())
		}
		if resp := f.do("PUT", url, tok, apitypes.ExpNotesUpdateRequest{Content: "x"}); resp.status() != http.StatusBadRequest {
			t.Errorf("PUT project %q: status = %d, want 400; body = %s", project, resp.status(), resp.rec.Body.String())
		}
	}
}

// A project name that needs escaping ("%2F" puts chi on the escaped path) is
// decoded before it becomes a path, like every other experiments endpoint.
func TestExperimentNotes_EncodedProjectName(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	url := "/api/v1/experiments/alice/exp/team%2Fsweep%201/notes"
	resp := f.do("PUT", url, tok, apitypes.ExpNotesUpdateRequest{Content: "hi\n"})
	if resp.status() != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "team/sweep 1/NOTES.md")); got != "hi\n" {
		t.Fatalf("content = %q", got)
	}
	if got := getNotes(t, f, url, tok); got.Path != "team/sweep 1/NOTES.md" || !got.Exists {
		t.Fatalf("GET = %+v", got)
	}
}

func TestExperimentNotes_ContentLimits(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	resp := f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: strings.Repeat("a", maxNotesBytes+1)})
	if resp.status() != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: status = %d, want 413; body = %s", resp.status(), resp.rec.Body.String())
	}
	resp = f.do("PUT", notesURL, tok, apitypes.ExpNotesUpdateRequest{Content: strings.Repeat("a", maxNotesBytes)})
	if resp.status() != http.StatusOK {
		t.Errorf("at the cap: status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}
}
