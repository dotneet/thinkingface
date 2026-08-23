// Command thinkingface runs the whole backend: REST API, git smart HTTP, LFS,
// the parquet viewer, and the sync worker, from one binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/api"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/modelmeta"
	"github.com/dotneet/thinkingface/backend/internal/sshserver"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/syncer"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
	"github.com/dotneet/thinkingface/backend/internal/webhooks"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	// `hook` is dispatched before run() so it never touches config.Load /
	// store.Open. It runs as a child process of `git receive-pack` on every
	// push (see docs/dev/continuity-design.md §14): it must not require
	// DATABASE_URL and must stay cheap to start.
	if command == "hook" {
		// Re-point slog at stderr as plain text: anything a library logs in
		// this process would otherwise land as JSON on stdout, which git
		// relays to the pushing client over the sideband.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		if err := runHook(os.Args[2:]); err != nil {
			// Plain text on stderr, not slog JSON: a hook's output is relayed
			// to the pushing git client over the sideband, and a JSON log
			// line there reads as noise in the user's terminal.
			fmt.Fprintf(os.Stderr, "thinkingface: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(command); err != nil {
		slog.Error("fatal", "command", command, "error", err)
		os.Exit(1)
	}
}

func run(command string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	warnInsecureDefaults(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.WaitReady(ctx, 60*time.Second); err != nil {
		return err
	}
	if err := db.Migrate(ctx); err != nil {
		return err
	}

	switch command {
	case "migrate":
		slog.Info("migrations applied")
		return nil
	case "seed":
		return seedAdmin(ctx, db, cfg)
	case "gc":
		obj, err := newStorage(ctx, cfg)
		if err != nil {
			return err
		}
		return runGC(ctx, db, obj, os.Args[2:])
	case "compact":
		obj, err := newStorage(ctx, cfg)
		if err != nil {
			return err
		}
		return runCompact(ctx, db, obj, cfg, os.Args[2:])
	case "wal-seed":
		obj, err := newStorage(ctx, cfg)
		if err != nil {
			return err
		}
		return runWALSeed(ctx, db, obj, cfg, os.Args[2:])
	case "wal-verify":
		obj, err := newStorage(ctx, cfg)
		if err != nil {
			return err
		}
		return runWALVerify(ctx, db, obj, cfg, os.Args[2:])
	case "serve":
	default:
		return fmt.Errorf("unknown command %q (expected serve, migrate, seed, gc, compact, wal-seed, wal-verify or hook)", command)
	}

	if err := seedAdmin(ctx, db, cfg); err != nil {
		return err
	}

	obj, err := newStorage(ctx, cfg)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.GitRoot, 0o755); err != nil {
		return fmt.Errorf("create git root %s: %w", cfg.GitRoot, err)
	}
	gitManager := gitrepo.NewManager(cfg.GitRoot)
	if cfg.WALMode == "authoritative" {
		// Phase 4+ (docs/dev/continuity-design.md §15): the WAL is the truth and
		// the directories under GIT_ROOT become a bounded cache.
		gitManager.EnableWAL(obj, cfg.GitCacheBytes)
	}
	parquet := viewer.New(obj, cfg.ViewerCacheDir, cfg.ViewerCacheBytes)
	// Checkpoint headers are read straight out of storage, so this cache only
	// has to hold the parsed result, not the file.
	checkpoints := modelmeta.NewCache(modelmeta.DefaultCacheEntries)
	indexer := experiments.NewIndexer(db, gitManager, obj, parquet)
	hooks := webhooks.New(db, webhooks.Options{
		AllowPrivateTargets: cfg.AllowPrivateWebhookTargets,
		Workers:             cfg.WebhookWorkers,
	})
	sync := syncer.New(db, gitManager, obj, parquet, indexer, hooks, cfg.SyncWorkers)
	// Route B's ingest buffer is only a buffer: the source of truth stays the
	// parquet inside the dataset repository (docs/dev/thinkingface-design.md §8),
	// so the sync worker periodically commits the buffered points there.
	if cfg.ExpFlushInterval > 0 {
		sync.EnableFlush(experiments.NewFlusher(db, gitManager, obj, parquet, cfg.WALMode), cfg.ExpFlushInterval)
	}

	server := api.NewServer(api.Deps{
		Config:      cfg,
		Store:       db,
		Git:         gitManager,
		Storage:     obj,
		Viewer:      parquet,
		Sessions:    auth.NewSessions(cfg.SessionSecret, cfg.SessionTTL),
		Syncer:      sync,
		Experiments: indexer,
		ModelMeta:   checkpoints,
		Webhooks:    hooks,
	})

	if n, err := db.RequeueRunningJobs(ctx); err != nil {
		return fmt.Errorf("requeue interrupted sync jobs: %w", err)
	} else if n > 0 {
		slog.Info("requeued sync jobs interrupted by a previous shutdown", "count", n)
	}

	go sync.Run(ctx)
	go sync.RunFlush(ctx)
	go hooks.Run(ctx)

	// Unencrypted HTTP/2 (h2c, prior knowledge) lets Cloud Run's "HTTP/2
	// end-to-end" reach the container without TLS termination, which is the
	// only way past the 32 MiB HTTP/1 request cap on large pushes
	// (docs/dev/continuity-design.md §12). Plain HTTP/1 clients are unaffected:
	// the server still speaks HTTP/1 on the same port.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	httpServer := &http.Server{
		Addr:      cfg.Addr,
		Handler:   server.Handler(),
		Protocols: protocols,
		// Uploads and clones can be slow; a write timeout would cut them off.
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "public_url", cfg.PublicURL,
			"storage", cfg.StorageDriver, "bucket", cfg.GCSBucket)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// git over SSH (docs/dev/thinkingface-design.md §5). Optional: HTTPS remains
	// the primary transport, so a failure to start SSH must not take the API
	// down with it -- it is logged loudly and the process keeps serving.
	var ssh *sshserver.Server
	if cfg.SSHEnabled {
		ssh, err = sshserver.New(sshserver.Options{
			Addr:        cfg.SSHAddr,
			HostKeyPath: cfg.SSHHostKeyPath,
			IdleTimeout: cfg.SSHIdleTimeout,
		}, db, server)
		if err != nil {
			slog.Error("ssh server disabled: it could not be started", "error", err)
		} else {
			go func() {
				slog.Info("listening for ssh", "addr", cfg.SSHAddr, "host_key", cfg.SSHHostKeyPath)
				if err := ssh.ListenAndServe(); err != nil && !errors.Is(err, sshserver.ErrServerClosed) {
					slog.Error("ssh server stopped", "error", err)
				}
			}()
		}
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if ssh != nil {
			if err := ssh.Shutdown(shutdownCtx); err != nil {
				slog.Warn("ssh server shutdown", "error", err)
			}
		}
		return httpServer.Shutdown(shutdownCtx)
	}
}

