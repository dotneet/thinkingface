// What a move leaves behind: the lineage edges that pointed at the old name,
// and the redirect that answers for it. Both are matched by (kind, namespace,
// name) rather than by id, so both have to agree with the rest of the schema
// about what those three mean.

package store

import (
	"context"
	"errors"
	"fmt"
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
// layer is concerned (api.roleIn, docs/dev/repo-transfer-design.md §5), so
// they may accept, reject or cancel any pending transfer by id. This listing
// deliberately does not follow that rule: it answers "what is waiting for
// me", and a request between two strangers is waiting for neither of them.
// While it did follow it, every pending transfer on the instance was listed
// at every administrator -- once as incoming and again as outgoing, since the
// same predicate is applied to both ends of the row -- and the header badge,
// which counts the incoming side on every page render, never went out.
func TestIntegrationSiteAdminIsNotShownStrangersTransfers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		r := f.repo(t, "alice", "foo", "model", nil)
		created, err := s.CreateRepoTransfer(ctx, TransferSpec{
			RepoID: r.ID, ToNamespaceID: f.ns(t, "bob").ID, ActorID: f.alice.ID,
		}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer: %v", err)
		}

		// The admin owns neither namespace and is a member of nothing, so
		// alice's offer to bob is none of their inbox's business.
		in, out, err := s.ListRepoTransfersForUser(ctx, f.admin.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(admin): %v", err)
		}
		if len(in) != 0 || len(out) != 0 {
			t.Errorf("site admin sees %d incoming / %d outgoing, want none of either", len(in), len(out))
		}

		// The two people it *is* waiting on still see it, each on one side
		// only -- proving the rows are listed at all and it is the
		// administrator arm that is gone.
		in, out, err = s.ListRepoTransfersForUser(ctx, f.bob.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(bob): %v", err)
		}
		if len(in) != 1 || in[0].ID != created.ID || len(out) != 0 {
			t.Errorf("bob (the destination) = %d incoming / %d outgoing, want just the transfer incoming", len(in), len(out))
		}
		in, out, err = s.ListRepoTransfersForUser(ctx, f.alice.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(alice): %v", err)
		}
		if len(out) != 1 || out[0].ID != created.ID || len(in) != 0 {
			t.Errorf("alice (the source) = %d incoming / %d outgoing, want just the transfer outgoing", len(in), len(out))
		}
	})
}

// Anybody may aim a transfer request at anybody's namespace, so the size of
// an inbox is not something its owner controls -- and the header badge reads
// it on every page render. Each side is capped; the newest survive.
func TestIntegrationTransferListingIsCapped(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		bobNS := f.ns(t, "bob")

		// Shrunk rather than filing 200 requests: the cap is the behaviour
		// under test, not the number.
		defer func(orig int) { maxTransfersListed = orig }(maxTransfersListed)
		maxTransfersListed = 2

		var last int64
		for i := range 3 {
			r := f.repo(t, "alice", fmt.Sprintf("flood-%d", i), "model", nil)
			// created_at orders the listing and comes from time.Now(), whose
			// resolution the SQLite text encoding truncates; spacing the rows
			// keeps "newest first" from being a coin toss.
			time.Sleep(2 * time.Millisecond)
			ct, err := s.CreateRepoTransfer(ctx, TransferSpec{
				RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID,
			}, time.Hour)
			if err != nil {
				t.Fatalf("CreateRepoTransfer %d: %v", i, err)
			}
			last = ct.ID
		}

		in, out, err := s.ListRepoTransfersForUser(ctx, f.bob.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(bob): %v", err)
		}
		if len(in) != 2 {
			t.Fatalf("bob incoming = %d, want the cap of %d", len(in), maxTransfersListed)
		}
		if in[0].ID != last {
			t.Errorf("incoming[0].ID = %d, want the newest request %d", in[0].ID, last)
		}
		if len(out) != 0 {
			t.Errorf("bob outgoing = %d, want none", len(out))
		}

		// The source side is capped by the same query.
		inAlice, outAlice, err := s.ListRepoTransfersForUser(ctx, f.alice.ID)
		if err != nil {
			t.Fatalf("ListRepoTransfersForUser(alice): %v", err)
		}
		if len(outAlice) != 2 || len(inAlice) != 0 {
			t.Errorf("alice = %d incoming / %d outgoing, want 0 / the cap of %d", len(inAlice), len(outAlice), maxTransfersListed)
		}
	})
}

