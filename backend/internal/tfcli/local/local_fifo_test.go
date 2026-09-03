//go:build !windows

package local

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestScanSkipsSymlinkToFIFO is the regression test for a symlink whose
// target is neither a directory nor a regular file (a FIFO, device node, or
// socket) being treated as an ordinary file to upload. The non-symlink
// branch already rejects such targets via d.Type().IsRegular(), but the
// symlink branch only checked target.IsDir() and fell straight through to
// keep() for anything else -- so a symlink to a FIFO was reported as a
// zero-content "file" that Scan happily includes, and a later os.Open +
// io.Copy on it hangs forever waiting for a writer that will never come.
//
// syscall.Mkfifo has no Windows equivalent, so this lives in its own
// !windows-tagged file rather than a runtime.GOOS check in local_test.go --
// unlike a runtime skip, an unconditional call to it would fail to *compile*
// on windows.
func TestScanSkipsSymlinkToFIFO(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "hello")

	fifoDir := t.TempDir() // outside root: keep the FIFO itself off the scan
	fifoPath := filepath.Join(fifoDir, "pipe")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.Symlink(fifoPath, filepath.Join(root, "data.bin")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	files, allPaths, skipped, err := Scan(root, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, f := range files {
		if f.RepoPath == "data.bin" {
			t.Fatalf("files = %v, want data.bin excluded (its target is a FIFO, not a regular file)", files)
		}
	}
	found := false
	for _, s := range skipped {
		if s.RepoPath == "data.bin" {
			found = true
			if s.Reason != ReasonIrregular {
				t.Errorf("skipped data.bin reason = %q, want %q", s.Reason, ReasonIrregular)
			}
		}
	}
	if !found {
		t.Fatalf("skipped = %v, want an entry for data.bin", skipped)
	}
	// The path still exists on disk (as a symlink), so --delete must not
	// mistake this for "gone locally" and remove a remote copy.
	if !containsString(allPaths, "data.bin") {
		t.Errorf("allPaths = %v, want data.bin present (it exists on disk, even though it's not uploaded)", allPaths)
	}
}
