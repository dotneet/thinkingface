package wal

import (
	"bufio"
	"context"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// refPolicy decides where a push gets the <old> values it hands to UpdateIndex.
// It is the only thing that differs between the two phases of the migration
// (docs/dev/continuity-design.md §15): everything after — packing, uploading, the
// CAS — is identical, and is shared below so the two paths cannot drift.
type refPolicy int

const (
	// mirrorRefs replaces every <old> with whatever the index currently says.
	// Used while the on-disk repository is still authoritative (Phase 2): the
	// WAL is a follower there, and refusing a mirror write because the WAL
	// disagrees would only stall the mirror, never protect anything.
	mirrorRefs refPolicy = iota

	// strictRefs keeps the client's <old> untouched, so UpdateIndex enforces
	// the §6.1 precondition and rejects a non-fast-forward. Used once the WAL
	// is authoritative (Phase 4+), where that check is the only thing standing
	// between two instances and a lost commit.
	strictRefs
)

// ShadowPush mirrors a push into the WAL while the local bare repository is
// still the source of truth (§15 Phase 2). It is a follower, and behaves like
// one:
//
//   - each update's Old is discarded and replaced with the index's current
//     value, so the WAL converges on the disk state instead of arguing with it;
//   - updates the index already agrees with are dropped, which makes replaying
//     the same push — or shadowing a repository that was seeded a moment ago —
//     free rather than an empty entry per push.
//
// Two instances mirroring the same ref concurrently can still interleave so
// that the older value wins, in which case UpdateIndex returns ErrStaleRef and
// the WAL is left behind. That is tolerated deliberately: nothing depends on
// the WAL yet, and Phase 3's Verify/Seed detect and repair the divergence.
// Callers must log the error and let the push through — failing a push because
// its shadow copy failed would make Phase 2 riskier than the state it replaces.
func ShadowPush(ctx context.Context, st storage.Storage, gitDir, storagePath string, updates []RefUpdate) error {
	return pushToIndex(ctx, st, gitDir, storagePath, updates, mirrorRefs)
}

// AuthoritativePush is the push path of §6, for the phase where the WAL is the
// source of truth. The client's <old> values survive untouched into
// UpdateIndex, so a ref another instance has moved produces ErrStaleRef (via
// *StaleRefError, which names the ref) and a repository too contended to settle
// produces ErrRetryExhausted. Both must reach the client as a rejected push:
// the pre-receive hook turns them into a non-zero exit, receive-pack discards
// the quarantine, and nothing at all is written.
//
// Returning nil means the update is durable and linearised — the only condition
// under which a push may be acknowledged (invariant 4 of §5).
func AuthoritativePush(ctx context.Context, st storage.Storage, gitDir, storagePath string, updates []RefUpdate) error {
	return pushToIndex(ctx, st, gitDir, storagePath, updates, strictRefs)
}

// pushToIndex is §6 steps a–c: pack the new objects, upload the entry, CAS the
// index. The order is invariant 2 of §5 — the pack is fully durable before the
// index names it — and it is enforced structurally here by UploadEntry
// returning before UpdateIndex is called.
func pushToIndex(ctx context.Context, st storage.Storage, gitDir, storagePath string, updates []RefUpdate, policy refPolicy) error {
	if len(updates) == 0 {
		return nil
	}

	ix, _, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return err
	}
	effective, want := plannedUpdates(ix, updates, policy)
	if len(effective) == 0 {
		return nil // mirror had nothing to add; no entry, no index revision
	}

	// The index refs are what the WAL already holds, so they are exactly the
	// right exclude set — after filtering to what this copy can resolve, since
	// the index may be ahead of it.
	exclude, err := knownObjects(ctx, gitDir, sortedRefValues(ix.Refs))
	if err != nil {
		return err
	}

	entry := ""
	if len(want) > 0 {
		entry, err = packAndUpload(ctx, st, gitDir, storagePath, ix.Seq+1, want, exclude)
		if err != nil {
			return err
		}
	}
	return UpdateIndex(ctx, st, storagePath, effective, entry)
}

// plannedUpdates rewrites the incoming updates according to policy and collects
// the tips the entry pack has to carry.
func plannedUpdates(ix *Index, updates []RefUpdate, policy refPolicy) ([]RefUpdate, []string) {
	effective := make([]RefUpdate, 0, len(updates))
	want := make([]string, 0, len(updates))
	wanted := make(map[string]bool, len(updates))

	for _, u := range updates {
		eff := u
		if policy == mirrorRefs {
			if sameHash(ix.Refs[u.Ref], u.New) {
				// Already mirrored — including "delete a ref the WAL never
				// had", where both sides read as absent.
				continue
			}
			eff.Old = ix.Refs[u.Ref]
		}
		effective = append(effective, eff)

		if isAbsent(eff.New) || wanted[eff.New] {
			continue // a deletion carries no objects; two refs may share a tip
		}
		wanted[eff.New] = true
		want = append(want, eff.New)
	}
	return effective, want
}

// packAndUpload returns the index-relative entry name, or "" when the pack
// turned out to be empty.
//
// An empty pack is normal — re-pushing what the server already has, or moving a
// ref onto an existing commit — and uploading it would leave every later
// materialisation with one more object to fetch and skip for nothing.
//
// The emptiness test peeks at the 12-byte header instead of buffering the pack:
// the first push after a repository is seeded carries its whole history, which
// does not belong in memory. Peek leaves the bytes in the buffer, so the body
// still streams straight from pack-objects into storage.
func packAndUpload(ctx context.Context, st storage.Storage, gitDir, storagePath string, seq int, want, exclude []string) (string, error) {
	rc, err := PackObjects(ctx, gitDir, want, exclude)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	br := bufio.NewReader(rc)
	count, err := packObjectCount(br)
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	return UploadEntry(ctx, st, storagePath, seq, br)
}

// AdoptIfConverged stamps the local copy with the current index generation, but
// only when the refs on disk already match the index exactly.
//
// It is the cheap follow-up to a successful push: receive-pack has just moved
// the quarantined objects into the repository and updated its refs, so the copy
// is byte-for-byte what a Materialize of that generation would produce. Without
// this, the next Materialize sees a stale state file and re-applies the entry
// the pusher itself created.
//
// The ref comparison is the safety argument, not an optimisation: it is direct
// evidence that the objects-then-refs ordering of §9 has already completed on
// disk, which is what the state file asserts. When the refs do not match — a
// partial push, a concurrent writer, a rebuild in progress — this does nothing
// and the next Materialize converges the copy properly. Doing nothing is always
// a correct outcome here.
func AdoptIfConverged(ctx context.Context, st storage.Storage, gitDir, storagePath string) error {
	idx, gen, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return err
	}
	if gen == 0 {
		return nil // no index yet: nothing to claim convergence with
	}
	if !isBareRepo(gitDir) {
		return nil
	}

	onDisk, err := listRefs(ctx, gitDir)
	if err != nil {
		return err
	}
	if !sameRefs(onDisk, idx.Refs) {
		return nil
	}
	return writeLocalState(gitDir, gen, idx)
}

func sameRefs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for ref, hash := range a {
		other, ok := b[ref]
		if !ok || !sameHash(hash, other) {
			return false
		}
	}
	return true
}
