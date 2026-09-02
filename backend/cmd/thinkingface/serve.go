// The `serve` subcommand: the dependency graph the running server is built
// from, the two listeners it answers on, and the shutdown that drains them.
//
// Separated from main.go because the two halves answer different questions.
// main.go's run() says what this binary can be asked to do; this file says
// what a server *is* -- which of the storage driver, git manager, parquet
// viewer, checkpoint cache, webhook dispatcher and sync worker each of the
// others needs, and in what order they have to be built.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/api"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/modelmeta"
	"github.com/dotneet/thinkingface/backend/internal/sshserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/syncer"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
	"github.com/dotneet/thinkingface/backend/internal/webhooks"
)

// runServe builds the server and blocks until ctx is cancelled or a listener
// fails. The database is already open and migrated; closing it is run()'s job,
// since it owns the handle.
func runServe(ctx context.Context, cfg *config.Config, db *store.Store) error {
	// Fail closed on a hooks directory git would silently ignore, before
	// anything starts listening. This is the server's check rather than
	// config.Load's: only the process that actually serves pushes is broken
	// by a missing hook, and Load runs for `admin` too -- the break-glass
	// path that has to keep working during exactly the incident (a bind mount
	// over the hooks directory) that trips this.
	if cfg.WALMode != "off" {
		if err := config.CheckPreReceiveHook(cfg.GitHooksPath); err != nil {
			return fmt.Errorf("TF_WAL_MODE=%s: %w", cfg.WALMode, err)
		}
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
	parquet := viewer.New(obj, cfg.ViewerMetadataCacheBytes)
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

	// Only jobs whose lease has lapsed, never every running row: with
	// api_max_instances above 1 this process is starting up *beside* live
	// replicas, and the unconditional sweep this replaced handed their
	// in-flight jobs to a second worker. The syncer repeats this sweep on a
	// ticker, so a job interrupted by this process's own shutdown is picked
	// up once its lease expires rather than needing a restart to notice.
	if n, err := db.RequeueExpiredSyncJobs(ctx); err != nil {
		return fmt.Errorf("requeue expired sync jobs: %w", err)
	} else if n > 0 {
		slog.Info("requeued sync jobs whose lease had expired", "count", n)
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
			// The same budget the HTTP auth guard uses: an unauthenticated
			// peer over SSH costs a database lookup per attempt just as one
			// over HTTP costs a bcrypt, and there is no reason for the two
			// transports to disagree about how much of that an address gets.
			AuthRateLimitPerMinute: cfg.AuthRateLimitPerMinute,
			// Two caps, and the per-address one is the one that matters: a
			// global-only cap is reachable by a single host, and because
			// gliderlabs arms IdleTimeout only on the first read or write, a
			// host that opens connections and then says nothing would hold
			// the whole ceiling for TF_SSH_IDLE_TIMEOUT and every other
			// client on the fleet would be refused at admit. Zero on either
			// means the sshserver default.
			MaxUnauthenticatedConns:        cfg.SSHMaxUnauthConns,
			MaxUnauthenticatedConnsPerAddr: cfg.SSHMaxUnauthConnsPerAddr,
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
		return drain(httpServer, ssh)
	}
}

// shutdownGrace is how long each listener gets to finish what it is already
// serving. It is per listener, not shared: a git clone over SSH and a git push
// over HTTP are unrelated transfers, and one being slow is no reason to cut
// the other short.
const shutdownGrace = 20 * time.Second

// drain stops both listeners at once and gives each its own budget.
//
// Sequential shutdown with one shared context was the bug: ssh.Shutdown waits
// on every in-flight clone, so a single slow one consumed the whole 20 seconds
// and handed httpServer.Shutdown an already-expired context — which drops
// in-flight HTTP requests instead of draining them, and on this server an
// in-flight request can be a push whose WAL entry has been written but whose
// response has not been sent. Concurrent is also simply the truth of it:
// neither listener's drain depends on the other's.
func drain(httpServer *http.Server, ssh *sshserver.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	sshDone := make(chan struct{})
	go func() {
		defer close(sshDone)
		if ssh == nil {
			return
		}
		if err := ssh.Shutdown(shutdownCtx); err != nil {
			slog.Warn("ssh server shutdown", "error", err)
		}
	}()

	// The HTTP error is the returned one: it is the transport this server
	// exists for, and SSH is optional (it may not even be running).
	err := httpServer.Shutdown(shutdownCtx)
	<-sshDone
	return err
}
