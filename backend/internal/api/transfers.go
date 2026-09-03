// Repository ownership transfer: the HF-compatible POST /api/repos/move and
// the web UI's own transfer/accept/reject/cancel endpoints, all funneled
// through startTransfer so the authorization and completion rules
// (docs/dev/repo-transfer-design.md §5-§7) live in one place.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// transferTTL is how long a pending transfer request waits for the
// destination namespace to decide before it is treated as expired
// (docs/dev/repo-transfer-design.md §7.2). The design leaves room to make this
// configurable later; for now it is a constant like the rest of the
// implementation it landed with.
const transferTTL = 7 * 24 * time.Hour

// forbiddenError marks a transfer failure caused by the actor lacking the
// right role, as opposed to a bad request or a genuine server error.
type forbiddenError struct{ msg string }

func (e forbiddenError) Error() string { return e.msg }

// startTransfer is the shared implementation behind POST /api/repos/move and
// POST /api/v1/repos/{kind}/{ns}/{name}/transfer (docs/dev/repo-transfer-design.md
// §5-§7). It authorizes the actor against the source namespace, decides
// whether the actor may also write the destination (completing the move
// immediately) or not (filing a pending request), performs the move, and
// fires the corresponding webhook. moved is non-nil only when the transfer
// completed immediately; transfer always describes the resulting
// repo_transfers row (synthesized from data already in hand for the
// immediate path, since store.TransferRepo does not hand back the row it
// inserts).
func (s *Server) startTransfer(ctx context.Context, actor *store.User, repo *store.Repo, toNamespace, toName string) (moved *store.Repo, transfer apitypes.RepoTransfer, pending bool, err error) {
	if toName == "" {
		toName = repo.Name
	}
	if verr := validateName(toName); verr != nil {
		return nil, apitypes.RepoTransfer{}, false, badInput("name %s", verr)
	}
	if verr := validateName(toNamespace); verr != nil {
		return nil, apitypes.RepoTransfer{}, false, badInput("namespace %s", verr)
	}

	// The source: write access, but an org source restricts this to admin
	// (docs/dev/repo-transfer-design.md §5 "permissions") so a write member cannot
	// carry a repository out from under the organisation. A personal namespace's
	// only "admin" is its owner, so this reads the same for both cases.
	role, rerr := s.roleIn(ctx, actor, repo.Namespace)
	if rerr != nil {
		return nil, apitypes.RepoTransfer{}, false, rerr
	}
	if role < RoleAdmin {
		return nil, apitypes.RepoTransfer{}, false,
			forbiddenError{"you must have admin access to " + repo.Namespace + " to transfer " + repo.FullName()}
	}

	toNS, err := s.store.GetNamespace(ctx, toNamespace)
	if err != nil {
		return nil, apitypes.RepoTransfer{}, false, err
	}

	// The destination: an actor who could create a repository there
	// completes the move immediately; anyone else's request waits for that
	// namespace's approval.
	destRole, err := s.roleIn(ctx, actor, toNamespace)
	if err != nil {
		return nil, apitypes.RepoTransfer{}, false, err
	}
	canDest := destRole >= RoleWrite

	kind, fromNS, fromName := repo.Kind, repo.Namespace, repo.Name
	now := time.Now()

	if canDest {
		moved, err = s.store.TransferRepo(ctx, store.TransferSpec{
			RepoID: repo.ID, ToNamespaceID: toNS.ID, ToName: toName, ActorID: actor.ID,
		})
		if err != nil {
			return nil, apitypes.RepoTransfer{}, false, err
		}
		s.fireWebhook(ctx, string(apitypes.WebhookEventRepoMoved), moved.Namespace, &moved.ID, map[string]any{
			"kind":      kind,
			"from":      apitypes.RepoLocation{Namespace: fromNS, Name: fromName},
			"to":        apitypes.RepoLocation{Namespace: moved.Namespace, Name: moved.Name},
			"full_name": moved.FullName(),
		})
		s.auditTransfer(ctx, actor, kind, fromNS, fromName, moved.Namespace, moved.Name)
		transfer = apitypes.RepoTransfer{
			Kind: apitypes.RepoKind(kind), FromNamespace: fromNS, FromName: fromName,
			ToNamespace: moved.Namespace, ToName: moved.Name, RequestedBy: actor.Username,
			Status: apitypes.RepoTransferAccepted, CreatedAt: now, ExpiresAt: now,
		}
		return moved, transfer, false, nil
	}

	created, err := s.store.CreateRepoTransfer(ctx, store.TransferSpec{
		RepoID: repo.ID, ToNamespaceID: toNS.ID, ToName: toName, ActorID: actor.ID,
	}, transferTTL)
	if err != nil {
		return nil, apitypes.RepoTransfer{}, false, err
	}
	s.fireWebhook(ctx, string(apitypes.WebhookEventRepoTransferRequested), toNamespace, &repo.ID, map[string]any{
		"transfer_id":  created.ID,
		"kind":         kind,
		"from":         apitypes.RepoLocation{Namespace: fromNS, Name: fromName},
		"to":           apitypes.RepoLocation{Namespace: toNamespace, Name: toName},
		"requested_by": actor.Username,
		"expires_at":   created.ExpiresAt,
	})
	return nil, toApitypesTransfer(created), true, nil
}

