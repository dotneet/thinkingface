package wal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// ErrIndexMissing reports that a repository which demonstrably has WAL state —
// a local copy holding refs, or packs in object storage — has no index.json.
//
// This is the §13 "index corrupted / deleted" failure, and it is deliberately
// its own error rather than a variant of "empty repository". The two states are
// byte-identical to a reader of the bucket (ReadIndex maps a missing object to
// an empty index at generation 0), and confusing them is destructive in both
// directions: serving the stale local copy hides the loss until the next push
// truncates the repository to the one ref it touched, and sweeping the packs
// destroys the material docs/dev/wal-index-recovery.md needs to put the index
// back.
var ErrIndexMissing = errors.New("wal: index is missing but the repository has WAL state")

// StateFileName is the local bookkeeping file that records which index
// generation a bare repository currently reflects. It lives directly in the
// bare repository directory under a name git never touches, so `git fsck`,
// `git repack` and friends ignore it.
const StateFileName = "thinkingface-wal-state.json"

// localState mirrors just enough of the index to answer "is this copy current,
// and if not, what is missing" without a second GCS round trip. It also carries
// the refs and seq that Compact needs to rebuild the index it is replacing.
type localState struct {
	Generation int64             `json:"generation"`
	Base       string            `json:"base"`
	Applied    []string          `json:"applied"`
	Refs       map[string]string `json:"refs"`
	Seq        int               `json:"seq"`
}

// Options steers a materialisation beyond what the index alone can say.
//
// The zero value is the pre-cutover behaviour: no known default branch, and
// the WAL treated as a mirror rather than the truth. Shadow and off mode keep
// using it, which is what makes the authoritative-only checks below impossible
// to trip during the migration phases that legitimately have no index yet.
type Options struct {
	// DefaultBranch is the repository's configured default branch
	// (store.Repo.DefaultBranch), without the refs/heads/ prefix. The index
	// does not carry the symbolic HEAD, so without this alignHEAD has to
	// guess; see the comment there for what the guess costs. Empty means
	// "unknown", which is the only reason the guess still exists.
	DefaultBranch string
	// Authoritative says the WAL — not the directory on disk — is the source
	// of truth for this repository (§15 Phase 4+). It turns "no index" from
	// an expected migration state into the §13 failure it is by then.
	Authoritative bool
}

// Materialize brings the bare repository at gitDir up to the current index
// (§9) with no extra knowledge about the repository. See MaterializeWith for
// the form that takes it; this one is what the migration and verification
// tools use, where a repository's metadata is not at hand.
func Materialize(ctx context.Context, st storage.Storage, gitDir, storagePath string) error {
	return MaterializeWith(ctx, st, gitDir, storagePath, Options{})
}

