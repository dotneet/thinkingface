// Package gitrepo owns the bare repositories on disk: creating them, reading
// trees and blobs, and building commits server-side for the upload API. The
// smart-HTTP transport (internal/gitserver) execs the git binary against the
// same directories.
package gitrepo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
)

var (
	ErrRepoNotFound = errors.New("gitrepo: repository not found")
	ErrPathNotFound = errors.New("gitrepo: path not found in tree")
	ErrEmptyRepo    = errors.New("gitrepo: repository has no commits")
)

// Manager resolves repository identities to directories under a single root
// and serialises writes per repository.
type Manager struct {
	root string

	// One mutex per repository directory, and it is never removed again.
	//
	// That is deliberate, and the obvious tidy-up is a correctness bug rather
	// than a saving. Open hands the mutex to the *Repo it returns, which the
	// caller then holds for as long as it is working -- a whole sync job, a
	// whole request -- and takes and releases many times inside that span
	// (commit.go, refs.go). Dropping the map entry when the last acquirer let
	// go would let a later Open of the same directory mint a *different*
	// mutex while an older *Repo still points at the first, so two writers to
	// one bare repository would serialise against different locks and neither
	// would notice. Reference-counting the entry cannot fix that either: the
	// thing that needs counting is the *Repo handle, whose lifetime this type
	// does not see.
	//
	// The cost of keeping them is one map entry -- a path and a pointer,
	// something like a hundred bytes -- per repository this process has ever
	// opened, which is small beside the materialised copy of any one of them.
	// Note it is not bounded by the WAL cache: eviction removes the working
	// directory and leaves the entry, so a long-lived instance accumulates one
	// per repository it has touched rather than one per repository it holds.
	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// wal, when non-nil, makes Open materialise from the WAL first and
	// bounds the local cache. See wal.go in this package.
	wal *walBackend
}

func NewManager(root string) *Manager {
	return &Manager{root: root, locks: map[string]*sync.Mutex{}}
}

// Dir returns the on-disk path of a repository's bare directory. It is
// keyed by the repository's immutable storage path (store.Repo.StoragePath),
// never by its name, so a transfer or rename never moves the directory
// (docs/dev/repo-transfer-design.md §3).
//
//	{root}/{storage_path}.git    e.g. {root}/repos/01J….git or (legacy) {root}/datasets/{ns}/{name}.git
func (m *Manager) Dir(storagePath string) string {
	return filepath.Join(m.root, filepath.FromSlash(strings.Trim(storagePath, "/"))+".git")
}

func (m *Manager) lockFor(dir string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[dir]
	if !ok {
		l = &sync.Mutex{}
		m.locks[dir] = l
	}
	return l
}

func (m *Manager) Exists(storagePath string) bool {
	st, err := os.Stat(m.Dir(storagePath))
	return err == nil && st.IsDir()
}

// Init creates the bare repository. It shells out to git rather than using
// go-git so the layout is exactly what upload-pack/receive-pack expect, and
// through gitexec so it is the same git -- same config, same template -- that
// wal.Materialize would rebuild the repository with.
//
// The background context matches Open below: creating a repository is a
// sub-second local operation, and abandoning it half-done because the client
// hung up would leave exactly the state createRepo rolls back for.
func (m *Manager) Init(storagePath, defaultBranch string) error {
	dir := m.Dir(storagePath)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	return gitexec.InitBare(context.Background(), dir, defaultBranch)
}

func (m *Manager) Remove(storagePath string) error {
	return os.RemoveAll(m.Dir(storagePath))
}

func (m *Manager) Open(storagePath string) (*Repo, error) {
	// When the WAL is authoritative every open catches the local copy up
	// first (§8: one index GET per request; a warm copy is a no-op). The
	// background context is deliberate — Open predates ctx plumbing and a
	// materialisation should finish once started (see materializeTimeout).
	if m.wal != nil {
		if err := m.EnsureLocal(context.Background(), storagePath); err != nil {
			return nil, err
		}
	}
	dir := m.Dir(storagePath)
	r, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, ErrRepoNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &Repo{repo: r, dir: dir, mu: m.lockFor(dir)}, nil
}

