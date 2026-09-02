// Reading the *contents* of a commit's change: the unified diff of every path
// it touched, against its first parent. diff.go answers "which paths changed",
// which is all the post-push indexer needs; this file answers "what changed in
// them", which is what a commit page shows.
//
// A merge commit is diffed against its first parent only. That is the same
// walk history.go does, and it is what keeps the file list readable: diffing a
// merge against both sides lists every path either branch touched, most of
// which the merge itself did not decide.

package gitrepo

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/diff"
	"github.com/go-git/go-git/v5/utils/merkletrie"

	dmp "github.com/sergi/go-diff/diffmatchpatch"
)

const (
	// DiffPatchMaxBlobBytes is the per-side ceiling on a blob a patch is
	// generated for. Above it the file is listed with its sizes and status
	// but no patch.
	//
	// The cost being bounded is not the disk read: producing a patch holds
	// both sides in memory as strings, runs a Myers diff whose cost grows
	// with the number of differing lines, and then builds the rendered
	// result on top of that. This server's repositories routinely hold
	// checkpoints and Parquet shards in the hundreds of megabytes, and one
	// commit can touch many of them at once, so an unbounded budget is a
	// straightforward way to spend a request's worth of memory on something
	// nobody can read. 1 MiB is far above any hand-written source file --
	// the largest text a commit page is plausibly asked to render -- and
	// anything larger is served by the file view instead.
	DiffPatchMaxBlobBytes = 1 << 20

	// DiffPatchMaxBytes is the ceiling on one rendered patch. A diff longer
	// than this is cut at a line boundary and flagged as truncated: it is
	// past what anyone reads in a browser, and the whole response is a single
	// JSON document that has to be held in memory on both ends.
	DiffPatchMaxBytes = 256 << 10

	// CommitDiffMaxFiles is the ceiling on how many paths one commit diff
	// lists. Each listed path costs up to two blob reads and a diff, and a
	// bulk import commit can touch tens of thousands of them. The true count
	// is always reported separately, so a capped list is a short list rather
	// than a wrong one.
	CommitDiffMaxFiles = 200

	// CommitDiffPatchBudgetBytes is the ceiling on the *sum* of the rendered
	// patches in one commit diff, and the reason it exists is that the
	// per-file ceilings do not bound the response or the work behind it:
	// CommitDiffMaxFiles files each just under DiffPatchMaxBytes is a ~50 MiB
	// JSON document, and each of those patches is a Myers diff that
	// diffmatchpatch will spend up to its own one-second deadline on. Since
	// this endpoint is readable without credentials (there is no repository
	// visibility here -- docs/dev/thinkingface-design.md §11), that product
	// is reachable by anyone who can find a large text commit.
	//
	// The budget is checked *before* each patch is computed, so it bounds the
	// CPU as well as the bytes. Files past it are still listed, with their
	// status and sizes, and say why they carry no patch.
	CommitDiffPatchBudgetBytes = 1 << 20

	// diffContextLines is the number of unchanged lines kept around each
	// hunk -- git's own default, and what every diff viewer expects.
	diffContextLines = fdiff.DefaultContextLines
)

// NoPatchReason says why a FileDiff carries no unified diff. The zero value
// means it carries one.
type NoPatchReason string

const (
	NoPatchNone         NoPatchReason = ""
	NoPatchLFS          NoPatchReason = "lfs"
	NoPatchBinary       NoPatchReason = "binary"
	NoPatchTooLarge     NoPatchReason = "too_large"
	NoPatchNoTextChange NoPatchReason = "no_text_change"
	NoPatchUnsupported  NoPatchReason = "unsupported"
	// NoPatchBudgetSpent is a file the response-wide patch budget ran out
	// before. Nothing is wrong with the file; the commit simply changed more
	// text than one response renders.
	NoPatchBudgetSpent NoPatchReason = "budget_spent"
)

