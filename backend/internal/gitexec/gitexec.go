// Package gitexec centralises how this system runs the git binary: the
// environment every invocation gets, the server-side configuration injected
// into it, and the single way a bare repository is created.
//
// The configuration travels in the environment (GIT_CONFIG_COUNT and friends,
// which git treats exactly like `-c`) rather than being written into each
// repository's config file. Two reasons:
//
//   - With the WAL authoritative the bare directory is a cache that
//     wal.Materialize deletes and re-creates from the index (recreateBare), so
//     anything `git config` wrote there would silently vanish on the next
//     catch-up. Half the repositories on an instance would end up configured
//     and half not, with no way to tell them apart.
//   - It keeps operational settings out of the repository's mutable state,
//     which a push can rewrite. Same reasoning as core.hooksPath in
//     internal/gitserver.
//
// Every exec of git in this codebase must use Env(): a bare exec.Command
// inherits the operator's ~/.gitconfig, which makes a developer's machine
// behave differently from the container.
package gitexec

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
)

// BigFileThreshold mirrors gitrepo.LFSInlineThreshold: the size at which this
// system stops treating a blob as an ordinary file. Telling git the same
// number (core.bigFileThreshold) means the blobs that only reach the object
// database because no .gitattributes rule matched them are stored deflated and
// never delta-compressed, instead of git spending a 512 MiB window on them.
// gitrepo's TestBigFileThresholdMatchesLFSThreshold keeps the two in step.
const BigFileThreshold = 10 << 20 // 10 MiB

// packThreads and packWindowMemory bound what one pack-objects can cost.
// pack-objects allocates the delta window per thread, so the ceiling is
// threads x windowMemory; left unconfigured, git auto-detects the host's CPU
// count (not the container's quota) and takes no memory limit at all, which is
// how a single clone of a large repository takes the whole server process down
// with it on a memory-capped Cloud Run instance. Repositories here stay small
// because the bulk of every model and dataset lives in LFS
// (docs/dev/continuity-design.md §16-3), so the packing this gives up is worth
// far less than the ceiling it buys.
const (
	packThreads      = 2
	packWindowMemory = "64m"
)

// serverConfig is the configuration every git invocation runs with. Keep the
// rationale next to each entry: these are load-bearing, and an entry nobody
// can justify is an entry nobody dares remove.
var serverConfig = [][2]string{
	// Reject malformed or hostile objects at the door. This server takes
	// pushes from anyone with write access to a repository, and in WAL mode
	// what a push produces is uploaded to object storage as an immutable
	// entry -- there is no cheap "undo" once a bad object is in the log.
	// receive-pack runs the check while the objects are still in the
	// quarantine directory, so a rejected push leaves nothing behind.
	// (This also covers the .gitmodules and .git-lookalike path checks that
	// protect clients cloning onto HFS+/NTFS.)
	{"receive.fsckObjects", "true"},

	// Do not run maintenance inside a push. By default receive-pack ends with
	// `git maintenance run --auto`, which repacks in the request path. In WAL
	// mode the local repository is a disposable cache whose packs wal.Compact
	// rebuilds anyway, so the work is pure latency; worse, it can run against
	// a directory the cache is about to evict or re-materialise.
	{"receive.autogc", "false"},

	{"core.bigFileThreshold", strconv.Itoa(BigFileThreshold)},
	{"pack.threads", strconv.Itoa(packThreads)},
	{"pack.windowMemory", packWindowMemory},

	// Partial clone (`git clone --filter=blob:none`) and fetching a commit by
	// its object name (`git fetch origin <sha>`) are both things Hub users
	// reach for; neither is enabled by default. Access control is per
	// repository and happens before upload-pack is reached, so allowing an
	// arbitrary object name in a want exposes nothing extra.
	{"uploadpack.allowFilter", "true"},
	{"uploadpack.allowAnySHA1InWant", "true"},
}

// Env is the environment for every git subprocess this system starts.
//
// The config sources are pinned to /dev/null so the result does not depend on
// the machine: without that, `make dev-api` picks up the developer's global
// git config (init.templateDir, core.hooksPath, commit signing) and the
// container does not.
func Env() []string {
	env := []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/tmp",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(len(serverConfig)),
	}
	for i, kv := range serverConfig {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]),
		)
	}
	return env
}

// InitBare creates one empty bare repository. Both callers -- creating a new
// repository (gitrepo.Manager.Init) and rebuilding a local copy from the WAL
// (wal.Materialize) -- go through here so a repository's starting state does
// not depend on which path produced it.
//
// --template= (empty) is what makes that true. Without it git copies whatever
// /usr/share/git-core/templates or an inherited GIT_TEMPLATE_DIR holds: fifteen
// sample hooks, info/exclude and a description file, none of which this system
// uses and any of which an operator could have replaced. --object-format is
// pinned for the same reason -- clients negotiate against what the repository
// was created with, so it must not follow git's default if that ever moves.
func InitBare(ctx context.Context, dir, defaultBranch string) error {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	cmd := exec.CommandContext(ctx, "git", "init", "--bare",
		"--template=", "--object-format=sha1", "--initial-branch="+defaultBranch, dir)
	cmd.Env = Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init %s: %w: %s", dir, err, out)
	}
	return nil
}
