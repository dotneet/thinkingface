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
	// OpCopy puts an existing blob at a second path. It is what
	// huggingface_hub's CommitOperationCopy asks for, and it is a hash copy
	// rather than a byte copy on purpose: the source is already an object in
	// this repository, so nothing has to be read, buffered or re-hashed, and
	// the cost is the same for a 2KB README and a 40GB LFS pointer's target.
	OpCopy
)

type Op struct {
	Kind OpKind
	Path string
	// Data is the literal file content for OpAdd. For an LFS file the caller
	// passes the pointer bytes (see FormatLFSPointer).
	Data []byte
	// SrcHash is the blob OpCopy points the new path at. It must already be
	// an object in this repository -- Commit refuses a hash it cannot find
	// rather than writing a tree entry with nothing behind it.
	SrcHash    plumbing.Hash
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
	// ParentCommit is an optimistic lock on the branch as a whole: when it is
	// set, the commit only applies if the branch's head is that commit. It is
	// huggingface_hub's `create_commit(parent_commit=...)`, whose whole point
	// is that a repository someone else moved must not be written to.
	//
	// The value is a lowercase hex prefix of at least 7 characters -- the
	// shorthand form huggingface_hub documents -- matched against the head
	// this commit would build on. An unborn branch matches nothing at all,
	// including a prefix of zeroes.
	//
	// Checked here rather than by the caller for the same reason
	// Preconditions are: the parent is selected under this mutex, so a check
	// made before the call is already stale by the time the tree is built.
	ParentCommit string
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

// StaleParentError reports a ParentCommit that is not the branch's head. It is
// deliberately its own type rather than a flavour of StalePathError: the
// caller's answer for it is "your view of the branch is out of date, fetch and
// decide again", which is not the same thing as the retryable contention every
// other write conflict here reports.
type StaleParentError struct {
	Branch   string
	Expected string
	Actual   string // "" when the branch has no commits yet
}

func (e *StaleParentError) Error() string {
	actual := e.Actual
	if actual == "" {
		actual = "<no commits>"
	}
	return fmt.Sprintf("commit: %s is at %s, not at the expected parent %s", e.Branch, actual, e.Expected)
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

	// The branch-level lock is checked before the per-path ones, and against
	// the same parent: a caller that named a parent commit is asserting the
	// branch has not moved at all, so nothing else about the request matters
	// once that is false.
	if req.ParentCommit != "" {
		// An unborn branch never matches: oldHash.String() is forty zeroes
		// there, which a prefix of zeroes would otherwise satisfy.
		if oldHash.IsZero() || !strings.HasPrefix(oldHash.String(), strings.ToLower(req.ParentCommit)) {
			actual := ""
			if !oldHash.IsZero() {
				actual = oldHash.String()
			}
			return plumbing.ZeroHash, oldHash, &StaleParentError{
				Branch: req.Branch, Expected: req.ParentCommit, Actual: actual,
			}
		}
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
		case OpAdd, OpCopy:
			blobHash, bErr := r.blobFor(op)
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

// ValidatePath is the exported form of the path check Commit applies to every
// op. Handlers use it to refuse a traversal ("../"), a .git component or a NUL
// byte with a 400 *before* any bytes are stored, rather than letting Commit
// fail at the end of a multi-gigabyte upload.
func ValidatePath(p string) error { return validatePath(p) }

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

// blobFor resolves the blob an add-shaped op puts at its path: freshly written
// from the request's bytes for OpAdd, an object already here for OpCopy.
//
// The existence check on the copy source is not a formality. A tree entry
// naming an object the repository does not hold is a *corrupt* commit rather
// than a failed one -- every later read of that path, and every clone, breaks
// on it -- and the caller resolved that hash before this ran, with the WAL's
// stale-ref retry free to rebuild the directory in between.
func (r *Repo) blobFor(op Op) (plumbing.Hash, error) {
	if op.Kind != OpCopy {
		return r.writeBlob(op.Data)
	}
	if op.SrcHash.IsZero() {
		return plumbing.ZeroHash, fmt.Errorf("commit: copy to %q names no source blob", op.Path)
	}
	if _, err := r.storer().EncodedObject(plumbing.BlobObject, op.SrcHash); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("commit: copy to %q from blob %s: %w", op.Path, op.SrcHash, err)
	}
	return op.SrcHash, nil
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
