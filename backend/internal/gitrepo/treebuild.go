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
		} else {
			d.blobs[te.Name] = te
		}
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
