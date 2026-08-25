package api

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ------------------------------------------------------------ commitSummary

func TestCommitSummary(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		message     string
		description string
		want        string
	}{
		{"message and description", "README.md", "Fix typo", "Also reword intro", "Fix typo\n\nAlso reword intro"},
		{"message only", "README.md", "Fix typo", "", "Fix typo"},
		{"default message", "docs/notes.txt", "", "", "Update docs/notes.txt"},
		{"default message with description", "docs/notes.txt", "", "add context", "Update docs/notes.txt\n\nadd context"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitSummary(tt.path, tt.message, tt.description); got != tt.want {
				t.Errorf("commitSummary(%q, %q, %q) = %q, want %q", tt.path, tt.message, tt.description, got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------- editConflict

func TestEditConflict(t *testing.T) {
	tests := []struct {
		name          string
		baseOID       string
		exists        bool
		currentOID    string
		wantConflict  bool
		wantMsgSubstr string
	}{
		{"no base_oid never conflicts", "", false, "", false, ""},
		{"no base_oid ignores existing content", "", true, "abc123", false, ""},
		{"matching base_oid on existing file", "abc123", true, "abc123", false, ""},
		{"stale base_oid on existing file", "abc123", true, "def456", true, "current blob is def456"},
		{"base_oid but file now missing", "abc123", false, "", true, "no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, isConflict := editConflict(tt.baseOID, tt.exists, tt.currentOID)
			if isConflict != tt.wantConflict {
				t.Fatalf("editConflict(%q, %v, %q) isConflict = %v, want %v", tt.baseOID, tt.exists, tt.currentOID, isConflict, tt.wantConflict)
			}
			if tt.wantConflict && !strings.Contains(msg, tt.wantMsgSubstr) {
				t.Errorf("editConflict(%q, %v, %q) message = %q, want substring %q", tt.baseOID, tt.exists, tt.currentOID, msg, tt.wantMsgSubstr)
			}
			if !tt.wantConflict && msg != "" {
				t.Errorf("editConflict(%q, %v, %q) message = %q, want empty", tt.baseOID, tt.exists, tt.currentOID, msg)
			}
		})
	}
}

// --------------------------------------------------------- lfsEditRejection

func TestLFSEditRejection(t *testing.T) {
	msg := lfsEditRejection("model.safetensors")
	if !strings.Contains(msg, "model.safetensors") {
		t.Errorf("lfsEditRejection message = %q, want it to mention the path", msg)
	}
	if !strings.Contains(msg, "LFS") {
		t.Errorf("lfsEditRejection message = %q, want it to mention LFS", msg)
	}
}

// ----------------------------------------------------------- renameSummary

func TestRenameSummary(t *testing.T) {
	tests := []struct {
		name        string
		oldPath     string
		newPath     string
		message     string
		description string
		want        string
	}{
		{"default message", "a.txt", "docs/b.txt", "", "", "Rename a.txt to docs/b.txt"},
		{"message only", "a.txt", "b.txt", "Tidy up", "", "Tidy up"},
		{"default message with description", "a.txt", "b.txt", "", "why", "Rename a.txt to b.txt\n\nwhy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renameSummary(tt.oldPath, tt.newPath, tt.message, tt.description); got != tt.want {
				t.Errorf("renameSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------- POST /api/v1/rename
//
// Driven over real HTTP against a real Server (the archiveFixture wiring that
// upload_test.go uses), because everything worth pinning here -- the single
// commit, the optimistic lock, path validation, the LFS rules -- lives in the
// handler rather than in a pure function.

// commitCount counts the commits reachable from a branch, which is how the
// "one commit, not two" claim is checked.
func commitCount(t *testing.T, f *archiveFixture, r *store.Repo, rev string) int {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	commits, _, err := repo.ListCommits(rev, "", plumbing.ZeroHash, 100)
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	return len(commits)
}

func TestRenameFile_MovesInOneCommit(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	oid := seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	tok := f.token(f.alice, "write")
	before := commitCount(t, f, r, "main")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/notes.txt", tok,
		apitypes.RenameFileRequest{NewPath: "docs/notes.txt", BaseOID: oid})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RenameFileResponse
	resp.json(t, &body)
	if body.Path != "docs/notes.txt" || body.OldPath != "notes.txt" {
		t.Fatalf("response paths = %q / %q", body.Path, body.OldPath)
	}
	// The blob is the same object: a rename moves a tree entry, it does not
	// rewrite content.
	if body.OID != oid {
		t.Fatalf("oid = %q, want the source blob %q", body.OID, oid)
	}
	if body.Size != int64(len("hello\n")) {
		t.Fatalf("size = %d, want %d", body.Size, len("hello\n"))
	}

	if !fileMissing(t, f, r, "main", "notes.txt") {
		t.Error("the old path still exists")
	}
	if got := string(readFile(t, f, r, "main", "docs/notes.txt")); got != "hello\n" {
		t.Errorf("content at the new path = %q", got)
	}
	if after := commitCount(t, f, r, "main"); after != before+1 {
		t.Errorf("commit count went %d -> %d, want exactly one new commit", before, after)
	}
}

func TestRenameFile_StaleBaseOIDIsRejected(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/notes.txt", tok,
		apitypes.RenameFileRequest{NewPath: "b.txt", BaseOID: "0000000000000000000000000000000000000000"})
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
	}
	// Refused means refused: neither path moved.
	if fileMissing(t, f, r, "main", "notes.txt") {
		t.Error("the source was removed by a rejected rename")
	}
	if !fileMissing(t, f, r, "main", "b.txt") {
		t.Error("the destination was created by a rejected rename")
	}
}

// An occupied destination is a 409 rather than a silent overwrite: the file
// already sitting there is not the caller's to lose.
func TestRenameFile_DestinationMustBeFree(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	seedFile(t, f, r, "taken.txt", []byte("mine\n"))
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/notes.txt", tok,
		apitypes.RenameFileRequest{NewPath: "taken.txt"})
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := string(readFile(t, f, r, "main", "taken.txt")); got != "mine\n" {
		t.Errorf("destination content = %q, want it untouched", got)
	}
	if fileMissing(t, f, r, "main", "notes.txt") {
		t.Error("the source was removed by a rejected rename")
	}
}

// The destination goes through gitrepo.ValidatePath, the same check the upload
// endpoint applies, so a traversal or a .git component is a 400 rather than a
// tree entry somewhere it must never be.
func TestRenameFile_RejectsUnsafeDestinations(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	tok := f.token(f.alice, "write")

	for _, newPath := range []string{"../escape.txt", "a/../../escape.txt", ".git/config", "sub/.GIT/hooks/pre-commit", "", "   ", "notes.txt"} {
		resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/notes.txt", tok,
			apitypes.RenameFileRequest{NewPath: newPath})
		if resp.status() != 400 {
			t.Errorf("new_path %q: status = %d, want 400; body = %s", newPath, resp.status(), resp.rec.Body.String())
		}
	}
	if fileMissing(t, f, r, "main", "notes.txt") {
		t.Error("the source was removed by a rejected rename")
	}
}

func TestRenameFile_MissingSourceIs404(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/nope.txt", tok,
		apitypes.RenameFileRequest{NewPath: "b.txt"})
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
}

// Renaming an LFS file is allowed where editing one is not, and it costs no
// transfer: the pointer blob is copied by hash and the object in the bucket is
// never touched.
func TestRenameFile_LFSPointerMovesByHash(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	pointer := gitrepo.FormatLFSPointer(strings.Repeat("ab", 32), 4096)
	oid := seedFile(t, f, r, "model.bin", pointer)
	tok := f.token(f.alice, "write")
	objectsBefore := len(f.obj.objects)

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/model.bin", tok,
		apitypes.RenameFileRequest{NewPath: "weights/model.bin", BaseOID: oid})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RenameFileResponse
	resp.json(t, &body)
	// The pointer blob itself is unchanged, and the reported size is the
	// object's rather than the pointer text's.
	if body.OID != oid {
		t.Errorf("oid = %q, want the pointer blob %q", body.OID, oid)
	}
	if body.Size != 4096 {
		t.Errorf("size = %d, want the object's 4096", body.Size)
	}
	if got := readFile(t, f, r, "main", "weights/model.bin"); string(got) != string(pointer) {
		t.Errorf("the pointer text changed: %q", got)
	}
	if !fileMissing(t, f, r, "main", "model.bin") {
		t.Error("the old path still exists")
	}
	if len(f.obj.objects) != objectsBefore {
		t.Errorf("object store went from %d to %d entries; a rename must transfer nothing",
			objectsBefore, len(f.obj.objects))
	}
}

