package hub

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// LocalFile is one file the caller wants in the repository. Open is called at
// least once for hashing and once more for the upload/commit, so it must
// return a fresh reader each time (a closure over an os.Open, or a
// bytes.Reader for generated content such as a README).
type LocalFile struct {
	RepoPath string // forward-slash path inside the repository
	Size     int64
	Open     func() (io.ReadCloser, error)
}

// Plan is everything Upload needs. The repository must already exist; the
// branch may not (the first commit creates it).
type Plan struct {
	Ref Ref
	Rev string // branch name, e.g. "main"

	Files []LocalFile

	// DeleteMissing removes remote files that are not in Files (like
	// `hf upload --delete "*"`). Two root files are never deleted: the
	// server seeds .gitattributes and it decides which paths route through
	// LFS, so dropping it would silently change later uploads; and README.md
	// is the repository card, which `tf up --license/--tag` generates
	// server-side without a local copy, so a later mirror run must not take
	// it away. Both are simply left alone when absent locally.
	DeleteMissing bool

	Summary     string // commit title; "" -> "Upload <n> files with tf"
	Description string // commit body

	// Workers bounds concurrent LFS transfers (0 -> 4).
	Workers int

	// DryRun stops after planning: no LFS upload, no commit. Result still
	// reports what would happen.
	DryRun bool
}

// EventKind enumerates progress notifications.
type EventKind int

const (
	// EventPlanned fires once after preupload + diffing, before any transfer.
	// Fields: Result is populated with counts (Commit == nil).
	EventPlanned EventKind = iota
	// EventHashing fires before a file is hashed (Path, Size, Mode).
	EventHashing
	// EventSkipped fires for a file that is unchanged on the server (Path).
	EventSkipped
	// EventUploadStart / EventUploadDone bracket one LFS transfer (Path, Size).
	EventUploadStart
	EventUploadDone
	// EventDeduplicated fires for an LFS object the server already had (Path).
	EventDeduplicated
	// EventCommitting fires right before the commit request is sent.
	EventCommitting
)

// Event is one progress notification. Only the fields relevant to Kind are set.
type Event struct {
	Kind EventKind
	Path string
	Size int64
	Mode UploadMode
	// Result is set on EventPlanned (a snapshot; Commit is nil at that point).
	Result *Result
}

// Result summarises what Upload did (or, on DryRun / EventPlanned, would do).
type Result struct {
	// Commit is nil when nothing changed (NothingToDo) or DryRun.
	Commit *CommitResult

	// Files that will be / were part of the commit, by mode.
	Regular []string
	LFS     []string
	// Unchanged files the diff skipped (same blob sha / LFS oid as on the server).
	Unchanged []string
	// Deleted remote paths (DeleteMissing). The root .gitattributes and
	// README.md are never listed here: they are excluded from DeleteMissing
	// by design (see Plan.DeleteMissing).
	Deleted []string

	// LFSUploaded is the subset of LFS that actually had to be transferred;
	// the rest were deduplicated server-side.
	LFSUploaded []string

	Bytes         int64 // total bytes of Regular + LFS
	LFSBytes      int64 // bytes of LFS files
	UploadedBytes int64 // bytes actually transferred via LFS
}

// NothingToDo reports whether the commit would be empty.
func (r *Result) NothingToDo() bool {
	return len(r.Regular) == 0 && len(r.LFS) == 0 && len(r.Deleted) == 0
}

// ErrNothingToDo is returned by Upload when every file is already on the
// server and nothing is to be deleted. The Result is still returned alongside.
var ErrNothingToDo = errors.New("nothing to upload: the repository is up to date")

// defaultWorkers bounds concurrent LFS transfers when the plan does not say.
const defaultWorkers = 4

// sampleBytes is how much of a file travels in a preupload request. The server
// routes on path and size alone, but huggingface_hub sends a sample and the
// contract allows one, so the shape stays compatible.
const sampleBytes = 512

// protectedPaths are the root files DeleteMissing never removes: the
// server-seeded .gitattributes (it decides LFS routing for later uploads) and
// README.md (the repository card, which `tf up` may have generated without a
// local copy). Both are normally absent locally, and silently deleting them
// would change the repository in ways the user did not ask for.
var protectedPaths = map[string]bool{".gitattributes": true, "README.md": true}

// defaultRev is the branch an upload targets when the plan names none.
const defaultRev = "main"

