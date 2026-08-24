// Repository CRUD: creating and deleting repositories from both the UI API
// and the HuggingFace-compatible endpoints, plus the summary and detail
// shapes those handlers return.

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

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

func (s *Server) buildDetail(ctx context.Context, r *store.Repo) apitypes.RepoDetail {
	d := apitypes.RepoDetail{
		RepoSummary: toSummary(r), Card: r.Card,
		CanWrite: s.canWrite(ctx, r), CanAdmin: s.canAdmin(ctx, r),
	}
	if d.Card == nil {
		d.Card = map[string]any{}
	}
	d.CloneURL = s.cloneURL(r)
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
			return nil, badInput("namespace %q does not exist", ns)
		}
		return nil, err
	}
	role, err := s.roleIn(ctx, user, ns)
	if err != nil {
		return nil, err
	}
	if role < RoleWrite {
		return nil, badInput("you do not have write access to namespace %q", ns)
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
		_ = s.store.DeleteRepo(ctx, repo.ID)
		return nil, fmt.Errorf("initialise git repository: %w", err)
	}

	// Seed the same starting files a HuggingFace repository gets, so LFS
	// routing works from the very first push. Through the WAL like every
	// other server-side commit: skipping it here would leave a repository
	// whose main predates its index, and the very next authoritative commit
	// would be rejected as stale (§7).
	readme := fmt.Sprintf("---\nlicense: unknown\ntags: []\n---\n\n# %s\n\n%s\n", name, description)
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
		_ = s.git.Remove(repo.StoragePath)
		_ = s.store.DeleteRepo(ctx, repo.ID)
		return nil, fmt.Errorf("write initial commit: %w", err)
	}

	if err := s.sync.Enqueue(ctx, repo.ID, "main", "", newHash.String()); err != nil {
		return nil, err
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

// writeCreateRepoError answers a createRepo failure. A name collision is left
// to the caller -- the two create endpoints report it differently -- so it
// returns false without writing anything in that one case.
func writeCreateRepoError(w http.ResponseWriter, err error) bool {
	var bad inputError
	switch {
	case errors.Is(err, store.ErrConflict):
		return false
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

func (s *Server) deleteRepo(ctx context.Context, repo *store.Repo) error {
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
	if err := s.git.Remove(repo.StoragePath); err != nil {
		return err
	}
	s.purgeWAL(ctx, repo)
	// Object storage is untouched, and deliberately so: lfs/ and blobs/ are
	// content-addressed layers shared across repositories, so this delete
	// removes references, not bytes. Another repository may hold the very same
	// content. `thinkingface gc` reclaims what nothing references any more.
	return nil
}

// ------------------------------------------------------------------- update

// handleUpdateRepo answers PATCH /api/v1/repos/{kind}/{ns}/{name}, a partial
// update over repository configuration; today the only field is
// default_branch (docs/dev/api-contract.md "Changing the default branch").
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
	if req.DefaultBranch == nil {
		badRequest(w, "nothing to update: send default_branch")
		return
	}
	branch := strings.TrimSpace(*req.DefaultBranch)
	if branch == "" {
		badRequest(w, "default_branch must not be empty")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open repository", err)
		return
	}
	tip, err := gitRepo.RefTarget("refs/heads/" + branch)
	if err != nil {
		notFound(w, "branch "+branch+" does not exist in "+repo.FullName())
		return
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
			return
		}
		updated, err = s.store.SetRepoDefaultBranch(r.Context(), repo.ID, branch)
		if err != nil {
			if rbErr := gitRepo.SetHead(r.Context(), repo.DefaultBranch); rbErr != nil {
				slog.Error("roll back HEAD after a failed default-branch write",
					"repo", repo.FullName(), "branch", repo.DefaultBranch, "err", rbErr)
			}
			internalError(w, "update repository", err)
			return
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
		return
	}

	writeJSON(w, http.StatusOK, apitypes.RepoDetailResponse{Repo: s.buildDetail(r.Context(), updated)})
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
	kind := "model"
	if req.Type != "" {
		kind = strings.TrimSuffix(req.Type, "s")
	}

	ns, name := req.Organization, req.Name
	// huggingface_hub sends either "name" or "org/name".
	if before, after, found := strings.Cut(req.Name, "/"); found {
		ns, name = before, after
	}
	if ns == "" {
		ns = user.Username
	}

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
	if _, ok := s.requireWrite(w, r); !ok {
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
	kind := "model"
	if req.Type != "" {
		kind = strings.TrimSuffix(req.Type, "s")
	}
	ns, name := req.Organization, req.Name
	if before, after, found := strings.Cut(req.Name, "/"); found {
		ns, name = before, after
	}
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
