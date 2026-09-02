// serveLFSFile used to take X-Linked-Size and Content-Length straight from the
// LFS pointer in the tree, which is text a writer committed. Every path that
// *creates* a link refuses a size that disagrees with the object
// (store.LinkLFSObjects, verifyCommitLFSFile), but a repository already linked
// to an object could then push a hand-written pointer naming it with any size
// it liked: on the emulator path net/http truncates the body at the declared
// Content-Length, and on the signed-URL path GCS serves the whole object so
// hf_hub_download's own size check fails instead. The size now comes from the
// ledger, which is written from the object's measured length at promotion.

package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

// newLyingPointerFixture links an object legitimately and then commits a
// pointer that names it with a size it does not have.
func newLyingPointerFixture(t *testing.T) (f *secFixture, realSize int64) {
	t.Helper()
	f = newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("the real weights, all of them")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	// "size 5" is the lie. The oid is this repository's own, so nothing on the
	// ownership side has anything to object to.
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid + "\nsize 5\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)
	return f, int64(len(body))
}

func TestResolveLFS_ServesTheObjectsSizeNotThePointers(t *testing.T) {
	f, realSize := newLyingPointerFixture(t)
	want := strconv.FormatInt(realSize, 10)

	rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q; want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != want {
		t.Errorf("Content-Length = %q, want %q -- the pointer's claim was served as fact", got, want)
	}
	// hf_hub_download compares this against the file it wrote, so a "5" here
	// fails every download of an object that is perfectly intact.
	if got := rec.Header().Get("X-Linked-Size"); got != want {
		t.Errorf("X-Linked-Size = %q, want %q", got, want)
	}
	if int64(rec.Body.Len()) != realSize {
		t.Errorf("body = %d bytes, want %d", rec.Body.Len(), realSize)
	}
}

// The HEAD is what huggingface_hub asks first (get_hf_file_metadata), so it
// has to agree with the GET rather than repeating the pointer.
func TestResolveLFS_HeadAgreesWithTheObject(t *testing.T) {
	f, realSize := newLyingPointerFixture(t)
	want := strconv.FormatInt(realSize, 10)

	rec := f.do(secRequest{method: "HEAD", path: "/alice/weights/resolve/main/model.bin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Length"); got != want {
		t.Errorf("Content-Length = %q, want %q", got, want)
	}
	if got := rec.Header().Get("X-Linked-Size"); got != want {
		t.Errorf("X-Linked-Size = %q, want %q", got, want)
	}
}

// A Range is measured against the object as well: bounding it by the pointer's
// claim would make "bytes=6-" unsatisfiable on a file that has those bytes.
func TestResolveLFS_RangeIsMeasuredAgainstTheObject(t *testing.T) {
	f, realSize := newLyingPointerFixture(t)

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/alice/weights/resolve/main/model.bin",
		headers: map[string]string{"Range": "bytes=6-"},
	})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, body = %q; want 206", rec.Code, rec.Body.String())
	}
	if int64(rec.Body.Len()) != realSize-6 {
		t.Errorf("body = %d bytes, want %d", rec.Body.Len(), realSize-6)
	}
}