// FileDiff is what one commit did to one path.
type FileDiff struct {
	Path string
	Kind ChangeKind
	// Additions and Deletions count changed lines across the whole diff, not
	// just the part kept in Patch. They are only meaningful when HasPatch is
	// true; for a binary, LFS or skipped path they stay 0 because nothing was
	// counted, not because nothing changed.
	Additions int
	Deletions int
	// Binary is set when either side's content is not text.
	Binary bool
	// LFS is set when either side is a Git LFS pointer. The pointer is text,
	// but its diff shows an oid changing rather than any content, so it is
	// reported instead of rendered.
	LFS bool
	// HasPatch reports whether Patch holds a unified diff.
	HasPatch bool
	// NoPatchReason says why there is no patch, whenever HasPatch is false.
	// It is stated rather than left to be inferred from Binary/LFS: an empty
	// file and a mode-only change are neither, and inferring "then it must
	// have been too big" from that is how an empty file came to be reported
	// as too large to diff.
	NoPatchReason NoPatchReason
	// Patch is the hunks of a unified diff (`@@ ... @@` onwards), without the
	// `diff --git` / `index` / `---` / `+++` preamble: Path, Kind and the
	// sizes below already carry everything the preamble would repeat.
	Patch          string
	PatchTruncated bool
	// OldSize and Size are the sizes on each side, 0 where the path did not
	// exist there. For an LFS file this is the size of the object the pointer
	// names, not the ~130 bytes of the pointer blob: these are the only
	// measure of how much changed on a row that carries no patch, and a row
	// with no patch is exactly what LFS produces.
	OldSize int64
	Size    int64
}

// CommitDiff is one commit's change against its first parent.
type CommitDiff struct {
	Commit CommitMeta
	// Parent is nil for the root commit, whose every path reads as added.
	Parent *plumbing.Hash
	// Files holds at most CommitDiffMaxFiles entries, sorted by path.
	// NumFiles is the true total and Truncated says the two differ.
	Files     []FileDiff
	NumFiles  int
	Truncated bool
	// Additions and Deletions total the per-file counts that were computed.
	Additions int
	Deletions int
}

// CommitDiff reads what the commit at hash changed relative to its first
// parent. A commit with no parents is diffed against the empty tree, so every
// path in it comes back as added.
func (r *Repo) CommitDiff(hash plumbing.Hash) (*CommitDiff, error) {
	c, err := object.GetCommit(r.storer(), hash)
	if err != nil {
		return nil, fmt.Errorf("load commit %s: %w", hash, err)
	}
	newTree, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree of %s: %w", hash, err)
	}

	out := &CommitDiff{Commit: metaOf(c), Files: []FileDiff{}}
	oldTree := &object.Tree{}
	if c.NumParents() > 0 {
		// Deliberately an error rather than a fall-back to the empty tree: a
		// parent this repository cannot read is a truncated history, and
		// calling every path "added" would be a confident lie about what the
		// commit did.
		parent, err := c.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("load first parent of %s: %w", hash, err)
		}
		oldTree, err = parent.Tree()
		if err != nil {
			return nil, fmt.Errorf("load tree of %s: %w", parent.Hash, err)
		}
		p := parent.Hash
		out.Parent = &p
	}

	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	// Sorted before the cap is applied so a truncated list is the first N
	// paths of a stable order rather than whichever N the tree walk happened
	// to emit first.
	sortable := make([]*object.Change, 0, len(changes))
	for _, ch := range changes {
		sortable = append(sortable, ch)
	}
	sort.Slice(sortable, func(i, j int) bool {
		return changePath(sortable[i]) < changePath(sortable[j])
	})

	out.NumFiles = len(sortable)
	out.Truncated = out.NumFiles > CommitDiffMaxFiles
	if out.Truncated {
		sortable = sortable[:CommitDiffMaxFiles]
	}

	budget := CommitDiffPatchBudgetBytes
	for _, ch := range sortable {
		fd, err := r.fileDiff(ch, budget)
		if err != nil {
			return nil, err
		}
		budget -= len(fd.Patch)
		out.Files = append(out.Files, fd)
		out.Additions += fd.Additions
		out.Deletions += fd.Deletions
	}
	return out, nil
}

// changePath is the path a change is reported under: the new name for
// anything that still exists, the old one for a deletion.
func changePath(ch *object.Change) string {
	if ch.To.Name != "" {
		return ch.To.Name
	}
	return ch.From.Name
}

