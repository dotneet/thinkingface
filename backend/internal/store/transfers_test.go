// What a move leaves behind: the lineage edges that pointed at the old name,
// and the redirect that answers for it. Both are matched by (kind, namespace,
// name) rather than by id, so both have to agree with the rest of the schema
// about what those three mean.

package store

import (
	"errors"
	"testing"
	"time"
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

// Expiry is lazy: nothing sweeps repo_transfers, and a row only stops being
// 'pending' when something touches it. But every path that could find a
// pending row filters on expires_at, so once the TTL passes there is nothing
// left to touch it -- and the row still occupies
// idx_repo_transfers_one_pending. Before CreateRepoTransfer reconciled it,
// that made one unanswered request wedge the repository's approval flow
// permanently: invisible to both parties, uncancellable, and answering every
// later request with ErrConflict.
func TestIntegrationExpiredTransferDoesNotWedgeTheNextRequest(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		bobNS := f.ns(t, "bob")

		r := f.repo(t, "alice", "thing", "model", nil)
		spec := TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}

		// A negative TTL is the eighth day of a seven-day request, without a
		// test that waits a week for it.
		stale, err := s.CreateRepoTransfer(ctx, spec, -time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer: %v", err)
		}

		// Nobody can see it any more: not the settings-page banner (which is
		// also what DELETE .../transfer cancels through)...
		if _, err := s.PendingRepoTransfer(ctx, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PendingRepoTransfer on an expired request = %v, want ErrNotFound", err)
		}
		// ...nor either party's /me/transfers.
		for _, u := range []*User{f.alice, f.bob} {
			in, out, err := s.ListRepoTransfersForUser(ctx, u.ID)
			if err != nil {
				t.Fatalf("ListRepoTransfersForUser(%s): %v", u.Username, err)
			}
			if len(in) != 0 || len(out) != 0 {
				t.Fatalf("%s sees %d incoming / %d outgoing expired transfers, want none",
					u.Username, len(in), len(out))
			}
		}

		// So the next request is the only thing that can ever reconcile it,
		// and it must not collide with it.
		fresh, err := s.CreateRepoTransfer(ctx, spec, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer after the previous one expired: %v", err)
		}
		if fresh.ID == stale.ID {
			t.Fatalf("the expired row was reused rather than superseded (id %d)", fresh.ID)
		}

		// The old row is recorded as expired, not left pending for the unique
		// index to trip over again.
		old, err := s.GetRepoTransfer(ctx, stale.ID)
		if err != nil {
			t.Fatalf("GetRepoTransfer(%d): %v", stale.ID, err)
		}
		if old.Status != "expired" {
			t.Errorf("expired request status = %q, want expired", old.Status)
		}

		// And the new one is the live one.
		got, err := s.PendingRepoTransfer(ctx, r.ID)
		if err != nil || got.ID != fresh.ID {
			t.Fatalf("PendingRepoTransfer = %+v, %v; want the fresh request %d", got, err, fresh.ID)
		}
	})
}

// A site administrator is RoleAdmin in every namespace as far as the API
// layer is concerned (api.roleIn, docs/dev/repo-transfer-design.md §5), which
// is what lets them accept, reject or cancel any pending transfer by id. The
// listing has to agree, or the one endpoint that could act on a stuck
// transfer is the one nothing in the UI ever points at.
func TestIntegrationSiteAdminSeesEveryPendingTransfer(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		carol, err := s.CreateUser(ctx, "carol", "carol@example.com", "hash", false)
		if err != nil {
			t.Fatalf("create carol: %v", err)
		}

		r := f.repo(t, "alice", "foo", "model", nil)
		created, err := s.CreateRepoTransfer(ctx, TransferSpec{
			RepoID: r.ID, ToNamespaceID: f.ns(t, "bob").ID, ActorID: f.alice.ID,
		}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer: %v", err)
		}

		// The admin is neither namespace's owner nor a member of anything,
		// and still sees it from both sides: they may accept it (destination)
		// and they may cancel it (source).
		in, out, err := s.ListRepoTransfersForUser(ctx, f.admin.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(admin): %v", err)
		}
		if len(in) != 1 || in[0].ID != created.ID {
			t.Errorf("admin incoming = %+v, want the pending transfer %d", in, created.ID)
		}
		if len(out) != 1 || out[0].ID != created.ID {
			t.Errorf("admin outgoing = %+v, want the pending transfer %d", out, created.ID)
		}

		// An ordinary account with no relationship to either side still sees
		// nothing: the admin arm widens the predicate for administrators
		// only.
		in, out, err = s.ListRepoTransfersForUser(ctx, carol.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(carol): %v", err)
		}
		if len(in) != 0 || len(out) != 0 {
			t.Errorf("carol sees %d incoming / %d outgoing, want none", len(in), len(out))
		}
	})
}
