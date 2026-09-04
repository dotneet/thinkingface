package syncer

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// TestPush_OverQuotaPointerIsIndexedButNotLinked is the bypass this gate
// closes: an oid/size pair copied out of any readable repository, committed
// as a plain pointer file and pushed without ever speaking the LFS protocol.
// The bytes and the lfs_objects row already exist (seeded through the other
// repository), so LinkLFSObjects alone would link them -- the namespace's
// allowance is the only thing standing in the way, and nothing consulted it.
func TestPush_OverQuotaPointerIsIndexedButNotLinked(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("large file bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	// Both repositories live in alice's namespace, which already owes the
	// seeded object. One byte of allowance leaves no room for the push.
	if err := f.st.SetNamespaceQuota(f.ctx, "alice", ptr(int64(1))); err != nil {
		t.Fatalf("set namespace quota: %v", err)
	}
	f.syn.EnforceNamespaceQuota(0)

	f.push("main",
		addOp("README.md", "# foo\n"),
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)

	// The revision is still indexed with the oid its pointer declares -- the
	// file is listed, exactly as a pointer for content nobody uploaded is.
	files, err := f.st.ListRepoFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("list repo files: %v", err)
	}
	var indexed bool
	for _, file := range files {
		if file.Path == "model.bin" && file.LFSOID != nil && *file.LFSOID == pointerOID {
			indexed = true
		}
	}
	if !indexed {
		t.Fatalf("repo_files does not name the oid for model.bin: %+v", files)
	}

	// But no link backs it, so the download paths keep answering 404 for it --
	// the same unresolvable state a never-uploaded pointer leaves, and the one
	// the HF commit path's verifyCommitLFSFile refuses at the door.
	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if owned {
		t.Error("an over-quota pointer push earned a link; the quota gate did not run")
	}
}

// TestPush_QuotaGateLinksWhatFits is the other half: enforcement switched on
// must not change anything for a namespace with room left.
func TestPush_QuotaGateLinksWhatFits(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("large file bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	if err := f.st.SetNamespaceQuota(f.ctx, "alice", ptr(int64(1)<<40)); err != nil {
		t.Fatalf("set namespace quota: %v", err)
	}
	f.syn.EnforceNamespaceQuota(0)

	f.push("main", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
		Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))})

	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !owned {
		t.Error("a pointer push inside the quota earned no link; downloads of model.bin would 404")
	}
}

// TestPush_RelinkingWhatItAlreadyHoldsCostsNothing: a repository at its limit
// must still be able to push what it already holds, the same rule the batch
// and promotion paths apply.
func TestPush_RelinkingWhatItAlreadyHoldsCostsNothing(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("large file bytes")
	other := spareRepo(t, f.harness, "origin")
	seedLFSObject(t, f.harness, other.ID, pointerOID, content)

	// First push earns the link while there is room...
	if err := f.st.SetNamespaceQuota(f.ctx, "alice", ptr(int64(1)<<40)); err != nil {
		t.Fatalf("set namespace quota: %v", err)
	}
	f.syn.EnforceNamespaceQuota(0)
	f.push("main", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
		Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))})

	// ...then the allowance collapses to nothing. Re-pushing the same file on
	// another ref must not fail the job: relinking adds nothing to what the
	// usage query sums.
	if err := f.st.SetNamespaceQuota(f.ctx, "alice", ptr(int64(0))); err != nil {
		t.Fatalf("set namespace quota: %v", err)
	}
	f.push("feature", gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
		Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))})

	owned, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !owned {
		t.Error("the link earned before the quota collapsed is gone")
	}
}

func ptr(v int64) *int64 { return &v }
