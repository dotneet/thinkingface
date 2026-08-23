package wal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/gitexec"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

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

// Materialize brings the bare repository at gitDir up to the current index
// (§9). Callers must hold the per-repository lock; concurrent materialisation
// of the same directory is not safe.
//
// The order — objects, then refs, then the state file — is load bearing. A
// crash between steps leaves unreferenced objects, which are harmless; the
// reverse order would leave refs pointing at objects that were never applied,
// which is a corrupt repository. The state file is written last for the same
// reason: it is only true once everything it claims has happened.
func Materialize(ctx context.Context, st storage.Storage, gitDir, storagePath string) error {
	idx, gen, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return err
	}
	if gen == 0 {
		// No index object exists: this repository has never been written
		// through the WAL. Rebuilding from an empty index would delete every
		// local ref, so leave the copy untouched and let the first write
		// create the index.
		//
		// This is load bearing during the shadow-write phases of the
		// migration (docs/dev/continuity-design.md §15 Phase 2/3), where the
		// on-disk copy is still the source of truth and many repositories
		// have no index yet. Once the WAL is authoritative (Phase 4+), a
		// missing index on a repository that should have one is the §13
		// "index corrupted / missing" failure and deserves an alarm — revisit
		// this branch when the cutover lands rather than letting it silently
		// absorb that state forever.
		return nil
	}

	local, haveState := readLocalState(gitDir)
	if haveState && local.Generation == gen && isBareRepo(gitDir) {
		return nil // cache hit: generation equality is the whole check (§4)
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

	if err := writeRefs(ctx, gitDir, idx.Refs); err != nil {
		return err
	}
	return writeLocalState(gitDir, gen, idx)
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
func writeRefs(ctx context.Context, gitDir string, refs map[string]string) error {
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
	return alignHEAD(ctx, gitDir, refs)
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

// alignHEAD keeps the symbolic HEAD pointing at a branch that exists, so clones
// of a materialised copy check something out.
//
// TODO(continuity-design): the default branch is repository metadata (Postgres
// `default_branch`) and the index does not carry it, so this reconstructs it
// with a rule — keep HEAD if its target still exists, else prefer
// refs/heads/main, else the alphabetically first branch. A repository whose
// default branch is neither "main" nor first alphabetically gets the wrong HEAD
// after a cache rebuild. Recording the symref in the index (a schema change)
// is the real fix; §9 leaves it open.
func alignHEAD(ctx context.Context, gitDir string, refs map[string]string) error {
	out, err := runGit(ctx, gitDir, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		if _, ok := refs[strings.TrimSpace(out)]; ok {
			return nil
		}
	}

	target := ""
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
	if target == "" {
		return nil // no branches at all: leave whatever init chose
	}
	_, err = runGit(ctx, gitDir, "symbolic-ref", "HEAD", target)
	return err
}
