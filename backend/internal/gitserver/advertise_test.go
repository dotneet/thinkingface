package gitserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

func newAdvertiseFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	manager := gitrepo.NewManager(t.TempDir())
	const storagePath = "repos/test"
	if err := manager.Init(storagePath, "main"); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}
	seedCommit(t, manager.Dir(storagePath))
	return New(manager), storagePath
}

func TestAdvertiseRefsWritesTheAdvertisement(t *testing.T) {
	h, storagePath := newAdvertiseFixture(t)

	rec := httptest.NewRecorder()
	if err := h.AdvertiseRefs(context.Background(), rec, storagePath, UploadPack); err != nil {
		t.Fatalf("AdvertiseRefs: %v", err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "001e# service=git-upload-pack\n0000") {
		t.Errorf("advertisement does not open with the service pkt-line and flush: %q", body)
	}
	// gitEnv frames every request as protocol v2, whose first response is
	// the capability list rather than the ref list.
	if !strings.Contains(body, "version 2") || !strings.Contains(body, "ls-refs") {
		t.Errorf("advertisement is not a protocol v2 capability list: %q", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-git-upload-pack-advertisement" {
		t.Errorf("Content-Type = %q", got)
	}
}

// TestAdvertiseRefsHonoursACancelledContext is the regression test for a
// request that outlived its client. AdvertiseRefs used exec.Command while
// Serve next to it used exec.CommandContext, so a client that hung up during
// negotiation left upload-pack walking the object database on its behalf --
// with the repository and the number of concurrent requests both chosen by
// whoever hung up.
func TestAdvertiseRefsHonoursACancelledContext(t *testing.T) {
	h, storagePath := newAdvertiseFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	err := h.AdvertiseRefs(ctx, rec, storagePath, UploadPack)
	if err == nil {
		t.Fatalf("AdvertiseRefs ran git for a cancelled request and returned success (body %q)", rec.Body.String())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not report the cancellation", err)
	}
	// Nothing may have been written: the handler still has to be able to turn
	// this into a real status code.
	if rec.Body.Len() != 0 {
		t.Errorf("a failed advertisement wrote %d bytes of body", rec.Body.Len())
	}
	if errors.Is(err, ErrResponseStarted) {
		t.Errorf("a failure before any output claimed the response had started")
	}
}

// TestAdvertiseRefsFailureWritesNothing pins down the property handleInfoRefs
// depends on: when the command fails, the response has not been committed, so
// the caller can answer 500 instead of the empty 200 that a git client reads
// as "this repository has no refs".
func TestAdvertiseRefsFailureWritesNothing(t *testing.T) {
	h, _ := newAdvertiseFixture(t)

	rec := httptest.NewRecorder()
	err := h.AdvertiseRefs(context.Background(), rec, "repos/does-not-exist", UploadPack)
	if err == nil {
		t.Fatalf("AdvertiseRefs on a missing repository returned success")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a failed advertisement wrote %d bytes of body", rec.Body.Len())
	}
	if rec.Header().Get("Content-Type") != "" {
		t.Errorf("a failed advertisement set Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if errors.Is(err, ErrResponseStarted) {
		t.Errorf("a failure before any output claimed the response had started")
	}
}

// TestAdvertiseRefsReportsAWriteFailureAsStarted covers the other branch: once
// the status line is out there is no status left to change, and the caller
// must log rather than try to write a second one.
func TestAdvertiseRefsReportsAWriteFailureAsStarted(t *testing.T) {
	h, storagePath := newAdvertiseFixture(t)

	w := &failingWriter{ResponseRecorder: httptest.NewRecorder()}
	err := h.AdvertiseRefs(context.Background(), w, storagePath, UploadPack)
	if err == nil {
		t.Fatalf("AdvertiseRefs ignored a failing response writer")
	}
	if !errors.Is(err, ErrResponseStarted) {
		t.Errorf("error %v is not marked with ErrResponseStarted", err)
	}
}

type failingWriter struct {
	*httptest.ResponseRecorder
}

func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client hung up") }