// Upload performs the whole "hf upload" dance against an existing repository:
//
//  1. Tree(rev) for the current remote state (nil for an unborn branch).
//  2. Preupload to learn each path's UploadMode.
//  3. Hash each file (sha256 for lfs, git blob sha1 "blob <size>\0<content>"
//     for regular) and drop the ones whose hash matches the remote entry of
//     the same path (EventSkipped). A remote LFS file vs local regular (or the
//     reverse) is always re-sent.
//  4. If DeleteMissing, remote paths absent locally become OpDeleteFile --
//     except the root .gitattributes and README.md, which are never deleted.
//  5. EventPlanned; return (Result, ErrNothingToDo) if the commit would be
//     empty; return (Result, nil) if DryRun.
//  6. LFSBatchUpload for the lfs files (unique by oid); PutLFSObject +
//     VerifyLFSObject for those with an upload action, Workers at a time.
//  7. Commit with file / lfsFile / deletedFile ops. Summary defaults to
//     "Upload N file(s) with tf".
//
// report may be nil. Errors from the server are *Error; a context
// cancellation aborts in-flight transfers.
func Upload(ctx context.Context, c *Client, plan Plan, report func(Event)) (*Result, error) {
	// Progress is reported from the transfer goroutines too, so serialise it:
	// a caller writing to a terminal should not have to hold its own lock.
	var reportMu sync.Mutex
	emit := func(e Event) {
		if report == nil {
			return
		}
		reportMu.Lock()
		defer reportMu.Unlock()
		report(e)
	}

	rev := plan.Rev
	if rev == "" {
		rev = defaultRev
	}

	remote, err := c.Tree(ctx, plan.Ref, rev)
	if err != nil {
		return nil, fmt.Errorf("read remote tree of %s: %w", plan.Ref, err)
	}
	remoteByPath := make(map[string]TreeEntry, len(remote))
	for _, e := range remote {
		remoteByPath[e.Path] = e
	}

	modes, err := preuploadModes(ctx, c, plan, rev)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	var ops []CommitOp
	// lfsGroups collects the objects to transfer, unique by oid: the same
	// bytes under two paths are uploaded once and committed twice.
	var lfsGroups []*lfsGroup
	groupByOID := make(map[string]*lfsGroup)
	local := make(map[string]struct{}, len(plan.Files))

	for _, f := range plan.Files {
		local[f.RepoPath] = struct{}{}
		mode := modes[f.RepoPath]
		if mode != ModeLFS {
			mode = ModeRegular
		}
		emit(Event{Kind: EventHashing, Path: f.RepoPath, Size: f.Size, Mode: mode})

		remoteEntry, onRemote := remoteByPath[f.RepoPath]
		if mode == ModeLFS {
			oid, size, err := hashSHA256(f)
			if err != nil {
				return nil, err
			}
			if onRemote && remoteEntry.LFS != nil && remoteEntry.LFS.OID == oid {
				res.Unchanged = append(res.Unchanged, f.RepoPath)
				emit(Event{Kind: EventSkipped, Path: f.RepoPath, Size: size, Mode: mode})
				continue
			}
			res.LFS = append(res.LFS, f.RepoPath)
			res.Bytes += size
			res.LFSBytes += size
			g, ok := groupByOID[oid]
			if !ok {
				g = &lfsGroup{oid: oid, size: size}
				groupByOID[oid] = g
				lfsGroups = append(lfsGroups, g)
			}
			g.paths = append(g.paths, f.RepoPath)
			ops = append(ops, CommitOp{Kind: OpLFSFile, Path: f.RepoPath, OID: oid, Size: size})
			continue
		}

		sha, err := hashGitBlob(f)
		if err != nil {
			return nil, err
		}
		// A path that flipped between inline and LFS storage always travels
		// again: the remote oid describes the pointer, not the content.
		if onRemote && remoteEntry.LFS == nil && remoteEntry.OID == sha {
			res.Unchanged = append(res.Unchanged, f.RepoPath)
			emit(Event{Kind: EventSkipped, Path: f.RepoPath, Size: f.Size, Mode: mode})
			continue
		}
		res.Regular = append(res.Regular, f.RepoPath)
		res.Bytes += f.Size
		ops = append(ops, CommitOp{Kind: OpFile, Path: f.RepoPath, Open: f.Open, Size: f.Size})
	}

	if plan.DeleteMissing {
		for _, e := range remote {
			if protectedPaths[e.Path] {
				continue
			}
			if _, ok := local[e.Path]; ok {
				continue
			}
			res.Deleted = append(res.Deleted, e.Path)
		}
		sort.Strings(res.Deleted)
		for _, p := range res.Deleted {
			ops = append(ops, CommitOp{Kind: OpDeleteFile, Path: p})
		}
	}

	planned := *res
	emit(Event{Kind: EventPlanned, Result: &planned})

	if res.NothingToDo() {
		return res, ErrNothingToDo
	}
	if plan.DryRun {
		return res, nil
	}

	if err := transferLFS(ctx, c, plan, lfsGroups, res, emit); err != nil {
		return res, err
	}

	emit(Event{Kind: EventCommitting})
	commit, err := c.Commit(ctx, plan.Ref, rev, commitSummary(plan, res), plan.Description, ops)
	if err != nil {
		return res, err
	}
	res.Commit = commit
	return res, nil
}

// lfsGroup is one object to transfer and every path that references it.
type lfsGroup struct {
	oid   string
	size  int64
	paths []string

	batch    LFSBatchResult
	uploaded bool
}

// preuploadModes asks the server how each path travels. An empty plan makes no
// request at all.
func preuploadModes(ctx context.Context, c *Client, plan Plan, rev string) (map[string]UploadMode, error) {
	if len(plan.Files) == 0 {
		return map[string]UploadMode{}, nil
	}
	files := make([]PreuploadFile, 0, len(plan.Files))
	for _, f := range plan.Files {
		sample, err := readSample(f)
		if err != nil {
			return nil, err
		}
		files = append(files, PreuploadFile{Path: f.RepoPath, Size: f.Size, Sample: sample})
	}
	modes, err := c.Preupload(ctx, plan.Ref, rev, files)
	if err != nil {
		return nil, fmt.Errorf("preupload: %w", err)
	}
	return modes, nil
}

