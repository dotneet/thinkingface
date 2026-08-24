// The consistency check promised by docs/dev/thinkingface-design.md §17
// ("Consistency between blobs/ and git: a Sync failure can cause them to
// drift apart"):
//
//	resync  cross-check the database index against the two content-addressed
//	        storage layers and against the real git trees, and -- with --yes
//	        -- repair whatever can be regenerated
//
// It is the inverse of `gc` (gc.go). gc starts from the bucket and asks which
// objects nothing references any more; resync starts from the database and
// asks whether what it promises is actually there. Both leave the instance
// alone by default and only report.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// blobsPrefix is where storage.BlobKey puts every published blob. Unlike
// lfs/ (storage.LFSPrefix) the package exposes no constant for it, so this is
// spelled the same way gc.go spells it -- and only ever for the one *listing*
// resync does. Every individual key still comes from storage.BlobKey /
// storage.LFSKey, so a change in the sharding layout cannot desynchronise the
// check from the write path.
const blobsPrefix = "blobs/"

// resyncProgressEvery is how often the scan reports how far it has come. A
// resync on a large instance is minutes of git reads with no output at all
// otherwise, and an operator watching it needs to know it is still moving.
const resyncProgressEvery = 25

// repoSelector restricts a run to one repository. An empty selector matches
// everything, which is the default.
type repoSelector struct {
	// kind is "model", "dataset" or "" for either. A repository name is only
	// unique per kind, so `--repo admin/foo` can legitimately match two.
	kind      string
	namespace string
	name      string
}

func (s repoSelector) empty() bool { return s.namespace == "" && s.name == "" }

func (s repoSelector) matches(ref store.RepoRef) bool {
	if s.empty() {
		return true
	}
	if s.kind != "" && s.kind != ref.Kind {
		return false
	}
	return s.namespace == ref.Namespace && s.name == ref.Name
}

// parseRepoSelector accepts "{ns}/{name}" and "{kind}/{ns}/{name}". The kind
// is taken in either the singular the database stores ("model") or the plural
// the URLs use ("models"), because an operator copying a repository out of a
// browser tab has the plural in front of them.
func parseRepoSelector(raw string) (repoSelector, error) {
	if raw == "" {
		return repoSelector{}, nil
	}
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	var sel repoSelector
	switch len(parts) {
	case 2:
		sel = repoSelector{namespace: parts[0], name: parts[1]}
	case 3:
		sel = repoSelector{kind: parts[0], namespace: parts[1], name: parts[2]}
	default:
		return repoSelector{}, fmt.Errorf("--repo %q: expected {ns}/{name} or {kind}/{ns}/{name}", raw)
	}
	switch sel.kind {
	case "", "model", "dataset":
	case "models":
		sel.kind = "model"
	case "datasets":
		sel.kind = "dataset"
	default:
		return repoSelector{}, fmt.Errorf("--repo %q: unknown kind %q (expected model or dataset)", raw, sel.kind)
	}
	if sel.namespace == "" || sel.name == "" {
		return repoSelector{}, fmt.Errorf("--repo %q: namespace and name must not be empty", raw)
	}
	return sel, nil
}

// storedObjects is the set of object names each content-addressed layer
// actually holds, keyed by the identifier the database records: a git blob
// sha for blobs/, an oid for lfs/. Both layers are listed once for the whole
// run rather than Stat-ed per file: a repository's files are shared instance
// wide, so the same key would otherwise be probed once per repository that
// carries it.
type storedObjects struct {
	blobs map[string]bool
	lfs   map[string]bool
}

// storedSet reduces a listing to the set of content identifiers it holds. Both
// layers shard by the first four characters of the identifier and end in the
// identifier itself, so the last path element is the name in both cases --
// the same reduction gc makes when it maps an object back onto a row.
func storedSet(objects []storage.ObjectInfo) map[string]bool {
	out := make(map[string]bool, len(objects))
	for _, o := range objects {
		out[path.Base(o.Key)] = true
	}
	return out
}

