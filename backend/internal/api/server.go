// Package api serves the HTTP surface: the HuggingFace-compatible endpoints
// that `huggingface_hub` talks to, the UI's own JSON API, git smart HTTP, and
// the LFS batch protocol.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/modelmeta"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// Enqueuer schedules post-commit indexing. Implemented by internal/syncer.
type Enqueuer interface {
	Enqueue(ctx context.Context, repoID int64, ref, oldSHA, newSHA string) error
}

// WebhookFirer records webhook deliveries for an event. Implemented by
// internal/webhooks.Dispatcher.
type WebhookFirer interface {
	Fire(ctx context.Context, event, namespace string, repoID *int64, payload any) error
}

type Server struct {
	cfg      *config.Config
	store    *store.Store
	git      *gitrepo.Manager
	storage  storage.Storage
	viewer   *viewer.Reader
	sessions *auth.Sessions
	sync     Enqueuer
	exp      *experiments.Indexer
	lfs      *lfs.Handler
	gitHTTP  *gitserver.Handler
	models   *modelmeta.Cache
	webhooks WebhookFirer
	// authGuard throttles password verification. It is process-local state,
	// so it lives on the Server rather than in the store (see ratelimit.go).
	authGuard *authGuard
}

// experiments exposes the indexer used by the experiment endpoints.
func (s *Server) experiments() *experiments.Indexer { return s.exp }

type Deps struct {
	Config      *config.Config
	Store       *store.Store
	Git         *gitrepo.Manager
	Storage     storage.Storage
	Viewer      *viewer.Reader
	Sessions    *auth.Sessions
	Syncer      Enqueuer
	Experiments *experiments.Indexer
	ModelMeta   *modelmeta.Cache
	Webhooks    WebhookFirer
}

