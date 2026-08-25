// Two things resolve.go used to get wrong about a request it could already
// answer cheaply.
//
// 1. A revision that does not resolve came back as a bare 404. huggingface_hub
//    turns that into HfHubHTTPError, which `file_exists()` and
//    `hf_hub_download()` do not catch as RevisionNotFoundError -- so a typo in
//    a branch name raised something the caller could not act on, and looked
//    exactly like a missing file. The two have to stay distinguishable, in
//    both directions: a missing *path* must not be reported as a missing
//    revision either.
//
// 2. The ETag was emitted and then ignored. A client holding a multi-gigabyte
//    checkpoint that asked whether its copy was current was answered with the
//    whole file again, on every path -- git blob, emulator-proxied LFS, and
//    the signed-URL redirect that costs a GCS egress on top.

package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// ------------------------------------------------------- revision vs. entry

func TestResolve_UnknownRevisionIsRevisionNotFound(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte("a,b\n"))

	rec := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/typo/rows.csv"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
	// The header is the entire contract: huggingface_hub picks the exception
	// class off it, and nothing else in the response says which of the two
	// things is missing.
	if got := rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Errorf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// The other half of the same fix: a path that is not there at a revision that
// is must stay EntryNotFound. Reporting every 404 as RevisionNotFound would
// send a caller looking for a branch problem it does not have.
func TestResolve_MissingPathStaysEntryNotFound(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte("a,b\n"))

	rec := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/absent.csv"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Error-Code"); got != "EntryNotFound" {
		t.Errorf("X-Error-Code = %q, want EntryNotFound", got)
	}
}

// A repository with no commits at all reads as an unresolvable revision inside
// git, but it is not the revision that is wrong: the repository exists and
// simply holds nothing. EntryNotFound keeps file_exists() answering False
// instead of raising, which is what an ordinary create_repo-then-check flow
// does.
func TestResolve_EmptyRepositoryIsEntryNotFound(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	f.repo("alice", "fresh", "model")

	rec := f.do(secRequest{method: "GET", path: "/alice/fresh/resolve/main/config.json"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Error-Code"); got != "EntryNotFound" {
		t.Errorf("X-Error-Code = %q, want EntryNotFound", got)
	}
}

// ------------------------------------------------------- conditional reads

const condBlob = "the bytes of a small checkpoint"

func newCondFixture(t *testing.T) *secFixture {
	t.Helper()
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "data", "dataset")
	f.writeFile(repo, "rows.csv", []byte(condBlob))
	return f
}

func TestResolve_ConditionalGetIsNotModified(t *testing.T) {
	f := newCondFixture(t)

	first := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv"})
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body = %q", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag to revalidate against")
	}

	for _, tt := range []struct {
		name   string
		method string
		header string
	}{
		{"get", "GET", etag},
		// Revalidation is as often a HEAD as a GET -- that is how
		// huggingface_hub reads file metadata -- so it has to answer 304 too.
		{"head", "HEAD", etag},
		// RFC 9110 §8.8.3.2: a list, and "*" for "whatever you have now".
		{"list", "GET", `"deadbeef", ` + etag},
		{"star", "GET", "*"},
		// A weak validator compares equal here: every ETag in this package is
		// a content hash, so weak and strong agree.
		{"weak", "GET", "W/" + etag},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(secRequest{
				method:  tt.method,
				path:    "/datasets/alice/data/resolve/main/rows.csv",
				headers: map[string]string{"If-None-Match": tt.header},
			})
			if rec.Code != http.StatusNotModified {
				t.Fatalf("status = %d, want 304; body = %q", rec.Code, rec.Body.String())
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want nothing on a 304", rec.Body.String())
			}
			// RFC 9110 §15.4.5: the validator has to come back with the 304,
			// or the client cannot refresh what it stored.
			if got := rec.Header().Get("ETag"); got != etag {
				t.Errorf("ETag = %q, want %q", got, etag)
			}
		})
	}
}

func TestResolve_StaleValidatorStillServesTheBody(t *testing.T) {
	f := newCondFixture(t)

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/datasets/alice/data/resolve/main/rows.csv",
		headers: map[string]string{"If-None-Match": `"0000000000000000000000000000000000000000"`},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != condBlob {
		t.Errorf("body = %q, want the file", rec.Body.String())
	}
}

