package wal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

func verifyOrFail(t *testing.T, fx *pushFixture) *VerifyReport {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch.git")
	report, err := Verify(context.Background(), fx.store, scratch, fx.dir, storagePath)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return report
}

func TestVerify_MatchingRepositoryReportsAMatch(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	second := commitTo(t, fx.dir, "main", "two")
	side := commitTo(t, fx.dir, "side", "side one")
	gitRun(t, fx.dir, "update-ref", "refs/tags/v1.0", first)
	fx.mustShadow(t,
		RefUpdate{Ref: "refs/heads/main", Old: first, New: second},
		RefUpdate{Ref: "refs/heads/side", Old: "", New: side},
		RefUpdate{Ref: "refs/tags/v1.0", Old: "", New: first},
	)

	report := verifyOrFail(t, fx)
	if !report.Match {
		t.Fatalf("Match = false, reason %q, report %+v", report.Reason, report)
	}
	if report.Reason != "" {
		t.Errorf("Reason = %q, want empty on a match", report.Reason)
	}
	_, gen := readIndexOrFail(t, fx.store)
	if report.Generation != gen {
		t.Errorf("Generation = %d, want %d", report.Generation, gen)
	}
}

func TestVerify_WithoutAnIndexIsReportedNotErrored(t *testing.T) {
	fx := newPushFixture(t)
	commitTo(t, fx.dir, "main", "one")

	report := verifyOrFail(t, fx)
	if report.Match || report.Reason != "no index" {
		t.Fatalf("report = %+v, want Match=false Reason=\"no index\"", report)
	}
	if report.Generation != 0 {
		t.Errorf("Generation = %d, want 0", report.Generation)
	}
}

func TestVerify_RefMissingFromTheWALIsReported(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	// A branch that only ever existed on disk: the shadow write that should
	// have mirrored it was lost.
	gitRun(t, fx.dir, "update-ref", "refs/heads/orphaned", head)

	report := verifyOrFail(t, fx)
	if report.Match {
		t.Fatal("Match = true, want false")
	}
	if len(report.RefsMissing) != 1 || report.RefsMissing[0] != "refs/heads/orphaned" {
		t.Errorf("RefsMissing = %v, want [refs/heads/orphaned]", report.RefsMissing)
	}
	if len(report.RefsExtra) != 0 || len(report.RefsDiffer) != 0 {
		t.Errorf("report = %+v, want only a missing ref", report)
	}
	if !strings.Contains(report.Reason, "refs differ") {
		t.Errorf("Reason = %q, want it to mention differing refs", report.Reason)
	}
}

func TestVerify_RefOnlyInTheWALIsReported(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	// Deleted on disk without the deletion reaching the WAL.
	gitRun(t, fx.dir, "update-ref", "refs/heads/main", head)
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/extra", Old: "", New: head})
	gitRun(t, fx.dir, "update-ref", "-d", "refs/heads/extra")

	report := verifyOrFail(t, fx)
	if report.Match {
		t.Fatal("Match = true, want false")
	}
	if len(report.RefsExtra) != 1 || report.RefsExtra[0] != "refs/heads/extra" {
		t.Errorf("RefsExtra = %v, want [refs/heads/extra]", report.RefsExtra)
	}
}

func TestVerify_RefAtADifferentHashIsReported(t *testing.T) {
	fx := newPushFixture(t)
	first := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: first})
	// The commit exists in the WAL's objects, but the ref was never moved.
	second := commitTo(t, fx.dir, "main", "two")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/tmp", Old: "", New: second})
	gitRun(t, fx.dir, "update-ref", "refs/heads/tmp", second)
	fx.store.writeIndexUnconditionally(t, storagePath, func(ix *Index) {
		ix.Refs["refs/heads/main"] = first
	})

	report := verifyOrFail(t, fx)
	if report.Match {
		t.Fatal("Match = true, want false")
	}
	if len(report.RefsDiffer) != 1 || report.RefsDiffer[0] != "refs/heads/main" {
		t.Errorf("RefsDiffer = %v, want [refs/heads/main]", report.RefsDiffer)
	}
	if len(report.RefsMissing) != 0 || len(report.RefsExtra) != 0 {
		t.Errorf("report = %+v, want only a hash mismatch", report)
	}
}