// MaterializeWith brings the bare repository at gitDir up to the current index
// (§9). Callers must hold the per-repository lock; concurrent materialisation
// of the same directory is not safe.
//
// The order — objects, then refs, then the state file — is load bearing. A
// crash between steps leaves unreferenced objects, which are harmless; the
// reverse order would leave refs pointing at objects that were never applied,
// which is a corrupt repository. The state file is written last for the same
// reason: it is only true once everything it claims has happened.
func MaterializeWith(ctx context.Context, st storage.Storage, gitDir, storagePath string, opts Options) error {
	idx, gen, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return err
	}
	if gen == 0 {
		// No index object exists. Rebuilding from an empty index would delete
		// every local ref, so this never rewrites the copy — the only
		// question is whether it is safe to keep serving it.
		//
		// Shadow / off (§15 Phase 2/3): yes, and it is the common case. The
		// on-disk copy is still the source of truth and most repositories
		// have no index yet.
		if !opts.Authoritative {
			return nil
		}
		// Authoritative (Phase 4+, what infra/main.tf deploys): "no index"
		// means one of two things and they must not share an outcome.
		//
		//   * a repository that was never written — freshly created, or one
		//     this instance has never served. No local copy, or a local copy
		//     with no refs. Nothing to serve wrongly; the first write creates
		//     the index.
		//   * a repository whose index is gone (deleted, or the bucket/prefix
		//     was repointed) while this instance still holds a materialised
		//     copy. Serving it means answering from a cache with no authority
		//     behind it, and the next push — whose <old> for a "new" ref is
		//     "" — writes a fresh index containing only that ref, truncating
		//     the repository for every other instance. §13 calls this the
		//     single point of failure; the recovery is
		//     docs/dev/wal-index-recovery.md, and it needs an operator, not a
		//     silent fallback.
		refs, rerr := localRefsIfAny(ctx, gitDir)
		if rerr != nil {
			return rerr
		}
		if len(refs) == 0 {
			return nil
		}
		local, haveState := readLocalState(gitDir)
		slog.Error("wal index is missing for a repository this instance has materialised; refusing to serve the stale local copy",
			"repo", storagePath, "local_refs", len(refs),
			"last_known_generation", local.Generation, "have_local_state", haveState,
			"recovery", "docs/dev/wal-index-recovery.md")
		return fmt.Errorf("%w: %s (%d local refs, last known generation %d)",
			ErrIndexMissing, storagePath, len(refs), local.Generation)
	}

	local, haveState := readLocalState(gitDir)
	if haveState && local.Generation == gen && isBareRepo(gitDir) {
		// Cache hit: generation equality is the whole check (§4) for the
		// object database and the refs, because both are derived from the
		// index and the index is what the generation identifies.
		//
		// HEAD is the exception, and it is why this is not a bare `return
		// nil`. The index does not carry the symbolic ref, so HEAD is
		// whatever alignHEAD made of it for the *first* caller that warmed
		// this copy — and on a cold instance that caller is almost always one
		// that does not know the default branch: a download or a UI page
		// load reaches gitrepo.Manager.Open, which materialises with
		// Options{}. alignHEAD then guesses (refs/heads/main, or the
		// alphabetically first branch), the state file is stamped at that
		// generation, and the `git clone` that arrives afterwards *with* the
		// configured branch returns right here and checks out the guess.
		// Read-before-clone is the common ordering, so repairing only on the
		// rebuild path would leave the wrong HEAD in place indefinitely.
		//
		// Repairing here costs one symbolic-ref read on a path that already
		// stats the directory, and it converges no matter which caller warms
		// the copy first.
		return repairHEAD(ctx, gitDir, opts.DefaultBranch)
	}

	// Anything we cannot reason about precisely — missing or corrupt state,
	// a directory that is not a repository, a base that changed under us
	// (compaction), or applied entries that are not a prefix of the index —
	// falls back to rebuilding from scratch. Wrong-but-plausible incremental
	// application is far more dangerous than a slow rebuild.
	rebuild := !haveState ||
		!isBareRepo(gitDir) ||
		local.Base != idx.Base ||
		!isPrefix(local.Applied, idx.Entries)

	if rebuild {
		if err := recreateBare(ctx, gitDir); err != nil {
			return err
		}
		local = localState{}
		if idx.Base != "" {
			if err := applyPack(ctx, st, gitDir, storage.WALKey(storagePath, idx.Base)); err != nil {
				return fmt.Errorf("apply base %s: %w", idx.Base, err)
			}
		}
	}

	for _, entry := range idx.Entries[len(local.Applied):] {
		if err := applyPack(ctx, st, gitDir, storage.WALKey(storagePath, entry)); err != nil {
			return fmt.Errorf("apply entry %s: %w", entry, err)
		}
	}

	if err := writeRefs(ctx, gitDir, idx.Refs, opts.DefaultBranch); err != nil {
		return err
	}
	return writeLocalState(gitDir, gen, idx)
}

// localRefsIfAny reads the on-disk refs of a directory that may not be a
// repository at all. "Not a repository" is not an error here: it is the
// answer — a repository this instance has never served has no local copy, and
// that is exactly the state that must stay a no-op.
func localRefsIfAny(ctx context.Context, gitDir string) (map[string]string, error) {
	if !isBareRepo(gitDir) {
		return nil, nil
	}
	refs, err := listRefs(ctx, gitDir)
	if err != nil {
		return nil, fmt.Errorf("read local refs %s: %w", gitDir, err)
	}
	return refs, nil
}