// Repo is a handle on one bare repository.
type Repo struct {
	repo *git.Repository
	dir  string
	mu   *sync.Mutex
}

func (r *Repo) Dir() string { return r.dir }

func (r *Repo) storer() storer.EncodedObjectStorer { return r.repo.Storer }

// minAbbrevLen is the shortest hex string Resolve will treat as an
// abbreviated commit id. Git's own default abbreviation is 7, which is what
// every short SHA a person copies out of a log looks like; anything shorter is
// either a ref name or nothing. Below it a prefix is not an identifier at all:
// "c" matches a sixteenth of the object store, and looking it up means walking
// every pack index for an unauthenticated GET.
const minAbbrevLen = 7

// maxTagChain bounds how many annotated tags Resolve peels through. Real
// repositories have one (a tag of a commit); a cycle is impossible in a
// content-addressed store, but a hostile push can still build a long chain.
const maxTagChain = 16

// Resolve turns a branch name, tag name, or commit SHA into a commit hash.
// An empty rev means the repository's HEAD.
//
// The lookup order is explicit rather than go-git's ResolveRevision, whose
// order is wrong for a hub in two ways. It tries rev as a hash prefix before
// any ref -- at any length, so a branch called "1", "c", "cafe" or "2024"
// resolved to whatever unrelated commit happened to start with those
// characters, and the syncer indexed that commit's tree under the branch's
// name. And it expands refs/tags/X before refs/heads/X. Here the order is:
//
//  1. HEAD
//  2. rev as a full ref name, when it starts with "refs/"
//  3. refs/heads/<rev> -- a branch beats a tag of the same name, which is
//     what huggingface_hub users expect, since every write targets a branch
//     (see HasBranch)
//  4. refs/tags/<rev>
//  5. a full 40-hex commit id
//  6. an abbreviated commit id of at least minAbbrevLen hex digits, only when
//     exactly one commit (or tag of one) has that prefix
//
// Annotated tags are peeled to the commit they name, and anything that is not
// a commit at the end of that (a tree or blob id, a tag of a tree) does not
// resolve. Revision expressions ("main~1", "v1^{}") are not accepted: no
// client of this server sends them, and parsing them is how a short name got
// read as something else in the first place.
//
// Every "nothing by that name" answer is ErrEmptyRepo, as it always has been:
// for an unborn HEAD that is literally true, and for any other rev callers
// ask IsEmpty to tell the two apart.
func (r *Repo) Resolve(rev string) (plumbing.Hash, error) {
	if rev == "" {
		rev = "HEAD"
	}
	h, err := r.resolveName(rev)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return r.peelToCommit(rev, h)
}

// resolveName is steps 1-6 of Resolve, before any peeling.
func (r *Repo) resolveName(rev string) (plumbing.Hash, error) {
	if rev == "HEAD" {
		ref, err := r.repo.Reference(plumbing.HEAD, true)
		switch {
		case err == nil:
			return ref.Hash(), nil
		case errors.Is(err, plumbing.ErrReferenceNotFound):
			// A fresh repository has a HEAD pointing at an unborn branch.
			return plumbing.ZeroHash, ErrEmptyRepo
		default:
			return plumbing.ZeroHash, fmt.Errorf("resolve HEAD: %w", err)
		}
	}

	candidates := make([]plumbing.ReferenceName, 0, 3)
	if strings.HasPrefix(rev, "refs/") {
		candidates = append(candidates, plumbing.ReferenceName(rev))
	}
	candidates = append(candidates, plumbing.NewBranchReferenceName(rev), plumbing.NewTagReferenceName(rev))
	for _, name := range candidates {
		// Validated before the storer sees it: the filesystem storer turns a
		// ref name into a path under the repository, so a rev like
		// "../../x" must never reach it.
		if name.Validate() != nil {
			continue
		}
		ref, err := r.repo.Reference(name, true)
		if err == nil {
			return ref.Hash(), nil
		}
		if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", rev, err)
		}
	}

	if len(rev) < minAbbrevLen || len(rev) > 2*len(plumbing.ZeroHash) || !isHex(rev) {
		return plumbing.ZeroHash, ErrEmptyRepo
	}
	rev = strings.ToLower(rev)
	if len(rev) == 2*len(plumbing.ZeroHash) {
		return plumbing.NewHash(rev), nil
	}
	return r.expandAbbrev(rev)
}

