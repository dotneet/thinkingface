// Write paths: applying a batch of file operations on top of a branch and
// advancing the ref, without ever checking out a working tree.

package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type OpKind int

const (
	OpAdd OpKind = iota
	OpDelete
	OpDeleteDir
)

type Op struct {
	Kind OpKind
	Path string
	// Data is the literal file content for OpAdd. For an LFS file the caller
	// passes the pointer bytes (see FormatLFSPointer).
	Data       []byte
	Executable bool
}

type Signature struct {
	Name  string
	Email string
	When  time.Time
}

type CommitRequest struct {
	Branch    string
	Message   string
	Author    Signature
	Ops       []Op
	AllowNoop bool
	// Preconditions are optimistic locks checked against the parent commit
	// under the same mutex that selects it. Validating a path's state before
	// calling Commit is not enough: between that check and the commit,
	// another writer can become the parent, and the caller would silently
	// overwrite a change its stale check never saw.
	Preconditions []PathPrecondition
}

// PathPrecondition asserts what a path must contain in the commit's parent.
// OID is the blob hash the caller last observed; empty asserts the path does
// not exist yet.
type PathPrecondition struct {
	Path string
	OID  string
}

// StalePathError reports a precondition that no longer holds; callers map it
// to an optimistic-concurrency conflict (HTTP 409).
type StalePathError struct {
	Path     string
	Expected string // "" means "expected absent"
	Actual   string // "" means "actually absent"
}

func (e *StalePathError) Error() string {
	expected, actual := e.Expected, e.Actual
	if expected == "" {
		expected = "<absent>"
	}
	if actual == "" {
		actual = "<absent>"
	}
	return fmt.Sprintf("commit: stale precondition on %s: expected %s, found %s", e.Path, expected, actual)
}

