// Ref writes that carry no commit: creating and deleting branches and tags.
//
// The push path never needs these -- receive-pack moves refs itself -- but the
// HuggingFace-compatible branch/tag API does, and it must not reach for the
// `git` binary (that is gitexec's job) nor hand-roll ref files. Everything here
// goes through the same per-repository mutex Commit and ResetBranch use.

package gitrepo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var (
	// ErrRefExists is returned when a ref creation would overwrite an
	// existing ref. Creation never forces: a caller that means to move a ref
	// says so with ResetBranch.
	ErrRefExists = errors.New("gitrepo: reference already exists")
	// ErrRefNotFound is returned when a ref deletion names a ref that is not
	// there.
	ErrRefNotFound = errors.New("gitrepo: reference not found")
	// ErrInvalidRefName is returned by ValidateRefName; callers map it to a
	// 400 rather than a 500. Its text carries no "gitrepo:" prefix on purpose:
	// the API echoes the wrapped message straight into the 400 body, where a
	// package name is noise.
	ErrInvalidRefName = errors.New("is not a valid git reference name")
)

// maxRefNameBytes bounds a branch or tag name. A ref name becomes a path
// component under refs/, so an unbounded one is a filesystem problem as much
// as a display one; git itself stops well short of this.
const maxRefNameBytes = 255

// BranchRef and TagRef build the full ref name for a validated short name.
func BranchRef(short string) string { return "refs/heads/" + short }
func TagRef(short string) string    { return "refs/tags/" + short }

// ValidateRefName checks a *short* branch or tag name -- "main", "v1.0",
// "feature/x" -- against git's check-ref-format rules, as they apply once the
// name is placed under refs/heads/ or refs/tags/.
//
// go-git's ReferenceName.Validate covers the rules that matter for safety: a
// component starting with ".", "..", control characters, the "~^:?*[" set,
// whitespace, "@{", a backslash, and a ".lock" suffix. It is applied to the
// full ref name because several of its rules are positional. What it does not
// cover, and this adds:
//
//   - the empty name, and one that starts or ends with "/" (an empty path
//     component reads as a valid ref to some tools and not to others);
//   - "HEAD", which is a ref of its own and must never also be a branch;
//   - a length ceiling.
//
// The rules are identical for branches and tags, so one function serves both.
//
// The returned error reads as the predicate of a sentence whose subject is the
// name -- `branch "a..b" is not a valid git reference name: ...` -- so an API
// handler can echo it without rewording.
func ValidateRefName(short string) error {
	switch {
	case short == "":
		return fmt.Errorf("%w: it is empty", ErrInvalidRefName)
	case len(short) > maxRefNameBytes:
		return fmt.Errorf("%w: it is longer than %d bytes", ErrInvalidRefName, maxRefNameBytes)
	case strings.HasPrefix(short, "/"), strings.HasSuffix(short, "/"):
		return fmt.Errorf(`%w: it starts or ends with "/"`, ErrInvalidRefName)
	case short == "HEAD":
		return fmt.Errorf("%w: HEAD is reserved", ErrInvalidRefName)
	}
	if err := plumbing.ReferenceName(BranchRef(short)).Validate(); err != nil {
		return fmt.Errorf(`%w: it must not contain "..", "~", "^", ":", "?", "*", "[", "\", "@{", `+
			`whitespace or control characters, must not have a component starting with ".", `+
			`and must not end with "." or ".lock"`, ErrInvalidRefName)
	}
	return nil
}

// refExists reports whether the exact ref is present. Callers hold r.mu.
func (r *Repo) refExists(name plumbing.ReferenceName) (plumbing.Hash, bool, error) {
	ref, err := r.repo.Storer.Reference(name)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, false, nil
	}
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	return ref.Hash(), true, nil
}

// HasBranch and HasTag report whether the exact ref exists, without resolving
// anything. They are the question every write path has to ask before it
// commits: Resolve answers "does this revision name something", and by go-git's
// RefRevParseRules a tag answers it before a branch of the same name does, so
// it cannot tell "refs/heads/v1" apart from "refs/tags/v1". A commit is only
// ever written to a branch, so the branch has to be looked up by its full name.
func (r *Repo) HasBranch(short string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists, err := r.refExists(plumbing.NewBranchReferenceName(short))
	return exists, err
}

func (r *Repo) HasTag(short string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists, err := r.refExists(plumbing.NewTagReferenceName(short))
	return exists, err
}

// CreateRef points refName at target, refusing to overwrite: an existing ref
// is ErrRefExists, which the API turns into the 409 huggingface_hub's
// `exist_ok=True` swallows.
//
// refName is the full name ("refs/heads/x", "refs/tags/v1"), and is validated
// again here even though the handler validated the short name -- this is the
// function that turns a string into a path under refs/, so it is where the
// check has to be load-bearing.
func (r *Repo) CreateRef(refName string, target plumbing.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := plumbing.ReferenceName(refName)
	if err := name.Validate(); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidRefName, refName)
	}
	if target.IsZero() {
		return fmt.Errorf("create ref %s: target is the zero hash", refName)
	}
	if _, exists, err := r.refExists(name); err != nil {
		return fmt.Errorf("read ref %s: %w", refName, err)
	} else if exists {
		return ErrRefExists
	}
	return r.repo.Storer.SetReference(plumbing.NewHashReference(name, target))
}

// DeleteRef removes refName and returns the object it pointed at, so the
// caller can record the deletion in the WAL with the right <old> value and put
// the ref back if that write fails. A ref that is not there is ErrRefNotFound.
func (r *Repo) DeleteRef(refName string) (plumbing.Hash, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := plumbing.ReferenceName(refName)
	if err := name.Validate(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("%w: %q", ErrInvalidRefName, refName)
	}
	old, exists, err := r.refExists(name)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("read ref %s: %w", refName, err)
	}
	if !exists {
		return plumbing.ZeroHash, ErrRefNotFound
	}
	if err := r.repo.Storer.RemoveReference(name); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("delete ref %s: %w", refName, err)
	}
	return old, nil
}

// WriteTagObject stores an annotated tag object pointing at target and returns
// its hash; the caller still has to create refs/tags/{name} for it. It is what
// makes `HfApi.create_tag(..., tag_message=...)` produce the same thing
// `git tag -m` would, rather than silently dropping the message.
//
// The object is written before the ref exists, which is the same order every
// other write here uses (§9 of docs/dev/continuity-design.md): an object no ref
// names is garbage a later gc collects, whereas a ref naming an absent object
// is a broken repository.
func (r *Repo) WriteTagObject(name string, target plumbing.Hash, message string, tagger Signature) (plumbing.Hash, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if target.IsZero() {
		return plumbing.ZeroHash, fmt.Errorf("write tag object %s: target is the zero hash", name)
	}
	when := tagger.When
	if when.IsZero() {
		when = time.Now()
	}
	// git's own tag messages end in a newline; without it `git show` runs the
	// message into whatever follows.
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	tag := &object.Tag{
		Name:       name,
		Message:    message,
		Target:     target,
		TargetType: plumbing.CommitObject,
		Tagger: object.Signature{
			Name: tagger.Name, Email: tagger.Email, When: when,
		},
	}
	obj := r.repo.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tag object %s: %w", name, err)
	}
	return r.repo.Storer.SetEncodedObject(obj)
}
