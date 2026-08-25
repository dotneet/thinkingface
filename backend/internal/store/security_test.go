package store

import (
	"context"
	"testing"
)

// RepoHasLFSObject is the authorisation predicate the LFS download paths use,
// so it has to answer per repository, not per object.
func TestRepoHasLFSObject(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			s := b.open(t)

			owner := mustUser(t, s, "alice")
			other := mustUser(t, s, "mallory")
			mine := mustRepo(t, s, owner, "alice", "weights")
			theirs := mustRepo(t, s, other, "mallory", "x")

			const oid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			if err := s.RecordLFSObject(ctx, mine.ID, oid, 4, func(string) (bool, error) { return true, nil }); err != nil {
				t.Fatalf("RecordLFSObject: %v", err)
			}

			has, err := s.RepoHasLFSObject(ctx, mine.ID, oid)
			if err != nil {
				t.Fatalf("RepoHasLFSObject(owner): %v", err)
			}
			if !has {
				t.Errorf("the repository that recorded the object does not have it")
			}

			// The bytes exist instance-wide, but this repository never saw
			// them. That distinction is the entire fix.
			has, err = s.RepoHasLFSObject(ctx, theirs.ID, oid)
			if err != nil {
				t.Fatalf("RepoHasLFSObject(other): %v", err)
			}
			if has {
				t.Errorf("a repository that never uploaded the object claims to have it")
			}

			// LinkLFSObjects (the route the HF commit handler and the
			// syncer's post-push pipeline take) is the other way a link is
			// created and must be visible here too.
			if err := s.LinkLFSObjects(ctx, theirs.ID, []LFSObjectRef{{OID: oid, Size: 4}}); err != nil {
				t.Fatalf("LinkLFSObjects: %v", err)
			}
			has, err = s.RepoHasLFSObject(ctx, theirs.ID, oid)
			if err != nil {
				t.Fatalf("RepoHasLFSObject(after link): %v", err)
			}
			if !has {
				t.Errorf("LinkLFSObjects did not make the object visible to the repository")
			}
		})
	}
}

func TestBumpSessionEpoch(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			s := b.open(t)

			u := mustUser(t, s, "alice")
			if u.SessionEpoch != 0 {
				t.Fatalf("a fresh user starts at epoch %d, want 0", u.SessionEpoch)
			}
			if err := s.BumpSessionEpoch(ctx, u.ID); err != nil {
				t.Fatalf("BumpSessionEpoch: %v", err)
			}
			// Every read path that resolves a session must see the new value.
			byID, err := s.GetUserByID(ctx, u.ID)
			if err != nil {
				t.Fatalf("GetUserByID: %v", err)
			}
			if byID.SessionEpoch != 1 {
				t.Errorf("GetUserByID epoch = %d, want 1", byID.SessionEpoch)
			}
			byName, err := s.GetUserByUsername(ctx, "alice")
			if err != nil {
				t.Fatalf("GetUserByUsername: %v", err)
			}
			if byName.SessionEpoch != 1 {
				t.Errorf("GetUserByUsername epoch = %d, want 1", byName.SessionEpoch)
			}
		})
	}
}

func mustUser(t *testing.T, s *Store, name string) *User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), name, name+"@example.com", "hash", false)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func mustRepo(t *testing.T, s *Store, _ *User, ns, name string) *Repo {
	t.Helper()
	ctx := context.Background()
	n, err := s.GetNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("namespace %s: %v", ns, err)
	}
	r, err := s.CreateRepo(ctx, n.ID, name, "model", "", "main", NewStoragePath())
	if err != nil {
		t.Fatalf("create repo %s/%s: %v", ns, name, err)
	}
	return r
}
