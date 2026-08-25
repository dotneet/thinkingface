package gitserver

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stalledBody is a request body that delivers a short prefix and then goes
// quiet without ever reaching EOF -- a client that sent its request and then
// stopped talking while holding the connection open.
type stalledBody struct {
	prefix  []byte
	release <-chan struct{}
}

func (b *stalledBody) Read(p []byte) (int, error) {
	if len(b.prefix) > 0 {
		n := copy(p, b.prefix)
		b.prefix = b.prefix[n:]
		return n, nil
	}
	<-b.release
	return 0, io.EOF
}

// TestServeDoesNotWaitForAStalledRequestBody is the regression test for a
// handler that outlived the work it was doing. With the body handed to
// cmd.Stdin, os/exec copies it on a goroutine that cmd.Wait blocks on -- and
// that copy ends when the client stops sending, not when git is done. A
// --stateless-rpc upload-pack exits on the first flush packet without draining
// its input, so a client that then said nothing more held this handler, its
// goroutines and its connection open indefinitely, as many times over as it
// cared to connect. WaitDelay does not cover this case (closing git's end of
// the pipe cannot unblock a read of the body), so the copy has to be detached
// from Wait instead.
func TestServeDoesNotWaitForAStalledRequestBody(t *testing.T) {
	h, storagePath := newAdvertiseFixture(t)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	// A lone flush packet: a complete, well-formed v2 request that upload-pack
	// answers by exiting straight away.
	req := httptest.NewRequest(http.MethodPost, "/git-upload-pack",
		&stalledBody{prefix: []byte("0000"), release: release})
	rec := httptest.NewRecorder()

	done := make(chan error, 1)
	go func() { done <- h.Serve(rec, req, storagePath, UploadPack) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Serve was still waiting on a silent client long after git had exited")
	}
}

// TestRequestBodyLeavesAPushUnlimited is the counterweight to the guard below:
// the bytes of a push are the payload this server exists to carry, so an
// ordinary body must reach git untouched and uncapped.
func TestRequestBodyLeavesAPushUnlimited(t *testing.T) {
	payload := bytes.Repeat([]byte("pack"), 1<<18) // 1 MiB
	req := httptest.NewRequest(http.MethodPost, "/git-receive-pack", bytes.NewReader(payload))

	body, closeBody, err := requestBody(req)
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	defer closeBody()
	if _, ok := body.(*ratioLimitedReader); ok {
		t.Error("a plain request body was wrapped in the expansion guard")
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("body read back as %d bytes, want %d unchanged", len(got), len(payload))
	}
}

// TestRequestBodyRejectsARunawayExpansion is the regression test for a gzipped
// body that was decompressed into git's stdin with no bound at all: a client
// could hand over a few hundred kilobytes and have the server manufacture
// gigabytes out of them.
func TestRequestBodyRejectsARunawayExpansion(t *testing.T) {
	compressed := gzipZeros(t, gzipExpansionFloor+(32<<20))
	if ratio := (gzipExpansionFloor + (32 << 20)) / len(compressed); ratio <= maxGzipRatio {
		t.Fatalf("test fixture only expands %dx, which is inside the allowed ratio", ratio)
	}

	req := httptest.NewRequest(http.MethodPost, "/git-receive-pack", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "gzip")

	body, closeBody, err := requestBody(req)
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	defer closeBody()

	n, err := io.Copy(io.Discard, body)
	if !errors.Is(err, errGzipRatio) {
		t.Fatalf("copied %d bytes with error %v, want errGzipRatio", n, err)
	}
	// It must give up promptly once past the floor rather than after another
	// arbitrary amount of work.
	if n > gzipExpansionFloor+(1<<20) {
		t.Errorf("the guard let %d bytes through, far past the %d-byte floor", n, int64(gzipExpansionFloor))
	}
}

// TestRequestBodyAllowsASmallCompressibleBody keeps the guard off the traffic
// it was never aimed at: below the floor any ratio is fine, so a short
// well-compressed request still gets through.
func TestRequestBodyAllowsASmallCompressibleBody(t *testing.T) {
	const size = 4 << 20
	req := httptest.NewRequest(http.MethodPost, "/git-upload-pack", bytes.NewReader(gzipZeros(t, size)))
	req.Header.Set("Content-Encoding", "gzip")

	body, closeBody, err := requestBody(req)
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	defer closeBody()

	n, err := io.Copy(io.Discard, body)
	if err != nil {
		t.Fatalf("a %d-byte body compressed far better than any ratio cap should care about: %v", size, err)
	}
	if n != size {
		t.Errorf("read %d bytes, want %d", n, size)
	}
}

// gzipZeros returns the gzip stream of n zero bytes -- the cheapest stand-in
// for a body whose author chose the compression ratio.
func gzipZeros(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	chunk := make([]byte, 1<<20)
	for written := 0; written < n; {
		size := min(len(chunk), n-written)
		if _, err := zw.Write(chunk[:size]); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		written += size
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestServeLeavesTheConnectionReusable is the other half of the stall fix.
// Releasing a parked body read means expiring the connection's read deadline,
// and doing that on a request that was not stalled poisons the connection:
// net/http starts a background read between keep-alive requests to notice a
// client hanging up, an expired deadline makes it fail immediately, and the
// connection's context is cancelled -- so the *next* request arrives with a
// context that is already done and fails before it does anything.
//
// A git clone is several RPCs over one connection, so that turned every clone
// into a 500 on its second request. Only the E2E suite noticed; this is the
// unit-level statement of the same thing.
func TestServeLeavesTheConnectionReusable(t *testing.T) {
	h, storagePath := newAdvertiseFixture(t)

	var mu sync.Mutex
	conns := map[net.Conn]bool{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h.Serve(w, r, storagePath, UploadPack); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	srv.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			conns[c] = true
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	for i := range 2 {
		// A lone flush packet: upload-pack answers it and exits, which is the
		// shape that leaves the body copy racing the handler's return.
		resp, err := client.Post(srv.URL, "application/x-git-upload-pack-request",
			strings.NewReader("0000"))
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, resp.StatusCode)
		}
	}

	// Both requests have to have travelled the same connection, or the test
	// would pass without ever exercising reuse.
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 1 {
		t.Fatalf("the two requests opened %d connections, want 1 (keep-alive was not exercised)", len(conns))
	}
}
