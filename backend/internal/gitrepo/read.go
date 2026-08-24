// Read paths: listing trees, stat-ing a single path, and streaming blobs.
// LFS pointers are recognised here so callers see the real target size.

package gitrepo

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Entry is one item in a tree listing.
type Entry struct {
	Path  string
	Name  string
	IsDir bool
	Mode  filemode.FileMode
	Hash  plumbing.Hash
	// Size is the blob's own size — for an LFS file that is the pointer size.
	Size int64
	// LFS is set when the blob's content parses as a Git LFS pointer.
	LFS *LFSPointer
}

// TargetSize is the size a client sees: the real object for LFS files.
func (e Entry) TargetSize() int64 {
	if e.LFS != nil {
		return e.LFS.Size
	}
	return e.Size
}

func (r *Repo) treeAt(rev string) (*object.Tree, plumbing.Hash, error) {
	h, err := r.Resolve(rev)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	c, err := object.GetCommit(r.storer(), h)
	if err != nil {
		return nil, h, fmt.Errorf("load commit %s: %w", h, err)
	}
	t, err := c.Tree()
	if err != nil {
		return nil, h, fmt.Errorf("load tree of %s: %w", h, err)
	}
	return t, h, nil
}

// Tree lists the entries directly under dir (or the whole subtree when
// recursive). dir "" means the repository root.
func (r *Repo) Tree(rev, dir string, recursive bool) ([]Entry, plumbing.Hash, error) {
	root, commit, err := r.treeAt(rev)
	if err != nil {
		if errors.Is(err, ErrEmptyRepo) {
			return nil, plumbing.ZeroHash, nil
		}
		return nil, plumbing.ZeroHash, err
	}
	dir = strings.Trim(dir, "/")
	t := root
	if dir != "" {
		sub, err := root.Tree(dir)
		if err != nil {
			return nil, commit, ErrPathNotFound
		}
		t = sub
	}
	entries, err := r.listTree(t, dir, recursive)
	return entries, commit, err
}

