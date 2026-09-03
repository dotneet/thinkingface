// Repository CRUD: creating and deleting repositories from both the UI API
// and the HuggingFace-compatible endpoints, plus the summary and detail
// shapes those handlers return.

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const maxReadmeBytes = 256 << 10

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

// inputError marks a createRepo failure the caller caused and may therefore
// be told about verbatim. Everything else -- database faults, a failed
// `git init`, storage errors -- carries text naming on-disk paths and
// connection details, so it is logged and reported as an internal error
// instead of echoed into the response body.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func badInput(format string, args ...any) error {
	return inputError{msg: fmt.Sprintf(format, args...)}
}

func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return errors.New("must be 1-96 characters of letters, digits, dot, dash or underscore, and start with a letter or digit")
	}
	if strings.HasSuffix(name, ".git") {
		return errors.New("must not end in .git")
	}
	return nil
}

// validateEmail applies the loosest check that still rules out the values
// that cause trouble downstream: an empty address (accounts were creatable
// without one), something that is not an address at all, and a value long
// enough to be a payload rather than an identifier. Deliverability is not
// this server's business -- there is no mail flow to verify against.
func validateEmail(email string) error {
	if email == "" {
		return errors.New("must not be empty")
	}
	if len(email) > 254 {
		return errors.New("must be at most 254 bytes")
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") ||
		!strings.Contains(domain, ".") || strings.ContainsAny(email, " \t\r\n") {
		return errors.New("must look like an email address")
	}
	return nil
}

// kindFromURL maps the plural URL segment to the stored kind.
func kindFromURL(seg string) string {
	if strings.HasPrefix(seg, "model") {
		return "model"
	}
	return "dataset"
}

func kindPlural(kind string) string {
	if kind == "model" {
		return "models"
	}
	return "datasets"
}

// ------------------------------------------------------------- UI responses
//
// The response shapes themselves live in internal/apitypes, which is what the
// frontend's TypeScript types are generated from.

func toSummary(r *store.Repo) apitypes.RepoSummary {
	return apitypes.RepoSummary{
		ID: r.ID, Kind: apitypes.RepoKind(r.Kind), Namespace: r.Namespace,
		NamespaceKind: apitypes.NamespaceKind(r.NamespaceKind),
		Name:          r.Name, FullName: r.FullName(),
		Description: r.Description, Tags: r.Tags(), License: r.License(),
		Downloads: r.Downloads, TotalSize: r.TotalSize, NumFiles: r.NumFiles,
		IsExperiment: r.IsExperiment, DefaultBranch: r.DefaultBranch, HeadSHA: r.HeadSHA,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Archived: r.Archived(), ArchivedAt: r.ArchivedAt,
	}
}

// toParquetSummaries drops the stored column schema, which the repository page
// never shows and which would bloat every detail response.
func toParquetSummaries(files []store.ParquetFile) []apitypes.ParquetSummary {
	out := make([]apitypes.ParquetSummary, 0, len(files))
	for _, f := range files {
		out = append(out, apitypes.ParquetSummary{
			Path: f.Path, NumRows: f.NumRows, NumRowGroups: f.NumRowGroups,
			NumColumns: f.NumColumns, Size: f.Size,
		})
	}
	return out
}

func (s *Server) cloneURL(r *store.Repo) string {
	return fmt.Sprintf("%s/%s/%s/%s.git", s.cfg.PublicURL, kindPlural(r.Kind), r.Namespace, r.Name)
}

// sshCloneURL builds the git-over-SSH remote documented in
// docs/users/guides/git.md ("ssh://git@host:port/{kind}s/{ns}/{name}.git"),
// or "" when the SSH listener is off.
//
// Neither half comes from TF_SSH_ADDR by preference: that is a *listen*
// address (":2222", "0.0.0.0:2222"), and a listen address describes where the
// process binds, not where anyone can reach it. The host is TF_PUBLIC_URL's,
// and the port is TF_SSH_PUBLIC_PORT when set -- compose, Kubernetes and a
// load balancer all routinely publish the listener on a different port, and
// advertising the internal one would hand every user a URL that does not
// connect. TF_SSH_ADDR's port is the fallback because it is right whenever
// nothing remaps it, which covers running the binary directly. Port 22 stays
// implicit so the URL keeps the short form people expect.
func (s *Server) sshCloneURL(r *store.Repo) string {
	if !s.cfg.SSHEnabled {
		return ""
	}
	host := publicHost(s.cfg.PublicURL)
	if host == "" {
		return ""
	}
	port := s.cfg.SSHPublicPort
	if port == "" {
		port = sshPort(s.cfg.SSHAddr)
	}
	if port != "" && port != "22" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		// A bare IPv6 literal needs its brackets back, which Hostname() removed.
		host = "[" + host + "]"
	}
	return fmt.Sprintf("ssh://git@%s/%s/%s/%s.git", host, kindPlural(r.Kind), r.Namespace, r.Name)
}

