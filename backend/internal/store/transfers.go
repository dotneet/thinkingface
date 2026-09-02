package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTransferNotPending is returned by the decision methods
// (AcceptRepoTransfer / RejectRepoTransfer / CancelRepoTransfer) when the
// transfer's status is no longer "pending", including when it has just been
// found past its expires_at (which flips it to "expired" as a side effect).
var ErrTransferNotPending = errors.New("store: transfer is not pending")

// RepoTransfer is one row of the transfer/rename audit trail
// (docs/dev/repo-transfer-design.md §4, §7): either a completed immediate move
// (status "accepted" from the moment it is created) or a request awaiting
// the target namespace's decision.
type RepoTransfer struct {
	ID     int64
	RepoID int64
	// Kind is the repository's kind ("model" | "dataset").
	Kind            string
	FromNamespaceID int64
	FromNamespace   string // namespaces.name, joined
	// FromName is the repository's name at request time, so this row still
	// reads sensibly after the repository has since moved again.
	FromName        string
	ToNamespaceID   int64
	ToNamespace     string // namespaces.name, joined
	ToName          string
	RequestedBy     int64
	RequestedByName string // users.username, joined
	Status          string // pending | accepted | rejected | cancelled | expired
	DecidedBy       *int64
	DecidedAt       *time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// TransferSpec is what both the immediate move and the pending-request path
// need to identify a transfer.
type TransferSpec struct {
	RepoID        int64
	ToNamespaceID int64
	// ToName is the repository's name after the move; "" keeps the current
	// name (a transfer with no rename).
	ToName string
	// ActorID is the user performing (TransferRepo) or requesting
	// (CreateRepoTransfer) the move.
	ActorID int64
}

// namespaceRef is the minimal identity of a namespace, looked up by id
// inside a transfer transaction.
type namespaceRef struct {
	ID   int64
	Name string
}

func getNamespaceRef(ctx context.Context, ex executor, id int64) (namespaceRef, error) {
	var n namespaceRef
	err := ex.QueryRow(ctx, `SELECT id, name FROM namespaces WHERE id = $1`, id).Scan(&n.ID, &n.Name)
	if err != nil {
		return namespaceRef{}, norm(err)
	}
	return n, nil
}

// repoLocation is where a repository lives at the moment a transfer looks at
// it, read under the row lock so nothing moves underneath the decision.
type repoLocation struct {
	NamespaceID int64
	Namespace   string // namespaces.name, joined
	Name        string
	Kind        string
}

// resolveTransferTarget locks the repository and works out where a transfer
// would put it, applying every rule the two entry points share: an empty
// ToName keeps the current name, both namespaces must exist, a move that
// changes nothing is ErrConflict, and so is a destination (namespace, name,
// kind) something else already occupies.
//
// One function because the immediate move (transferMove) and the pending
// request (CreateRepoTransfer) have to agree about all of it -- a request
// filed on terms the accept path would then refuse is a request that should
// never have been accepted in the first place. The two copies had already
// drifted: only one of them read the source namespace at all.
//
// Errors: ErrNotFound when the repository or either namespace is missing;
// ErrConflict for the no-op and the taken destination.
func resolveTransferTarget(ctx context.Context, ex executor, d dialect, spec TransferSpec) (cur repoLocation, toNS namespaceRef, toName string, err error) {
	fail := func(err error) (repoLocation, namespaceRef, string, error) {
		return repoLocation{}, namespaceRef{}, "", err
	}

	if err = ex.QueryRow(ctx,
		`SELECT namespace_id, name, kind FROM repositories WHERE id = $1`+d.forUpdate(""), spec.RepoID,
	).Scan(&cur.NamespaceID, &cur.Name, &cur.Kind); err != nil {
		return fail(norm(err))
	}

	toName = spec.ToName
	if toName == "" {
		toName = cur.Name
	}

	if toNS, err = getNamespaceRef(ctx, ex, spec.ToNamespaceID); err != nil {
		return fail(err)
	}
	fromNS, err := getNamespaceRef(ctx, ex, cur.NamespaceID)
	if err != nil {
		return fail(err)
	}
	cur.Namespace = fromNS.Name

	if toNS.ID == cur.NamespaceID && toName == cur.Name {
		return fail(ErrConflict)
	}

	var exists bool
	if err = ex.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM repositories WHERE namespace_id = $1 AND name = $2 AND kind = $3 AND id <> $4)`,
		toNS.ID, toName, cur.Kind, spec.RepoID).Scan(&exists); err != nil {
		return fail(err)
	}
	if exists {
		return fail(ErrConflict)
	}
	return cur, toNS, toName, nil
}

const repoTransferColumns = `t.id, t.repo_id, r.kind, t.from_namespace_id, fn.name, t.from_name,
	t.to_namespace_id, tn.name, t.to_name, t.requested_by, u.username, t.status,
	t.decided_by, t.decided_at, t.expires_at, t.created_at`

const repoTransferFrom = `repo_transfers t
	JOIN repositories r ON r.id = t.repo_id
	JOIN namespaces fn ON fn.id = t.from_namespace_id
	JOIN namespaces tn ON tn.id = t.to_namespace_id
	JOIN users u ON u.id = t.requested_by`

func scanRepoTransfer(row rowScanner) (*RepoTransfer, error) {
	t := &RepoTransfer{}
	err := row.Scan(&t.ID, &t.RepoID, &t.Kind, &t.FromNamespaceID, &t.FromNamespace, &t.FromName,
		&t.ToNamespaceID, &t.ToNamespace, &t.ToName, &t.RequestedBy, &t.RequestedByName, &t.Status,
		&t.DecidedBy, &t.DecidedAt, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return nil, norm(err)
	}
	return t, nil
}

// transferMove performs the physical relocation of a repository: updating
// repositories.namespace_id/name, leaving a redirect at the old name,
// rewriting repo_lineage targets that pointed at the old name and dropping
// repo-scoped webhooks when the owner actually changes --
// docs/dev/repo-transfer-design.md §7.1's steps between
// the repositories UPDATE and the repo_transfers INSERT. Object storage needs
// no step of its own: every key is content-addressed, so not one byte moves
// when a repository changes hands. It does not touch repo_transfers itself:
// TransferRepo inserts a fresh 'accepted' row after this returns, while
// AcceptRepoTransfer instead flips its existing pending row, so the two
// callers share this and diverge only on that last step.
//
// Any other pending transfer for the repository is cancelled here: once the
// repository has moved, a request filed against its previous location must
// not be accepted later and pull it out from under the new owner.
// keepTransferID names the row the caller is about to flip to 'accepted'
// itself (AcceptRepoTransfer); TransferRepo passes 0 to cancel every pending
// row.
//
// Errors: ErrNotFound when the repository or the target namespace does not
// exist; ErrConflict when the destination (namespace, name, kind) is already
// taken, or when the move is a no-op (same namespace and name).
func (s *Store) transferMove(ctx context.Context, ex executor, spec TransferSpec, keepTransferID int64, now time.Time) (repo *Repo, fromNamespaceID int64, fromName string, err error) {
	cur, toNS, toName, err := resolveTransferTarget(ctx, ex, s.d, spec)
	if err != nil {
		return nil, 0, "", err
	}
	repoNSID, repoName, repoKind := cur.NamespaceID, cur.Name, cur.Kind

	if _, err = ex.Exec(ctx,
		`UPDATE repositories SET namespace_id = $2, name = $3, updated_at = $4 WHERE id = $1`,
		spec.RepoID, toNS.ID, toName, now); err != nil {
		return nil, 0, "", err
	}

	// Leave a redirect at the old name, repointed to this repository even if
	// it was already someone else's former name (multi-hop moves all
	// resolve to the latest name).
	if _, err = ex.Exec(ctx,
		`INSERT INTO repo_redirects (kind, from_namespace, from_name, repo_id, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (kind, from_namespace, from_name) DO UPDATE SET repo_id = EXCLUDED.repo_id`,
		repoKind, cur.Namespace, repoName, spec.RepoID, now); err != nil {
		return nil, 0, "", err
	}

	// The new name is now a real repository again, so any redirect that used
	// to live there must go (docs/dev/repo-transfer-design.md §5 "conflicts").
	if _, err = ex.Exec(ctx,
		`DELETE FROM repo_redirects WHERE kind = $1 AND LOWER(from_namespace) = LOWER($2) AND from_name = $3`,
		repoKind, toNS.Name, toName); err != nil {
		return nil, 0, "", err
	}

	// Only edges that point at a repository of this kind follow the move: a
	// model and a dataset may share a name, and `base_model` edges name
	// models while `dataset` / `run` edges name datasets (see lineage.go).
	// The namespace half of the target is card-authored text, so it is
	// matched folded, exactly as ListLineageDependents finds these edges: an
	// edge written `Alice/foo` describes the repository being moved just as
	// much as `alice/foo` does, and leaving it behind would dangle it.
	// `new_version` is the exception -- it targets whatever kind declared it
	// (LineageEdge.TargetKind) -- so its kind is read from the source row,
	// the same expression ListRepoLineage resolves the edge with. Lumping it
	// in with the datasets both stranded a moved model's successors and let a
	// moved dataset rewrite the edges between two same-named models.
	if _, err = ex.Exec(ctx,
		`UPDATE repo_lineage SET target_namespace = $3, target_name = $4, updated_at = $5
		 WHERE LOWER(target_namespace) = LOWER($1) AND target_name = $2
		   AND (CASE edge_kind
		          WHEN 'base_model' THEN 'model'
		          WHEN 'new_version' THEN (SELECT src.kind FROM repositories src WHERE src.id = repo_lineage.repo_id)
		          ELSE 'dataset' END) = $6`,
		cur.Namespace, repoName, toNS.Name, toName, now, repoKind); err != nil {
		return nil, 0, "", err
	}

	// A pending request filed against the old location is void now.
	if _, err = ex.Exec(ctx,
		`UPDATE repo_transfers SET status = 'cancelled', decided_by = $2, decided_at = $3
		 WHERE repo_id = $1 AND status = 'pending' AND id <> $4`,
		spec.RepoID, spec.ActorID, now, keepTransferID); err != nil {
		return nil, 0, "", err
	}

	// A repository-scoped webhook subscription belonged to the old owner;
	// keeping it would leak events about a repository they no longer own.
	// A rename inside the same namespace has no old owner, so it keeps its
	// subscriptions: the reason to drop them never arises, and dropping them
	// would silently destroy configuration on what is, to the person doing
	// it, just a change of name.
	if spec.ToNamespaceID != repoNSID {
		if _, err = ex.Exec(ctx, `DELETE FROM webhooks WHERE repo_id = $1`, spec.RepoID); err != nil {
			return nil, 0, "", err
		}
	}

	repo, err = scanRepo(ex.QueryRow(ctx,
		repoSelect+` WHERE r.id = $1`,
		spec.RepoID))
	if err != nil {
		return nil, 0, "", err
	}
	return repo, cur.NamespaceID, repoName, nil
}

// TransferRepo performs an immediate move in one transaction
// (docs/dev/repo-transfer-design.md §7.1): the caller already has write access
// to both the source and destination namespaces (or is a server admin), so
// there is nothing left to wait on. It records one 'accepted'
// repo_transfers row and returns the repository at its new name.
func (s *Store) TransferRepo(ctx context.Context, spec TransferSpec) (*Repo, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	now := time.Now()
	repo, fromNSID, fromName, err := s.transferMove(ctx, tx, spec, 0, now)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO repo_transfers (repo_id, from_namespace_id, from_name, to_namespace_id, to_name,
		                             requested_by, status, decided_by, decided_at, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'accepted', $6, $7, $7, $7)`,
		spec.RepoID, fromNSID, fromName, repo.NamespaceID, repo.Name, spec.ActorID, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

// CreateRepoTransfer records a pending request that must be accepted by
// someone with write access to the target namespace
// (docs/dev/repo-transfer-design.md §7.2). ErrConflict when a pending transfer
// already exists for the repository (the unique partial index enforces this
// under a race) or the target (namespace, name, kind) is already taken, or
// when the request is a no-op; ErrNotFound when the repository or target
// namespace does not exist.
func (s *Store) CreateRepoTransfer(ctx context.Context, spec TransferSpec, ttl time.Duration) (*RepoTransfer, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	cur, toNS, toName, err := resolveTransferTarget(ctx, tx, s.d, spec)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO repo_transfers (repo_id, from_namespace_id, from_name, to_namespace_id, to_name,
		                             requested_by, status, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8) RETURNING id`,
		spec.RepoID, cur.NamespaceID, cur.Name, toNS.ID, toName, spec.ActorID, now.Add(ttl), now,
	).Scan(&id)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}

	t, err := scanRepoTransfer(tx.QueryRow(ctx,
		`SELECT `+repoTransferColumns+` FROM `+repoTransferFrom+` WHERE t.id = $1`, id))
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// GetRepoTransfer loads one transfer by id.
func (s *Store) GetRepoTransfer(ctx context.Context, id int64) (*RepoTransfer, error) {
	return scanRepoTransfer(s.db.QueryRow(ctx,
		`SELECT `+repoTransferColumns+` FROM `+repoTransferFrom+` WHERE t.id = $1`, id))
}

// PendingRepoTransfer returns the pending, unexpired transfer for a
// repository, for the settings-page banner. ErrNotFound when there is none.
func (s *Store) PendingRepoTransfer(ctx context.Context, repoID int64) (*RepoTransfer, error) {
	return scanRepoTransfer(s.db.QueryRow(ctx,
		`SELECT `+repoTransferColumns+` FROM `+repoTransferFrom+`
		 WHERE t.repo_id = $1 AND t.status = 'pending' AND t.expires_at > $2`, repoID, time.Now()))
}

// ListRepoTransfersForUser splits the pending, unexpired transfers relevant
// to a user: incoming are the ones they could accept or reject (write
// access to the target namespace: its owner, or an org member with role
// admin/write), outgoing are the ones they could cancel (the same, for the
// source namespace). A pending row past its expires_at is treated as if it
// were not pending; it is flipped to 'expired' lazily on first touch by the
// decision methods rather than through a cleanup job
// (docs/dev/repo-transfer-design.md §7.2).
func (s *Store) ListRepoTransfersForUser(ctx context.Context, userID int64) (incoming, outgoing []RepoTransfer, err error) {
	now := time.Now()

	incoming, err = s.queryRepoTransfers(ctx,
		`t.status = 'pending' AND t.expires_at > $2 AND `+namespaceWritable("t.to_namespace_id"), userID, now)
	if err != nil {
		return nil, nil, fmt.Errorf("list incoming transfers: %w", err)
	}
	outgoing, err = s.queryRepoTransfers(ctx,
		`t.status = 'pending' AND t.expires_at > $2 AND `+namespaceWritable("t.from_namespace_id"), userID, now)
	if err != nil {
		return nil, nil, fmt.Errorf("list outgoing transfers: %w", err)
	}
	return incoming, outgoing, nil
}

func (s *Store) queryRepoTransfers(ctx context.Context, where string, args ...any) ([]RepoTransfer, error) {
	return collect(ctx, s.db,
		`SELECT `+repoTransferColumns+` FROM `+repoTransferFrom+` WHERE `+where+` ORDER BY t.created_at DESC`,
		args,
		func(row rowScanner) (RepoTransfer, error) {
			t, err := scanRepoTransfer(row)
			if err != nil {
				return RepoTransfer{}, err
			}
			return *t, nil
		})
}

// AcceptRepoTransfer completes a pending transfer: it locks the transfer
// row, requires it to still be pending and unexpired (an expired row is
// flipped to 'expired' as a side effect and reported as
// ErrTransferNotPending), then runs the same physical move as TransferRepo
// and flips the row to 'accepted' (docs/dev/repo-transfer-design.md §7.2). A
// request whose from-location no longer matches the repository (it was moved
// or renamed in the meantime) is voided -- status 'cancelled' -- and reported
// as ErrTransferNotPending instead of being executed.
func (s *Store) AcceptRepoTransfer(ctx context.Context, id, actorID int64) (*Repo, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock order is repository first, then the transfer row -- the same order
	// TransferRepo takes (it locks the repository, then updates pending
	// transfer rows), so the two cannot deadlock. The first read is only to
	// learn which repository to lock; everything is re-read under the lock.
	var repoID int64
	if err := tx.QueryRow(ctx, `SELECT repo_id FROM repo_transfers WHERE id = $1`, id).Scan(&repoID); err != nil {
		return nil, norm(err)
	}
	var curNSID int64
	var curName string
	if err := tx.QueryRow(ctx,
		`SELECT namespace_id, name FROM repositories WHERE id = $1`+s.d.forUpdate(""), repoID,
	).Scan(&curNSID, &curName); err != nil {
		return nil, norm(err)
	}

	var fromNSID, toNamespaceID int64
	var fromName, toName, status string
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT from_namespace_id, from_name, to_namespace_id, to_name, status, expires_at
		 FROM repo_transfers WHERE id = $1`+s.d.forUpdate(""), id,
	).Scan(&fromNSID, &fromName, &toNamespaceID, &toName, &status, &expiresAt)
	if err != nil {
		return nil, norm(err)
	}

	now := time.Now()
	if expired, err := expireIfPastDue(ctx, tx, id, status, expiresAt, now); expired || err != nil {
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrTransferNotPending
	}

	// The request described a move *from* a specific location. If the
	// repository has since been moved or renamed by its owner, the request
	// no longer means what the destination is agreeing to: void it instead
	// of pulling the repository out of wherever it lives now.
	if curNSID != fromNSID || curName != fromName {
		if _, err := tx.Exec(ctx,
			`UPDATE repo_transfers SET status = 'cancelled', decided_by = $2, decided_at = $3 WHERE id = $1`,
			id, actorID, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrTransferNotPending
	}

	repo, _, _, err := s.transferMove(ctx, tx, TransferSpec{RepoID: repoID, ToNamespaceID: toNamespaceID, ToName: toName, ActorID: actorID}, id, now)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE repo_transfers SET status = 'accepted', decided_by = $2, decided_at = $3 WHERE id = $1`,
		id, actorID, now); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

// RejectRepoTransfer marks a pending transfer as rejected.
// ErrTransferNotPending when it is not pending (including an expired row,
// which this flips to 'expired').
func (s *Store) RejectRepoTransfer(ctx context.Context, id, actorID int64) error {
	return s.decideRepoTransfer(ctx, id, actorID, "rejected")
}

// CancelRepoTransfer marks a pending transfer as cancelled (the transfer's
// originator changed their mind). ErrTransferNotPending when it is not
// pending (including an expired row, which this flips to 'expired').
func (s *Store) CancelRepoTransfer(ctx context.Context, id, actorID int64) error {
	return s.decideRepoTransfer(ctx, id, actorID, "cancelled")
}

// decideRepoTransfer backs Reject/CancelRepoTransfer, which differ only in
// the terminal status they record.
func (s *Store) decideRepoTransfer(ctx context.Context, id, actorID int64, newStatus string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status string
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT status, expires_at FROM repo_transfers WHERE id = $1`+s.d.forUpdate(""), id,
	).Scan(&status, &expiresAt)
	if err != nil {
		return norm(err)
	}

	now := time.Now()
	if expired, err := expireIfPastDue(ctx, tx, id, status, expiresAt, now); expired || err != nil {
		if err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrTransferNotPending
	}

	if _, err := tx.Exec(ctx,
		`UPDATE repo_transfers SET status = $2, decided_by = $3, decided_at = $4 WHERE id = $1`,
		id, newStatus, actorID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// expireIfPastDue reports whether the transfer identified by id should be
// treated as not-pending: either its status already isn't 'pending', or it
// is but expires_at is at or before now, in which case this also flips the
// row to 'expired' so the lazily-discovered expiry is persisted.
func expireIfPastDue(ctx context.Context, tx tx, id int64, status string, expiresAt, now time.Time) (bool, error) {
	if status != "pending" {
		return true, nil
	}
	if expiresAt.After(now) {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_transfers SET status = 'expired' WHERE id = $1`, id); err != nil {
		return true, err
	}
	return true, nil
}