func (r *Repo) listTree(t *object.Tree, prefix string, recursive bool) ([]Entry, error) {
	var out []Entry
	for _, te := range t.Entries {
		p := te.Name
		if prefix != "" {
			p = prefix + "/" + te.Name
		}
		if te.Mode == filemode.Dir {
			out = append(out, Entry{Path: p, Name: te.Name, IsDir: true, Mode: te.Mode, Hash: te.Hash})
			if recursive {
				sub, err := object.GetTree(r.storer(), te.Hash)
				if err != nil {
					return nil, fmt.Errorf("load subtree %s: %w", p, err)
				}
				children, err := r.listTree(sub, p, true)
				if err != nil {
					return nil, err
				}
				out = append(out, children...)
			}
			continue
		}
		e, err := r.blobEntry(te, p)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (r *Repo) blobEntry(te object.TreeEntry, p string) (Entry, error) {
	e := Entry{Path: p, Name: te.Name, Mode: te.Mode, Hash: te.Hash}
	obj, err := r.storer().EncodedObject(plumbing.BlobObject, te.Hash)
	if err != nil {
		return e, fmt.Errorf("load blob %s: %w", p, err)
	}
	e.Size = obj.Size()
	// Only blobs small enough to *be* a pointer are worth reading.
	if e.Size > 0 && e.Size <= LFSPointerMaxSize {
		rc, err := obj.Reader()
		if err != nil {
			return e, fmt.Errorf("read blob %s: %w", p, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, LFSPointerMaxSize))
		_ = rc.Close()
		if err != nil {
			return e, fmt.Errorf("read blob %s: %w", p, err)
		}
		if ptr, ok := ParseLFSPointer(data); ok {
			e.LFS = &ptr
		}
	}
	return e, nil
}

// Stat resolves a single path. Directories come back with IsDir set.
func (r *Repo) Stat(rev, p string) (Entry, plumbing.Hash, error) {
	root, commit, err := r.treeAt(rev)
	if err != nil {
		return Entry{}, plumbing.ZeroHash, err
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return Entry{Path: "", Name: "", IsDir: true, Mode: filemode.Dir, Hash: root.Hash}, commit, nil
	}
	te, err := root.FindEntry(p)
	if err != nil {
		return Entry{}, commit, ErrPathNotFound
	}
	if te.Mode == filemode.Dir {
		return Entry{Path: p, Name: path.Base(p), IsDir: true, Mode: te.Mode, Hash: te.Hash}, commit, nil
	}
	e, err := r.blobEntry(*te, p)
	return e, commit, err
}

// StatMany resolves rev once and stats every path against that one tree, which
// is what a batch endpoint wants: Stat re-resolves the revision and reloads the
// root tree on every call, so asking about a few hundred paths one at a time
// pays for the same walk a few hundred times.
//
// Paths that are absent, or whose blob cannot be read, are simply missing from
// the returned map -- a caller batching lookups has nothing useful to do with a
// per-path error, and "not there" and "unreadable" lead to the same answer. A
// revision that does not resolve at all (an empty repository included) comes
// back as an error with a nil map, since that is a fact about the request
// rather than about any one path.
func (r *Repo) StatMany(rev string, paths []string) (map[string]Entry, plumbing.Hash, error) {
	root, commit, err := r.treeAt(rev)
	if err != nil {
		return nil, commit, err
	}
	out := make(map[string]Entry, len(paths))
	for _, p := range paths {
		// Keyed by the path as the caller spelled it, so the lookup on the
		// way back needs no second round of cleaning.
		if _, done := out[p]; done {
			continue
		}
		clean := strings.Trim(p, "/")
		if clean == "" {
			out[p] = Entry{Path: "", Name: "", IsDir: true, Mode: filemode.Dir, Hash: root.Hash}
			continue
		}
		te, err := root.FindEntry(clean)
		if err != nil {
			continue
		}
		if te.Mode == filemode.Dir {
			out[p] = Entry{Path: clean, Name: path.Base(clean), IsDir: true, Mode: te.Mode, Hash: te.Hash}
			continue
		}
		e, err := r.blobEntry(*te, clean)
		if err != nil {
			continue
		}
		out[p] = e
	}
	return out, commit, nil
}

// BlobReader streams a blob's bytes.
func (r *Repo) BlobReader(hash plumbing.Hash) (io.ReadCloser, int64, error) {
	obj, err := r.storer().EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		return nil, 0, fmt.Errorf("load blob %s: %w", hash, err)
	}
	rc, err := obj.Reader()
	if err != nil {
		return nil, 0, fmt.Errorf("read blob %s: %w", hash, err)
	}
	return rc, obj.Size(), nil
}

// ErrBlobTooLarge is returned by ReadBlob/ReadFile when the blob exists but
// exceeds the caller-supplied maxBytes limit, so callers can tell "too big"
// apart from "missing" (errors.Is).
var ErrBlobTooLarge = errors.New("blob exceeds size limit")

// ReadBlob reads a whole blob, refusing anything over maxBytes.
func (r *Repo) ReadBlob(hash plumbing.Hash, maxBytes int64) ([]byte, error) {
	rc, size, err := r.BlobReader(hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if maxBytes > 0 && size > maxBytes {
		return nil, fmt.Errorf("blob %s is %d bytes, over the %d byte limit: %w", hash, size, maxBytes, ErrBlobTooLarge)
	}
	return io.ReadAll(rc)
}

// ReadFile is the convenience path: resolve rev+path and return the content.
func (r *Repo) ReadFile(rev, p string, maxBytes int64) ([]byte, error) {
	e, _, err := r.Stat(rev, p)
	if err != nil {
		return nil, err
	}
	if e.IsDir {
		return nil, ErrPathNotFound
	}
	return r.ReadBlob(e.Hash, maxBytes)
}