// publicHost is TF_PUBLIC_URL's hostname, with any HTTP port dropped: the SSH
// listener has its own. An IPv6 literal keeps its brackets so the result can be
// re-joined with a port.
func publicHost(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// sshPort is the port half of a listen address such as ":2222" or
// "0.0.0.0:2222". An address with no port at all yields "".
func sshPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

func (s *Server) buildDetail(ctx context.Context, r *store.Repo) apitypes.RepoDetail {
	d := apitypes.RepoDetail{
		RepoSummary: toSummary(r), Card: r.Card,
		CanWrite: s.canWrite(ctx, r), CanAdmin: s.canAdmin(ctx, r),
	}
	if d.Card == nil {
		d.Card = map[string]any{}
	}
	d.CloneURL = s.cloneURL(r)
	d.SSHCloneURL = s.sshCloneURL(r)
	d.Branches = []string{}
	d.TagsRefs = []string{}
	d.ParquetFiles = []apitypes.ParquetSummary{}

	if repo, err := s.git.Open(r.StoragePath); err == nil {
		if branches, err := repo.Branches(); err == nil && branches != nil {
			d.Branches = branches
		}
		if tags, err := repo.Tags(); err == nil && tags != nil {
			d.TagsRefs = tags
		}
		if readme, err := repo.ReadFile(r.DefaultBranch, "README.md", maxReadmeBytes); err == nil {
			d.Readme = repocard.Parse(readme).Body
		} else if errors.Is(err, gitrepo.ErrBlobTooLarge) {
			d.ReadmeTooLarge = true
		}
	}
	if files, err := s.store.ListParquetFiles(ctx, r.ID, r.DefaultBranch); err == nil {
		d.ParquetFiles = toParquetSummaries(files)
	}
	if n, err := s.store.PendingSyncCount(ctx, r.ID); err == nil {
		d.Indexing = n > 0
	}
	if n, err := s.store.DownloadsSince(ctx, r.ID, time.Now().AddDate(0, 0, -30)); err == nil {
		d.DownloadsLast30Days = n
	}
	return d
}

func (s *Server) handleRepoDetail(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, apitypes.RepoDetailResponse{Repo: s.buildDetail(r.Context(), repo)})
}