func NewServer(d Deps) *Server {
	s := &Server{
		cfg:      d.Config,
		store:    d.Store,
		git:      d.Git,
		storage:  d.Storage,
		viewer:   d.Viewer,
		sessions: d.Sessions,
		sync:     d.Syncer,
		exp:      d.Experiments,
		models:   d.ModelMeta,
		webhooks: d.Webhooks,
	}
	s.authGuard = newAuthGuard(d.Config.AuthRateLimitPerMinute)
	if s.models == nil {
		// A Server built without one still has to answer checkpoint
		// requests; the cache is pure memoisation, never a dependency.
		s.models = modelmeta.NewCache(modelmeta.DefaultCacheEntries)
	}
	s.lfs = lfs.New(d.Store, d.Storage, d.Config.SignedURLTTL, d.Config.SignedURLMaxTTL, d.Config.PublicURL, d.Config.SessionSecret)
	s.gitHTTP = gitserver.New(d.Git)
	if d.Config.WALMode != "off" && d.Config.GitHooksPath != "" {
		cfg := d.Config
		// The hook is a separate process: everything it needs travels as
		// environment variables — the repository's storage path plus the
		// object-store coordinates. Never DATABASE_URL (§14).
		s.gitHTTP.EnableHooks(cfg.GitHooksPath, func(storagePath string) []string {
			env := []string{
				"TF_WAL_MODE=" + cfg.WALMode,
				"TF_WAL_STORAGE_PATH=" + storagePath,
				"STORAGE_DRIVER=" + cfg.StorageDriver,
				"GCS_BUCKET=" + cfg.GCSBucket,
				"GCS_PREFIX=" + cfg.GCSPrefix,
			}
			if cfg.EmulatorHost != "" {
				env = append(env, "STORAGE_EMULATOR_HOST="+cfg.EmulatorHost)
			}
			return env
		})
	}
	return s
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// No middleware.RealIP here: it rewrites RemoteAddr from headers any client
	// can set (GHSA-3fxj-6jh8-hvhx), and nothing in this server reads RemoteAddr
	// anyway. Anything that starts to should resolve the peer against a known
	// proxy list rather than trusting X-Forwarded-For.
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(s.cors)
	// Identity is resolved once per request and never rejects here; each
	// handler decides what it requires.
	r.Use(s.identify)
	// After identify, because it only applies to cookie-authenticated calls.
	r.Use(s.requireSameOrigin)

	// Unmatched routes must still answer JSON: huggingface_hub parses every
	// response body, and chi's plain-text default breaks it with a decode
	// error instead of a readable message.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		notFound(w, "no route for "+req.Method+" "+req.URL.Path)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			req.Method+" is not allowed on "+req.URL.Path)
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// ------------------------------------------------ HuggingFace-compatible
	r.Get("/api/whoami-v2", s.handleWhoami)
	r.Post("/api/repos/create", s.handleHFCreateRepo)
	r.Delete("/api/repos/delete", s.handleHFDeleteRepo)
	r.Post("/api/repos/move", s.handleHFMoveRepo)
	// huggingface_hub.HfApi.list_organization_members().
	r.Get("/api/organizations/{org}/members", s.handleHFOrgMembers)
	// HfApi.get_user_overview() / get_organization_overview(). The two share
	// one name space, so each answers 404 for the other's kind
	// (docs/dev/namespace-design.md §7.2).
	r.Get("/api/users/{username}/overview", s.handleHFUserOverview)
	r.Get("/api/organizations/{org}/overview", s.handleHFOrgOverview)
	// huggingface_hub calls this before every commit that touches a README
	// (HfApi._validate_yaml), always with the commit's own token.
	r.Post("/api/validate-yaml", s.handleValidateYAML)

	r.Route("/api/{repoType:models|datasets}/{ns}/{name}", func(r chi.Router) {
		r.Get("/", s.handleHFRepoInfo)
		r.Get("/revision/{rev}", s.handleHFRepoInfo)
		r.Get("/refs", s.handleHFRefs)
		r.Get("/tree/{rev}/*", s.handleHFTree)
		r.Get("/tree/{rev}", s.handleHFTree)
		r.Post("/paths-info/{rev}", s.handleHFPathsInfo)
		r.Post("/preupload/{rev}", s.handlePreupload)
		// huggingface_hub >= 1.0 reaches for Xet before LFS whenever the hf_xet
		// package is installed. thinkingface speaks LFS only, so answer with a
		// message that says what to do instead of a bare 404.
		r.Get("/xet-write-token/{rev}", s.handleXetUnsupported)
		r.Get("/xet-read-token/{rev}", s.handleXetUnsupported)
		r.Post("/commit/{rev}", s.handleCommit)
	})

	// HF list endpoints, used by `HfApi.list_datasets()` and friends.
	r.Get("/api/models", s.handleHFList)
	r.Get("/api/datasets", s.handleHFList)

	// --------------------------------------------------------- thinkingface
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/signup", s.handleSignup)
		r.Post("/auth/logout", s.handleLogout)
		r.Get("/me", s.handleMe)
		r.Patch("/me/profile", s.handleUpdateMyProfile)

		r.Get("/tokens", s.handleListTokens)
		r.Post("/tokens", s.handleCreateToken)
		r.Delete("/tokens/{id}", s.handleDeleteToken)

		r.Get("/me/ssh-keys", s.handleListSSHKeys)
		r.Post("/me/ssh-keys", s.handleCreateSSHKey)
		r.Delete("/me/ssh-keys/{id}", s.handleDeleteSSHKey)

		r.Get("/stats", s.handleStats)
		r.Get("/usage", s.handleUsage)
		r.Get("/repos", s.handleListRepos)
		r.Post("/repos", s.handleCreateRepo)
		r.Get("/repos/{kind}/{ns}/{name}", s.handleRepoDetail)
		r.Patch("/repos/{kind}/{ns}/{name}", s.handleUpdateRepo)
		r.Delete("/repos/{kind}/{ns}/{name}", s.handleDeleteRepo)
		r.Get("/repos/{kind}/{ns}/{name}/tree/{rev}/*", s.handleUITree)
		r.Get("/repos/{kind}/{ns}/{name}/tree/{rev}", s.handleUITree)
		r.Get("/repos/{kind}/{ns}/{name}/gcs/{rev}", s.handleRepoGCS)
		r.Get("/repos/{kind}/{ns}/{name}/refs", s.handleUIRefs)
		r.Get("/repos/{kind}/{ns}/{name}/commits/{rev}", s.handleUICommits)
		r.Get("/repos/{kind}/{ns}/{name}/lineage", s.handleRepoLineage)
		r.Post("/repos/{kind}/{ns}/{name}/archive", s.handleArchiveRepo)
		r.Delete("/repos/{kind}/{ns}/{name}/archive", s.handleUnarchiveRepo)
		r.Post("/repos/{kind}/{ns}/{name}/transfer", s.handleUIStartTransfer)
		r.Get("/repos/{kind}/{ns}/{name}/transfer", s.handleGetTransfer)
		r.Delete("/repos/{kind}/{ns}/{name}/transfer", s.handleCancelTransfer)

		r.Get("/orgs", s.handleListOrgs)
		r.Post("/orgs", s.handleCreateOrg)
		r.Get("/orgs/{org}", s.handleGetOrg)
		r.Patch("/orgs/{org}", s.handleUpdateOrg)
		r.Delete("/orgs/{org}", s.handleDeleteOrg)
		r.Get("/orgs/{org}/members", s.handleListOrgMembers)
		r.Post("/orgs/{org}/members", s.handleAddOrgMember)
		r.Patch("/orgs/{org}/members/{username}", s.handleUpdateOrgMember)
		r.Delete("/orgs/{org}/members/{username}", s.handleRemoveOrgMember)
		r.Get("/orgs/{org}/audit-log", s.handleOrgAuditLog)
		r.Get("/me/orgs", s.handleMyOrgs)

		r.Get("/me/transfers", s.handleMyTransfers)
		r.Post("/transfers/{id}/accept", s.handleAcceptTransfer)
		r.Post("/transfers/{id}/reject", s.handleRejectTransfer)

		r.Get("/raw/{kind}/{ns}/{name}/{rev}/*", s.handleRaw)

		r.Get("/model-meta/{kind}/{ns}/{name}/{rev}/*", s.handleModelMeta)
		r.Put("/edit/{kind}/{ns}/{name}/{rev}/*", s.handleEditFile)

		r.Get("/parquet/{kind}/{ns}/{name}/schema/{rev}/*", s.handleParquetSchema)
		r.Get("/parquet/{kind}/{ns}/{name}/rows/{rev}/*", s.handleParquetRows)

		r.Get("/experiments", s.handleListExperiments)
		r.Get("/experiments/{ns}/{repo}", s.handleExperimentRepo)
		r.Get("/experiments/{ns}/{repo}/{project}/runs", s.handleExperimentRuns)
		r.Patch("/experiments/{ns}/{repo}/{project}/runs/{run}", s.handleExperimentRunAnnotation)
		r.Delete("/experiments/{ns}/{repo}/{project}/runs/{run}", s.handleDeleteExperimentRun)
		r.Get("/experiments/{ns}/{repo}/{project}/runs/{run}/artifacts", s.handleExperimentRunArtifacts)
		r.Get("/experiments/{ns}/{repo}/{project}/metrics", s.handleExperimentMetrics)
		r.Get("/experiments/{ns}/{repo}/{project}/lineage", s.handleExperimentLineage)
		r.Post("/experiments/{ns}/{repo}/{project}/log", s.handleExperimentLog)
		r.Post("/experiments/{ns}/{repo}/{project}/finish", s.handleExperimentFinish)

		// Public: the sign-up form's availability check reads this
		// unauthenticated (docs/dev/namespace-design.md §5.5).
		r.Get("/namespaces/{ns}", s.handleGetNamespace)

		r.Get("/namespaces/{ns}/webhooks", s.handleListWebhooks)
		r.Post("/namespaces/{ns}/webhooks", s.handleCreateWebhook)
		r.Get("/webhooks/{id}", s.handleGetWebhook)
		r.Put("/webhooks/{id}", s.handleUpdateWebhook)
		r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
		r.Get("/webhooks/{id}/deliveries", s.handleListWebhookDeliveries)
		r.Post("/webhooks/{id}/deliveries/{deliveryId}/redeliver", s.handleRedeliverWebhook)

		// Emulator-only transfer proxy. In GCS mode clients get signed URLs and
		// never touch these.
		r.Put("/lfs/{repoID}/{oid}", s.handleLFSProxyUpload)
		r.Get("/lfs/{repoID}/{oid}", s.handleLFSProxyDownload)
		// Verify runs in both storage modes: even a directly-signed upload has
		// to be recorded against the repository before a commit can use it.
		r.Post("/lfs/{repoID}/verify", s.handleLFSVerifyByID)
	})

	// ------------------------------------------------------------- transfers
	// Datasets carry a /datasets prefix; models sit at the root, matching the
	// URL shapes huggingface_hub builds.
	r.Route("/datasets/{ns}/{name}", func(r chi.Router) {
		s.mountRepoTransport(r, "dataset")
	})
	r.Route("/models/{ns}/{name}", func(r chi.Router) {
		s.mountRepoTransport(r, "model")
	})
	r.Route("/{ns}/{name}", func(r chi.Router) {
		s.mountRepoTransport(r, "model")
	})

	return r
}

