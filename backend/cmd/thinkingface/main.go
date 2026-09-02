// Command thinkingface runs the whole backend: REST API, git smart HTTP, LFS,
// the parquet viewer, and the sync worker, from one binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
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
		// -h is not a failure. Every subcommand parses with
		// flag.ContinueOnError so that the deferred cleanups in run() still
		// happen, which means `flag.Parse` hands back ErrHelp instead of
		// exiting 0 on its own the way ExitOnError did; usage has already been
		// printed by then.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		slog.Error("fatal", "command", command, "error", err)
		os.Exit(1)
	}
}

// run prepares what every subcommand needs -- configuration, a migrated
// database, and a context cancelled on SIGINT/SIGTERM -- and dispatches.
//
// `serve` is one case among the others rather than the tail of this function,
// which it used to be: everything the server itself needs (the storage
// driver, the git manager, the viewer, the sync worker, two listeners and
// their shutdown) now lives in runServe, so this reads as the list of things
// this binary can be asked to do.
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
	case "serve":
		return runServe(ctx, cfg, db)
	case "migrate":
		slog.Info("migrations applied")
		return nil
	case "seed":
		return seedAdmin(ctx, db, cfg)
	case "admin":
		// The break-glass path (admincli.go). Nothing but the database is
		// needed, so it runs here rather than after the storage driver, the
		// git manager and the sync worker are built -- an instance that
		// cannot reach its bucket must still be able to reset a password.
		return runAdmin(ctx, db, os.Args[2:], os.Stdout)
	case "gc":
		return withStorage(ctx, cfg, func(obj storage.Storage) error {
			return runGC(ctx, db, obj, cfg.SignedURLMaxTTL, os.Args[2:])
		})
	case "resync":
		return withStorage(ctx, cfg, func(obj storage.Storage) error {
			return runResync(ctx, db, obj, cfg, os.Args[2:])
		})
	case "compact":
		return withStorage(ctx, cfg, func(obj storage.Storage) error {
			return runCompact(ctx, db, obj, cfg, os.Args[2:])
		})
	case "wal-seed":
		return withStorage(ctx, cfg, func(obj storage.Storage) error {
			return runWALSeed(ctx, db, obj, cfg, os.Args[2:])
		})
	case "wal-verify":
		return withStorage(ctx, cfg, func(obj storage.Storage) error {
			return runWALVerify(ctx, db, obj, cfg, os.Args[2:])
		})
	default:
		return fmt.Errorf("unknown command %q (expected serve, migrate, seed, admin, gc, resync, compact, wal-seed, wal-verify or hook)", command)
	}
}

// withStorage builds the object store driver and hands it to fn. The five
// operational subcommands that need bucket access all did this inline, which
// is three lines of error handling repeated per case for one call -- and the
// shape they share is worth naming, because it is also the boundary at which
// a driver that cannot be built stops the subcommand before it touches
// anything.
func withStorage(ctx context.Context, cfg *config.Config, fn func(storage.Storage) error) error {
	obj, err := newStorage(ctx, cfg)
	if err != nil {
		return err
	}
	return fn(obj)
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