// fileDiff builds one path's entry, reading only as much of either side as the
// size ceilings allow. budget is what is left of the response-wide patch
// allowance; at or below zero the path is listed without a patch and without
// the diff being computed at all.
func (r *Repo) fileDiff(ch *object.Change, budget int) (FileDiff, error) {
	action, err := ch.Action()
	if err != nil {
		return FileDiff{}, fmt.Errorf("classify change: %w", err)
	}
	fd := FileDiff{Path: changePath(ch)}
	switch action {
	case merkletrie.Insert:
		fd.Kind = ChangeAdd
	case merkletrie.Delete:
		fd.Kind = ChangeDelete
	default:
		fd.Kind = ChangeModify
	}

	from, fromOK, err := r.diffSide(ch.From)
	if err != nil {
		return FileDiff{}, err
	}
	to, toOK, err := r.diffSide(ch.To)
	if err != nil {
		return FileDiff{}, err
	}
	// TargetSize, not the blob size: for an LFS pointer the blob is the ~130
	// bytes of the pointer file, so reporting it turned a 5 GB checkpoint
	// being replaced into "132 B -> 133 B". These sizes are the only measure
	// of how much changed on a row that carries no patch, which is exactly
	// the row LFS produces. The ceilings below deliberately keep using Size:
	// what they bound is how much is *read*, and only a non-LFS path gets
	// that far, where the two are equal anyway.
	fd.OldSize, fd.Size = from.TargetSize(), to.TargetSize()
	fd.LFS = from.LFS != nil || to.LFS != nil

	switch {
	case fd.LFS:
		// Nothing to render: the pointer's two lines of oid and size say less
		// than the sizes already on this struct.
		fd.NoPatchReason = NoPatchLFS
		return fd, nil
	case !fromOK && !toOK:
		// Neither side is a regular file -- a submodule or a symlink-to-tree
		// change. There is no blob to diff.
		fd.NoPatchReason = NoPatchUnsupported
		return fd, nil
	case from.Size > DiffPatchMaxBlobBytes || to.Size > DiffPatchMaxBlobBytes:
		fd.NoPatchReason = NoPatchTooLarge
		return fd, nil
	case budget <= 0:
		// Checked before the blobs are read and the diff is run, which is
		// the point: the budget bounds the work, not just the output.
		fd.NoPatchReason = NoPatchBudgetSpent
		return fd, nil
	}

	oldData, err := r.diffContent(from, fromOK)
	if err != nil {
		return FileDiff{}, err
	}
	newData, err := r.diffContent(to, toOK)
	if err != nil {
		return FileDiff{}, err
	}
	if isBinaryContent(oldData) || isBinaryContent(newData) {
		fd.Binary = true
		fd.NoPatchReason = NoPatchBinary
		return fd, nil
	}

	chunks := diffChunks(string(oldData), string(newData))
	fd.Additions, fd.Deletions = countChangedLines(chunks)
	body, err := renderUnifiedPatch(ch, fromOK, toOK, chunks)
	if err != nil {
		return FileDiff{}, err
	}
	if body == "" {
		// No hunks at all, which happens for a change that has no lines in
		// it: an added or deleted empty file, or a mode-only change whose
		// two blobs are byte-identical. Claiming a patch that is the empty
		// string would read as "here is the diff, it is empty"; saying why
		// there is none is the honest version.
		fd.NoPatchReason = NoPatchNoTextChange
		return fd, nil
	}
	// The limit is the smaller of this file's own ceiling and what is left
	// of the response budget, so the sum really is bounded: checking the
	// budget only before each file would let the last one start just inside
	// it and still render a full DiffPatchMaxBytes past it.
	fd.Patch, fd.PatchTruncated = truncatePatch(body, budget)
	fd.HasPatch = true
	return fd, nil
}

// diffSide describes one side of a change. ok=false means the side is absent
// or is not a regular file, in which case the Entry is zero.
func (r *Repo) diffSide(ce object.ChangeEntry) (Entry, bool, error) {
	if ce.Name == "" || !ce.TreeEntry.Mode.IsFile() {
		return Entry{}, false, nil
	}
	// blobEntry is the one place LFS pointers are recognised (read.go); going
	// through it is what keeps the diff's idea of "this is an LFS file" the
	// same as the file browser's.
	e, err := r.blobEntry(ce.TreeEntry, ce.Name)
	if err != nil {
		return Entry{}, false, fmt.Errorf("read %s: %w", ce.Name, err)
	}
	return e, true, nil
}

func (r *Repo) diffContent(e Entry, ok bool) ([]byte, error) {
	if !ok {
		return nil, nil
	}
	data, err := r.ReadBlob(e.Hash, DiffPatchMaxBlobBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", e.Path, err)
	}
	return data, nil
}

// isBinaryContent decides whether bytes are shown as text. A NUL byte is the
// classic marker git itself uses; invalid UTF-8 is the second half of the test
// because the patch travels to the client inside a JSON string, and bytes that
// are not valid UTF-8 would be replaced with U+FFFD on the way -- a "diff"
// whose every non-ASCII line is question marks is worse than an honest
// "binary".
func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