// Commit applies ops on top of Branch and advances the ref. It returns the new
// commit hash, plus the previous head (zero when the branch was unborn).
func (r *Repo) Commit(req CommitRequest) (newHash, oldHash plumbing.Hash, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if req.Branch == "" {
		return plumbing.ZeroHash, plumbing.ZeroHash, errors.New("commit: branch is required")
	}
	refName := plumbing.NewBranchReferenceName(req.Branch)
	// The branch name arrives from a URL and becomes a path under refs/, so it
	// has to survive git's own check-ref-format rules before anything is
	// written: ".." or a control character would otherwise name a file outside
	// refs/heads.
	if err := refName.Validate(); err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("commit: invalid branch name %q", req.Branch)
	}

	var parents []plumbing.Hash
	var parentCommit *object.Commit
	root := newDirNode()
	if ref, refErr := r.repo.Reference(refName, true); refErr == nil {
		oldHash = ref.Hash()
		parent, cErr := object.GetCommit(r.storer(), oldHash)
		if cErr != nil {
			return plumbing.ZeroHash, oldHash, fmt.Errorf("load branch head: %w", cErr)
		}
		parents = []plumbing.Hash{oldHash}
		parentCommit = parent
		root = &dirNode{hash: parent.TreeHash}
	}

	// Preconditions are evaluated against the parent this very commit will
	// build on — the only reading that makes the optimistic lock sound.
	for _, pc := range req.Preconditions {
		actual := ""
		if parentCommit != nil {
			if h := r.entryHashAt(parentCommit, strings.Trim(pc.Path, "/")); !h.IsZero() {
				actual = h.String()
			}
		}
		if actual != pc.OID {
			return plumbing.ZeroHash, oldHash, &StalePathError{Path: pc.Path, Expected: pc.OID, Actual: actual}
		}
	}

	for _, op := range req.Ops {
		p := strings.Trim(op.Path, "/")
		if p == "" {
			return plumbing.ZeroHash, oldHash, fmt.Errorf("commit: invalid path %q", op.Path)
		}
		if err := validatePath(p); err != nil {
			return plumbing.ZeroHash, oldHash, err
		}
		switch op.Kind {
		case OpAdd:
			blobHash, bErr := r.writeBlob(op.Data)
			if bErr != nil {
				return plumbing.ZeroHash, oldHash, bErr
			}
			mode := filemode.Regular
			if op.Executable {
				mode = filemode.Executable
			}
			if err := root.setBlob(r.storer(), p, object.TreeEntry{
				Name: path.Base(p), Mode: mode, Hash: blobHash,
			}); err != nil {
				return plumbing.ZeroHash, oldHash, err
			}
		case OpDelete:
			if err := root.deleteBlob(r.storer(), p); err != nil {
				return plumbing.ZeroHash, oldHash, err
			}
		case OpDeleteDir:
			if err := root.deleteDir(r.storer(), p); err != nil {
				return plumbing.ZeroHash, oldHash, err
			}
		}
	}

	treeHash, err := root.write(r.storer())
	if err != nil {
		return plumbing.ZeroHash, oldHash, err
	}
	if treeHash.IsZero() {
		// Every file was deleted. dirNode.write reports an empty directory as
		// the zero hash so parents can drop the entry, but a commit's root must
		// point at git's real empty-tree object or the commit is unreadable.
		treeHash, err = r.writeEmptyTree()
		if err != nil {
			return plumbing.ZeroHash, oldHash, err
		}
	}

	if len(parents) == 1 && !req.AllowNoop {
		if parent, pErr := object.GetCommit(r.storer(), parents[0]); pErr == nil && parent.TreeHash == treeHash {
			// Nothing changed; report the existing head rather than piling up
			// empty commits from repeated uploads.
			return parents[0], oldHash, nil
		}
	}

	when := req.Author.When
	if when.IsZero() {
		when = time.Now()
	}
	sig := object.Signature{Name: req.Author.Name, Email: req.Author.Email, When: when}
	if sig.Name == "" {
		sig.Name = "thinkingface"
	}
	if sig.Email == "" {
		sig.Email = "noreply@thinkingface.local"
	}
	msg := req.Message
	if msg == "" {
		msg = "Update repository"
	}
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}

	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      msg,
		TreeHash:     treeHash,
		ParentHashes: parents,
	}
	obj := r.repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, oldHash, fmt.Errorf("encode commit: %w", err)
	}
	newHash, err = r.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, oldHash, fmt.Errorf("store commit: %w", err)
	}

	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(refName, newHash)); err != nil {
		return plumbing.ZeroHash, oldHash, fmt.Errorf("update %s: %w", refName, err)
	}
	// Point HEAD at the branch when the repository was empty, so clones land
	// on something.
	if head, hErr := r.repo.Reference(plumbing.HEAD, false); hErr != nil || head.Target() == "" {
		_ = r.repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, refName))
	}
	return newHash, oldHash, nil
}

func validatePath(p string) error {
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("commit: path contains a NUL byte")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("commit: invalid path segment in %q", p)
		}
		// git refuses a .git component anywhere in a tree, not just at the
		// root, and matches it case-insensitively because a checkout onto a
		// case-insensitive filesystem would otherwise land in the real .git
		// directory.
		if strings.EqualFold(seg, ".git") {
			return fmt.Errorf("commit: refusing to write inside .git (%q)", p)
		}
	}
	return nil
}

// writeEmptyTree stores git's canonical empty tree and returns its hash
// (4b825dc6… for SHA-1 repositories).
func (r *Repo) writeEmptyTree() (plumbing.Hash, error) {
	obj := r.repo.Storer.NewEncodedObject()
	if err := (&object.Tree{}).Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode empty tree: %w", err)
	}
	return r.repo.Storer.SetEncodedObject(obj)
}

func (r *Repo) writeBlob(data []byte) (plumbing.Hash, error) {
	obj := r.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(data)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("open blob writer: %w", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("write blob: %w", err)
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("close blob: %w", err)
	}
	return r.repo.Storer.SetEncodedObject(obj)
}