func TestVerify_RefusesToMaterializeOverTheRepositoryUnderTest(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	// Materialize rebuilds by deleting the directory; aiming it at the source
	// would destroy the evidence.
	if _, err := Verify(context.Background(), fx.store, fx.dir, fx.dir, storagePath); err == nil {
		t.Fatal("Verify accepted the same directory for both sides")
	}
	assertRefs(t, fx.dir, map[string]string{"refs/heads/main": head})
}

func TestVerify_CorruptWALPackIsAnErrorNotAVerdict(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})

	ix, _ := readIndexOrFail(t, fx.store)
	key := storage.WALKey(storagePath, ix.Entries[0])
	body, err := fx.store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	raw := readPack(t, body)
	// Keep the header intact so the pack still looks like a pack and claims the
	// same object count: index-pack --strict has to catch the damaged body.
	for i := packHeaderSize; i < len(raw); i++ {
		raw[i] ^= 0xff
	}
	if err := fx.store.Put(context.Background(), key, strings.NewReader(string(raw)), packContentType); err != nil {
		t.Fatalf("put corrupt entry: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch.git")
	if _, err := Verify(context.Background(), fx.store, scratch, fx.dir, storagePath); err == nil {
		t.Fatal("Verify accepted a corrupt pack")
	}
}

func TestVerify_MissingObjectsBehindAMatchingRefAreCaught(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	src := newBare(t, dir, "src.git")
	first := commitTo(t, src, "main", "one")
	head := commitTo(t, src, "main", "two")

	// A shallow clone is the cleanest way to produce the failure this check
	// exists for: the same ref at the same hash on both sides, with part of the
	// history behind it absent. Ref comparison alone calls these two equal.
	shallow := filepath.Join(dir, "shallow.git")
	clone := exec.Command("git", "clone", "--bare", "--quiet", "--depth=1", "file://"+src, shallow)
	clone.Env = testGitEnv()
	if out, err := clone.CombinedOutput(); err != nil {
		t.Skipf("shallow clone unavailable in this environment: %v\n%s", err, out)
	}

	refs := map[string]string{"refs/heads/main": head}
	ref, err := compareReachable(context.Background(), src, shallow, refs)
	if err != nil {
		t.Fatalf("compareReachable: %v", err)
	}
	if ref != "refs/heads/main" {
		t.Fatalf("compareReachable = %q, want refs/heads/main: a truncated history passed", ref)
	}

	// The same call against two complete copies must stay quiet, or the check
	// is just noise.
	full := filepath.Join(dir, "full.git")
	clone = exec.Command("git", "clone", "--bare", "--quiet", "file://"+src, full)
	clone.Env = testGitEnv()
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	ref, err = compareReachable(context.Background(), src, full, refs)
	if err != nil {
		t.Fatalf("compareReachable: %v", err)
	}
	if ref != "" {
		t.Fatalf("compareReachable = %q, want \"\" for two identical copies", ref)
	}
	if objs, err := reachableObjects(context.Background(), src, head); err != nil {
		t.Fatalf("reachableObjects: %v", err)
	} else if _, ok := objs[first]; !ok {
		t.Error("the parent commit is missing from the reachable set")
	}
}

func TestVerify_UnreadableRepositoryUnderTestIsAnError(t *testing.T) {
	fx := newPushFixture(t)
	head := commitTo(t, fx.dir, "main", "one")
	fx.mustShadow(t, RefUpdate{Ref: "refs/heads/main", Old: "", New: head})
	if err := os.RemoveAll(fx.dir); err != nil {
		t.Fatalf("remove: %v", err)
	}

	scratch := filepath.Join(t.TempDir(), "scratch.git")
	if _, err := Verify(context.Background(), fx.store, scratch, fx.dir, storagePath); err == nil {
		t.Fatal("Verify reported on a repository it could not read")
	}
}
