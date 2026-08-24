// serveLFSFile used to set Content-Length from the pointer's declared size
// before it ever asked storage for the bytes. When the emulator storage path
// (SupportsSignedURL() == false) then failed to find the object -- exactly
// what happens once GC has reclaimed it, or an untracked LFS object has been
// swept -- the 404 JSON body was written underneath a stale Content-Length
// that still described the pointer's size instead of the body actually
// written. A client reading that many bytes either read past a truncated
// JSON body or blocked waiting for bytes the server was never going to send.
// These tests pin the fix: an error response's Content-Length must always
// match the body it is paired with, and a normal response must keep the
// headers hf_hub_download relies on.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// newMissingLFSFixture links an LFS pointer into a repository and then
// removes the underlying bytes from storage, the state GC (or the untracked
// sweep) leaves behind: the repository still owns the oid, but the object is
// gone.
func newMissingLFSFixture(t *testing.T) (f *secFixture, oid string, size int64) {
	t.Helper()
	f = newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("hello lfs, soon to be gc'd")
	oid = f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)

	// The pointer still names oid, and the repository still owns it in the
	// database -- only the bytes behind it are gone.
	if err := f.obj.Delete(context.Background(), storage.LFSKey(oid)); err != nil {
		t.Fatalf("delete lfs object from storage: %v", err)
	}
	return f, oid, int64(len(body))
}

func TestResolveLFS_MissingObjectIsACleanNotFound(t *testing.T) {
	f, oid, _ := newMissingLFSFixture(t)

	rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %q", rec.Code, rec.Body.String())
	}

	// The bug: Content-Length used to be set from the pointer's declared size
	// before storage was ever asked for the bytes, so a real server would
	// have shipped the JSON error body underneath a stale, explicit
	// Content-Length it can no longer correct (net/http only auto-computes
	// Content-Length when the handler leaves it unset). Every other error
	// path in this handler (a missing pointer, a foreign oid, ...) writes
	// its JSON with no Content-Length of its own and lets the server get it
	// right; this path must match that now.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want unset; a stale, explicit value here is exactly "+
			"the bug this test guards against (see TestResolve_FailedRequestsCountOnNeitherCounter "+
			"and TestResolveAndRaw_RefuseForeignLFSPointer for how every sibling error path behaves)", got)
	}

	var body apitypes.ApiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body does not parse as JSON: %v (body = %q)", err, rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Error("error message is empty")
	}
	// The message should name the object, not leak anything about why it
	// vanished (GC vs. never uploaded looks the same from here).
	if want := oid; !strings.Contains(body.Error.Message, want) {
		t.Errorf("error message %q does not mention the missing oid %q", body.Error.Message, want)
	}
}

// A HEAD never touches storage (it answers from the pointer alone), so it
// must keep reporting the declared size even once the bytes are gone --
// exactly as it did before the fix.
func TestResolveLFS_MissingObjectHeadStillReportsPointerSize(t *testing.T) {
	f, _, size := newMissingLFSFixture(t)

	rec := f.do(secRequest{method: "HEAD", path: "/alice/weights/resolve/main/model.bin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.FormatInt(size, 10) {
		t.Errorf("Content-Length = %q, want %d", got, size)
	}
}

// Regression guard for the success paths the fix must not touch: a whole-file
// GET and a ranged GET through the emulator still carry the headers
// hf_hub_download reads before it trusts the body.
func TestResolveLFS_SuccessHeadersAreUnchanged(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("hello lfs, still very much present")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)

	t.Run("whole file", func(t *testing.T) {
		rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != string(body) {
			t.Errorf("body = %q, want %q", got, string(body))
		}
		if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
			t.Errorf("Content-Length = %q, want %q", got, want)
		}
		if got := rec.Header().Get("ETag"); got != `"`+oid+`"` {
			t.Errorf("ETag = %q, want %q", got, `"`+oid+`"`)
		}
		if got := rec.Header().Get("X-Linked-Etag"); got != `"`+oid+`"` {
			t.Errorf("X-Linked-Etag = %q, want %q", got, `"`+oid+`"`)
		}
		if got, want := rec.Header().Get("X-Linked-Size"), strconv.Itoa(len(body)); got != want {
			t.Errorf("X-Linked-Size = %q, want %q", got, want)
		}
	})

	t.Run("ranged", func(t *testing.T) {
		rec := f.do(secRequest{
			method:  "GET",
			path:    "/alice/weights/resolve/main/model.bin",
			headers: map[string]string{"Range": "bytes=6-9"},
		})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206; body = %q", rec.Code, rec.Body.String())
		}
		if got, want := rec.Body.String(), string(body[6:10]); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		wantRange := "bytes 6-9/" + strconv.Itoa(len(body))
		if got := rec.Header().Get("Content-Range"); got != wantRange {
			t.Errorf("Content-Range = %q, want %q", got, wantRange)
		}
		if got, want := rec.Header().Get("Content-Length"), "4"; got != want {
			t.Errorf("Content-Length = %q, want %q", got, want)
		}
	})

	t.Run("head", func(t *testing.T) {
		rec := f.do(secRequest{method: "HEAD", path: "/alice/weights/resolve/main/model.bin"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
		}
		if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
			t.Errorf("Content-Length = %q, want %q", got, want)
		}
	})
}
