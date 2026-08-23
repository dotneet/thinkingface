package gitserver

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

func TestSanitizeGitProtocol(t *testing.T) {
	accepted := []string{
		"version=2",
		"version=1",
		"version=2:object-format=sha256",
	}
	for _, v := range accepted {
		if got := sanitizeGitProtocol(v); got != v {
			t.Errorf("sanitizeGitProtocol(%q) = %q, want it kept", v, got)
		}
	}

	// The value is the only client-controlled string that reaches the git
	// service's environment, so anything unexpected is dropped rather than
	// escaped.
	rejected := []string{
		"",
		"2",
		"agent=git",
		"version=2 LD_PRELOAD=/tmp/x.so",
		"version=2\nGIT_SSH_COMMAND=sh",
		"version=2;id",
		"version=2:",
		":version=2",
		"version=2:$(id)",
		"version=" + strings.Repeat("2", 100),
	}
	for _, v := range rejected {
		if got := sanitizeGitProtocol(v); got != "" {
			t.Errorf("sanitizeGitProtocol(%q) = %q, want it dropped", v, got)
		}
	}
}

func TestSSHEnvCarriesTheClientProtocolOrNone(t *testing.T) {
	withProto := sshEnv("version=2")
	if !slices.Contains(withProto, "GIT_PROTOCOL=version=2") {
		t.Errorf("sshEnv did not pass the client's protocol through: %v", withProto)
	}
	if count := countPrefix(withProto, "GIT_PROTOCOL="); count != 1 {
		t.Errorf("GIT_PROTOCOL appears %d times, want exactly 1", count)
	}

	// Without an env request the variable must be absent: telling upload-pack
	// the client asked for v2 when it did not makes the exchange unreadable
	// to that client.
	if count := countPrefix(sshEnv(""), "GIT_PROTOCOL="); count != 0 {
		t.Errorf("GIT_PROTOCOL appears %d times with no client request, want 0", count)
	}
	if count := countPrefix(sshEnv("version=2; rm -rf /"), "GIT_PROTOCOL="); count != 0 {
		t.Errorf("a malformed protocol value survived into the environment")
	}

	// The isolation gitEnv provides must not be lost on the way.
	for _, want := range []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0"} {
		if !slices.Contains(withProto, want) {
			t.Errorf("sshEnv dropped %q", want)
		}
	}
}

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			n++
		}
	}
	return n
}

// TestServeSSHRunsUploadPackAgainstARealRepository exercises the exec
// plumbing end to end: argument order, the three streams, and the wait. A
// mistake in any of them would only show up as a hung or empty clone.
func TestServeSSHRunsUploadPackAgainstARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	manager := gitrepo.NewManager(root)
	const storagePath = "repos/test"
	if err := manager.Init(storagePath, "main"); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}
	head := seedCommit(t, manager.Dir(storagePath))

	var stdout, stderr bytes.Buffer
	// A lone flush packet is what a client sends to say "I want nothing";
	// upload-pack answers with the advertisement and exits 0.
	err := New(manager).ServeSSH(context.Background(), storagePath, UploadPack, "", Streams{
		In:  strings.NewReader("0000"),
		Out: &stdout,
		Err: &stderr,
	})
	if err != nil {
		t.Fatalf("ServeSSH: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), head) {
		t.Errorf("advertisement %q does not mention HEAD %s", stdout.String(), head)
	}
	if !strings.Contains(stdout.String(), "refs/heads/main") {
		t.Errorf("advertisement %q does not mention refs/heads/main", stdout.String())
	}
}

// seedCommit puts one empty commit on main in a bare repository and returns
// its sha.
func seedCommit(t *testing.T, dir string) string {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tree := run("hash-object", "-t", "tree", "/dev/null", "-w")
	sha := run("commit-tree", tree, "-m", "seed")
	run("update-ref", "refs/heads/main", sha)
	return sha
}