// A pending request carries the requester's authority over the source
// namespace for up to a week, and the accept path used to check only the
// accepter's side. So an organisation admin could file acme/x -> an outsider,
// be removed from acme (or suspended), and have the outsider accept it
// afterwards. Accept re-reads the requester's standing under the move's own
// locks: a requester who could no longer file the request voids it, and an
// archive made since refuses it without voiding.
func TestIntegrationAcceptRechecksTheRequestersAuthority(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		bobNS := f.ns(t, "bob")

		// acme has two admins so one can be removed (ErrLastAdmin otherwise).
		acme, err := s.CreateOrg(ctx, "acme", f.admin.ID, OrgUpdate{})
		if err != nil {
			t.Fatalf("CreateOrg: %v", err)
		}
		if _, err := s.AddOrgMember(ctx, acme.ID, f.alice.ID, "admin", f.admin.ID); err != nil {
			t.Fatalf("AddOrgMember: %v", err)
		}

		file := func(ns, name string, requester *User) (*Repo, *RepoTransfer) {
			t.Helper()
			r := f.repo(t, ns, name, "model", nil)
			tr, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: requester.ID}, time.Hour)
			if err != nil {
				t.Fatalf("CreateRepoTransfer(%s): %v", name, err)
			}
			return r, tr
		}
		assertVoided := func(r *Repo, tr *RepoTransfer) {
			t.Helper()
			if _, err := s.AcceptRepoTransfer(ctx, tr.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
				t.Fatalf("AcceptRepoTransfer = %v, want ErrTransferNotPending", err)
			}
			got, err := s.GetRepoTransfer(ctx, tr.ID)
			if err != nil {
				t.Fatalf("GetRepoTransfer: %v", err)
			}
			if got.Status != "cancelled" {
				t.Errorf("status = %q, want cancelled", got.Status)
			}
			if back, err := s.GetRepoByID(ctx, r.ID); err != nil || back.Namespace != r.Namespace {
				t.Fatalf("repository = %+v, %v; want it still in acme", back, err)
			}
		}

		// Removed from the source organisation.
		r1, t1 := file("acme", "crown-jewels", f.alice)
		if err := s.RemoveOrgMember(ctx, acme.ID, f.alice.ID); err != nil {
			t.Fatalf("RemoveOrgMember: %v", err)
		}
		assertVoided(r1, t1)

		// Demoted to write: still a member, no longer allowed to transfer out.
		if _, err := s.AddOrgMember(ctx, acme.ID, f.alice.ID, "admin", f.admin.ID); err != nil {
			t.Fatalf("re-add alice: %v", err)
		}
		r2, t2 := file("acme", "demoted", f.alice)
		if _, err := s.UpdateOrgMemberRole(ctx, acme.ID, f.alice.ID, "write"); err != nil {
			t.Fatalf("UpdateOrgMemberRole: %v", err)
		}
		assertVoided(r2, t2)

		// Suspended while still an admin.
		if _, err := s.UpdateOrgMemberRole(ctx, acme.ID, f.alice.ID, "admin"); err != nil {
			t.Fatalf("restore admin: %v", err)
		}
		r3, t3 := file("acme", "suspended", f.alice)
		if err := s.SetUserDisabled(ctx, "alice", true, f.admin.ID); err != nil {
			t.Fatalf("SetUserDisabled: %v", err)
		}
		assertVoided(r3, t3)

		// Archived since: refused, but the request survives the archive. The
		// requester is a site administrator holding no role in globex at all:
		// api.roleIn makes them admin everywhere, and the accept path must
		// agree with it.
		carol, err := s.CreateUser(ctx, "carol", "carol@example.com", "hash", false)
		if err != nil {
			t.Fatalf("create carol: %v", err)
		}
		if _, err := s.CreateOrg(ctx, "globex", carol.ID, OrgUpdate{}); err != nil {
			t.Fatalf("CreateOrg(globex): %v", err)
		}
		r4, t4 := file("globex", "archived", f.admin)
		if _, err := s.SetRepoArchived(ctx, r4.ID, true, f.admin.ID); err != nil {
			t.Fatalf("SetRepoArchived: %v", err)
		}
		if _, err := s.AcceptRepoTransfer(ctx, t4.ID, f.bob.ID); !errors.Is(err, ErrTransferRepoArchived) {
			t.Fatalf("AcceptRepoTransfer on an archive = %v, want ErrTransferRepoArchived", err)
		}
		if got, err := s.GetRepoTransfer(ctx, t4.ID); err != nil || got.Status != "pending" {
			t.Fatalf("transfer after a refused accept on an archive = %+v, %v; want still pending", got, err)
		}

		// ...and completes once the repository is unarchived.
		if _, err := s.SetRepoArchived(ctx, r4.ID, false, f.admin.ID); err != nil {
			t.Fatalf("unarchive: %v", err)
		}
		moved, err := s.AcceptRepoTransfer(ctx, t4.ID, f.bob.ID)
		if err != nil {
			t.Fatalf("AcceptRepoTransfer after unarchive: %v", err)
		}
		if moved.Namespace != "bob" {
			t.Fatalf("moved to %q, want bob", moved.Namespace)
		}
	})
}