// missingObject is one indexed file whose bytes are not in storage.
type missingObject struct {
	Path string
	// ID is the blob sha (LFS false) or the oid (LFS true).
	ID  string
	LFS bool
}

// missingObjects reports the indexed files of one ref whose content-addressed
// object is absent. It works from repo_files, which is exactly the promise
// being checked: the sync worker publishes every blob of a revision *before*
// it writes that revision's rows, so a row whose object is missing means the
// publish was undone (or never happened) after the fact.
//
// Pure, so the decision can be unit tested without a bucket.
func missingObjects(files []store.RepoFile, stored storedObjects) []missingObject {
	out := make([]missingObject, 0)
	for _, f := range files {
		if f.LFSOID != nil {
			if !stored.lfs[*f.LFSOID] {
				out = append(out, missingObject{Path: f.Path, ID: *f.LFSOID, LFS: true})
			}
			continue
		}
		if !stored.blobs[f.BlobSHA] {
			out = append(out, missingObject{Path: f.Path, ID: f.BlobSHA})
		}
	}
	return out
}

// indexDiff is how far one ref's repo_files rows have drifted from the tree
// git actually holds at that ref.
type indexDiff struct {
	// Missing are paths the tree carries and the index does not.
	Missing []string
	// Extra are paths the index still carries and the tree does not.
	Extra []string
	// Changed are paths both carry, pointing at different content.
	Changed []string
}

func (d indexDiff) total() int { return len(d.Missing) + len(d.Extra) + len(d.Changed) }

// diffIndex compares the real tree against the indexed listing for one ref.
// A non-empty result is the shape a failed sync job leaves behind: the ref
// moved, its job never completed, and the index froze at the previous push.
//
// Pure, so the decision can be unit tested without git or a database.
func diffIndex(tree, indexed []store.RepoFile) indexDiff {
	byPath := make(map[string]store.RepoFile, len(indexed))
	for _, f := range indexed {
		byPath[f.Path] = f
	}
	diff := indexDiff{Missing: []string{}, Extra: []string{}, Changed: []string{}}
	seen := make(map[string]bool, len(tree))
	for _, f := range tree {
		seen[f.Path] = true
		got, ok := byPath[f.Path]
		switch {
		case !ok:
			diff.Missing = append(diff.Missing, f.Path)
		case !sameContent(f, got):
			diff.Changed = append(diff.Changed, f.Path)
		}
	}
	for _, f := range indexed {
		if !seen[f.Path] {
			diff.Extra = append(diff.Extra, f.Path)
		}
	}
	return diff
}

// sameContent compares the two identities a row carries. Size is deliberately
// left out: it is derived from the blob (or from the pointer, for LFS), so it
// cannot differ on its own without one of these differing too, and comparing
// it would turn a harmless size backfill into a reported inconsistency.
func sameContent(a, b store.RepoFile) bool {
	if a.BlobSHA != b.BlobSHA {
		return false
	}
	switch {
	case a.LFSOID == nil && b.LFSOID == nil:
		return true
	case a.LFSOID == nil || b.LFSOID == nil:
		return false
	default:
		return *a.LFSOID == *b.LFSOID
	}
}

// treeFiles converts a recursive tree listing into the rows the index would
// hold for it. It mirrors the conversion in syncer.runPushPipeline, which is
// what makes the comparison meaningful: resync has to build the exact rows the
// sync worker would have written, not an approximation of them.
func treeFiles(entries []gitrepo.Entry) []store.RepoFile {
	out := make([]store.RepoFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		f := store.RepoFile{Path: e.Path, Size: e.TargetSize(), BlobSHA: e.Hash.String()}
		if e.LFS != nil {
			oid := e.LFS.OID
			f.LFSOID = &oid
		}
		out = append(out, f)
	}
	return out
}

