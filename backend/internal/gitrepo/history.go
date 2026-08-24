// History reads: resolving which commit last touched each entry of a
// directory, and paging through a revision's log. Both walk first-parent
// history only, so a change that arrived through a merge is attributed to the
// merge commit itself rather than the side branch.

package gitrepo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// CommitMeta is the one-line summary of a commit that the UI shows.
type CommitMeta struct {
	Hash plumbing.Hash
	// Message is the subject line only.
	Message string
	// Body is everything after the subject line, trimmed; empty for a
	// single-line commit message. The UI never shows it, but
	// huggingface_hub's GitCommitInfo splits a commit into `title` and
	// `message` exactly this way, so the HF-compatible commits endpoint needs
	// both halves.
	Body   string
	Author string
	When   time.Time
}

func metaOf(c *object.Commit) CommitMeta {
	subject, body, _ := strings.Cut(c.Message, "\n")
	return CommitMeta{
		Hash:    c.Hash,
		Message: strings.TrimSpace(subject),
		Body:    strings.TrimSpace(body),
		Author:  c.Author.Name,
		When:    c.Author.When,
	}
}

// LastCommitsMaxWalk bounds how many commits LastCommits inspects. Entries
// whose last change is older than the cap come back unattributed, which the
// API surfaces as a null last_commit.
const LastCommitsMaxWalk = 1000

// LastCommits resolves, for each direct child of dir at rev, the most recent
// first-parent commit that changed it. The second result is the commit rev
// itself resolves to. An empty repository yields (nil, nil, nil).
func (r *Repo) LastCommits(rev, dir string) (map[string]CommitMeta, *CommitMeta, error) {
	h, err := r.Resolve(rev)
	if err != nil {
		if errors.Is(err, ErrEmptyRepo) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	head, err := object.GetCommit(r.storer(), h)
	if err != nil {
		return nil, nil, fmt.Errorf("load commit %s: %w", h, err)
	}
	headMeta := metaOf(head)

	// current holds each entry's object hash at rev. An entry stays in
	// unassigned exactly as long as every newer commit we passed left it at
	// that hash, so comparing a parent against current is comparing against
	// the entry's state in the child commit.
	current, err := r.dirEntryHashes(head, strings.Trim(dir, "/"))
	if err != nil {
		return nil, &headMeta, err
	}
	unassigned := make(map[string]struct{}, len(current))
	for name := range current {
		unassigned[name] = struct{}{}
	}

	out := make(map[string]CommitMeta, len(current))
	c := head
	for steps := 0; len(unassigned) > 0 && steps < LastCommitsMaxWalk; steps++ {
		if c.NumParents() == 0 {
			// The root commit introduced whatever nothing newer changed.
			meta := metaOf(c)
			for name := range unassigned {
				out[name] = meta
			}
			break
		}
		parent, err := c.Parent(0)
		if err != nil {
			// A shallow or otherwise truncated history: report what was
			// resolved so far instead of failing the whole listing.
			return out, &headMeta, nil
		}
		parentEntries, err := r.dirEntryHashes(parent, strings.Trim(dir, "/"))
		if err != nil {
			return out, &headMeta, err
		}
		var meta *CommitMeta
		for name := range unassigned {
			if parentEntries[name] != current[name] {
				if meta == nil {
					m := metaOf(c)
					meta = &m
				}
				out[name] = *meta
				delete(unassigned, name)
			}
		}
		c = parent
	}
	return out, &headMeta, nil
}

// dirEntryHashes maps each direct child of dir in the commit's tree to its
// object hash (a mode-only change is deliberately ignored). A commit that
// predates dir yields an empty map.
func (r *Repo) dirEntryHashes(c *object.Commit, dir string) (map[string]plumbing.Hash, error) {
	t, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree of %s: %w", c.Hash, err)
	}
	if dir != "" {
		sub, err := t.Tree(dir)
		if err != nil {
			return map[string]plumbing.Hash{}, nil
		}
		t = sub
	}
	out := make(map[string]plumbing.Hash, len(t.Entries))
	for _, te := range t.Entries {
		out[te.Name] = te.Hash
	}
	return out, nil
}

// ListCommitsMaxScan bounds how many commits one ListCommits call inspects.
// A path filter can skip most of what it scans, so a page may come back short
// (or empty) with a cursor that resumes the scan where it stopped.
const ListCommitsMaxScan = 1000

// ListCommits pages through rev's first-parent history, newest first. A
// non-empty path restricts the page to commits that changed that file or
// directory. A non-zero after starts at the first parent of that commit
// instead of rev. next is the hash to pass as after for the following page;
// it is zero when the scan reached the root commit.
func (r *Repo) ListCommits(rev, path string, after plumbing.Hash, limit int) ([]CommitMeta, plumbing.Hash, error) {
	var start *object.Commit
	if after.IsZero() {
		h, err := r.Resolve(rev)
		if err != nil {
			if errors.Is(err, ErrEmptyRepo) {
				return []CommitMeta{}, plumbing.ZeroHash, nil
			}
			return nil, plumbing.ZeroHash, err
		}
		start, err = object.GetCommit(r.storer(), h)
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("load commit %s: %w", h, err)
		}
	} else {
		prev, err := object.GetCommit(r.storer(), after)
		if err != nil {
			return nil, plumbing.ZeroHash, fmt.Errorf("%w: cursor %s", ErrPathNotFound, after)
		}
		if prev.NumParents() == 0 {
			return []CommitMeta{}, plumbing.ZeroHash, nil
		}
		start, err = prev.Parent(0)
		if err != nil {
			return []CommitMeta{}, plumbing.ZeroHash, nil
		}
	}

	path = strings.Trim(path, "/")
	commits := make([]CommitMeta, 0, limit)
	c := start
	for scanned := 1; ; scanned++ {
		var parent *object.Commit
		if c.NumParents() > 0 {
			p, err := c.Parent(0)
			if err != nil {
				// Truncated history: end pagination here.
				return commits, plumbing.ZeroHash, nil
			}
			parent = p
		}
		changed := true
		if path != "" {
			// A deletion shows up as hash-present vs hash-zero, so it is a
			// change like any other; absent on both sides is not.
			ph := plumbing.ZeroHash
			if parent != nil {
				ph = r.entryHashAt(parent, path)
			}
			changed = r.entryHashAt(c, path) != ph
		}
		if changed {
			commits = append(commits, metaOf(c))
		}
		if parent == nil {
			return commits, plumbing.ZeroHash, nil
		}
		if len(commits) >= limit || scanned >= ListCommitsMaxScan {
			return commits, c.Hash, nil
		}
		c = parent
	}
}

// entryHashAt is the object hash of path in the commit's tree, or zero when
// the path (or the tree itself) is unreadable there.
func (r *Repo) entryHashAt(c *object.Commit, path string) plumbing.Hash {
	t, err := c.Tree()
	if err != nil {
		return plumbing.ZeroHash
	}
	te, err := t.FindEntry(path)
	if err != nil {
		return plumbing.ZeroHash
	}
	return te.Hash
}