// Two accepts that cross between the same two namespaces -- X moves a
// repository alice -> bob while Y moves one bob -> alice -- must both
// complete. The authority re-check used to lock the source namespace FOR
// UPDATE, and each move's foreign-key check on repositories.namespace_id takes
// FOR KEY SHARE on its destination, which is the other accept's source: a lock
// cycle Postgres broke by failing one side with 40P01. Row locks only exist on
// Postgres (SQLite serialises writers), so this is a Postgres-only case.
//
// X is driven by hand and parked right after its authority check, holding
// whatever that check locks; Y then has to run to completion past it. Under
// the old FOR UPDATE, Y blocks on X's row and the deadline below fires.
func TestIntegrationCrossingAcceptsDoNotDeadlock(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		if _, ok := s.d.(pgDialect); !ok {
			t.Skip("row locks are Postgres-only; SQLite serialises write transactions")
		}
		f := newFixture(t, s)
		ctx := f.ctx
		aliceNS, bobNS := f.ns(t, "alice"), f.ns(t, "bob")

		rX := f.repo(t, "alice", "outbound", "model", nil)
		tX, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: rX.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer(X): %v", err)
		}
		rY := f.repo(t, "bob", "inbound", "model", nil)
		tY, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: rY.ID, ToNamespaceID: aliceNS.ID, ActorID: f.bob.ID}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer(Y): %v", err)
		}

		txX, err := s.db.Begin(ctx)
		if err != nil {
			t.Fatalf("begin X: %v", err)
		}
		defer txX.Rollback(ctx) //nolint:errcheck
		if ok, err := requesterMayStillTransfer(ctx, txX, s.d, f.alice.ID, aliceNS.ID); err != nil || !ok {
			t.Fatalf("X authority check = %v, %v; want true", ok, err)
		}

		yctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		moved, err := s.AcceptRepoTransfer(yctx, tY.ID, f.alice.ID)
		if err != nil {
			t.Fatalf("AcceptRepoTransfer(Y) while X holds its authority check = %v; want it to complete", err)
		}
		if moved.Namespace != "alice" {
			t.Fatalf("Y moved to %q, want alice", moved.Namespace)
		}

		// X's own move now needs FOR KEY SHARE on bob's namespace, which Y
		// share-locked and has since released.
		if _, _, _, err := s.transferMove(ctx, txX, TransferSpec{RepoID: rX.ID, ToNamespaceID: bobNS.ID, ActorID: f.bob.ID}, tX.ID, time.Now()); err != nil {
			t.Fatalf("X move: %v", err)
		}
		if err := txX.Commit(ctx); err != nil {
			t.Fatalf("commit X: %v", err)
		}
		if back, err := s.GetRepoByID(ctx, rX.ID); err != nil || back.Namespace != "bob" {
			t.Fatalf("X repository = %+v, %v; want it in bob", back, err)
		}
	})
}

// The shared lock the authority re-check takes must still hold off what would
// change its answer: an org membership change (which locks the namespace row
// FOR UPDATE) and a suspension (an UPDATE of the requester's users row). Both
// have to wait for an accept that has already checked, so that the accept is
// ordered strictly before them. Postgres-only for the same reason as above.
func TestIntegrationAuthorityCheckHoldsOffRemovalAndSuspension(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		if _, ok := s.d.(pgDialect); !ok {
			t.Skip("row locks are Postgres-only; SQLite serialises write transactions")
		}
		f := newFixture(t, s)
		ctx := f.ctx
		// Two admins, so removing alice is not refused as ErrLastAdmin -- a
		// quick refusal must not pass for "it waited".
		acme, err := s.CreateOrg(ctx, "acme", f.admin.ID, OrgUpdate{})
		if err != nil {
			t.Fatalf("CreateOrg: %v", err)
		}
		if _, err := s.AddOrgMember(ctx, acme.ID, f.alice.ID, "admin", f.admin.ID); err != nil {
			t.Fatalf("AddOrgMember: %v", err)
		}

		held, err := s.db.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer held.Rollback(ctx) //nolint:errcheck
		if ok, err := requesterMayStillTransfer(ctx, held, s.d, f.alice.ID, acme.ID); err != nil || !ok {
			t.Fatalf("authority check = %v, %v; want true", ok, err)
		}

		mustWait := func(what string, op func(context.Context) error) {
			t.Helper()
			wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			err := op(wctx)
			if err == nil || wctx.Err() == nil {
				t.Fatalf("%s while an accept holds the authority check = %v; want it to wait until the deadline", what, err)
			}
		}
		mustWait("RemoveOrgMember", func(c context.Context) error { return s.RemoveOrgMember(c, acme.ID, f.alice.ID) })
		mustWait("SetUserDisabled", func(c context.Context) error { return s.SetUserDisabled(c, "alice", true, f.admin.ID) })

		if err := held.Rollback(ctx); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := s.RemoveOrgMember(ctx, acme.ID, f.alice.ID); err != nil {
			t.Fatalf("RemoveOrgMember once released: %v", err)
		}
	})
}