// warnInsecureDefaults flags the development credentials at boot. They are
// convenient on a laptop and fatal in production: the seeded admin password is
// public knowledge, and the default session secret lets anyone forge a
// tf_session cookie for any user id.
//
// An https TF_PUBLIC_URL is refused outright by config.Load, so by the time
// this runs the instance is plain HTTP -- a laptop or the compose stack --
// and a warning on every boot is the right weight. Refusing here too would
// break `docker compose up`.
func warnInsecureDefaults(cfg *config.Config) {
	if cfg.AdminPassword == config.DefaultAdminPassword {
		slog.Warn("TF_ADMIN_PASSWORD is unset, so the seeded admin account uses the well-known default password; set it before exposing this instance")
	}
	if cfg.SessionSecret == config.DefaultSessionSecret {
		slog.Warn("TF_SESSION_SECRET is unset, so session cookies and LFS transfer URLs are signed with a publicly known key; set it before exposing this instance")
	}
}

// newStorage builds the object store driver. It is shared by `serve` and the
// operational subcommands (`gc`, `compact`, the `wal-*` pair), which need
// bucket access but not the git manager, viewer or sync worker `serve` also
// constructs.
func newStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
	return storage.NewGCS(ctx, storage.GCSOptions{
		Bucket:       cfg.GCSBucket,
		Prefix:       cfg.GCSPrefix,
		EmulatorHost: emulatorHost(cfg),
	})
}

// emulatorHost returns the emulator address only in emulator mode, so a
// stray STORAGE_EMULATOR_HOST cannot silently redirect a production instance.
func emulatorHost(cfg *config.Config) string {
	if cfg.StorageDriver == "gcs-emulator" {
		return cfg.EmulatorHost
	}
	return ""
}

// seedAdmin creates the first account on an empty instance so a fresh
// `docker compose up` is immediately usable.
func seedAdmin(ctx context.Context, db *store.Store, cfg *config.Config) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	user, err := db.CreateUser(ctx, cfg.AdminUsername, cfg.AdminEmail, hash, true)
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	slog.Info("created initial admin user", "username", user.Username)
	return nil
}