// mountRepoTransport registers the download, git, and LFS routes shared by
// every URL shape a repository answers on.
func (s *Server) mountRepoTransport(r chi.Router, kind string) {
	withKind := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) { h(w, req, kind) }
	}
	r.Get("/resolve/{rev}/*", withKind(s.handleResolve))
	r.Head("/resolve/{rev}/*", withKind(s.handleResolve))

	r.Get("/info/refs", withKind(s.handleInfoRefs))
	r.Post("/git-upload-pack", withKind(s.handleUploadPack))
	r.Post("/git-receive-pack", withKind(s.handleReceivePack))

	r.Post("/info/lfs/objects/batch", withKind(s.handleLFSBatch))
	r.Post("/info/lfs/objects/verify", withKind(s.handleLFSVerify))
}

// repoName strips the .git suffix git clients append.
func repoName(raw string) string {
	return strings.TrimSuffix(raw, ".git")
}

// wildcardPath returns the chi "*" parameter, cleaned of leading slashes.
func wildcardPath(r *http.Request) string {
	return strings.Trim(chi.URLParam(r, "*"), "/")
}

// originAllowed reports whether a browser origin may make credentialed calls.
// Empty origins are not browser calls at all (huggingface_hub, git, curl) and
// are handled by the callers, not here.
func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.cfg.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