// auditTransfer records a completed move on whichever side of it is an
// organisation: the source logs repo.transferred_out, the destination
// repo.transferred_in (docs/dev/organization-design.md §5). A move inside a
// single organisation records both, which is what the log should show for a
// rename that stayed put.
func (s *Server) auditTransfer(ctx context.Context, actor *store.User, kind, fromNS, fromName, toNS, toName string) {
	from := fromNS + "/" + fromName
	to := toNS + "/" + toName
	s.auditNamespace(ctx, fromNS, actor, auditRepoTransferredOut, from,
		map[string]any{"kind": kind, "to": to})
	s.auditNamespace(ctx, toNS, actor, auditRepoTransferredIn, to,
		map[string]any{"kind": kind, "from": from})
}

// writeTransferError maps a startTransfer/decision failure onto a response.
func writeTransferError(w http.ResponseWriter, err error) {
	var bad inputError
	var forb forbiddenError
	switch {
	case errors.As(err, &forb):
		forbidden(w, forb.Error())
	case errors.As(err, &bad):
		badRequest(w, bad.Error())
	case errors.Is(err, store.ErrNotFound):
		notFound(w, "destination namespace not found")
	case errors.Is(err, store.ErrConflict):
		conflict(w, "the destination already has a repository with that name, or a transfer is already pending")
	default:
		internalError(w, "transfer repository", err)
	}
}

func writeTransferNotPending(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "transfer_not_pending", "transfer is no longer pending")
}

func toApitypesTransfer(t *store.RepoTransfer) apitypes.RepoTransfer {
	return apitypes.RepoTransfer{
		ID: t.ID, Kind: apitypes.RepoKind(t.Kind), FromNamespace: t.FromNamespace, FromName: t.FromName,
		ToNamespace: t.ToNamespace, ToName: t.ToName, RequestedBy: t.RequestedByName,
		Status: apitypes.RepoTransferStatus(t.Status), CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	}
}

func toApitypesTransfers(ts []store.RepoTransfer) []apitypes.RepoTransfer {
	out := make([]apitypes.RepoTransfer, 0, len(ts))
	for i := range ts {
		out = append(out, toApitypesTransfer(&ts[i]))
	}
	return out
}

// -------------------------------------------------------- HF-compatible

// splitRepoID parses the "ns/name" shape huggingface_hub's move_repo sends
// for both fromRepo and toRepo.
func splitRepoID(id string) (ns, name string, ok bool) {
	ns, name, found := strings.Cut(id, "/")
	if !found || ns == "" || name == "" {
		return "", "", false
	}
	return ns, name, true
}