// A matched precondition outranks a Range: RFC 9110 §13.2.2 evaluates
// If-None-Match first, so this must be a 304 and not a 206.
func TestResolve_ConditionalBeatsRange(t *testing.T) {
	f := newCondFixture(t)
	first := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv"})
	etag := first.Header().Get("ETag")

	rec := f.do(secRequest{
		method: "GET",
		path:   "/datasets/alice/data/resolve/main/rows.csv",
		headers: map[string]string{
			"If-None-Match": etag,
			"Range":         "bytes=0-3",
		},
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304; body = %q", rec.Code, rec.Body.String())
	}
}

func TestResolve_CacheControlFollowsTheRevision(t *testing.T) {
	f := newCondFixture(t)

	byBranch := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/main/rows.csv"})
	if byBranch.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", byBranch.Code)
	}
	// A branch is a moving target: storing the response is fine, reusing it
	// without asking is not.
	if got := byBranch.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control for a branch = %q, want no-cache", got)
	}

	commit := byBranch.Header().Get("X-Repo-Commit")
	if commit == "" {
		t.Fatal("X-Repo-Commit is missing, so there is no pinned URL to test")
	}
	byCommit := f.do(secRequest{method: "GET", path: "/datasets/alice/data/resolve/" + commit + "/rows.csv"})
	if byCommit.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", byCommit.Code, byCommit.Body.String())
	}
	if got := byCommit.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control for a commit-pinned URL = %q, want the immutable form", got)
	}
}

// ------------------------------------------------------------- the LFS path

func TestResolveLFS_ConditionalGetIsNotModified(t *testing.T) {
	f, oid := newLinkedLFSFixture(t)

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/alice/weights/resolve/main/model.bin",
		headers: map[string]string{"If-None-Match": `"` + oid + `"`},
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304; body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing on a 304", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"`+oid+`"` {
		t.Errorf("ETag = %q, want the object's oid", got)
	}
}

// The conditional check runs *after* the ownership check, so a repository that
// merely names another repository's oid in a pointer cannot use a 304 to
// confirm the guess the 404 exists to refuse.
func TestResolveLFS_ConditionalDoesNotBypassOwnership(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("bytes that belong to somebody else")
	oid := f.putLFSObject(body) // in the bucket, linked to no repository
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/alice/weights/resolve/main/model.bin",
		headers: map[string]string{"If-None-Match": `"` + oid + `"`},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != "" {
		t.Errorf("ETag = %q, want nothing: a refused pointer must confirm nothing about the object",
			rec.Header().Get("ETag"))
	}
}

// signingStore is the storage driver in its production shape: it can sign, so
// resolve redirects instead of proxying the bytes.
type signingStore struct {
	*memStore
}

func (signingStore) SupportsSignedURL() bool { return true }

func (signingStore) SignedGetURL(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
	return "https://signed.example.test/" + key + "?expires=soon", nil
}

// The redirect names a URL that expires, so the 302 itself must never be
// reusable from a cache -- a stored redirect hands a later client a signature
// GCS has since started rejecting. The ETag still travels with it, which is
// what lets the *next* request come back here and be answered 304 without any
// signing at all.
func TestResolveLFS_RedirectIsNeverCachedButStillRevalidates(t *testing.T) {
	f, oid := newLinkedLFSFixture(t)
	f.s.storage = signingStore{f.obj}

	rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin"})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on the redirect = %q, want no-store", got)
	}
	if got := rec.Header().Get("ETag"); got != `"`+oid+`"` {
		t.Errorf("ETag = %q, want the object's oid", got)
	}

	cond := f.do(secRequest{
		method:  "GET",
		path:    "/alice/weights/resolve/main/model.bin",
		headers: map[string]string{"If-None-Match": `"` + oid + `"`},
	})
	if cond.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304; body = %q", cond.Code, cond.Body.String())
	}
	if cond.Header().Get("Location") != "" {
		t.Errorf("Location = %q, want nothing: a 304 must not send the client to storage",
			cond.Header().Get("Location"))
	}
}

// newLinkedLFSFixture is the ordinary LFS state: the object is in the bucket
// and the repository owns it.
func newLinkedLFSFixture(t *testing.T) (*secFixture, string) {
	t.Helper()
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("hello lfs, present and accounted for")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)
	return f, oid
}