// transferLFS runs the batch call and the PUT/verify transfers it asks for.
func transferLFS(ctx context.Context, c *Client, plan Plan, groups []*lfsGroup, res *Result, emit func(Event)) error {
	if len(groups) == 0 {
		return nil
	}
	objs := make([]LFSObject, 0, len(groups))
	for _, g := range groups {
		objs = append(objs, LFSObject{OID: g.oid, Size: g.size})
	}
	batch, err := c.LFSBatchUpload(ctx, plan.Ref, objs)
	if err != nil {
		return fmt.Errorf("lfs batch: %w", err)
	}
	if len(batch) != len(groups) {
		return fmt.Errorf("lfs batch: server answered for %d of %d objects", len(batch), len(groups))
	}

	var pending []*lfsGroup
	for i, g := range groups {
		g.batch = batch[i]
		switch {
		case g.batch.Err != nil:
			return fmt.Errorf("lfs batch for %s: %w", g.paths[0], g.batch.Err)
		case g.batch.Upload == nil:
			// Already in the bucket: the commit may reference it as is.
			for _, p := range g.paths {
				emit(Event{Kind: EventDeduplicated, Path: p, Size: g.size, Mode: ModeLFS})
			}
		default:
			pending = append(pending, g)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	workers := plan.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	byPath := make(map[string]func() (io.ReadCloser, error), len(plan.Files))
	for _, f := range plan.Files {
		byPath[f.RepoPath] = f.Open
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for _, group := range pending {
		g.Go(func() error {
			path := group.paths[0]
			open := byPath[path]
			if open == nil {
				return fmt.Errorf("internal: no reader for %s", path)
			}
			emit(Event{Kind: EventUploadStart, Path: path, Size: group.size, Mode: ModeLFS})
			if err := c.PutLFSObject(gctx, *group.batch.Upload, open, group.size); err != nil {
				return fmt.Errorf("upload %s: %w", path, err)
			}
			if group.batch.Verify != nil {
				obj := LFSObject{OID: group.oid, Size: group.size}
				if err := c.VerifyLFSObject(gctx, *group.batch.Verify, obj); err != nil {
					return fmt.Errorf("verify %s: %w", path, err)
				}
			}
			group.uploaded = true
			emit(Event{Kind: EventUploadDone, Path: path, Size: group.size, Mode: ModeLFS})
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Reported in plan order rather than completion order, so two runs of the
	// same upload print the same thing.
	for _, group := range groups {
		if !group.uploaded {
			continue
		}
		res.LFSUploaded = append(res.LFSUploaded, group.paths...)
		res.UploadedBytes += group.size
	}
	return nil
}

// commitSummary is the plan's title, or a generated one describing the change.
func commitSummary(plan Plan, res *Result) string {
	if plan.Summary != "" {
		return plan.Summary
	}
	if n := len(res.Regular) + len(res.LFS); n > 0 {
		return fmt.Sprintf("Upload %s with tf", countFiles(n))
	}
	return fmt.Sprintf("Delete %s with tf", countFiles(len(res.Deleted)))
}

func countFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// readSample reads the first bytes of a file for the preupload request.
func readSample(f LocalFile) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.RepoPath, err)
	}
	defer rc.Close()
	buf := make([]byte, sampleBytes)
	n, err := io.ReadFull(rc, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read %s: %w", f.RepoPath, err)
	}
	return buf[:n], nil
}

// hashSHA256 digests a file for the LFS protocol and reports its real size.
func hashSHA256(f LocalFile) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", f.RepoPath, err)
	}
	defer rc.Close()
	oid, size, err := SHA256Hex(rc)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", f.RepoPath, err)
	}
	return oid, size, nil
}

// hashGitBlob digests a file the way git names a blob.
func hashGitBlob(f LocalFile) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("open %s: %w", f.RepoPath, err)
	}
	defer rc.Close()
	sha, err := GitBlobSHA1(rc, f.Size)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", f.RepoPath, err)
	}
	return sha, nil
}

// GitBlobSHA1 hashes content the way git does for a blob ("blob <size>\0" +
// bytes), so a local file can be compared with TreeEntry.OID without a
// round-trip. It is exported for tests and for the CLI's --dry-run output.
func GitBlobSHA1(r io.Reader, size int64) (string, error) {
	h := sha1.New()
	if _, err := fmt.Fprintf(h, "blob %d\x00", size); err != nil {
		return "", err
	}
	n, err := io.Copy(h, r)
	if err != nil {
		return "", err
	}
	// The declared size is part of the digest, so a stale one would produce a
	// hash git never would -- worth an error rather than a silent mismatch.
	if n != size {
		return "", fmt.Errorf("read %d bytes but size says %d", n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Hex streams r and returns the lowercase hex digest.
func SHA256Hex(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