// createRepo is the shared path behind both the UI and HF create endpoints.
func (s *Server) createRepo(ctx context.Context, user *store.User, kind, ns, name, description string) (*store.Repo, error) {
	if kind != "dataset" && kind != "model" {
		return nil, badInput("kind must be dataset or model, got %q", kind)
	}
	if err := validateName(name); err != nil {
		return nil, badInput("repository name %s", err)
	}
	if ns == "" {
		ns = user.Username
	}
	if err := validateName(ns); err != nil {
		return nil, badInput("namespace %s", err)
	}
	namespace, err := s.store.GetNamespace(ctx, ns)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Deliberately still a 400 rather than a 403 or a 404. A
			// namespace's existence is not a secret -- GET
			// /api/v1/namespaces/{ns} answers it unauthenticated, and every
			// repository URL spells one out (docs/dev/namespace-design.md §10)
			// -- so distinguishing it leaks nothing, and naming a namespace
			// that is not there is a fault in the request body, not an
			// authorization outcome. 404 would be the wrong shape for a POST
			// whose own URL exists, and would change what huggingface_hub's
			// create_repo sees.
			return nil, badInput("namespace %q does not exist", ns)
		}
		return nil, err
	}
	role, err := s.roleIn(ctx, user, ns)
	if err != nil {
		return nil, err
	}
	if role < RoleWrite {
		// A permission failure, so 403 like every other one in this package
		// (loadRepoForWriteAllowArchived, requireOrgRole, loadRepoForDelete);
		// folding it into inputError reported "you do not have write access"
		// as a 400 bad_request, which reads as "fix your request" for
		// something no request body can fix.
		return nil, forbiddenError{fmt.Sprintf("you do not have write access to namespace %q", ns)}
	}
	// storagePath is freshly minted (store.NewStoragePath(), a random ULID)
	// and never reused, so it cannot collide with the WAL or bare directory
	// a previously deleted repository left behind — unlike the old
	// (kind, ns, name) keying, there is nothing here to purge before the
	// first write (docs/dev/repo-transfer-design.md §8).
	repo, err := s.store.CreateRepo(ctx, namespace.ID, name, kind, description, "main", store.NewStoragePath())
	if err != nil {
		return nil, err
	}
	if err := s.git.Init(repo.StoragePath, "main"); err != nil {
		// Roll the row back so a retry is not blocked by a half-created repo.
		rollbackCreateRepo(ctx, s, repo, false)
		return nil, fmt.Errorf("initialise git repository: %w", err)
	}

	// Seed the same starting files a HuggingFace repository gets, so LFS
	// routing works from the very first push. Through the WAL like every
	// other server-side commit: skipping it here would leave a repository
	// whose main predates its index, and the very next authoritative commit
	// would be rejected as stale (§7).
	//
	// No `license:` key: the creator hasn't chosen one yet, and asserting
	// "unknown" made every fresh repository a first-class (bogus) value in
	// the license facet. License() (internal/store/repos.go) and the license
	// facet both already treat an absent key as "" / excluded.
	readme := fmt.Sprintf("---\ntags: []\n---\n\n# %s\n\n%s\n", name, description)
	newHash, _, err := s.commitThroughWAL(ctx, repo, gitrepo.CommitRequest{
		Branch:  "main",
		Message: "Initial commit",
		Author:  gitrepo.Signature{Name: user.Username, Email: user.Email, When: time.Now()},
		Ops: []gitrepo.Op{
			{Kind: gitrepo.OpAdd, Path: ".gitattributes", Data: []byte(gitrepo.DefaultGitAttributes(kind))},
			{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte(readme)},
		},
	}, true)
	if err != nil {
		// Same rollback as a failed Init: without it a repository row with no
		// initial commit (and, in authoritative mode, no WAL index) would be
		// unusable yet block its name against re-creation forever.
		rollbackCreateRepo(ctx, s, repo, true)
		return nil, fmt.Errorf("write initial commit: %w", err)
	}

	if err := s.sync.Enqueue(ctx, repo.ID, "main", "", newHash.String()); err != nil {
		// Rolled back like the two failures above, rather than logged and
		// shrugged off the way refs.go does for a branch creation. The
		// difference is that this is the *first* job for the repository: the
		// syncer diffs OldSHA..NewSHA (syncer.changedPaths), so the next push
		// enqueues a diff rooted at this very commit and .gitattributes and
		// README.md would never be indexed at all. Leaving the row and the
		// bare directory behind while answering 500 also blocks the retry the
		// client will make with a 409 on a name it does not own yet.
		rollbackCreateRepo(ctx, s, repo, true)
		return nil, fmt.Errorf("schedule initial index: %w", err)
	}
	s.fireWebhook(ctx, string(apitypes.WebhookEventRepoCreated), ns, &repo.ID, map[string]any{
		"namespace": ns, "name": name, "kind": kind, "full_name": ns + "/" + name,
	})
	if namespace.Kind == "org" {
		s.audit(ctx, namespace.ID, user, auditRepoCreated, ns+"/"+name,
			map[string]any{"kind": kind})
	}
	return s.store.GetRepoByID(ctx, repo.ID)
}

