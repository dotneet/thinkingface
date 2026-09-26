// Tests for what happens after receive-pack runs (finishPush in git.go),
// driven with a real git receive-pack on both transports.

package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// goneClient is a client that disconnected before the status report reached
// it: every write fails. receive-pack has already updated the refs by the time
// it writes that report, so the push is committed while the transport fails.
type goneClient struct{ header http.Header }

func (g *goneClient) Header() http.Header       { return g.header }
func (g *goneClient) WriteHeader(int)           {}
func (g *goneClient) Write([]byte) (int, error) { return 0, errors.New("client went away") }

type goneWriter struct{}

func (goneWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }

// pushRequest builds a protocol-v0 push of one ref update. The new commit is
// written straight into the server's repository (without moving any ref), so
// the pack the client sends can be empty, exactly as `git push` sends when the
// server already has every object.
func pushRequest(t *testing.T, f *refsFixture, repo *store.Repo, oldSHA string) (newSHA string, body []byte) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
	gitDir := f.git.Dir(repo.StoragePath)
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = append(gitexec.Env(), "GIT_DIR="+gitDir,
			"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@example.com")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out))
	}
	main := run("rev-parse", "refs/heads/main")
	newSHA = run("commit-tree", main+"^{tree}", "-p", main, "-m", "pushed")

	var buf bytes.Buffer
	line := fmt.Sprintf("%s %s refs/heads/main\x00report-status\n", oldSHA, newSHA)
	fmt.Fprintf(&buf, "%04x%s0000", len(line)+4, line)
	pack := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	sum := sha1.Sum(pack)
	buf.Write(pack)
	buf.Write(sum[:])
	return newSHA, buf.Bytes()
}

func mainTip(t *testing.T, f *refsFixture, repo *store.Repo) string {
	t.Helper()
	heads, err := f.s.gitHTTP.HeadsAfterPush(repo.StoragePath)
	if err != nil {
		t.Fatalf("HeadsAfterPush: %v", err)
	}
	return heads["main"]
}

func httpPushToGoneClient(t *testing.T, f *refsFixture, token string, body []byte) {
	t.Helper()
	req := httptest.NewRequest("POST", "/models/alice/foo/git-receive-pack", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	f.s.Handler().ServeHTTP(&goneClient{header: http.Header{}}, req)
}

func TestReceivePack_PushThatLandedIsIndexedEvenWhenTheClientVanished(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	old := mainTip(t, f, repo)
	pushed, body := pushRequest(t, f, repo, old)

	httpPushToGoneClient(t, f, tok, body)

	if got := mainTip(t, f, repo); got != pushed {
		t.Fatalf("main = %s, want %s: the fixture did not land the push", got, pushed)
	}
	// Before the fix the Serve error returned early, and a committed push got
	// no sync job at all.
	jobs := f.sync.snapshot()
	if len(jobs) != 1 || jobs[0].Ref != "main" || jobs[0].OldSHA != old || jobs[0].NewSHA != pushed {
		t.Fatalf("sync jobs = %+v, want one for main %s -> %s", jobs, old, pushed)
	}
}

func TestReceivePack_RejectedPushToAVanishedClientSchedulesNothing(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	old := mainTip(t, f, repo)
	// A stale <old>: receive-pack refuses the update, so the refs never move.
	_, body := pushRequest(t, f, repo, strings.Repeat("1", 40))

	httpPushToGoneClient(t, f, tok, body)

	if got := mainTip(t, f, repo); got != old {
		t.Fatalf("main = %s, want %s untouched", got, old)
	}
	if jobs := f.sync.snapshot(); len(jobs) != 0 {
		t.Fatalf("sync jobs = %+v, want none for a push that did not land", jobs)
	}
}

func TestServeGit_SSHPushThatLandedIsIndexedEvenWhenTheSessionDropped(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	old := mainTip(t, f, repo)
	pushed, body := pushRequest(t, f, repo, old)

	err := f.s.ServeGit(context.Background(), f.alice, gitserver.ReceivePack, "model", "alice", "foo", "",
		gitserver.Streams{In: bytes.NewReader(body), Out: goneWriter{}, Err: io.Discard})
	if err == nil {
		t.Fatal("ServeGit succeeded, want the relay failure the dropped session causes")
	}

	if got := mainTip(t, f, repo); got != pushed {
		t.Fatalf("main = %s, want %s: the fixture did not land the push", got, pushed)
	}
	jobs := f.sync.snapshot()
	if len(jobs) != 1 || jobs[0].Ref != "main" || jobs[0].OldSHA != old || jobs[0].NewSHA != pushed {
		t.Fatalf("sync jobs = %+v, want one for main %s -> %s", jobs, old, pushed)
	}
}
