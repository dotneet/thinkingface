package store

import (
	"slices"
	"testing"
)

// A restricted token's repository rows: written with the token, matched by id,
// following a rename, and -- the fail-closed half -- surviving the deletion of
// the repository they name, so the token stays restricted rather than falling
// back to "no rows = unrestricted".
func TestIntegrationTokenRepos(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		exp := f.repo(t, "alice", "exp", "dataset", nil)
		ocr := f.repo(t, "alice", "ocr", "model", nil)

		plain, err := s.CreateToken(f.ctx, f.alice.ID, "plain", "write", "hash-plain", nil)
		if err != nil {
			t.Fatalf("create plain token: %v", err)
		}
		tok, err := s.CreateTokenWithRepos(f.ctx, f.alice.ID, "agent", "write", "hash-agent", nil, []TokenRepo{
			{RepoID: exp.ID, Kind: exp.Kind, Namespace: exp.Namespace, Name: exp.Name},
			{RepoID: ocr.ID, Kind: ocr.Kind, Namespace: ocr.Namespace, Name: ocr.Name},
		})
		if err != nil {
			t.Fatalf("create restricted token: %v", err)
		}
		if len(tok.Repos) != 2 {
			t.Fatalf("created token repos = %v, want 2", tok.Repos)
		}

		if r, err := s.LookupTokenRestriction(f.ctx, plain.ID); err != nil || r.Restricted || len(r.RepoIDs) != 0 {
			t.Fatalf("plain token restriction = %+v, %v; want unrestricted", r, err)
		}
		r, err := s.LookupTokenRestriction(f.ctx, tok.ID)
		if err != nil || !r.Restricted || !equalInt64s(sortedInt64s(r.RepoIDs), sortedInt64s([]int64{exp.ID, ocr.ID})) {
			t.Fatalf("restricted token restriction = %+v, %v; want both repos", r, err)
		}

		// A duplicate entry is a conflict and leaves no token behind.
		if _, err := s.CreateTokenWithRepos(f.ctx, f.alice.ID, "dup", "write", "hash-dup", nil, []TokenRepo{
			{RepoID: exp.ID, Kind: "dataset", Namespace: "alice", Name: "exp"},
			{RepoID: exp.ID, Kind: "dataset", Namespace: "alice", Name: "exp"},
		}); err == nil {
			t.Fatal("duplicate repository entry was accepted")
		}
		if _, _, err := s.LookupToken(f.ctx, "hash-dup"); err == nil {
			t.Fatal("a failed restricted create left its token row behind")
		}
		// An entry without an id is refused before anything is written.
		if _, err := s.CreateTokenWithRepos(f.ctx, f.alice.ID, "noid", "write", "hash-noid", nil, []TokenRepo{
			{Kind: "dataset", Namespace: "alice", Name: "exp"},
		}); err == nil {
			t.Fatal("an entry without a repository id was accepted")
		}

		// Deleting a listed repository keeps the token restricted.
		if err := s.DeleteRepo(f.ctx, exp.ID); err != nil {
			t.Fatalf("delete repo: %v", err)
		}
		r, err = s.LookupTokenRestriction(f.ctx, tok.ID)
		if err != nil || !r.Restricted || !equalInt64s(r.RepoIDs, []int64{ocr.ID}) {
			t.Fatalf("after delete restriction = %+v, %v; want restricted to ocr only", r, err)
		}
		if err := s.DeleteRepo(f.ctx, ocr.ID); err != nil {
			t.Fatalf("delete repo: %v", err)
		}
		r, err = s.LookupTokenRestriction(f.ctx, tok.ID)
		if err != nil || !r.Restricted || len(r.RepoIDs) != 0 {
			t.Fatalf("after deleting every listed repo restriction = %+v, %v; want restricted to nothing", r, err)
		}

		// A new repository at an old name is not on the list.
		again := f.repo(t, "alice", "exp", "dataset", nil)
		r, _ = s.LookupTokenRestriction(f.ctx, tok.ID)
		for _, id := range r.RepoIDs {
			if id == again.ID {
				t.Fatal("a repository re-created at a listed name inherited the grant")
			}
		}

		// The listing still says what the token was restricted to.
		list, err := s.ListTokens(f.ctx, f.alice.ID)
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		for _, lt := range list {
			switch lt.ID {
			case plain.ID:
				if len(lt.Repos) != 0 {
					t.Errorf("plain token listed with repos %v", lt.Repos)
				}
			case tok.ID:
				if len(lt.Repos) != 2 || lt.Repos[0].RepoID != 0 || lt.Repos[1].RepoID != 0 {
					t.Errorf("restricted token listed as %+v; want both deleted repos by name", lt.Repos)
				}
			}
		}

		// Revoking the token takes its rows with it.
		if err := s.DeleteToken(f.ctx, f.alice.ID, tok.ID); err != nil {
			t.Fatalf("delete token: %v", err)
		}
		if r, err := s.LookupTokenRestriction(f.ctx, tok.ID); err != nil || r.Restricted {
			t.Fatalf("deleted token restriction = %+v, %v; want no rows", r, err)
		}
	})
}

// A listed repository is shown by its current name after a rename.
func TestIntegrationTokenReposFollowRename(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		exp := f.repo(t, "alice", "exp", "dataset", nil)
		tok, err := s.CreateTokenWithRepos(f.ctx, f.alice.ID, "agent", "write", "hash-agent", nil, []TokenRepo{
			{RepoID: exp.ID, Kind: exp.Kind, Namespace: exp.Namespace, Name: exp.Name},
		})
		if err != nil {
			t.Fatalf("create restricted token: %v", err)
		}
		bobNS := f.ns(t, "bob")
		if _, err := s.TransferRepo(f.ctx, TransferSpec{RepoID: exp.ID, ToNamespaceID: bobNS.ID, ToName: "exp2", ActorID: f.admin.ID}); err != nil {
			t.Fatalf("transfer: %v", err)
		}
		list, err := s.ListTokens(f.ctx, f.alice.ID)
		if err != nil {
			t.Fatalf("list tokens: %v", err)
		}
		if len(list) != 1 || len(list[0].Repos) != 1 {
			t.Fatalf("list = %+v", list)
		}
		got := list[0].Repos[0]
		if got.RepoID != exp.ID || got.Namespace != "bob" || got.Name != "exp2" {
			t.Fatalf("listed repo = %+v; want the current name bob/exp2 with the same id", got)
		}
		if r, _ := s.LookupTokenRestriction(f.ctx, tok.ID); !equalInt64s(r.RepoIDs, []int64{exp.ID}) {
			t.Fatalf("restriction after transfer = %+v; want the same id", r)
		}
	})
}

func sortedInt64s(v []int64) []int64 {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}