// resyncStats is the run's tally, printed as the closing summary.
type resyncStats struct {
	repos int
	refs  int

	missingBlobs int
	missingLFS   int
	staleRefs    int

	republished int
	reenqueued  int
	// repairFailures counts repairs that were attempted and failed, as
	// opposed to problems left alone because nothing can repair them.
	repairFailures int
}

// unrepaired reports how many findings the run leaves behind: everything in
// dry run, and in execute mode whatever could not be regenerated.
func (s resyncStats) unrepaired() int {
	return (s.missingBlobs - s.republished) + s.missingLFS + (s.staleRefs - s.reenqueued)
}

// resyncRun carries one invocation's dependencies and tally.
type resyncRun struct {
	db      *store.Store
	obj     storage.Storage
	git     *gitrepo.Manager
	stored  storedObjects
	execute bool
	stats   resyncStats
}

// runResync cross-checks the database index against storage and git:
//
//  1. repo_files ⇄ blobs/     -- every indexed non-LFS file has its object
//  2. repo_files ⇄ lfs/       -- every indexed LFS file has its object
//  3. git tree   ⇄ repo_files -- the index matches what the ref really holds
//
// The default is a report and nothing else. --yes repairs only what can be
// regenerated from something that still exists: a missing blob is re-published
// out of the git object database, and a drifted index is re-enqueued for the
// sync worker. A missing LFS object is *never* repaired -- its bytes only ever
// lived in the bucket, so there is nothing to regenerate them from and the
// only honest answer is to name the file and let a human re-upload it.
//
// A run that ends with findings it did not repair exits non-zero, so this can
// be scheduled and its verdict acted on the way `wal-verify` already is.
func runResync(ctx context.Context, db *store.Store, obj storage.Storage, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("resync", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", true, "report inconsistencies without changing anything (default)")
	yes := fs.Bool("yes", false, "republish missing blobs and re-enqueue sync jobs for drifted indexes")
	repoFlag := fs.String("repo", "", "check only this repository: {ns}/{name} or {kind}/{ns}/{name}")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Same authorisation rule as gc: either flag on its own is enough, and
	// the default (dry-run=true, yes=false) only reports.
	execute := *yes || !*dryRun

	selector, err := parseRepoSelector(*repoFlag)
	if err != nil {
		return err
	}

	all, err := db.AllRepoRefs(ctx)
	if err != nil {
		return fmt.Errorf("list repositories: %w", err)
	}
	targets := make([]store.RepoRef, 0, len(all))
	for _, ref := range all {
		if selector.matches(ref) {
			targets = append(targets, ref)
		}
	}
	if len(targets) == 0 {
		if !selector.empty() {
			return fmt.Errorf("--repo %q matched no repository", *repoFlag)
		}
		fmt.Println("no repositories to check")
		return nil
	}

	// Both layers are listed before any repository is read, so an object
	// published while the scan runs is missing from these sets rather than
	// wrongly present in them. That ordering can only cause a false *report*
	// (of an object that has since appeared), never a false repair: a
	// re-publish of an object that is already there is a no-op, and a stale
	// index is decided against git, not against these listings.
	blobs, err := obj.List(ctx, blobsPrefix)
	if err != nil {
		return fmt.Errorf("list blobs: %w", err)
	}
	lfsObjects, err := obj.List(ctx, storage.LFSPrefix)
	if err != nil {
		return fmt.Errorf("list lfs objects: %w", err)
	}

	git := gitrepo.NewManager(cfg.GitRoot)
	if cfg.WALMode == "authoritative" {
		// The same read path `serve` uses: with the WAL authoritative the
		// directories under GIT_ROOT are a cache, and a repository this
		// machine has never served is not on disk at all. Without this the
		// scan would silently find nothing to compare against.
		git.EnableWAL(obj, cfg.GitCacheBytes)
	}

	run := &resyncRun{
		db:      db,
		obj:     obj,
		git:     git,
		stored:  storedObjects{blobs: storedSet(blobs), lfs: storedSet(lfsObjects)},
		execute: execute,
	}

	fmt.Printf("checking %d repositories against %d blobs and %d lfs objects\n",
		len(targets), len(run.stored.blobs), len(run.stored.lfs))

	var errs []error
	for i, ref := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := run.repo(ctx, ref); err != nil {
			// One unreadable repository must not cost the rest their check:
			// the passes share no state, and an operator running this
			// wants the whole picture, not the part before the first
			// failure.
			slog.Error("resync: repository check failed", "repo", refName(ref), "error", err)
			errs = append(errs, err)
		}
		if n := i + 1; n%resyncProgressEvery == 0 && n < len(targets) {
			fmt.Printf("… checked %d/%d repositories\n", n, len(targets))
		}
	}

	run.summarise(len(targets))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if run.stats.repairFailures > 0 {
		return fmt.Errorf("%d repairs failed; see the logged errors above", run.stats.repairFailures)
	}
	if n := run.stats.unrepaired(); n > 0 {
		if execute {
			return fmt.Errorf("resync: %d inconsistencies remain that nothing can regenerate", n)
		}
		return fmt.Errorf("resync: %d inconsistencies found (re-run with --yes to repair what can be)", n)
	}
	return nil
}

