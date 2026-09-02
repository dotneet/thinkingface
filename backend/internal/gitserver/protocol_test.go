package gitserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloneWorksAtEveryProtocolVersion is the regression test for the worst
// shape a transport bug can take: a success that is wrong.
//
// The HTTP transport used to put GIT_PROTOCOL=version=2 in the service's
// environment unconditionally, whatever the client had asked for. A client
// pinned to protocol v0 or v1 -- protocol.version in a corporate config, an
// older git, or one of the reimplementations (libgit2, dulwich, go-git) --
// then read a v2 capability list where it expected a ref advertisement, found
// no refs in it, and concluded the repository was empty. `git clone` printed
// "warning: You appear to have cloned an empty repository." and exited 0, so
// nothing downstream had any way to notice that the checkout it had just been
// handed was missing every file.
//
// This drives the real git binary end to end, at all three versions, and
// insists each clone actually contains the commit the server holds.
func TestCloneWorksAtEveryProtocolVersion(t *testing.T) {
	h, storagePath, head := newAdvertiseFixture(t)

	// The two halves of the smart HTTP transport, wired the way the API layer
	// wires them -- including handing AdvertiseRefs the client's header, which
	// is the entire point of the test.
	mux := http.NewServeMux()
	mux.HandleFunc("/repo.git/info/refs", func(w http.ResponseWriter, r *http.Request) {
		service, ok := ParseService(r.URL.Query().Get("service"))
		if !ok {
			http.Error(w, "unknown service", http.StatusBadRequest)
			return
		}
		if err := h.AdvertiseRefs(r.Context(), w, storagePath, service, r.Header.Get("Git-Protocol")); err != nil {
			t.Errorf("AdvertiseRefs: %v", err)
		}
	})
	mux.HandleFunc("/repo.git/git-upload-pack", func(w http.ResponseWriter, r *http.Request) {
		if err := h.Serve(w, r, storagePath, UploadPack); err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, version := range []string{"0", "1", "2"} {
		t.Run("protocol.version="+version, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "clone")
			out, err := runGit(t, "", "-c", "protocol.version="+version, "clone", srv.URL+"/repo.git", dest)
			if err != nil {
				t.Fatalf("clone at protocol.version=%s: %v: %s", version, err, out)
			}
			// The failure mode this guards against is not an error but a
			// cheerful warning, so it has to be checked for by name.
			if strings.Contains(out, "empty repository") {
				t.Errorf("clone at protocol.version=%s reported an empty repository: %s", version, out)
			}
			got, err := runGit(t, dest, "rev-parse", "HEAD")
			if err != nil {
				t.Fatalf("the clone at protocol.version=%s has no HEAD: %v: %s", version, err, got)
			}
			if strings.TrimSpace(got) != head {
				t.Errorf("clone at protocol.version=%s checked out %q, want %s", version, strings.TrimSpace(got), head)
			}
		})
	}
}

// runGit runs one git command in dir (the process's own directory when empty)
// with the developer's own git configuration kept out of it, so a global
// protocol.version cannot decide what this test measures.
func runGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