// cors answers preflights and decorates responses for the origins the
// operator named in TF_ALLOWED_ORIGINS.
//
// Reflecting an arbitrary Origin alongside Allow-Credentials: true, which is
// what this used to do, means any page on the internet can read authenticated
// responses the moment the cookie's SameSite attribute changes. An unlisted
// origin now gets no CORS headers at all; the request itself still runs, so
// nothing that does not rely on the browser's cross-origin read is affected.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Vary regardless of the decision: the response body is the same
			// but the header set is not, so a shared cache must key on it.
			w.Header().Add("Vary", "Origin")
		}
		if origin != "" && s.originAllowed(origin) {
			// Credentialed requests forbid a wildcard, so echo the origin.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, X-Requested-With, Accept, Range")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers",
				"ETag, X-Repo-Commit, X-Linked-Etag, X-Linked-Size, Content-Length, Location")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSameOrigin is the CSRF backstop for cookie sessions.
//
// The cookie is SameSite=Lax, which already keeps it off most cross-site
// requests -- but that is one attribute away from being the only defence, and
// Lax has its own gaps (top-level POST navigations during Chrome's two-minute
// grace window). So: a state-changing request that authenticated with the
// cookie must carry an Origin (or, failing that, a Referer) this server
// accepts.
//
// A request with neither header passes. That is not a hole: every current
// browser attaches Origin to a cross-site POST, form submissions included, so
// "no Origin at all" means the caller is not a browser -- curl, the e2e
// suite's requests.Session, or the Next.js server forwarding a cookie from a
// Server Component. None of those can be steered by a hostile page, which is
// the only thing this check exists to stop.
func (s *Server) requireSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if safeMethod(r.Method) || !cookieAuthenticated(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		if origin := requestOrigin(r); origin != "" && !s.originAllowed(origin) {
			forbidden(w, "cross-origin request refused; sign in from an allowed origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// requestOrigin prefers the Origin header and falls back to the Referer's
// origin, which is what a same-origin form post from an older browser sends.
func requestOrigin(r *http.Request) string {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// securityHeaders applies the three headers every response wants, regardless
// of handler:
//
//   - nosniff, so a response's declared Content-Type is the only one the
//     browser will consider (resolve.go depends on this).
//   - DENY framing, so the settings pages cannot be overlaid in a hidden
//     iframe and clicked through.
//   - a Referrer-Policy that keeps repository paths out of the Referer sent
//     to third-party hosts.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		level := slog.LevelInfo
		if ww.Status() >= 500 {
			level = slog.LevelError
		}
		slog.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"bytes", ww.BytesWritten(), "duration", time.Since(start).String())
	})
}
