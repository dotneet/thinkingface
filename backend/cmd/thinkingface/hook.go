package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// walPushTimeout bounds one hook run. Generous on purpose: the first shadow
// push of a pre-existing repository uploads its whole history as one WAL
// entry, and cutting that off would leave the WAL forever one push behind.
const walPushTimeout = 10 * time.Minute

// runHook dispatches `thinkingface hook <name>`. It is called before
// config.Load / store.Open (see main, and docs/dev/continuity-design.md §14):
// the hook runs as a child process of `git receive-pack` on every push, so
// it must not require DATABASE_URL and must stay cheap to start.
func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("hook: missing hook name (expected pre-receive)")
	}
	name := args[0]
	switch name {
	case "pre-receive":
		return runPreReceiveHook(os.Stdin, os.Stderr)
	default:
		return fmt.Errorf("hook: unknown hook %q (expected pre-receive)", name)
	}
}

// runPreReceiveHook implements `thinkingface hook pre-receive`, the binary
// that backend/hooks/pre-receive execs via core.hooksPath on every push
// (docs/dev/continuity-design.md §6.2). Its exit code is the push's fate: 0 lets
// receive-pack migrate the quarantined objects and update refs, non-zero
// discards everything.
//
// Behaviour by TF_WAL_MODE (§15):
//   - ""/"off":       pass through. The hook being wired while the WAL is
//     disabled must not break pushes.
//   - "shadow":       mirror the push into the WAL, but never fail the push —
//     disk is still the truth, and a WAL outage must not take pushes down.
//     Failures go to stderr (relayed to the pushing client) and to the job of
//     `wal-verify` to catch up on.
//   - "authoritative": §6 for real. The push is acknowledged only if the WAL
//     entry is durable and the index CAS succeeded. Any failure rejects the
//     push; ErrStaleRef maps to git's own "fetch first" experience.
//
// Unknown modes fail closed: a typo in TF_WAL_MODE must not silently bypass
// the WAL.
func runPreReceiveHook(stdin io.Reader, stderr io.Writer) error {
	updates, err := parsePreReceiveInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "thinkingface: malformed pre-receive input: %v\n", err)
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	mode := os.Getenv("TF_WAL_MODE")
	if mode == "" || mode == "off" {
		return nil
	}

	storagePath := os.Getenv("TF_WAL_STORAGE_PATH")
	gitDir, dirErr := hookGitDir()

	ctx, cancel := context.WithTimeout(context.Background(), walPushTimeout)
	defer cancel()

	run := func() error {
		if storagePath == "" {
			return fmt.Errorf("hook: TF_WAL_STORAGE_PATH not set")
		}
		if dirErr != nil {
			return dirErr
		}
		st, err := hookStorage(ctx)
		if err != nil {
			return err
		}
		if mode == "shadow" {
			return wal.ShadowPush(ctx, st, gitDir, storagePath, updates)
		}
		return wal.AuthoritativePush(ctx, st, gitDir, storagePath, updates)
	}

	switch mode {
	case "shadow":
		if err := run(); err != nil {
			// Never fail the push: disk is authoritative in this phase. The
			// message reaches the pushing client as a "remote:" line, which
			// is exactly the visibility a silently diverging mirror needs.
			fmt.Fprintf(stderr, "thinkingface: WAL shadow write failed (push accepted, disk remains authoritative): %v\n", err)
		}
		return nil
	case "authoritative":
		err := run()
		if err == nil {
			return nil
		}
		if errors.Is(err, wal.ErrStaleRef) {
			fmt.Fprintf(stderr, "thinkingface: stale info — another push moved this ref; fetch and retry\n")
		} else {
			fmt.Fprintf(stderr, "thinkingface: WAL write failed, push rejected: %v\n", err)
		}
		return err
	default:
		err := fmt.Errorf("hook: unknown TF_WAL_MODE %q", mode)
		fmt.Fprintf(stderr, "thinkingface: %v (push rejected: failing open would bypass the WAL)\n", err)
		return err
	}
}

// hookGitDir resolves the bare repository the hook is running in. git runs
// pre-receive with the repository as the working directory and GIT_DIR in the
// environment (often the relative "."), so anchor it to the cwd.
func hookGitDir() (string, error) {
	dir := os.Getenv("GIT_DIR")
	if dir == "" {
		dir = "."
	}
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("hook: resolve git dir: %w", err)
		}
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir), nil
}

// hookStorage builds the object-store client from the environment gitserver
// passed down (§14). Deliberately not config.Load: that requires DATABASE_URL,
// which the hook must never see.
func hookStorage(ctx context.Context) (storage.Storage, error) {
	driver := os.Getenv("STORAGE_DRIVER")
	emulator := ""
	switch driver {
	case "gcs":
	case "gcs-emulator":
		emulator = os.Getenv("STORAGE_EMULATOR_HOST")
		if emulator == "" {
			return nil, fmt.Errorf("hook: STORAGE_EMULATOR_HOST is required when STORAGE_DRIVER=gcs-emulator")
		}
	default:
		return nil, fmt.Errorf("hook: STORAGE_DRIVER must be gcs or gcs-emulator, got %q", driver)
	}
	return storage.NewGCS(ctx, storage.GCSOptions{
		Bucket:       os.Getenv("GCS_BUCKET"),
		Prefix:       strings.Trim(os.Getenv("GCS_PREFIX"), "/"),
		EmulatorHost: emulator,
	})
}

// parsePreReceiveInput reads the "<old> <new> <ref>" lines git pipes to a
// pre-receive hook's stdin, one per updated ref, straight into wal.RefUpdate —
// one shared type, so this wiring cannot transpose fields while converting
// between two look-alike structs.
// Format: https://git-scm.com/docs/githooks#pre-receive
func parsePreReceiveInput(r io.Reader) ([]wal.RefUpdate, error) {
	var updates []wal.RefUpdate
	scanner := bufio.NewScanner(r)
	// Ref updates only carry object ids and ref names, so the default 64KiB
	// token cap is already ample per line; the max below merely says a
	// single absurd line fails parsing instead of crashing the scanner.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("pre-receive: malformed line %q (want \"<old> <new> <ref>\")", line)
		}
		updates = append(updates, wal.RefUpdate{Ref: fields[2], Old: fields[0], New: fields[1]})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("pre-receive: read stdin: %w", err)
	}
	return updates, nil
}
