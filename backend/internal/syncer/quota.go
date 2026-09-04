// Package syncer enforces namespace storage quotas on the push path it owns.
//
// The LFS batch API refuses an over-quota push before a byte moves, and the
// promotion check (lfs.chargeQuota) refuses the uploads that arrive through
// the transfer proxy or the browser. Neither sees the push this pipeline
// handles: a pointer file committed as an ordinary blob by a client that never
// spoke the LFS protocol. That push carries no transfer to refuse -- the link
// below is the first and only write it causes -- so the quota has to be
// consulted here, before LinkLFSObjects.
//
// A refused object is simply not linked. The revision is still indexed with
// the oid its pointer declares, which is exactly the state a pointer for
// content nobody uploaded leaves behind: repo_files names it, no link backs
// it, and every download path (resolve, the batch's download branch, the
// transfer proxy) answers 404 for it. The HF commit handler's
// verifyCommitLFSFile refuses the same push when it arrives through the commit
// API, so both write paths now agree that an over-quota object is an
// unresolvable file rather than a linked one.
//
// What is charged is the declared size, the same number LinkLFSObjects checks
// against the recorded one. A pointer that lies about the size earns no link
// there anyway, so charging its claim can only deny the link it was never
// going to get. An object this repository already links to is free, exactly as
// on the batch and promotion paths: relinking adds nothing to what
// store.UsageByRepo sums.
//
// Like those paths this is check-then-act without a reservation ledger (see
// the "Known limitation" note in lfs/quota.go): two pushes racing each other
// can both read the same usage and both be admitted. The sync jobs for one ref
// already serialise on the ref lock, so the remaining window is pushes to
// different refs of one namespace landing at once -- and the overshoot is
// bounded by what those pushes carry, not by the check being absent.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// EnforceNamespaceQuota switches quota enforcement on for the post-push
// pipeline. defaultBytes is the instance-wide allowance
// (TF_DEFAULT_STORAGE_QUOTA_BYTES) applied to namespaces carrying no override
// of their own; zero means unlimited, so a Syncer that never calls this
// behaves exactly as before. A Syncer that calls it with the default unset
// still enforces namespaces carrying an explicit override (zero included):
// the override survives resolution through store.EffectiveQuota, so only a
// namespace with no override and no default is unlimited.
//
// It is a separate call rather than a New argument for the same reason the
// LFS handler's switch is: "quotas are off" has to be a state the type can be
// in, in which the pipeline asks the database nothing extra per push.
func (s *Syncer) EnforceNamespaceQuota(defaultBytes int64) {
	s.quotaEnforced = true
	s.quotaDefault = defaultBytes
}

// filterLFSByQuota returns the refs the namespace may still link: everything
// that fits in what is left of its allowance, measured as a running total so
// a revision naming many objects is judged as a whole rather than one file at
// a time. Refs that do not fit are dropped with a warning, and the caller
// links only what comes back.
//
// A repository deleted mid-pipeline is success with nothing to link, not a
// failure: the pipeline's other steps (GetRepoByID, ReplaceRepoFiles) already
// treat a vanished repository that way, and failing here would retry a job
// whose work no longer exists. The ownership lookup below is one query per
// ref (N+1); revisions name few objects and the gate runs once per push, so
// batching it is not worth the complexity.
func (s *Syncer) filterLFSByQuota(ctx context.Context, repoID int64, refs []store.LFSObjectRef) ([]store.LFSObjectRef, error) {
	if !s.quotaEnforced || len(refs) == 0 {
		return refs, nil
	}
	q, err := s.store.NamespaceQuotaForRepo(ctx, repoID, s.quotaDefault)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Deleted between the GetRepoByID in process and now -- there
			// is no namespace left to charge, and no link left to write.
			return nil, nil
		}
		return nil, fmt.Errorf("read namespace storage quota: %w", err)
	}
	limit := store.EffectiveQuota(q.QuotaBytes, s.quotaDefault)
	if limit == nil {
		return refs, nil
	}
	allowed := make([]store.LFSObjectRef, 0, len(refs))
	used := q.UsedBytes
	for _, ref := range refs {
		linked, err := s.store.RepoHasLFSObject(ctx, repoID, ref.OID)
		if err != nil {
			return nil, fmt.Errorf("check lfs object ownership: %w", err)
		}
		if linked {
			allowed = append(allowed, ref)
			continue
		}
		if want := addSaturating(used, ref.Size); want <= *limit {
			used = want
			allowed = append(allowed, ref)
			continue
		}
		// Dropped, not failed: the push itself already landed, and the file
		// stays listed with an oid nothing backs -- the same unresolvable
		// state a never-uploaded pointer leaves. Failing the job would retry
		// a decision that comes out the same way every time.
		slog.Warn("syncer: lfs object refused by namespace quota; leaving it unlinked",
			"repo_id", repoID, "namespace", q.Namespace, "oid", ref.OID,
			"used_bytes", used, "quota_bytes", *limit)
	}
	return allowed, nil
}

// addSaturating adds two non-negative byte counts without wrapping, mirroring
// the LFS handler's own guard: sizes originate in pointer text a writer
// controls, and a sum that wraps negative compares below every quota.
func addSaturating(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