// LocalGeneration reports the index generation the local copy reflects, or 0
// when there is no usable state file. Integration code can use it to decide
// whether a cached copy is worth keeping.
func LocalGeneration(gitDir string) int64 {
	local, ok := readLocalState(gitDir)
	if !ok {
		return 0
	}
	return local.Generation
}

func statePath(gitDir string) string { return filepath.Join(gitDir, StateFileName) }

// readLocalState returns ok=false for a missing *or* unparsable file: both mean
// "we do not know what this directory contains", and the caller rebuilds.
func readLocalState(gitDir string) (localState, bool) {
	body, err := os.ReadFile(statePath(gitDir))
	if err != nil {
		return localState{}, false
	}
	var s localState
	if err := json.Unmarshal(body, &s); err != nil {
		return localState{}, false
	}
	if s.Generation <= 0 {
		return localState{}, false
	}
	return s, true
}

// writeLocalState writes atomically: a half-written state file would be
// indistinguishable from a stale one and could claim entries were applied when
// they were not.
func writeLocalState(gitDir string, generation int64, idx *Index) error {
	s := localState{
		Generation: generation,
		Base:       idx.Base,
		Applied:    append([]string(nil), idx.Entries...),
		Refs:       idx.Refs,
		Seq:        idx.Seq,
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode wal state: %w", err)
	}
	tmp := statePath(gitDir) + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write wal state: %w", err)
	}
	if err := os.Rename(tmp, statePath(gitDir)); err != nil {
		return fmt.Errorf("commit wal state: %w", err)
	}
	return nil
}

func isBareRepo(gitDir string) bool {
	st, err := os.Stat(filepath.Join(gitDir, "objects"))
	return err == nil && st.IsDir()
}

func isPrefix(applied, entries []string) bool {
	if len(applied) > len(entries) {
		return false
	}
	for i, a := range applied {
		if entries[i] != a {
			return false
		}
	}
	return true
}

// recreateBare throws the local copy away and initialises an empty bare
// repository. Safe by construction: the local copy holds nothing that is not
// derivable from the WAL.
func recreateBare(ctx context.Context, gitDir string) error {
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("remove %s: %w", gitDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", gitDir, err)
	}
	// HEAD is realigned from the index refs later; "main" is only the seed.
	// Through gitexec so a rebuilt copy is byte-for-byte the repository
	// gitrepo.Manager.Init would have created.
	return gitexec.InitBare(ctx, gitDir, "main")
}

