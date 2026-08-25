package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// These tests are about what reaches the *store* when a transfer dies half way
// through, so they cannot use a hand-written Storage fake — the behaviour under
// test lives in the GCS client library, not in this file. They therefore run
// against a stub of the GCS JSON API that records the upload requests it is
// asked to serve, which needs no emulator and no network.
//
// Two request shapes finalise an object, and both are recorded as "commits":
//   - a single-request upload (uploadType=multipart / media), where the whole
//     body arrives at once;
//   - the last chunk of a resumable upload, recognisable by a Content-Range
//     that names a definite total ("bytes 0-9/10") rather than the "/*" every
//     intermediate chunk carries.
//
// A truncated commit is exactly the failure mode being guarded against: at a
// content-addressed key such as blobs/{sha} it is indistinguishable from a
// good object and, because writers skip a key that already exists, it is never
// repaired.
type uploadRecorder struct {
	srv *httptest.Server

	mu sync.Mutex
	// commits holds the byte count of every finalising upload request.
	commits []int64
	// chunks counts intermediate resumable chunks, which are harmless on their
	// own: without a finalising request the object never becomes visible.
	chunks int
	offset int64
}

func (u *uploadRecorder) commitCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.commits)
}

func (u *uploadRecorder) commitSizes() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.commits...)
}

const stubObjectJSON = `{"kind":"storage#object","bucket":"stub","name":"o","generation":"17","size":"0"}`

// newUploadRecorder returns a GCS driver wired to the stub, plus the recorder.
func newUploadRecorder(t *testing.T) (*GCS, *uploadRecorder) {
	t.Helper()
	u := &uploadRecorder{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)

		switch {
		case r.URL.Path == "/resumable-session":
			contentRange := r.Header.Get("Content-Range")
			u.mu.Lock()
			if strings.HasSuffix(contentRange, "/*") {
				// Not the last chunk: acknowledge and ask for more. The
				// library sends X-GUploader-No-308, so "resume incomplete" is
				// signalled as 200 + the override header, not as a real 308.
				u.chunks++
				u.offset += n
				off := u.offset
				u.mu.Unlock()
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", off-1))
				w.Header().Set("X-Http-Status-Code-Override", "308")
				w.WriteHeader(http.StatusOK)
				return
			}
			u.commits = append(u.commits, u.offset+n)
			u.mu.Unlock()

		case strings.Contains(r.URL.Path, "/upload/"):
			if strings.Contains(r.URL.RawQuery, "uploadType=resumable") {
				// Hand out an upload session; nothing is committed yet.
				w.Header().Set("Location", u.srv.URL+"/resumable-session")
				w.WriteHeader(http.StatusOK)
				return
			}
			u.mu.Lock()
			u.commits = append(u.commits, n)
			u.mu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stubObjectJSON)
	}))
	t.Cleanup(u.srv.Close)

	g, err := NewGCS(context.Background(), GCSOptions{
		Bucket:       "stub",
		EmulatorHost: strings.TrimPrefix(u.srv.URL, "http://"),
	})
	if err != nil {
		t.Fatalf("NewGCS(stub): %v", err)
	}
	// The bucket bootstrap in NewGCS is not part of what is being measured.
	u.mu.Lock()
	u.commits = nil
	u.chunks, u.offset = 0, 0
	u.mu.Unlock()
	return g, u
}

// failingReader yields failAfter bytes and then fails, standing in for a
// truncated LFS download, a dropped upstream connection, or a short read off
// the git object store.
type failingReader struct {
	total     int
	failAfter int
	off       int
}

var errTransferDied = errors.New("transfer died")

