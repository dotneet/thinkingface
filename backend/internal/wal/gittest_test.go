package wal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
)

// These helpers drive the real git binary against real bare repositories: the
// WAL's correctness is defined by what git accepts, so faking it would test the
// wrong thing.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
}

func testGitEnv() []string {
	return append(gitexec.Env(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
}

func gitIn(t *testing.T, gitDir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = gitDir
	cmd.Env = append(testGitEnv(), "GIT_DIR="+gitDir)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func gitRun(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	return gitIn(t, gitDir, "", args...)
}

// newBare creates an empty bare repository at a fresh path under dir.
func newBare(t *testing.T, dir, name string) string {
	t.Helper()
	requireGit(t)
	path := filepath.Join(dir, name)
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", path)
	cmd.Env = testGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return path
}

// commitTo writes a commit straight into a bare repository with plumbing, which
// keeps the fixtures deterministic: no work tree, no clone, no config.
func commitTo(t *testing.T, gitDir, branch, content string) string {
	t.Helper()
	blob := gitIn(t, gitDir, content, "hash-object", "-w", "--stdin")
	tree := gitIn(t, gitDir, fmt.Sprintf("100644 blob %s\tfile.txt\n", blob), "mktree")

	args := []string{"commit-tree", tree, "-m", "commit " + content}
	if parent := refTarget(t, gitDir, "refs/heads/"+branch); parent != "" {
		args = append(args, "-p", parent)
	}
	hash := gitRun(t, gitDir, args...)
	gitRun(t, gitDir, "update-ref", "refs/heads/"+branch, hash)
	return hash
}

func refTarget(t *testing.T, gitDir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = gitDir
	cmd.Env = append(testGitEnv(), "GIT_DIR="+gitDir)
	out, err := cmd.Output()
	if err != nil {
		return "" // rev-parse --quiet exits 1 for an unknown ref
	}
	return strings.TrimSpace(string(out))
}

func allRefs(t *testing.T, gitDir string) map[string]string {
	t.Helper()
	refs, err := listRefs(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("listRefs: %v", err)
	}
	return refs
}

func assertRefs(t *testing.T, gitDir string, want map[string]string) {
	t.Helper()
	got := allRefs(t, gitDir)
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for ref, hash := range want {
		if got[ref] != hash {
			t.Fatalf("refs[%s] = %s, want %s (all refs: %v)", ref, got[ref], hash, got)
		}
	}
}

// assertHealthy is the same fsck-based check gitrepo's tests use: git itself is
// the authority on whether a materialised repository is usable.
func assertHealthy(t *testing.T, gitDir string) {
	t.Helper()
	out := gitRun(t, gitDir, "fsck", "--full", "--strict")
	lower := strings.ToLower(out)
	if strings.Contains(lower, "corrupt") || strings.Contains(lower, "error") || strings.Contains(lower, "missing") {
		t.Fatalf("git fsck reported a problem in %s:\n%s", gitDir, out)
	}
}

// pushToWAL performs the server side of one push exactly as §6 describes:
// pack the new objects, upload the entry, then CAS the index. Tests use it as
// their fixture builder, which keeps the fixture honest.
func pushToWAL(t *testing.T, f *fakeStore, srcDir, branch, oldHash, newHash string) {
	t.Helper()
	ctx := context.Background()

	ix, _, err := ReadIndex(ctx, f, storagePath)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	exclude := make([]string, 0, len(ix.Refs))
	for _, hash := range ix.Refs {
		exclude = append(exclude, hash)
	}

	rc, err := PackObjects(ctx, srcDir, []string{newHash}, exclude)
	if err != nil {
		t.Fatalf("PackObjects: %v", err)
	}
	entry, err := UploadEntry(ctx, f, storagePath, ix.Seq+1, rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("UploadEntry: %v", err)
	}
	if err := UpdateIndex(ctx, f, storagePath,
		[]RefUpdate{{Ref: "refs/heads/" + branch, Old: oldHash, New: newHash}}, entry); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
}

func readPack(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read pack: %v", err)
	}
	return body
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stringReader is the tiny reader the storage-level tests use to plant objects
// directly, without going through git.
func stringReader(s string) io.Reader { return strings.NewReader(s) }