// diffChunks runs the line-oriented diff and converts it into the chunk
// interface the unified encoder consumes.
func diffChunks(oldText, newText string) []fdiff.Chunk {
	diffs := diff.Do(oldText, newText)
	chunks := make([]fdiff.Chunk, 0, len(diffs))
	for _, d := range diffs {
		var op fdiff.Operation
		switch d.Type {
		case dmp.DiffInsert:
			op = fdiff.Add
		case dmp.DiffDelete:
			op = fdiff.Delete
		default:
			op = fdiff.Equal
		}
		chunks = append(chunks, patchChunk{content: d.Text, op: op})
	}
	return chunks
}

func countChangedLines(chunks []fdiff.Chunk) (additions, deletions int) {
	for _, c := range chunks {
		switch c.Type() {
		case fdiff.Add:
			additions += countLines(c.Content())
		case fdiff.Delete:
			deletions += countLines(c.Content())
		}
	}
	return additions, deletions
}

// countLines counts the lines in a chunk, including a final one that has no
// trailing newline -- git counts that as a changed line too.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// renderUnifiedPatch encodes the chunks as a unified diff and returns the
// hunks only. The `diff --git` / `index` / `---` / `+++` preamble is dropped:
// every fact in it is already a field of FileDiff, and a client rendering the
// hunks would have to skip it anyway.
func renderUnifiedPatch(ch *object.Change, fromOK, toOK bool, chunks []fdiff.Chunk) (string, error) {
	fp := &filePatch{chunks: chunks}
	if fromOK {
		fp.from = &patchFile{path: ch.From.Name, hash: ch.From.TreeEntry.Hash, mode: ch.From.TreeEntry.Mode}
	}
	if toOK {
		fp.to = &patchFile{path: ch.To.Name, hash: ch.To.TreeEntry.Hash, mode: ch.To.TreeEntry.Mode}
	}
	var buf bytes.Buffer
	if err := fdiff.NewUnifiedEncoder(&buf, diffContextLines).Encode(singlePatch{fp}); err != nil {
		return "", fmt.Errorf("encode patch for %s: %w", changePath(ch), err)
	}
	body := buf.String()
	// Matched at the start of a line, not anywhere: the preamble carries the
	// file's path, and a path may contain "@@ ".
	if strings.HasPrefix(body, "@@ ") {
		return body, nil
	}
	if i := strings.Index(body, "\n@@ "); i >= 0 {
		return body[i+1:], nil
	}
	// No hunk header means no hunks: an identical pair of blobs.
	return "", nil
}

// truncatePatch cuts an over-long patch at a line boundary, so a client never
// has to render half a line.
func truncatePatch(body string, limit int) (string, bool) {
	if limit > DiffPatchMaxBytes {
		limit = DiffPatchMaxBytes
	}
	if len(body) <= limit {
		return body, false
	}
	if limit < 0 {
		limit = 0
	}
	cut := body[:limit]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i+1]
	}
	return cut, true
}

// ------------------------------------- the diff-format interfaces, minimally

// singlePatch adapts one file's patch to fdiff.Patch, which is what the
// unified encoder takes. Each file is encoded on its own so one oversized or
// binary path cannot cost anything for the rest of the commit.
type singlePatch struct{ fp fdiff.FilePatch }

func (p singlePatch) FilePatches() []fdiff.FilePatch { return []fdiff.FilePatch{p.fp} }
func (p singlePatch) Message() string                { return "" }

type filePatch struct {
	from, to fdiff.File
	chunks   []fdiff.Chunk
}

// IsBinary is always false here: a binary path never reaches the encoder,
// because fileDiff answers it with Binary and no patch instead.
func (p *filePatch) IsBinary() bool               { return false }
func (p *filePatch) Files() (from, to fdiff.File) { return p.from, p.to }
func (p *filePatch) Chunks() []fdiff.Chunk        { return p.chunks }

type patchFile struct {
	path string
	hash plumbing.Hash
	mode filemode.FileMode
}

func (p *patchFile) Hash() plumbing.Hash     { return p.hash }
func (p *patchFile) Mode() filemode.FileMode { return p.mode }
func (p *patchFile) Path() string            { return p.path }

type patchChunk struct {
	content string
	op      fdiff.Operation
}

func (c patchChunk) Content() string       { return c.content }
func (c patchChunk) Type() fdiff.Operation { return c.op }
