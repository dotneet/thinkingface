package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Generation-pinned copies and reads are what let LFS promotion publish the
// exact version it verified (internal/lfs publishStaged). Like the CAS tests
// these need the store to arbitrate, so they run against fake-gcs-server and
// are skipped unless TF_TEST_GCS_EMULATOR names one; see casTestStorage.

func TestGCS_CopyGeneration_CopiesOnlyTheNamedGeneration(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	if err := g.Put(ctx, "staged", strings.NewReader("checked"), ""); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	checked := generationOf(t, g, "staged")
	if err := g.Put(ctx, "staged", strings.NewReader("swapped in later"), ""); err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	live := generationOf(t, g, "staged")
	if live == checked {
		t.Fatalf("a rewrite kept generation %d", live)
	}

	// The bucket does not keep superseded generations, so the checked one is
	// gone: the copy must fail rather than publish its replacement.
	err := g.CopyGeneration(ctx, "staged", checked, "published")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CopyGeneration(superseded) error = %v, want ErrNotFound", err)
	}
	if _, err := g.Stat(ctx, "published"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat(published) after a refused copy = %v, want ErrNotFound", err)
	}

	if err := g.CopyGeneration(ctx, "staged", live, "published"); err != nil {
		t.Fatalf("CopyGeneration(live): %v", err)
	}
	info, err := g.Stat(ctx, "published")
	if err != nil {
		t.Fatalf("Stat(published): %v", err)
	}
	if want := int64(len("swapped in later")); info.Size != want {
		t.Errorf("published size = %d, want %d", info.Size, want)
	}
}

func TestGCS_GetGeneration_ReadsOnlyTheNamedGeneration(t *testing.T) {
	g := casTestStorage(t)
	ctx := context.Background()

	if err := g.Put(ctx, "staged", strings.NewReader("checked"), ""); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	checked := generationOf(t, g, "staged")
	rc, err := g.GetGeneration(ctx, "staged", checked)
	if errors.Is(err, ErrNotFound) {
		t.Skip("emulator refuses object downloads at this host name")
	}
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(body) != "checked" {
		t.Fatalf("GetGeneration body = %q, %v; want %q", body, err, "checked")
	}

	if err := g.Put(ctx, "staged", strings.NewReader("swapped in later"), ""); err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	if _, err := g.GetGeneration(ctx, "staged", checked); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetGeneration(superseded) error = %v, want ErrNotFound", err)
	}
}

// A non-positive generation would reach the client library as "latest",
// silently turning a pinned operation back into an unpinned one.
func TestGCS_GenerationPinnedCallsRefuseANonPositiveGeneration(t *testing.T) {
	g := &GCS{}
	if _, err := g.GetGeneration(context.Background(), "k", 0); err == nil {
		t.Error("GetGeneration accepted generation 0")
	}
	if err := g.CopyGeneration(context.Background(), "k", -1, "d"); err == nil {
		t.Error("CopyGeneration accepted generation -1")
	}
}