func (f *failingReader) Read(p []byte) (int, error) {
	if f.off >= f.failAfter {
		return 0, errTransferDied
	}
	n := len(p)
	if remaining := f.failAfter - f.off; n > remaining {
		n = remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	f.off += n
	return n, nil
}

func TestGCS_Put_TransferFailureCommitsNothing(t *testing.T) {
	g, rec := newUploadRecorder(t)

	err := g.Put(context.Background(), "blobs/ab/cd/abcd", &failingReader{total: 200000, failAfter: 100000}, "application/octet-stream")
	if !errors.Is(err, errTransferDied) {
		t.Fatalf("Put error = %v, want the reader's error", err)
	}
	if got := rec.commitSizes(); len(got) != 0 {
		t.Errorf("the store was asked to commit %v; a failed transfer must leave no object behind", got)
	}
}

func TestGCS_Put_ReaderFailingBeforeAnyByteCommitsNothing(t *testing.T) {
	g, rec := newUploadRecorder(t)

	// The nastier half of the same bug: with nothing buffered the writer was
	// never opened, and Close opens one and commits a zero-length object.
	err := g.Put(context.Background(), "blobs/ab/cd/abcd", &failingReader{}, "application/octet-stream")
	if !errors.Is(err, errTransferDied) {
		t.Fatalf("Put error = %v, want the reader's error", err)
	}
	if got := rec.commitSizes(); len(got) != 0 {
		t.Errorf("the store was asked to commit %v; an empty object is still a wrong object", got)
	}
}

func TestGCS_Put_LargeTransferFailureIsNeverFinalised(t *testing.T) {
	g, rec := newUploadRecorder(t)

	// Past the 16 MiB default chunk size the library switches to a resumable
	// upload, so earlier chunks have already reached the store by the time the
	// reader dies. Those are fine; what must not happen is the finalising
	// request that turns them into a visible, short object.
	err := g.Put(context.Background(), "blobs/ab/cd/abcd", &failingReader{failAfter: 35 << 20}, "application/octet-stream")
	if !errors.Is(err, errTransferDied) {
		t.Fatalf("Put error = %v, want the reader's error", err)
	}
	rec.mu.Lock()
	chunks := rec.chunks
	rec.mu.Unlock()
	if chunks == 0 {
		t.Fatal("no resumable chunk was sent; the test no longer exercises the multi-chunk path")
	}
	if got := rec.commitSizes(); len(got) != 0 {
		t.Errorf("the upload was finalised at %v bytes after the transfer failed", got)
	}
}

func TestGCS_PutIfGeneration_TransferFailureCommitsNothing(t *testing.T) {
	g, rec := newUploadRecorder(t)

	_, err := g.PutIfGeneration(context.Background(), "wal/index.json", 0, &failingReader{failAfter: 100000}, "application/json")
	if !errors.Is(err, errTransferDied) {
		t.Fatalf("PutIfGeneration error = %v, want the reader's error", err)
	}
	if got := rec.commitSizes(); len(got) != 0 {
		t.Errorf("the store was asked to commit %v; a failed transfer must not consume the generation", got)
	}
}

func TestGCS_Put_SuccessStillCommits(t *testing.T) {
	// The abort is implemented by cancelling the writer's context on every
	// return, so the happy path has to be pinned down too: the cancel must land
	// after the upload has been observed complete, not instead of it.
	g, rec := newUploadRecorder(t)

	if err := g.Put(context.Background(), "blobs/ab/cd/abcd", strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := rec.commitCount(); got != 1 {
		t.Errorf("%d commits, want 1", got)
	}
}

func TestGCS_PutIfGeneration_SuccessStillReportsTheGeneration(t *testing.T) {
	// Attrs() is read after Close and before the deferred cancel; a cancel that
	// ran too early would show up here as a zero generation or a panic.
	g, rec := newUploadRecorder(t)

	gen, err := g.PutIfGeneration(context.Background(), "wal/index.json", 0, strings.NewReader("{}"), "application/json")
	if err != nil {
		t.Fatalf("PutIfGeneration: %v", err)
	}
	if gen != 17 { // the generation the stub reports
		t.Errorf("generation = %d, want 17", gen)
	}
	if got := rec.commitCount(); got != 1 {
		t.Errorf("%d commits, want 1", got)
	}
}

// TestGCS_Put_FailedTransferLeavesNoObjectInTheEmulator is the same property
// checked end to end, where the store rather than a stub decides what exists.
// Skipped unless TF_TEST_GCS_EMULATOR is set (see gcs_cas_test.go).
func TestGCS_Put_FailedTransferLeavesNoObjectInTheEmulator(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	err := g.Put(ctx, "blobs/ab/cd/abcd", &failingReader{failAfter: 100000}, "application/octet-stream")
	if !errors.Is(err, errTransferDied) {
		t.Fatalf("Put error = %v, want the reader's error", err)
	}
	if _, statErr := g.Stat(ctx, "blobs/ab/cd/abcd"); !errors.Is(statErr, ErrNotFound) {
		t.Fatalf("Stat after a failed transfer = %v, want ErrNotFound", statErr)
	}
}
