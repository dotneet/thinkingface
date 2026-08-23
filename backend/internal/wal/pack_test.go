package wal

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestPackObjects_PackIsSelfContainedAndComplete(t *testing.T) {
	requireGit(t)
	src := newBare(t, t.TempDir(), "src.git")
	head := commitTo(t, src, "main", "one")

	body := readPack(t, mustPack(t, src, []string{head}, nil))
	if IsEmptyPack(body) {
		t.Fatal("pack is empty")
	}

	// Applying it to a repository that has never seen these objects is the
	// definition of self-contained: --strict would reject a thin pack here.
	dst := newBare(t, t.TempDir(), "dst.git")
	f := newFakeStore()
	if err := f.Put(context.Background(), "k.pack", strings.NewReader(string(body)), packContentType); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := applyPack(context.Background(), f, dst, "k.pack"); err != nil {
		t.Fatalf("applyPack: %v", err)
	}
	gitRun(t, dst, "update-ref", "refs/heads/main", head)
	assertHealthy(t, dst)
}

func TestPackObjects_ExcludeDropsAlreadyKnownObjects(t *testing.T) {
	requireGit(t)
	src := newBare(t, t.TempDir(), "src.git")
	first := commitTo(t, src, "main", "one")
	second := commitTo(t, src, "main", "two")

	full := readPack(t, mustPack(t, src, []string{second}, nil))
	incremental := readPack(t, mustPack(t, src, []string{second}, []string{first}))

	if len(incremental) >= len(full) {
		t.Errorf("incremental pack (%d bytes) is not smaller than the full pack (%d bytes)", len(incremental), len(full))
	}
	if IsEmptyPack(incremental) {
		t.Error("incremental pack is empty, but the second commit adds objects")
	}
}

func TestPackObjects_WantFullyCoveredByExcludeYieldsAnEmptyPack(t *testing.T) {
	requireGit(t)
	src := newBare(t, t.TempDir(), "src.git")
	head := commitTo(t, src, "main", "one")

	// A client re-pushing what the server already has. pack-objects succeeds
	// and emits a 32-byte header-only pack; nothing downstream may treat that
	// as a failure.
	body := readPack(t, mustPack(t, src, []string{head}, []string{head}))
	if !IsEmptyPack(body) {
		t.Fatalf("pack of %d bytes is not empty: % x", len(body), body[:min(len(body), 16)])
	}

	// applyPack must skip it rather than storing a useless pack file.
	dst := newBare(t, t.TempDir(), "dst.git")
	f := newFakeStore()
	if err := f.Put(context.Background(), "empty.pack", strings.NewReader(string(body)), packContentType); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := applyPack(context.Background(), f, dst, "empty.pack"); err != nil {
		t.Fatalf("applyPack on empty pack: %v", err)
	}
	if got := gitRun(t, dst, "count-objects", "-v"); !strings.Contains(got, "packs: 0") {
		t.Errorf("empty pack was stored anyway:\n%s", got)
	}
}

func TestPackObjects_UnknownObjectFailsWhileReading(t *testing.T) {
	requireGit(t)
	src := newBare(t, t.TempDir(), "src.git")
	commitTo(t, src, "main", "one")

	rc, err := PackObjects(context.Background(), src, []string{hashA}, nil)
	if err != nil {
		// Failing at start is acceptable too; what matters is that the caller
		// never receives a short pack with a nil error.
		return
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("reading a pack of a nonexistent object returned no error: a truncated pack would be uploaded as if complete")
	}
}

func TestPackObjects_NoWantedObjectsIsRejected(t *testing.T) {
	requireGit(t)
	src := newBare(t, t.TempDir(), "src.git")
	commitTo(t, src, "main", "one")

	if _, err := PackObjects(context.Background(), src, nil, nil); err == nil {
		t.Error("PackObjects with no wants returned no error")
	}
	// A deletion-only update sends the zero hash as <new>; that is not a want.
	if _, err := PackObjects(context.Background(), src, []string{zeroHash}, nil); err == nil {
		t.Error("PackObjects with only the zero hash returned no error")
	}
}

func TestIsEmptyPack_RejectsNonPackData(t *testing.T) {
	if IsEmptyPack([]byte("not a pack at all")) {
		t.Error("arbitrary bytes reported as an empty pack")
	}
	if IsEmptyPack([]byte("PACK")) {
		t.Error("truncated header reported as an empty pack")
	}
}

func mustPack(t *testing.T, gitDir string, want, exclude []string) io.ReadCloser {
	t.Helper()
	rc, err := PackObjects(context.Background(), gitDir, want, exclude)
	if err != nil {
		t.Fatalf("PackObjects: %v", err)
	}
	return rc
}