// handleHFMoveRepo implements huggingface_hub.HfApi.move_repo
// (docs/dev/repo-transfer-design.md §6). A same-namespace call is a rename, using
// the same path.
func (s *Server) handleHFMoveRepo(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		FromRepo string `json:"fromRepo"`
		ToRepo   string `json:"toRepo"`
		Type     string `json:"type"`
	}
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with fromRepo, toRepo and type") {
		return
	}
	// hfRepoType, not hfRepoTarget: move_repo names both repositories in full
	// ("ns/name" for each) and has no "organization" field, so there is no
	// caller's-own-namespace fallback to apply -- an unqualified name here is
	// the caller's mistake, which splitRepoID reports.
	kind := hfRepoType(req.Type)
	fromNS, fromName, ok := splitRepoID(req.FromRepo)
	if !ok {
		badRequest(w, "fromRepo must be namespace/name")
		return
	}
	toNS, toName, ok := splitRepoID(req.ToRepo)
	if !ok {
		badRequest(w, "toRepo must be namespace/name")
		return
	}

	// redirectNone, not redirectHF, for the same reason DELETE
	// /api/repos/delete uses it (repos.go, docs/dev/api-contract.md "Accessing
	// the old name"): the repository this route names is in the *body*, so
	// there is nothing in the path for movedLocation to rewrite. It would hand
	// back a 308 to the very URL that was just requested, and requests replays
	// a 308 with the same body -- so move_repo() called on an old name spins
	// until it raises TooManyRedirects instead of reporting that the name is
	// gone. Answering as though the old name never existed also keeps a caller
	// from moving, by accident, the repository that now sits at the new one.
	repo, ok := s.loadRepoForWrite(w, r, kind, fromNS, fromName, redirectNone)
	if !ok {
		return
	}

	moved, transfer, pending, err := s.startTransfer(r.Context(), user, repo, toNS, toName)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	if pending {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"url": s.repoWebURLFor(kind, toNS, toName), "pending": true, "transfer_id": transfer.ID,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": s.repoWebURL(moved)})
}

// -------------------------------------------------------------------- UI

// handleUIStartTransfer answers POST /api/v1/repos/{kind}/{ns}/{name}/transfer.
func (s *Server) handleUIStartTransfer(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	var req apitypes.RepoTransferRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a namespace") {
		return
	}
	if req.Namespace == "" {
		badRequest(w, "namespace is required")
		return
	}
	user := currentUser(r.Context())

	moved, transfer, pending, err := s.startTransfer(r.Context(), user, repo, req.Namespace, req.Name)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	if pending {
		writeJSON(w, http.StatusAccepted, apitypes.RepoTransferResponse{Transfer: transfer})
		return
	}
	detail := s.buildDetail(r.Context(), moved)
	writeJSON(w, http.StatusOK, apitypes.RepoTransferResponse{Transfer: transfer, Repo: &detail})
}

// handleGetTransfer answers GET .../transfer: the pending transfer for the
// settings-page banner, if any. Archived repositories answer it too -- the
// settings page loads this on every visit, and a pending transfer filed
// before the archive still needs to be visible and cancellable.
func (s *Server) handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	t, err := s.store.PendingRepoTransfer(r.Context(), repo.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no pending transfer")
			return
		}
		internalError(w, "load transfer", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.RepoTransferResponse{Transfer: toApitypesTransfer(t)})
}

