// File-level diffing between two commits, used to decide what post-commit
// indexing has to re-read.

package gitrepo

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type ChangeKind int

const (
	ChangeAdd ChangeKind = iota
	ChangeModify
	ChangeDelete
)

func (c ChangeKind) String() string {
	switch c {
	case ChangeAdd:
		return "add"
	case ChangeModify:
		return "modify"
	default:
		return "delete"
	}
}

type Change struct {
	Kind ChangeKind
	Path string
}

// Diff lists the file-level changes between two commits. A zero oldHash means
// "everything in newHash is new".
func (r *Repo) Diff(oldHash, newHash plumbing.Hash) ([]Change, error) {
	newTree, err := r.commitTree(newHash)
	if err != nil {
		return nil, err
	}
	var oldTree *object.Tree
	if !oldHash.IsZero() {
		oldTree, err = r.commitTree(oldHash)
		if err != nil {
			// A force-push or a rewritten history can leave a dangling old
			// hash; treat the new tree as entirely new rather than failing.
			oldTree = nil
		}
	}
	if oldTree == nil {
		oldTree = &object.Tree{}
	}

	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	out := make([]Change, 0, len(changes))
	for _, ch := range changes {
		action, err := ch.Action()
		if err != nil {
			return nil, fmt.Errorf("classify change: %w", err)
		}
		switch action.String() {
		case "Insert":
			out = append(out, Change{Kind: ChangeAdd, Path: ch.To.Name})
		case "Modify":
			out = append(out, Change{Kind: ChangeModify, Path: ch.To.Name})
		case "Delete":
			out = append(out, Change{Kind: ChangeDelete, Path: ch.From.Name})
		}
	}
	return out, nil
}

func (r *Repo) commitTree(h plumbing.Hash) (*object.Tree, error) {
	c, err := object.GetCommit(r.storer(), h)
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", h, err)
	}
	return c.Tree()
}
