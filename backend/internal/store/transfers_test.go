// What a move leaves behind: the lineage edges that pointed at the old name,
// and the redirect that answers for it. Both are matched by (kind, namespace,
// name) rather than by id, so both have to agree with the rest of the schema
// about what those three mean.

package store

import (
	"errors"
	"testing"
)

// A new_version edge targets the kind that declared it -- a model's successor
// is a model, a dataset's is a dataset (LineageEdge.TargetKind) -- and a
// model and a dataset may share a name. A move therefore has to pick the
// edges apart by their source repository's kind: lumping every new_version
// edge in with the datasets both stranded a moved model's successors and let
// a moved dataset rewrite edges between two models.
func TestIntegrationTransferFollowsNewVersionEdgesByKind(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		bobNS := f.ns(t, "bob")

		f.repo(t, "alice", "x", "model", nil)
		f.repo(t, "alice", "x", "dataset", nil)
		modelSrc := f.repo(t, "alice", "old-model", "model", nil)
		datasetSrc := f.repo(t, "alice", "old-dataset", "dataset", nil)
		// A card is free to write the namespace in any case, and it still
		// names the same repository -- so the move has to find this edge too.
		shoutySrc := f.repo(t, "alice", "old-model-2", "model", nil)

		// Every card says `new_version: alice/x`, and each means its own kind.
		edge := LineageEdge{Kind: LineageKindNewVersion, Raw: "alice/x", Namespace: "alice", Name: "x"}
		for _, r := range []*Repo{modelSrc, datasetSrc} {
			if err := s.ReplaceRepoLineage(ctx, r.ID, []LineageEdge{edge}); err != nil {
				t.Fatalf("ReplaceRepoLineage %s: %v", r.FullName(), err)
			}
		}
		if err := s.ReplaceRepoLineage(ctx, shoutySrc.ID, []LineageEdge{
			{Kind: LineageKindNewVersion, Raw: "Alice/x", Namespace: "Alice", Name: "x"},
		}); err != nil {
			t.Fatalf("ReplaceRepoLineage %s: %v", shoutySrc.FullName(), err)
		}

		successor := func(t *testing.T, src *Repo) LineageUpstream {
			t.Helper()
			edges, err := s.ListRepoLineage(ctx, src.ID)
			if err != nil || len(edges) != 1 {
				t.Fatalf("ListRepoLineage %s = %+v, %v", src.FullName(), edges, err)
			}
			return edges[0]
		}
		want := func(t *testing.T, src *Repo, ns, name string) {
			t.Helper()
			got := successor(t, src)
			if got.Namespace != ns || got.Name != name {
				t.Errorf("%s successor = %s/%s, want %s/%s", src.FullName(), got.Namespace, got.Name, ns, name)
			}
			// The move must not leave the edge dangling either: Exists
			// resolves the target with the very expression the move classifies
			// the edge by, so the two disagreeing shows up here.
			if !got.Exists {
				t.Errorf("%s successor %s/%s does not resolve", src.FullName(), got.Namespace, got.Name)
			}
		}

		if _, err := s.TransferRepo(ctx, TransferSpec{
			RepoID: f.mustRepo(t, "model", "alice", "x").ID, ToNamespaceID: bobNS.ID, ToName: "y", ActorID: f.admin.ID,
		}); err != nil {
			t.Fatalf("TransferRepo model: %v", err)
		}
		want(t, modelSrc, "bob", "y")
		want(t, shoutySrc, "bob", "y")
		want(t, datasetSrc, "alice", "x")

		if _, err := s.TransferRepo(ctx, TransferSpec{
			RepoID: f.mustRepo(t, "dataset", "alice", "x").ID, ToNamespaceID: bobNS.ID, ToName: "z", ActorID: f.admin.ID,
		}); err != nil {
			t.Fatalf("TransferRepo dataset: %v", err)
		}
		want(t, datasetSrc, "bob", "z")
		want(t, modelSrc, "bob", "y")
	})
}

// /Alice/foo and /alice/foo are one repository, so they stay one repository
// after a transfer: the redirect left behind is matched the way GetRepo
// matches a live one -- folded on the namespace, exact on the name.
func TestIntegrationRepoRedirectFoldsNamespaceCase(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		r := f.repo(t, "alice", "foo", "model", nil)
		if _, err := s.TransferRepo(ctx, TransferSpec{
			RepoID: r.ID, ToNamespaceID: f.ns(t, "bob").ID, ActorID: f.admin.ID,
		}); err != nil {
			t.Fatalf("TransferRepo: %v", err)
		}

		red, err := s.ResolveRepoRedirect(ctx, "model", "Alice", "foo")
		if err != nil || red.ID != r.ID || red.Namespace != "bob" {
			t.Fatalf("ResolveRepoRedirect(Alice/foo) = %+v, %v; the same URL resolved before the move", red, err)
		}
		// Repository names stay case-sensitive, exactly as GetRepo has them.
		if _, err := s.ResolveRepoRedirect(ctx, "model", "alice", "FOO"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveRepoRedirect(alice/FOO) err = %v, want ErrNotFound", err)
		}
	})
}

// mustRepo is GetRepo for a repository the test knows exists.
func (f *fixture) mustRepo(t *testing.T, kind, ns, name string) *Repo {
	t.Helper()
	r, err := f.s.GetRepo(f.ctx, kind, ns, name)
	if err != nil {
		t.Fatalf("GetRepo %s %s/%s: %v", kind, ns, name, err)
	}
	return r
}

// A repository-scoped webhook is dropped by a transfer because it belonged to
// the previous owner. A rename inside the same namespace has no previous
// owner, so the same code path must not treat it as one: renaming used to be
// reachable only through the transfer form, which is how deleting the
// subscriptions passed for correct. It is now its own settings action, and
// silently destroying a repository's webhooks is not what "rename" means.
func TestIntegrationRenameKeepsRepoScopedWebhooks(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		aliceNS := f.ns(t, "alice")
		bobNS := f.ns(t, "bob")

		hooksOf := func(t *testing.T, repoID int64, nsID int64) int {
			t.Helper()
			hooks, err := s.ListWebhooksForNamespace(ctx, nsID)
			if err != nil {
				t.Fatalf("list webhooks: %v", err)
			}
			n := 0
			for _, h := range hooks {
				if h.RepoID != nil && *h.RepoID == repoID {
					n++
				}
			}
			return n
		}

		renamed := f.repo(t, "alice", "before", "model", nil)
		if _, err := s.CreateWebhook(ctx, aliceNS.ID, &renamed.ID, "https://example.test/renamed", "s", []string{"repo.push"}, true); err != nil {
			t.Fatalf("create webhook: %v", err)
		}
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: renamed.ID, ToNamespaceID: aliceNS.ID, ToName: "after", ActorID: f.alice.ID}); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if got := hooksOf(t, renamed.ID, aliceNS.ID); got != 1 {
			t.Errorf("after a same-namespace rename: %d repo-scoped webhooks, want 1", got)
		}

		// The transfer case is the one the deletion was written for, and it
		// still has to happen: the old owner must stop receiving events.
		moved := f.repo(t, "alice", "moving", "model", nil)
		if _, err := s.CreateWebhook(ctx, aliceNS.ID, &moved.ID, "https://example.test/moved", "s", []string{"repo.push"}, true); err != nil {
			t.Fatalf("create webhook: %v", err)
		}
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: moved.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}); err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if got := hooksOf(t, moved.ID, aliceNS.ID); got != 0 {
			t.Errorf("after a cross-namespace transfer: %d repo-scoped webhooks left behind, want 0", got)
		}
	})
}