// applyPack streams one WAL pack straight from storage into the object
// database.
//
// index-pack rather than unpack-objects: it keeps the pack whole instead of
// exploding it into loose objects (cheaper for large pushes, and it preserves
// the deltas), and --strict makes it verify object contents and links as it
// goes. Silently accepting a corrupt pack would poison every later
// materialisation of this repository, so failing loudly here is the point.
// --fix-thin is *not* passed: entries are produced without --thin, so a pack
// that needs fixing up is a bug we want to see rather than paper over.
func applyPack(ctx context.Context, st storage.Storage, gitDir, key string) error {
	rc, err := st.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", key, err)
	}
	defer rc.Close()

	br := bufio.NewReader(rc)
	count, err := packObjectCount(br)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if count == 0 {
		// git would happily store a zero-object pack, but it contributes
		// nothing and every later repack has to sweep it up. Empty entries do
		// occur: a push whose commits the server already had produces one.
		return nil
	}

	cmd := gitCommand(ctx, gitDir, "index-pack", "--stdin", "--strict")
	cmd.Stdin = br
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("index-pack %s: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// writeRefs projects the index refs onto the local copy in one batch, including
// deletions: a ref another instance removed must disappear here too, or clones
// served from this copy would resurrect it (§9).
//
// No old-value assertions are used. The index is the authority; whatever the
// local copy believed is irrelevant (invariant 5 of §5).
func writeRefs(ctx context.Context, gitDir string, refs map[string]string, defaultBranch string) error {
	current, err := listRefs(ctx, gitDir)
	if err != nil {
		return err
	}

	var stdin bytes.Buffer
	names := make([]string, 0, len(refs))
	for ref := range refs {
		names = append(names, ref)
	}
	sort.Strings(names)
	for _, ref := range names {
		if current[ref] == refs[ref] {
			continue
		}
		fmt.Fprintf(&stdin, "update %s %s\n", ref, refs[ref])
	}
	stale := make([]string, 0)
	for ref := range current {
		if _, ok := refs[ref]; !ok {
			stale = append(stale, ref)
		}
	}
	sort.Strings(stale)
	for _, ref := range stale {
		fmt.Fprintf(&stdin, "delete %s\n", ref)
	}

	if stdin.Len() > 0 {
		cmd := gitCommand(ctx, gitDir, "update-ref", "--stdin")
		cmd.Stdin = &stdin
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("update-ref --stdin: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	return alignHEAD(ctx, gitDir, refs, defaultBranch)
}

func listRefs(ctx context.Context, gitDir string) (map[string]string, error) {
	out, err := runGit(ctx, gitDir, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		ref, hash, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		refs[ref] = hash
	}
	return refs, nil
}

// alignHEAD points the symbolic HEAD at the repository's default branch, so a
// clone of a materialised copy checks out what the repository says it should.
//
// defaultBranch is the configured branch (store.Repo.DefaultBranch), passed in
// by the caller because the index does not carry the symref. When it is known
// it wins outright — including when the branch is unborn, which is what an
// empty repository's HEAD is supposed to look like, and which recreateBare
// otherwise leaves seeded as "main".
//
// The guess only survives for callers that cannot know: `wal-verify`, `compact`
// and other tooling that works from the bucket alone. It is the old rule — keep
// HEAD if its target still exists, else prefer refs/heads/main, else the
// alphabetically first branch — and it is wrong for a repository whose default
// branch is neither. That is tolerable there (nothing clones from a scratch
// directory) and is why the parameter exists for the paths that serve clients.
// repairHEAD points an already-current local copy's HEAD at the configured
// default branch when it is not there already.
//
// It is a no-op when the caller does not know the branch: there is nothing
// better to say than whatever guess alignHEAD already made, and overwriting it
// from here would only replace one guess with another. It is also a no-op when
// HEAD already agrees, which is the overwhelmingly common case — so the cache
// hit stays one cheap read of a file git keeps in the repository root.
func repairHEAD(ctx context.Context, gitDir, defaultBranch string) error {
	if defaultBranch == "" {
		return nil
	}
	want := "refs/heads/" + defaultBranch
	// Read the HEAD file rather than asking git for it. This runs on the
	// generation cache hit, which is every git smart-HTTP request and every
	// flush once the local copy is warm, so a `git symbolic-ref` here would
	// add one fork+exec per request to the hottest path in the server for an
	// answer that is a single line of text in the repository root.
	//
	// A symref HEAD is "ref: <target>\n"; anything else (a raw sha, i.e. a
	// detached HEAD, or an unreadable file) simply does not agree, and the fix
	// is the same in every one of those cases, so they share one branch.
	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		if target, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok &&
			strings.TrimSpace(target) == want {
			return nil
		}
	}
	if _, err := runGit(ctx, gitDir, "symbolic-ref", "HEAD", want); err != nil {
		return fmt.Errorf("align HEAD of %s to %s: %w", gitDir, want, err)
	}
	return nil
}

func alignHEAD(ctx context.Context, gitDir string, refs map[string]string, defaultBranch string) error {
	target := ""
	if defaultBranch != "" {
		target = "refs/heads/" + defaultBranch
	} else {
		out, err := runGit(ctx, gitDir, "symbolic-ref", "--quiet", "HEAD")
		if err == nil {
			if _, ok := refs[strings.TrimSpace(out)]; ok {
				return nil
			}
		}
		if _, ok := refs["refs/heads/main"]; ok {
			target = "refs/heads/main"
		} else {
			branches := make([]string, 0)
			for ref := range refs {
				if strings.HasPrefix(ref, "refs/heads/") {
					branches = append(branches, ref)
				}
			}
			sort.Strings(branches)
			if len(branches) > 0 {
				target = branches[0]
			}
		}
	}
	if target == "" {
		return nil // no branches at all: leave whatever init chose
	}
	_, err := runGit(ctx, gitDir, "symbolic-ref", "HEAD", target)
	return err
}