// expandAbbrev resolves an abbreviated commit id. Only commit-ish objects
// count, the way `git rev-parse <prefix>^{commit}` disambiguates: a tree or
// blob sharing the prefix is not what anyone meant by a revision. More than
// one match is refused rather than guessed at.
func (r *Repo) expandAbbrev(prefix string) (plumbing.Hash, error) {
	type prefixLister interface {
		HashesWithPrefix(prefix []byte) ([]plumbing.Hash, error)
	}
	lister, ok := r.repo.Storer.(prefixLister)
	if !ok {
		// Every repository here is on the filesystem storer, which has the
		// indexed lookup. Without it the only option is a scan of every
		// object, which is not worth offering for a convenience.
		return plumbing.ZeroHash, ErrEmptyRepo
	}
	even, err := hex.DecodeString(prefix[:len(prefix)&^1])
	if err != nil {
		return plumbing.ZeroHash, ErrEmptyRepo
	}
	hashes, err := lister.HashesWithPrefix(even)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", prefix, err)
	}
	var match plumbing.Hash
	found := 0
	for _, h := range hashes {
		if !strings.HasPrefix(h.String(), prefix) {
			continue // the odd trailing nybble
		}
		if _, err := r.peelToCommit(prefix, h); err != nil {
			continue
		}
		if found > 0 && h == match {
			continue // one object listed from two packs
		}
		match = h
		found++
	}
	switch found {
	case 0:
		return plumbing.ZeroHash, ErrEmptyRepo
	case 1:
		return match, nil
	default:
		return plumbing.ZeroHash, fmt.Errorf("%w: abbreviated commit id %q is ambiguous", ErrEmptyRepo, prefix)
	}
}

// peelToCommit follows annotated tags from h down to the commit they name.
// Anything that ends somewhere other than a commit -- including an object
// that is not in the repository at all -- does not resolve.
func (r *Repo) peelToCommit(rev string, h plumbing.Hash) (plumbing.Hash, error) {
	for range maxTagChain {
		obj, err := r.repo.Storer.EncodedObject(plumbing.AnyObject, h)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return plumbing.ZeroHash, ErrEmptyRepo
		}
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", rev, err)
		}
		switch obj.Type() {
		case plumbing.CommitObject:
			return h, nil
		case plumbing.TagObject:
			tag, err := object.DecodeTag(r.repo.Storer, obj)
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("resolve %q: decode tag %s: %w", rev, h, err)
			}
			h = tag.Target
		default:
			return plumbing.ZeroHash, fmt.Errorf("%w: %q names a %s, not a commit", ErrEmptyRepo, rev, obj.Type())
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("resolve %q: more than %d nested tags", rev, maxTagChain)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// IsEmpty reports whether the repository has no branches and no tags -- no
// commit anything could name. It deliberately does not look at HEAD: a
// repository whose first push went to a branch other than the default one
// ("git push origin master" when HEAD is main, an upload with
// revision="dev") has an unborn HEAD and real history, and reading that as
// empty is what made an unknown revision there answer 200 with nothing in it.
func (r *Repo) IsEmpty() bool {
	iter, err := r.repo.Storer.IterReferences()
	if err != nil {
		// Unreadable refs are not evidence of emptiness; answering "not
		// empty" keeps callers on their revision-not-found path.
		return false
	}
	defer iter.Close()
	empty := true
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if name := ref.Name(); name.IsBranch() || name.IsTag() {
			empty = false
			return storer.ErrStop
		}
		return nil
	})
	return empty
}