// handleCancelTransfer answers DELETE .../transfer: whoever could have
// started the transfer changed their mind. Allowed on an archived repository:
// cancelling withdraws a request rather than changing anything about the
// repository.
//
// "Cancel = whoever could start it" (docs/dev/repo-transfer-design.md §7), so
// the bar is admin on the source namespace, exactly as startTransfer requires.
// The write-access gate this used to stop at was looser than the one it undoes:
// under an organisation, a `write` member who may not transfer a repository out
// could still cancel the admin's pending request.
func (s *Server) handleCancelTransfer(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	// After the load, so this answers 403 only to someone who could already
	// see the repository -- and reads the *current* namespace, which is the
	// source of the pending transfer (a repository only leaves it once the
	// transfer is accepted).
	if !s.canAdmin(r.Context(), repo) {
		forbidden(w, "you must have admin access to "+repo.Namespace+
			" to cancel a transfer of "+repo.FullName())
		return
	}
	t, err := s.store.PendingRepoTransfer(r.Context(), repo.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no pending transfer")
			return
		}
		internalError(w, "load transfer", err)
		return
	}
	user := currentUser(r.Context())
	if err := s.store.CancelRepoTransfer(r.Context(), t.ID, user.ID); err != nil {
		if errors.Is(err, store.ErrTransferNotPending) {
			writeTransferNotPending(w)
			return
		}
		internalError(w, "cancel transfer", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMyTransfers answers GET /api/v1/me/transfers: the pending transfers
// that are the caller's own to act on -- ones aimed at, or leaving, a
// namespace they personally own or hold org admin/write in.
//
// That is narrower than what handleDecideTransfer below authorises, on
// purpose. A site admin is RoleAdmin in every namespace (roleIn) and may
// therefore decide any transfer by id, but this endpoint is an inbox, not a
// capability list: listing them the whole instance meant every pending
// transfer appeared here twice (the same account matches both the source and
// the destination side) and left a permanent count in the header badge for
// requests between two strangers. store.ListRepoTransfersForUser holds the
// rule and the reasoning.
func (s *Server) handleMyTransfers(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	incoming, outgoing, err := s.store.ListRepoTransfersForUser(r.Context(), user.ID)
	if err != nil {
		internalError(w, "list transfers", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.MyTransfersResponse{
		Incoming: toApitypesTransfers(incoming),
		Outgoing: toApitypesTransfers(outgoing),
	})
}

// handleAcceptTransfer and handleRejectTransfer answer
// POST /api/v1/transfers/{id}/accept and .../reject. Both require write
// access to the destination namespace: its owner, or an org admin/write
// member (docs/dev/repo-transfer-design.md §5 "accept / reject") -- and a site
// admin, whom roleIn answers RoleAdmin for everywhere, which is what lets one
// unstick a request whose destination has gone unresponsive. They reach such a
// transfer by its id, from the repository's settings page or an operator's
// notes; handleMyTransfers deliberately does not list other people's requests
// at them.
func (s *Server) handleAcceptTransfer(w http.ResponseWriter, r *http.Request) {
	s.handleDecideTransfer(w, r, true)
}

func (s *Server) handleRejectTransfer(w http.ResponseWriter, r *http.Request) {
	s.handleDecideTransfer(w, r, false)
}

func (s *Server) handleDecideTransfer(w http.ResponseWriter, r *http.Request, accept bool) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	id, ok := int64Param(w, r, "id", "transfer")
	if !ok {
		return
	}
	t, err := s.store.GetRepoTransfer(r.Context(), id)
	if err != nil {
		handleStoreError(w, "load transfer", err)
		return
	}

	destRole, err := s.roleIn(r.Context(), user, t.ToNamespace)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if destRole < RoleWrite {
		// Not a 403 naming the namespace: the transfer is fetched by numeric
		// id before the permission check, so a distinguishable answer here
		// lets anyone walk the ids and read off every pending destination.
		notFound(w, "transfer not found")
		return
	}

	if !accept {
		if err := s.store.RejectRepoTransfer(r.Context(), id, user.ID); err != nil {
			if errors.Is(err, store.ErrTransferNotPending) {
				writeTransferNotPending(w)
				return
			}
			internalError(w, "reject transfer", err)
			return
		}
		writeJSON(w, http.StatusOK, apitypes.RepoTransferResponse{Transfer: apitypes.RepoTransfer{
			ID: t.ID, Kind: apitypes.RepoKind(t.Kind), FromNamespace: t.FromNamespace, FromName: t.FromName,
			ToNamespace: t.ToNamespace, ToName: t.ToName, RequestedBy: t.RequestedByName,
			Status: apitypes.RepoTransferRejected, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		}})
		return
	}

	repo, err := s.store.AcceptRepoTransfer(r.Context(), id, user.ID)
	if err != nil {
		if errors.Is(err, store.ErrTransferNotPending) {
			writeTransferNotPending(w)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			conflict(w, "the destination already has a repository with that name")
			return
		}
		internalError(w, "accept transfer", err)
		return
	}
	s.fireWebhook(r.Context(), string(apitypes.WebhookEventRepoMoved), repo.Namespace, &repo.ID, map[string]any{
		"kind":      repo.Kind,
		"from":      apitypes.RepoLocation{Namespace: t.FromNamespace, Name: t.FromName},
		"to":        apitypes.RepoLocation{Namespace: repo.Namespace, Name: repo.Name},
		"full_name": repo.FullName(),
	})
	s.auditTransfer(r.Context(), user, repo.Kind, t.FromNamespace, t.FromName, repo.Namespace, repo.Name)
	detail := s.buildDetail(r.Context(), repo)
	writeJSON(w, http.StatusOK, apitypes.RepoTransferResponse{
		Transfer: apitypes.RepoTransfer{
			ID: t.ID, Kind: apitypes.RepoKind(t.Kind), FromNamespace: t.FromNamespace, FromName: t.FromName,
			ToNamespace: repo.Namespace, ToName: repo.Name, RequestedBy: t.RequestedByName,
			Status: apitypes.RepoTransferAccepted, CreatedAt: t.CreatedAt, ExpiresAt: time.Now(),
		},
		Repo: &detail,
	})
}