// rollbackCreateRepo undoes a half-created repository. It runs on a detached
// context on purpose: the most likely reason the step it is undoing failed is
// that the client hung up, and on the request's own context the DeleteRepo
// would then fail too -- while git.Remove, which takes no context, would
// succeed. That leaves the worst of both: the name still taken by a row whose
// bare repository is gone, and the 409 on the client's retry that this rollback
// exists to prevent.
//
// Both steps are best-effort and logged rather than returned: the caller is
// already reporting the original failure, which is the one worth seeing.
func rollbackCreateRepo(ctx context.Context, s *Server, repo *store.Repo, removeGit bool) {
	rollbackCtx, cancel := detachedWrite(ctx)
	defer cancel()

	if removeGit {
		if err := s.git.Remove(repo.StoragePath); err != nil {
			slog.Error("roll back the bare repository of a failed create",
				"repo", repo.FullName(), "path", repo.StoragePath, "error", err)
		}
	}
	if err := s.store.DeleteRepo(rollbackCtx, repo.ID); err != nil {
		slog.Error("roll back the row of a failed create",
			"repo", repo.FullName(), "error", err)
	}
}

// writeCreateRepoError answers a createRepo failure. A name collision is left
// to the caller -- the two create endpoints report it differently -- so it
// returns false without writing anything in that one case.
func writeCreateRepoError(w http.ResponseWriter, err error) bool {
	var bad inputError
	var forb forbiddenError
	switch {
	case errors.Is(err, store.ErrConflict):
		return false
	case errors.As(err, &forb):
		forbidden(w, forb.Error())
	case errors.As(err, &bad):
		badRequest(w, bad.Error())
	default:
		internalError(w, "create repository", err)
	}
	return true
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Kind        string `json:"kind"`
		Namespace   string `json:"namespace"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with kind, namespace and name") {
		return
	}
	repo, err := s.createRepo(r.Context(), user, req.Kind, req.Namespace, req.Name, req.Description)
	if err != nil {
		if !writeCreateRepoError(w, err) {
			conflict(w, "a repository with that name already exists")
		}
		return
	}
	writeJSON(w, http.StatusOK, apitypes.RepoDetailResponse{Repo: s.buildDetail(r.Context(), repo)})
}

// loadRepoForDelete enforces the gate both delete paths share: namespace
// admin, the same role that may archive or transfer. A write member can push
// anything and revert it; deleting takes the history, the LFS links and the
// exports with it, so it belongs with the other one-way operations.
//
// AllowArchived because archiving freezes content, it does not protect
// against disposal -- an archived repository can still be deleted.
func (s *Server) loadRepoForDelete(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r, kind, ns, name, mode)
	if !ok {
		return nil, false
	}
	if !s.canAdmin(r.Context(), repo) {
		forbidden(w, "you must have admin access to "+repo.Namespace+" to delete "+repo.FullName())
		return nil, false
	}
	return repo, true
}

// handleDeleteRepo answers DELETE /api/v1/repos/{kind}/{ns}/{name}. The
// irreversible-confirmation step (typing the repository id) lives in the web
// UI; the API itself is a plain delete, like HF's.
func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForDelete(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	if err := s.deleteRepo(r.Context(), repo); err != nil {
		internalError(w, "delete repository", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteRepo removes a repository everywhere it exists: the bare repository on
// disk, the database rows, and the WAL prefix that is the actual source of
// truth for its git data.
//
// The order is load bearing, and it is the reverse of the obvious one. Doing
// the database row first reads naturally -- the repository stops existing,
// then the storage behind it is cleaned up -- but every step after that row is
// gone has no second chance: `git.Remove` failing turned into a 500 whose
// retry answered 404, leaving the bare repository *and* its WAL prefix behind
// with nothing that would ever find them again. Neither `thinkingface gc` nor
// wal compaction can: both enumerate repositories through the database
// (store.AllRepoRefs), so a repository with no row is invisible to them.
//
// Removing the local copy first inverts that. The bare repository on disk is
// a cache whose authority is the WAL (docs/dev/continuity-design.md §2: "an
// instance's tmpfs disappears -> materialized on the next request"), so this
// is the one step of the three that costs nothing to lose: if it fails, the
// row is still there and the retry is a plain delete; if it half-succeeds --
// os.RemoveAll empties the directory and then cannot unlink it -- the leftover
// is not a bare repository any more, which is precisely what makes
// wal.Materialize rebuild it from scratch rather than trust it. And if the row
// delete then fails, the repository is whole again on the next request.
//
// The second Remove is for exactly that: a concurrent read between the first
// one and the row delete can rebuild the directory. It comes after the WAL is
// gone, so nothing can rebuild it again, and it is best effort because by then
// the repository is deleted as far as every caller is concerned.
func (s *Server) deleteRepo(ctx context.Context, repo *store.Repo) error {
	if err := s.git.Remove(repo.StoragePath); err != nil {
		return err
	}
	if err := s.store.DeleteRepo(ctx, repo.ID); err != nil {
		return err
	}
	if repo.NamespaceKind == "org" {
		s.audit(ctx, repo.NamespaceID, currentUser(ctx), auditRepoDeleted, repo.FullName(),
			map[string]any{"kind": repo.Kind})
	}
	// Fired after the row (and, by cascade, any repo-scoped webhooks) is
	// gone: repoID is still passed through so ListMatchingWebhooks can join
	// it, but with no repo-scoped rows left to match it naturally reaches
	// only namespace-wide subscriptions -- which is the only kind that could
	// still be listening for the deletion of a repo that no longer exists.
	s.fireWebhook(ctx, string(apitypes.WebhookEventRepoDeleted), repo.Namespace, &repo.ID, map[string]any{
		"namespace": repo.Namespace, "name": repo.Name, "kind": repo.Kind, "full_name": repo.FullName(),
	})
	s.purgeWAL(ctx, repo)
	if err := s.git.Remove(repo.StoragePath); err != nil {
		slog.Warn("remove git directory after repo delete",
			"repo", repo.FullName(), "error", err)
	}
	// Object storage is untouched, and deliberately so: lfs/ and blobs/ are
	// content-addressed layers shared across repositories, so this delete
	// removes references, not bytes. Another repository may hold the very same
	// content. `thinkingface gc` reclaims what nothing references any more.
	return nil
}

// ------------------------------------------------------------------- update

// handleUpdateRepo answers PATCH /api/v1/repos/{kind}/{ns}/{name}, a partial
// update over repository configuration: default_branch
// (docs/dev/api-contract.md "Changing the default branch"), name and
// description. Absent fields are left alone; present ones are applied in the
// order below, and the response describes the repository as it stands
// afterwards.
//
// Gated by canAdmin rather than canWrite -- the same namespace-admin bar as
// archive/transfer/delete, not a plain write member -- because switching the
// default branch changes what a bare `git clone` checks out and which ref
// every listing, the README card, lineage and the parquet index read from:
// a repository-configuration decision, not a content edit.
//
// Explicitly rejects an archived repository even though
// loadRepoForWriteAllowArchived lets a write member's *request* through:
// unlike unarchiving (the one write archive must still allow) this is not
// the escape hatch for its own state, so it stays refused like every other
// content-adjacent change until the repository is unarchived.
func (s *Server) handleUpdateRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r,
		chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	if !s.canAdmin(r.Context(), repo) {
		forbidden(w, "you must have admin access to "+repo.Namespace+" to change settings on "+repo.FullName())
		return
	}
	if repo.Archived() {
		writeError(w, http.StatusForbidden, "repository_archived",
			repo.FullName()+" is archived and read-only; unarchive it in the repository settings to make changes")
		return
	}

	var req apitypes.RepoUpdateRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	if req.DefaultBranch == nil && req.Name == nil && req.Description == nil {
		badRequest(w, "nothing to update: send default_branch, name or description")
		return
	}

	// The three updates are separate writes with no transaction around them
	// -- one of them repoints git's HEAD, which no database transaction could
	// roll back anyway -- so every reason this request has to fail is checked
	// here, before the first of them lands. Otherwise
	// {"description":"new","name":"taken"} answers 409 with the description
	// already committed, which is the one thing a refusal must not do.
	//
	// What that buys is all-or-nothing for the request as written; it is not a
	// lock. A repository created at newName in the window between this check
	// and the rename still turns into a 409 after the first two writes -- the
	// store's own constraint is the backstop for that race, and closing it
	// properly means holding the destination, which a rename does not get to
	// do. The check below is the one that is otherwise deferred all the way
	// into resolveTransferTarget; validateName and the description ceiling
	// were already early, and stay here beside it.
	newName := ""
	if req.Name != nil {
		newName = strings.TrimSpace(*req.Name)
		if verr := validateName(newName); verr != nil {
			badRequest(w, "name "+verr.Error())
			return
		}
		if newName != repo.Name {
			switch _, err := s.store.GetRepo(r.Context(), repo.Kind, repo.Namespace, newName); {
			case err == nil:
				conflict(w, repo.Namespace+"/"+newName+" already exists")
				return
			case !errors.Is(err, store.ErrNotFound):
				internalError(w, "check repository name", err)
				return
			}
		}
	}
	description := ""
	if req.Description != nil {
		description = strings.TrimSpace(*req.Description)
		// The same rune ceiling the profile fields use
		// (docs/dev/namespace-design.md §10): prose typed by a person, so
		// counted in characters rather than bytes.
		if utf8.RuneCountInString(description) > maxDescriptionRunes {
			badRequest(w, fmt.Sprintf("description must be at most %d characters", maxDescriptionRunes))
			return
		}
	}

	updated := repo
	if req.DefaultBranch != nil {
		branch := strings.TrimSpace(*req.DefaultBranch)
		if branch == "" {
			badRequest(w, "default_branch must not be empty")
			return
		}
		if updated, ok = s.setDefaultBranch(w, r, repo, branch); !ok {
			return
		}
	}

	if req.Description != nil {
		var err error
		// Overwritten by the README card's own `description` on the next
		// push, when the card has one -- see store.UpdateRepoIndex, which
		// keeps what is set here only while the card says nothing. The
		// settings form says so; this is not a field that quietly wins.
		if updated, err = s.store.SetRepoDescription(r.Context(), repo.ID, description); err != nil {
			handleStoreError(w, "update repository", err)
			return
		}
	}

	// The rename runs last, so the response (and the redirect left behind)
	// describe the repository's final location.
	//
	// It goes through startTransfer, the same path the transfer endpoints
	// use, because a rename *is* a move as far as the data is concerned:
	// repo_redirects, the lineage edges pointing at the old name, the
	// cancelled pending transfers and the repo.moved webhook are all the same
	// work, and writing a second implementation of it would be writing a
	// second chance to get one of them wrong. What a rename deliberately does
	// not inherit is the approval flow: that exists so the *destination
	// namespace* can consent, and here the destination is the namespace the
	// repository already lives in, so startTransfer's own destination check
	// (write access there) is satisfied by definition and the move completes
	// immediately.
	if req.Name != nil && newName != updated.Name {
		moved, _, _, err := s.startTransfer(r.Context(), currentUser(r.Context()), updated, updated.Namespace, newName)
		if err != nil {
			writeTransferError(w, err)
			return
		}
		updated = moved
	}

	writeJSON(w, http.StatusOK, apitypes.RepoDetailResponse{Repo: s.buildDetail(r.Context(), updated)})
}

// setDefaultBranch is the default_branch half of handleUpdateRepo: it
// repoints HEAD, writes the row and re-runs the post-push indexers for the
// newly-default ref. It answers the request itself on failure and reports
// ok=false; on success it returns the repository as it now stands.
func (s *Server) setDefaultBranch(w http.ResponseWriter, r *http.Request, repo *store.Repo, branch string) (*store.Repo, bool) {
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return nil, false
	}
	tip, err := gitRepo.RefTarget("refs/heads/" + branch)
	if err != nil {
		notFound(w, "branch "+branch+" does not exist in "+repo.FullName())
		return nil, false
	}

	// Already the default: neither HEAD nor the row needs touching, and the
	// request is idempotent. The reindex below still runs, and that is the
	// point -- it is what turns a retry into a repair after an earlier
	// attempt got as far as the row and then failed to queue the job. The
	// cost when nothing went wrong is one redundant pass over a ref that is
	// already indexed.
	updated := repo
	if branch != repo.DefaultBranch {
		// HEAD before the row, and HEAD back again if the row write fails:
		// the two must never name different branches, or a clone checks out
		// one while every listing reads the other.
		if err := gitRepo.SetHead(r.Context(), branch); err != nil {
			internalError(w, "update HEAD", err)
			return nil, false
		}
		updated, err = s.store.SetRepoDefaultBranch(r.Context(), repo.ID, branch)
		if err != nil {
			if rbErr := gitRepo.SetHead(r.Context(), repo.DefaultBranch); rbErr != nil {
				slog.Error("roll back HEAD after a failed default-branch write",
					"repo", repo.FullName(), "branch", repo.DefaultBranch, "err", rbErr)
			}
			internalError(w, "update repository", err)
			return nil, false
		}
	}

	// Re-run the post-push pipeline for the newly-default branch: the syncer
	// only refreshes head_sha/card/lineage/is_experiment for
	// job.Ref == repo.DefaultBranch (internal/syncer/syncer.go), so a branch
	// that was pushed to long before it became the default -- the case this
	// endpoint exists for -- still carries the *old* default branch's
	// metadata until this runs. repo_files and the parquet index are
	// refreshed unconditionally by the same job, which makes re-running them
	// here harmless when the branch was already indexed.
	//
	// old == new (both the branch's current tip) rather than the row's stale
	// previous head: no commit was made, so the eventual repo.push webhook's
	// changed_files comes back 0 and its old_sha/new_sha are equal -- a
	// subscriber can tell this apart from a real push on the same ref. A new
	// webhook event type was deliberately not added for this
	// (docs/dev/api-contract.md "Changing the default branch" explains why).
	if err := s.sync.Enqueue(r.Context(), repo.ID, branch, tip.String(), tip.String()); err != nil {
		// Nothing is undone here, deliberately. HEAD and the row already
		// agree on the new branch, so the repository is consistent -- only
		// its index is stale. Undoing the switch would mean a second write to
		// the very store that just refused one, and the outage that failed
		// the enqueue fails that too: HEAD would go back while the row stayed
		// put, leaving the two naming different branches, which is a worse
		// state than a stale index. Retrying the request repairs it instead,
		// because the already-default path above still queues the job.
		internalError(w, "enqueue reindex", err)
		return nil, false
	}

	return updated, true
}

// ------------------------------------------------------------------ archive

// handleArchiveRepo and handleUnarchiveRepo answer POST and DELETE on
// /api/v1/repos/{kind}/{ns}/{name}/archive. Archiving makes the repository
// read-only without touching a byte of it: git history and the stored objects
// all stay, and reads keep working, but loadRepoForWrite
// refuses every write until it is undone. Only the namespace's owner or an
// organisation admin (or the site admin) may flip it -- the same role that
// may transfer or delete it, since a plain write member being able to freeze
// the repository out from under everyone else is the wrong default.
func (s *Server) handleArchiveRepo(w http.ResponseWriter, r *http.Request) {
	s.setRepoArchived(w, r, true)
}

func (s *Server) handleUnarchiveRepo(w http.ResponseWriter, r *http.Request) {
	s.setRepoArchived(w, r, false)
}

func (s *Server) setRepoArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	// AllowArchived: unarchiving is by definition a call against an archived
	// repository, and re-archiving an archived one is a harmless no-op.
	repo, ok := s.loadRepoForWriteAllowArchived(w, r,
		chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	if !s.canAdmin(r.Context(), repo) {
		forbidden(w, "you must have admin access to "+repo.Namespace+" to archive "+repo.FullName())
		return
	}
	user := currentUser(r.Context())
	updated, err := s.store.SetRepoArchived(r.Context(), repo.ID, archived, user.ID)
	if err != nil {
		internalError(w, "archive repository", err)
		return
	}
	event := apitypes.WebhookEventRepoUnarchived
	if archived {
		event = apitypes.WebhookEventRepoArchived
	}
	s.fireWebhook(r.Context(), string(event), updated.Namespace, &updated.ID, map[string]any{
		"namespace": updated.Namespace, "name": updated.Name, "kind": updated.Kind,
		"full_name": updated.FullName(), "archived": updated.Archived(),
	})
	writeJSON(w, http.StatusOK, apitypes.RepoDetailResponse{Repo: s.buildDetail(r.Context(), updated)})
}

// ------------------------------------------------------- HF-compatible repos

func (s *Server) handleHFCreateRepo(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		Organization string `json:"organization"`
		// Accepted and ignored, both of them: huggingface_hub < 1.0 sends
		// Private and 1.x sends Visibility ("public" | "private"). There is no
		// visibility concept here, so neither changes what gets created --
		// they are decoded only so a client that sends them still succeeds.
		Private    *bool  `json:"private"`
		Visibility string `json:"visibility"`
		SDK        string `json:"sdk"`
	}
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	kind, ns, name := hfRepoTarget(user, req.Type, req.Name, req.Organization)

	repo, err := s.createRepo(r.Context(), user, kind, ns, name, "")
	if err != nil {
		if writeCreateRepoError(w, err) {
			return
		}
		// exist_ok=True in huggingface_hub swallows the 409 but still reads the
		// body's "url", so the conflict response carries the same fields as a
		// successful create.
		existing, lookupErr := s.store.GetRepo(r.Context(), kind, ns, name)
		if lookupErr != nil {
			conflict(w, "a repository with that name already exists")
			return
		}
		body := hfRepoCreateResponse(s.repoWebURL(existing), existing.FullName(), kind)
		body["error"] = fmt.Sprintf("You already created this %s repo", kind)
		writeJSON(w, http.StatusConflict, body)
		return
	}
	writeJSON(w, http.StatusOK, hfRepoCreateResponse(s.repoWebURL(repo), repo.FullName(), kind))
}

func hfRepoCreateResponse(url, fullName, kind string) map[string]any {
	return map[string]any{"url": url, "repo_id": fullName, "name": fullName, "type": kind}
}

func (s *Server) repoWebURL(r *store.Repo) string {
	return s.repoWebURLFor(r.Kind, r.Namespace, r.Name)
}

// repoWebURLFor builds the public web URL for a (kind, ns, name) that may not
// have a *store.Repo at hand yet -- namely a transfer that is still pending,
// which the caller answers with the destination's future URL.
func (s *Server) repoWebURLFor(kind, ns, name string) string {
	if kind == "model" {
		return fmt.Sprintf("%s/%s/%s", s.cfg.PublicURL, ns, name)
	}
	return fmt.Sprintf("%s/datasets/%s/%s", s.cfg.PublicURL, ns, name)
}

func (s *Server) handleHFDeleteRepo(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		Organization string `json:"organization"`
	}
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	kind, ns, name := hfRepoTarget(user, req.Type, req.Name, req.Organization)
	repo, ok := s.loadRepoForDelete(w, r, kind, ns, name, redirectNone)
	if !ok {
		return
	}
	if err := s.deleteRepo(r.Context(), repo); err != nil {
		internalError(w, "delete repository", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