// repo checks every branch of one repository. Branches are the refs the index
// is keyed by (the sync worker enqueues per branch after a push), and git is
// the authority on which of them exist, so they are read from the repository
// rather than from the rows being verified.
func (r *resyncRun) repo(ctx context.Context, ref store.RepoRef) error {
	repo, err := r.db.GetRepo(ctx, ref.Kind, ref.Namespace, ref.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Deleted between AllRepoRefs and now: nothing left to check.
			return nil
		}
		return fmt.Errorf("load %s: %w", refName(ref), err)
	}
	gitRepo, err := r.git.Open(repo.StoragePath)
	if err != nil {
		if errors.Is(err, gitrepo.ErrRepoNotFound) {
			slog.Warn("resync: no git directory, skipping", "repo", refName(ref))
			return nil
		}
		return fmt.Errorf("open %s: %w", refName(ref), err)
	}
	branches, err := gitRepo.Branches()
	if err != nil {
		return fmt.Errorf("list branches of %s: %w", refName(ref), err)
	}
	r.stats.repos++

	var errs []error
	for _, branch := range branches {
		if err := r.ref(ctx, repo, gitRepo, branch); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ref runs all three checks for one branch.
func (r *resyncRun) ref(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, branch string) error {
	entries, commit, err := gitRepo.Tree(branch, "", true)
	if err != nil {
		if errors.Is(err, gitrepo.ErrEmptyRepo) {
			return nil
		}
		return fmt.Errorf("read tree %s@%s: %w", repo.FullName(), branch, err)
	}
	indexed, err := r.db.ListRepoFiles(ctx, repo.ID, branch)
	if err != nil {
		return fmt.Errorf("list indexed files of %s@%s: %w", repo.FullName(), branch, err)
	}
	r.stats.refs++

	r.checkObjects(ctx, repo, gitRepo, branch, indexed)
	r.checkIndex(ctx, repo, branch, commit.String(), diffIndex(treeFiles(entries), indexed))
	return nil
}

// checkObjects reports (and, with --yes, repairs) indexed files whose bytes
// are not in the bucket.
//
// A missing blob is re-published straight from the git object database with
// gitrepo.PublishBlob -- the single write path of the blobs/ layer, shared
// with the sync worker and the readers that get ahead of it. Re-enqueuing the
// sync job would *not* fix this: Syncer.publishBlobs deliberately skips every
// sha the ref's index already covers, which is precisely the set this pass
// finds broken.
func (r *resyncRun) checkObjects(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, branch string, indexed []store.RepoFile) {
	for _, m := range missingObjects(indexed, r.stored) {
		if m.LFS {
			r.stats.missingLFS++
			// Reported and never repaired: the bytes of an LFS file exist
			// nowhere but the bucket (git holds a pointer), so nothing on
			// this instance can regenerate them.
			fmt.Printf("missing lfs     %s@%s  %s  %s  (not recoverable: re-upload the file)\n",
				repo.FullName(), branch, m.Path, m.ID)
			continue
		}
		r.stats.missingBlobs++
		fmt.Printf("missing blob    %s@%s  %s  %s\n", repo.FullName(), branch, m.Path, m.ID)
		if !r.execute {
			continue
		}
		if _, err := gitRepo.PublishBlob(ctx, r.obj, plumbing.NewHash(m.ID)); err != nil {
			slog.Error("resync: republish failed", "repo", repo.FullName(), "ref", branch,
				"path", m.Path, "sha", m.ID, "error", err)
			r.stats.repairFailures++
			continue
		}
		// Remember it, so the same sha carried by another repository is not
		// republished (or reported) a second time in this run.
		r.stored.blobs[m.ID] = true
		r.stats.republished++
	}
}

// checkIndex reports (and, with --yes, repairs) a ref whose repo_files rows no
// longer describe the tree git holds -- the state a failed or abandoned sync
// job leaves behind.
//
// The repair is an ordinary sync job, not a write from here. Rebuilding the
// index means republishing blobs, re-reading the repo card, re-indexing
// parquet, lineage and experiments; the sync worker is that pipeline, and a
// second implementation of it in a CLI would be one more thing to drift.
// old_sha is left empty on purpose: it makes the pipeline treat the whole
// tree as changed instead of trusting a diff against a revision the frozen
// index may never have covered.
func (r *resyncRun) checkIndex(ctx context.Context, repo *store.Repo, branch, head string, diff indexDiff) {
	if diff.total() == 0 {
		return
	}
	r.stats.staleRefs++
	fmt.Printf("stale index     %s@%s  %d missing, %d extra, %d changed  (head %s)\n",
		repo.FullName(), branch, len(diff.Missing), len(diff.Extra), len(diff.Changed), head)
	for _, p := range diff.Missing {
		fmt.Printf("  not indexed   %s\n", p)
	}
	for _, p := range diff.Extra {
		fmt.Printf("  indexed only  %s\n", p)
	}
	for _, p := range diff.Changed {
		fmt.Printf("  differs       %s\n", p)
	}
	if !r.execute {
		return
	}
	if err := r.db.EnqueueSync(ctx, repo.ID, branch, "", head); err != nil {
		slog.Error("resync: re-enqueue failed", "repo", repo.FullName(), "ref", branch, "error", err)
		r.stats.repairFailures++
		return
	}
	r.stats.reenqueued++
}

// summarise prints the closing report. It always prints, including when
// everything is consistent: "nothing to report" is the answer an operator ran
// this command for.
func (r *resyncRun) summarise(targets int) {
	s := r.stats
	fmt.Printf("checked %d of %d repositories (%d refs): %d missing blobs, %d missing lfs objects, %d stale ref indexes\n",
		s.repos, targets, s.refs, s.missingBlobs, s.missingLFS, s.staleRefs)
	if s.missingBlobs+s.missingLFS+s.staleRefs == 0 {
		fmt.Println("index, git and storage agree")
		return
	}
	if !r.execute {
		fmt.Println("dry run: nothing changed. Re-run with --yes to republish missing blobs and re-enqueue the drifted refs.")
		return
	}
	fmt.Printf("republished %d of %d missing blobs, re-enqueued %d of %d stale ref indexes\n",
		s.republished, s.missingBlobs, s.reenqueued, s.staleRefs)
	if s.missingLFS > 0 {
		fmt.Printf("%d missing lfs objects were only reported: their bytes cannot be regenerated\n", s.missingLFS)
	}
}