// A pointer moved to a path .gitattributes does not track would sit there as
// literal text no client ever smudges, so the two paths have to agree.
func TestRenameFile_RejectsCrossingTheLFSBoundary(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	oid := seedFile(t, f, r, "model.bin", gitrepo.FormatLFSPointer(strings.Repeat("cd", 32), 4096))
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/model.bin", tok,
		apitypes.RenameFileRequest{NewPath: "docs/model.txt", BaseOID: oid})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "LFS") {
		t.Errorf("body = %s, want it to name LFS", resp.rec.Body.String())
	}
	if fileMissing(t, f, r, "main", "model.bin") {
		t.Error("the source was removed by a rejected rename")
	}
}

func TestRenameFile_RejectsWhenArchived(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	tok := f.token(f.alice, "write")
	f.archive("model", "alice", "foo", tok)

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/main/notes.txt", tok,
		apitypes.RenameFileRequest{NewPath: "b.txt"})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if errorType(t, resp) != "repository_archived" {
		t.Errorf("error type = %q, want repository_archived", errorType(t, resp))
	}
}

// A commit SHA is not a branch, so there is nothing to advance.
func TestRenameFile_RejectsADetachedRevision(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "foo", "model")
	seedFile(t, f, r, "notes.txt", []byte("hello\n"))
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	head, err := repo.Resolve("main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/rename/model/alice/foo/"+head.String()+"/notes.txt", tok,
		apitypes.RenameFileRequest{NewPath: "b.txt"})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
	}
}
