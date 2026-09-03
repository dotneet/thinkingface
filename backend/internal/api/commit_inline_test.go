// The inline `file` entry of the NDJSON commit body: how big one may be, and
// which paths may arrive through it at all.
//
// Both bounds come from the same place. preupload answers every path with
// "regular" or "lfs" -- from .gitattributes, falling back to
// gitrepo.LFSInlineThreshold -- and huggingface_hub, `datasets` and the tf CLI
// all send an `lfsFile` pointer for everything that came back "lfs". So an
// inline entry over that threshold is a caller ignoring the answer it was just
// given, and it used to be accepted: a 400 MiB *.safetensors blob went
// straight into the git object database, past LFS entirely, having been copied
// four times on the way there.

package api

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// inlineFileLine is one `file` entry with base64 content, the shape
// huggingface_hub writes.
func inlineFileLine(path string, data []byte) string {
	return fmt.Sprintf(`{"key":"file","value":{"path":%q,"content":%q,"encoding":"base64"}}`,
		path, base64.StdEncoding.EncodeToString(data))
}

func committedBytes(t *testing.T, f *archiveFixture, r *store.Repo, rev, path string) []byte {
	t.Helper()
	repo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	content, err := repo.ReadFile(rev, path, 64<<20)
	if err != nil {
		t.Fatalf("read %s at %s: %v", path, rev, err)
	}
	return content
}

// The size ceiling. Over it the answer is 413 naming LFS, and nothing is
// committed.
func TestCommit_RefusesAnInlineFileOverTheCeiling(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	before := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	// A path nothing routes to LFS by pattern, so the ceiling is what
	// refuses it rather than the routing check below.
	oversized := make([]byte, maxCommitInlineFileBytes+1)
	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"too big"}}`,
		inlineFileLine("notes.log", oversized))

	if resp.status() != 413 {
		t.Fatalf("status = %d, body = %s; want 413", resp.status(), resp.rec.Body.String())
	}
	if body := resp.rec.Body.String(); !strings.Contains(body, "LFS") {
		t.Errorf("body = %s; want it to point the caller at LFS", body)
	}
	if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != before {
		t.Errorf("head = %s, want it unmoved at %s", got, before)
	}
	if !fileMissing(t, f, r, "main", "notes.log") {
		t.Error("the oversized file was committed anyway")
	}
}

// The routing check: a path .gitattributes tracks with LFS may not be
// smuggled in as an inline blob once it is big enough to matter. This is the
// reproduction from the audit -- a 400 MiB inline *.safetensors -- at a size a
// test can afford.
func TestCommit_RefusesAnInlineFileThatBelongsInLFS(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	before := refTargetOf(t, f, r, gitrepo.BranchRef("main"))
	tok := f.token(f.alice, "write")

	blob := make([]byte, gitrepo.LFSInlineThreshold+1)
	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"straight into the odb"}}`,
		inlineFileLine("model.safetensors", blob))

	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
	if body := resp.rec.Body.String(); !strings.Contains(body, "git-lfs") {
		t.Errorf("body = %s; want the LFS routing message", body)
	}
	if got := refTargetOf(t, f, r, gitrepo.BranchRef("main")); got != before {
		t.Errorf("head = %s, want it unmoved at %s", got, before)
	}
	if !fileMissing(t, f, r, "main", "model.safetensors") {
		t.Error("the LFS-tracked path was committed into the git object database")
	}
}

// The same rule for a path no pattern matches: over the threshold, preupload
// answered "lfs" on size alone, so an inline entry contradicts it.
func TestCommit_RefusesAnUnmatchedPathOverTheLFSThreshold(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	blob := make([]byte, gitrepo.LFSInlineThreshold)
	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"big log"}}`,
		inlineFileLine("train.log", blob))

	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
}

// Compatibility is the top priority, so nothing below the threshold changes
// behaviour -- not even for a path the default .gitattributes tracks with LFS.
// huggingface_hub sends such a file as an `lfsFile` pointer, but a caller that
// does not is still answered exactly as it was before.
func TestCommit_StillAcceptsSmallInlineFiles(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	tests := []struct {
		name string
		path string
		data []byte
	}{
		{"an ordinary text file", "notes.txt", []byte("hello\n")},
		{"an LFS-tracked path under the threshold", "tiny.safetensors", []byte("not really a tensor")},
		{"just under the threshold", "big.txt", make([]byte, gitrepo.LFSInlineThreshold-1)},
		{"an empty file", "empty.txt", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				fmt.Sprintf(`{"key":"header","value":{"summary":"add %s"}}`, tt.path),
				inlineFileLine(tt.path, tt.data))
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
			}
			if got := committedBytes(t, f, r, "main", tt.path); len(got) != len(tt.data) {
				t.Fatalf("committed %d bytes; want %d", len(got), len(tt.data))
			}
		})
	}
}

// The three content encodings this endpoint has always accepted still decode
// to the same bytes: explicit base64, the unset encoding that is tried as
// base64, and a literal string that is not base64 at all.
func TestCommit_InlineContentEncodings(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	tests := []struct {
		name string
		line string
		path string
		want string
	}{
		{
			name: "explicit base64",
			path: "a.txt",
			line: `{"key":"file","value":{"path":"a.txt","content":"aGVsbG8=","encoding":"base64"}}`,
			want: "hello",
		},
		{
			name: "encoding omitted, content is base64",
			path: "b.txt",
			line: `{"key":"file","value":{"path":"b.txt","content":"aGVsbG8="}}`,
			want: "hello",
		},
		{
			name: "a literal string with JSON escapes",
			path: "c.txt",
			line: `{"key":"file","value":{"path":"c.txt","content":"one\ntwo \"quoted\"","encoding":"utf-8"}}`,
			want: "one\ntwo \"quoted\"",
		},
		{
			name: "encoding omitted, content is not base64",
			path: "d.txt",
			line: `{"key":"file","value":{"path":"d.txt","content":"plain text!"}}`,
			want: "plain text!",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
				`{"key":"header","value":{"summary":"encodings"}}`, tt.line)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
			}
			if got := string(committedBytes(t, f, r, "main", tt.path)); got != tt.want {
				t.Fatalf("committed %q; want %q", got, tt.want)
			}
		})
	}
}

// Content declared base64 that is not base64 is still the caller's mistake,
// and still a 400 rather than a file full of the encoded text.
func TestCommit_RefusesInvalidBase64(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "weights", "model")
	seedFile(t, f, r, "README.md", []byte("# hi\n"))
	tok := f.token(f.alice, "write")

	resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"header","value":{"summary":"bad base64"}}`,
		`{"key":"file","value":{"path":"a.txt","content":"not base64 at all!!","encoding":"base64"}}`)
	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "not valid base64") {
		t.Errorf("body = %s; want the base64 message", resp.rec.Body.String())
	}
}

// The raw-line bound is what keeps an oversized entry from being copied out of
// the body at all, so it has to sit above the largest legal one. A base64
// payload of exactly the ceiling must not trip it.
func TestMaxCommitInlineLineLeavesRoomForALegalEntry(t *testing.T) {
	encoded := base64.StdEncoding.EncodedLen(maxCommitInlineFileBytes)
	if maxCommitInlineLine <= encoded {
		t.Fatalf("maxCommitInlineLine = %d; a legal %d-byte entry encodes to %d bytes",
			maxCommitInlineLine, maxCommitInlineFileBytes, encoded)
	}
}
