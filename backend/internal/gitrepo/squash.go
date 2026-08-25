// Squashing a branch's history: replacing everything reachable from a branch
// with one commit that has the same tree and no parent at all.
//
// This is what `HfApi.super_squash_history()` asks for, and the operation the
// Hub offers because a repository of multi-gigabyte checkpoints accumulates
// history that costs storage without ever being read: every superseded
// revision keeps its blobs alive. Squashing is deliberately not a merge and
// not a rebase -- the old commits become unreachable, which is the entire
// point, and huggingface_hub documents it as non-revertible.

package gitrepo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// SquashBranch replaces branch's history with a single parentless commit
// carrying the branch's current tree, and returns the new head together with
// the one it replaced.
//
// A branch that is not there is ErrRefNotFound, which the API maps to the
// RevisionNotFoundError `super_squash_history` documents. Only branches are
// squashable: a tag names a point in a history rather than the head of one,
// and huggingface_hub says so too ("You cannot squash history on tags").
//
// When the head already has no parents there is nothing to squash, and the
// existing head is returned as both values. That makes a repeated call a
// no-op instead of a fresh commit with the same tree and a new timestamp:
// rewriting the head would change the sha every caller and every clone has
// just been told about, in exchange for nothing.
//
// The tree is reused by hash, so no blob is read, rewritten or re-uploaded --
// squashing a terabyte of checkpoints costs one commit object. The old
// commits are left where they are: they are simply no longer reachable, and
// reclaiming their objects is gc's job, not this function's.
func (r *Repo) SquashBranch(branch, message string, author Signature) (newHash, oldHash plumbing.Hash, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	refName := plumbing.NewBranchReferenceName(branch)
	// The name arrives from a URL and becomes a path under refs/, so it has to
	// survive git's own rules before anything is read or written with it.
	if err := refName.Validate(); err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("%w: %q", ErrInvalidRefName, branch)
	}

	old, exists, err := r.refExists(refName)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("read ref %s: %w", refName, err)
	}
	if !exists {
		return plumbing.ZeroHash, plumbing.ZeroHash, ErrRefNotFound
	}

	head, err := object.GetCommit(r.storer(), old)
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			// The ref points at something that is not a commit, or at an
			// object this copy does not hold. Either way there is no history
			// here to squash.
			return plumbing.ZeroHash, old, ErrRefNotFound
		}
		return plumbing.ZeroHash, old, fmt.Errorf("load branch head %s: %w", branch, err)
	}
	if len(head.ParentHashes) == 0 {
		return old, old, nil
	}

	when := author.When
	if when.IsZero() {
		when = time.Now()
	}
	sig := object.Signature{Name: author.Name, Email: author.Email, When: when}
	if sig.Name == "" {
		sig.Name = "thinkingface"
	}
	if sig.Email == "" {
		sig.Email = "noreply@thinkingface.local"
	}
	msg := message
	if msg == "" {
		msg = "Super-squash branch '" + branch + "'"
	}
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}

	// TreeHash rather than a rebuilt tree: the squashed commit must contain
	// exactly what the branch contains right now, and the cheapest way to
	// guarantee that is to point at the very same tree object.
	squashed := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      msg,
		TreeHash:     head.TreeHash,
		ParentHashes: nil,
	}
	obj := r.repo.Storer.NewEncodedObject()
	if err := squashed.Encode(obj); err != nil {
		return plumbing.ZeroHash, old, fmt.Errorf("encode squashed commit: %w", err)
	}
	newHash, err = r.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, old, fmt.Errorf("store squashed commit: %w", err)
	}
	// The object is written before the ref moves, the order every write in
	// this package uses (docs/dev/continuity-design.md §9): an object no ref
	// names is garbage a later gc collects, whereas a ref naming an absent
	// object is a broken repository.
	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(refName, newHash)); err != nil {
		return plumbing.ZeroHash, old, fmt.Errorf("update %s: %w", refName, err)
	}
	return newHash, old, nil
}