func (r *Repo) HeadSHA() string {
	h, err := r.Resolve("HEAD")
	if err != nil {
		return ""
	}
	return h.String()
}

// CommitObject loads a commit by hash.
func (r *Repo) CommitObject(hash plumbing.Hash) (*object.Commit, error) {
	return object.GetCommit(r.storer(), hash)
}

func (r *Repo) refNames(prefix string) ([]string, error) {
	iter, err := r.repo.References()
	if err != nil {
		return nil, err
	}
	var out []string
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference && strings.HasPrefix(ref.Name().String(), prefix) {
			out = append(out, strings.TrimPrefix(ref.Name().String(), prefix))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// ResetBranch moves refs/heads/branch back to target, but only if it still
// points at expect; a zero target deletes the ref (an aborted first commit on
// an unborn branch). It exists for the WAL write path: Commit advances the
// local ref before the WAL CAS runs, and if that CAS fails the local ref must
// be rolled back -- otherwise this instance serves a commit the WAL never
// accepted, and every later commit attempt sees a head the index disagrees
// with and is rejected as stale.
//
// expect is the commit the caller itself created, and the compare is the
// whole point. Commit releases r.mu before the WAL CAS, so two local commits
// on one branch can interleave: A advances main X->A, B advances A->B, A's
// CAS fails. An unconditional reset would put main back to X underneath B;
// then B's CAS (Old=A) fails as stale and resets main to A -- a commit the WAL
// never accepted, which wedges the branch. With the compare, A's rollback sees
// main at B, not A, and leaves it: whoever moved the ref owns its rollback
// (or its WAL entry). That case is reported as success, since there is
// nothing left for this caller to undo.
func (r *Repo) ResetBranch(branch string, expect, target plumbing.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := plumbing.NewBranchReferenceName(branch)
	if err := name.Validate(); err != nil {
		return fmt.Errorf("reset branch: invalid name %q", branch)
	}
	current, exists, err := r.refExists(name)
	if err != nil {
		return fmt.Errorf("reset branch %s: %w", branch, err)
	}
	if !exists || current != expect {
		return nil
	}
	if target.IsZero() {
		return r.repo.Storer.RemoveReference(name)
	}
	// CheckAndSet rather than Set: r.mu only serialises this process's
	// writers, while `git receive-pack` updates the same ref file directly.
	err = r.repo.Storer.CheckAndSetReference(
		plumbing.NewHashReference(name, target), plumbing.NewHashReference(name, expect))
	if errors.Is(err, storage.ErrReferenceHasChanged) {
		return nil
	}
	return err
}

func (r *Repo) Branches() ([]string, error) { return r.refNames("refs/heads/") }
func (r *Repo) Tags() ([]string, error)     { return r.refNames("refs/tags/") }

// RefTarget returns the object a branch or tag ref names.
//
// It deliberately does NOT peel an annotated tag: a tag ref names a tag
// object, exactly as git does, and the ref-write paths need that raw value --
// it is what the WAL records as the ref's old/new value and what a delete
// reports. Anything that wants a commit (the /refs listings' targetCommit,
// every revision lookup) goes through Resolve, the peeling counterpart.
func (r *Repo) RefTarget(refName string) (plumbing.Hash, error) {
	ref, err := r.repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// SetHead repoints the bare repository's HEAD symref at branch, which is
// what a `git clone` of this repository checks out by default. It is the
// on-disk half of changing the repository's default branch; the caller is
// expected to have already confirmed branch exists (RefTarget) and to update
// store.Repo.DefaultBranch in the same request. Through gitexec, like every
// other git invocation in this package -- never exec.Command directly.
func (r *Repo) SetHead(ctx context.Context, branch string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := plumbing.NewBranchReferenceName(branch)
	if err := name.Validate(); err != nil {
		return fmt.Errorf("set head: invalid branch name %q", branch)
	}
	return gitexec.SetHead(ctx, r.dir, branch)
}
