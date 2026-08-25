// The in-memory tree builder behind Commit: a lazily-materialised directory
// node that only decodes and rewrites the subtrees an operation touches.

package gitrepo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// dirNode is a lazily-materialised tree. Subtrees that no operation touches
// keep their original hash and are never decoded or rewritten.
type dirNode struct {
	hash   plumbing.Hash
	loaded bool
	dirty  bool
	subs   map[string]*dirNode
	blobs  map[string]object.TreeEntry
}

func newDirNode() *dirNode {
	return &dirNode{loaded: true, dirty: true, subs: map[string]*dirNode{}, blobs: map[string]object.TreeEntry{}}
}

func (d *dirNode) ensureLoaded(s storer.EncodedObjectStorer) error {
	if d.loaded {
		return nil
	}
	d.subs = map[string]*dirNode{}
	d.blobs = map[string]object.TreeEntry{}
	d.loaded = true
	if d.hash.IsZero() {
		return nil
	}
	t, err := object.GetTree(s, d.hash)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", d.hash, err)
	}
	for _, te := range t.Entries {
		if te.Mode == filemode.Dir {
			d.subs[te.Name] = &dirNode{hash: te.Hash}
			// A tree written before this package refused to build one can
			// carry the same name as both a file and a directory -- the
			// duplicateEntries shape git fsck rejects. Resolving it here, the
			// way a checkout does (the directory wins, since that is what
			// clients already see on disk), is what lets the next commit
			// repair such a repository instead of failing on it forever:
			// write() below treats the collision as unrecoverable, and it has
			// to, or a genuine bug in walk/setBlob would go out as a corrupt
			// tree again.
			if _, dup := d.blobs[te.Name]; dup {
				delete(d.blobs, te.Name)
				// The loaded hash no longer describes what this node holds,
				// so it must be rewritten rather than reused as clean.
				d.dirty = true
			}
			continue
		}
		if _, dup := d.subs[te.Name]; dup {
			d.dirty = true
			continue
		}
		d.blobs[te.Name] = te
	}
	return nil
}

// walk descends to the parent directory of p, creating nodes as needed when
// create is true, and marks the path dirty.
func (d *dirNode) walk(s storer.EncodedObjectStorer, p string, create bool) (*dirNode, string, error) {
	segs := strings.Split(p, "/")
	cur := d
	for _, seg := range segs[:len(segs)-1] {
		if err := cur.ensureLoaded(s); err != nil {
			return nil, "", err
		}
		next, ok := cur.subs[seg]
		if !ok {
			if !create {
				return nil, "", nil
			}
			next = newDirNode()
			cur.subs[seg] = next
		}
		if create {
			// Descending through a name that is currently a *file* turns it
			// into a directory, so the file has to go. git does the same
			// thing -- `git add foo/bar` when `foo` is a tracked file
			// replaces `foo` rather than refusing -- and setBlob already
			// implements the mirror image of this rule for the other
			// direction, so an implicit replacement is the only choice that
			// makes the two directions agree.
			//
			// Erroring out instead was the alternative, and it would arguably
			// be friendlier to a client that got its paths wrong. It loses to
			// two things: HF clients (and git itself) treat this as an
			// ordinary rename-shaped edit, and a commit is applied op by op,
			// so a batch that legitimately deletes `foo` and adds `foo/bar`
			// would have to be rejected on op ordering alone.
			//
			// Whichever way this went, the entry could not be left in both
			// maps: write() emits one tree entry per name in each, and a tree
			// carrying the same name twice is what `git fsck --strict` calls
			// duplicateEntries -- a repository that ordinary clients clone
			// while silently losing the file, and that a fsck-ing client
			// refuses outright.
			delete(cur.blobs, seg)
		}
		cur.dirty = true
		cur = next
	}
	if err := cur.ensureLoaded(s); err != nil {
		return nil, "", err
	}
	cur.dirty = true
	return cur, segs[len(segs)-1], nil
}

func (d *dirNode) setBlob(s storer.EncodedObjectStorer, p string, te object.TreeEntry) error {
	parent, name, err := d.walk(s, p, true)
	if err != nil {
		return err
	}
	// Writing a file where a directory stands replaces the directory.
	delete(parent.subs, name)
	parent.blobs[name] = te
	return nil
}

func (d *dirNode) deleteBlob(s storer.EncodedObjectStorer, p string) error {
	parent, name, err := d.walk(s, p, false)
	if err != nil || parent == nil {
		return err
	}
	delete(parent.blobs, name)
	return nil
}

func (d *dirNode) deleteDir(s storer.EncodedObjectStorer, p string) error {
	parent, name, err := d.walk(s, p, false)
	if err != nil || parent == nil {
		return err
	}
	delete(parent.subs, name)
	delete(parent.blobs, name)
	return nil
}

// write serialises the node, reusing the original hash for clean subtrees.
// A directory that ends up empty returns the zero hash and is dropped by the
// caller, matching git's rule that trees never contain empty trees.
func (d *dirNode) write(s storer.EncodedObjectStorer) (plumbing.Hash, error) {
	if !d.dirty && !d.hash.IsZero() {
		return d.hash, nil
	}
	if err := d.ensureLoaded(s); err != nil {
		return plumbing.ZeroHash, err
	}

	entries := make([]object.TreeEntry, 0, len(d.blobs)+len(d.subs))
	for name, te := range d.blobs {
		// A name held in both maps would be written as two entries of the
		// same name, which is a corrupt tree. walk and setBlob keep that from
		// happening; this is the assertion that says so out loud, because the
		// failure it guards against is silent at write time and permanent
		// once it reaches the WAL.
		if _, dup := d.subs[name]; dup {
			return plumbing.ZeroHash, fmt.Errorf("tree entry %q is both a file and a directory", name)
		}
		te.Name = name
		entries = append(entries, te)
	}
	for name, sub := range d.subs {
		h, err := sub.write(s)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if h.IsZero() {
			continue
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	if len(entries) == 0 {
		return plumbing.ZeroHash, nil
	}
	sortTreeEntries(entries)

	tree := &object.Tree{Entries: entries}
	obj := s.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store tree: %w", err)
	}
	d.hash = h
	d.dirty = false
	return h, nil
}

// sortTreeEntries applies git's ordering, in which a directory sorts as though
// its name ended in a slash. Getting this wrong produces trees that git itself
// reports as corrupt.
func sortTreeEntries(entries []object.TreeEntry) {
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
}
