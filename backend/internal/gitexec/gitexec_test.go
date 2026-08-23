package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests run the real git binary: what is being asserted is that git
// agrees with what this package believes it is telling it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestEnvConfigPairsAreWellFormed(t *testing.T) {
	env := Env()
	byKey := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		byKey[k] = v
	}
	count, err := strconv.Atoi(byKey["GIT_CONFIG_COUNT"])
	if err != nil {
		t.Fatalf("GIT_CONFIG_COUNT is not a number: %q", byKey["GIT_CONFIG_COUNT"])
	}
	if count != len(serverConfig) {
		t.Fatalf("GIT_CONFIG_COUNT = %d, want %d", count, len(serverConfig))
	}
	// A missing KEY_n/VALUE_n pair makes git reject every invocation, so the
	// numbering has to be dense.
	for i := range count {
		if _, ok := byKey["GIT_CONFIG_KEY_"+strconv.Itoa(i)]; !ok {
			t.Errorf("GIT_CONFIG_KEY_%d missing", i)
		}
		if _, ok := byKey["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]; !ok {
			t.Errorf("GIT_CONFIG_VALUE_%d missing", i)
		}
	}
}

func TestEnvIsWhatGitReads(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	if err := InitBare(context.Background(), dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, kv := range serverConfig {
		if got := gitOut(t, dir, "config", "--get", kv[0]); got != kv[1] {
			t.Errorf("git config %s = %q, want %q", kv[0], got, kv[1])
		}
	}
}

// The point of shutting the ambient environment out: a repository must not be
// configured by whoever happens to run the server.
func TestEnvIgnoresAmbientConfig(t *testing.T) {
	requireGit(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[receive]\n\tautogc = true\n[core]\n\thooksPath = /tmp/evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	dir := t.TempDir()
	if err := InitBare(context.Background(), dir, "main"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := gitOut(t, dir, "config", "--get", "receive.autogc"); got != "false" {
		t.Errorf("receive.autogc = %q, want false (ambient config leaked in)", got)
	}
	// Sanity check on the fixture, not on Env: a bare exec -- the shape this
	// codebase must never use -- has to actually pick the ambient config up,
	// otherwise the assertion above proves nothing.
	out, _ := exec.Command("git", "-C", dir, "config", "--get", "core.hooksPath").Output()
	if strings.TrimSpace(string(out)) != "/tmp/evil" {
		t.Fatalf("fixture ineffective: a bare exec read core.hooksPath = %q, want /tmp/evil", strings.TrimSpace(string(out)))
	}
}

func TestInitBareIsMinimalAndDeterministic(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	if err := InitBare(context.Background(), dir, "trunk"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// --template= means the template's sample hooks, description and
	// info/exclude are never copied in.
	for _, unwanted := range []string{"hooks", "info", "description"} {
		if _, err := os.Stat(filepath.Join(dir, unwanted)); err == nil {
			t.Errorf("%s exists; the empty template should have left it out", unwanted)
		}
	}
	if got := gitOut(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/trunk" {
		t.Errorf("HEAD = %q, want refs/heads/trunk", got)
	}
	if got := gitOut(t, dir, "rev-parse", "--show-object-format"); got != "sha1" {
		t.Errorf("object format = %q, want sha1", got)
	}
	if got := gitOut(t, dir, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Errorf("is-bare-repository = %q, want true", got)
	}
}

func TestInitBareDefaultsToMain(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	if err := InitBare(context.Background(), dir, ""); err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := gitOut(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("HEAD = %q, want refs/heads/main", got)
	}
}
