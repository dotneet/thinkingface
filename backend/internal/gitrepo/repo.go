// Package gitrepo owns the bare repositories on disk: creating them, reading
// trees and blobs, and building commits server-side for the upload API. The
// smart-HTTP transport (internal/gitserver) execs the git binary against the
// same directories.
package gitrepo

import (
	"context"
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

// Resolve turns a branch name, tag name, or commit SHA into a commit hash.
// An empty rev means the repository's HEAD.
func (r *Repo) Resolve(rev string) (plumbing.Hash, error) {
	if rev == "" {
		rev = "HEAD"
	}
	h, err := r.repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		// A fresh repository has a HEAD pointing at an unborn branch.
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash, ErrEmptyRepo
		}
		return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", rev, err)
	}
	// Peel annotated tags down to the commit they point at.
	if tag, tagErr := r.repo.TagObject(*h); tagErr == nil {
		return tag.Target, nil
	}
	return *h, nil
}

func (r *Repo) IsEmpty() bool {
	_, err := r.Resolve("HEAD")
	return errors.Is(err, ErrEmptyRepo)
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

// ResetBranch forces refs/heads/branch back to target; a zero target deletes
// the ref (an aborted first commit on an unborn branch). It exists for the
// WAL write path: Commit advances the local ref before the WAL CAS runs, and
// if that CAS fails the local ref must be rolled back — otherwise this
// instance serves a commit the WAL never accepted, and every later commit
// attempt sees a head the index disagrees with and is rejected as stale.
func (r *Repo) ResetBranch(branch string, target plumbing.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := plumbing.NewBranchReferenceName(branch)
	if err := name.Validate(); err != nil {
		return fmt.Errorf("reset branch: invalid name %q", branch)
	}
	if target.IsZero() {
		return r.repo.Storer.RemoveReference(name)
	}
	return r.repo.Storer.SetReference(plumbing.NewHashReference(name, target))
}

func (r *Repo) Branches() ([]string, error) { return r.refNames("refs/heads/") }
func (r *Repo) Tags() ([]string, error)     { return r.refNames("refs/tags/") }

// RefTarget returns the commit a branch or tag points at.
func (r *Repo) RefTarget(refName string) (plumbing.Hash, error) {
	ref, err := r.repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}
