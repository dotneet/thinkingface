package syncer

import (
	"bytes"
	"testing"

	"github.com/parquet-go/parquet-go"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// testParquetRow is the minimal shape needed to get a real, readable parquet
// file out of parquet.Write for these fixtures; the column itself is never
// inspected, only the row count.
type testParquetRow struct {
	X int64 `parquet:"x"`
}

func buildTestParquet(t *testing.T, rows int) []byte {
	t.Helper()
	data := make([]testParquetRow, rows)
	for i := range data {
		data[i] = testParquetRow{X: int64(i)}
	}
	var buf bytes.Buffer
	if err := parquet.Write(&buf, data); err != nil {
		t.Fatalf("write test parquet (%d rows): %v", rows, err)
	}
	return buf.Bytes()
}

// TestIndexParquet_PathsThatFoldTogetherAgreeWithRepoFiles is the syncer half
// of the fold ReplaceRepoFiles applies (store/text.go, store/files.go): two
// raw paths that are distinct to git but collapse to the same name once
// invalid UTF-8 is folded to U+FFFD. ReplaceRepoFiles keeps only the first one
// it sees in repo_files; indexParquet used to index every one of them anyway,
// so whichever raw path's viewer.Schema call happened to run last won the
// ON CONFLICT upsert into parquet_files (UpsertParquetFile folds the very same
// way) -- independent of which one repo_files, and the tree the Web UI
// builds from it, actually kept. A parquet viewer opened at that path could
// then read a completely different file's row count and schema than the one
// it was told is there.
func TestIndexParquet_PathsThatFoldTogetherAgreeWithRepoFiles(t *testing.T) {
	f := newPushFixture(t)

	// Two Latin-1/CP1252-ish names, distinct as raw bytes, that sanitizeText
	// folds onto the same replacement-character name (mirrors
	// store.TestIntegrationReplaceRepoFilesSurvivesPathsThatFoldTogether).
	const pathA = "Gr\xf6\xdfe.parquet"
	const pathB = "Gr\xfc\xdfe.parquet"
	if store.SanitizeIndexPath(pathA) != store.SanitizeIndexPath(pathB) {
		t.Fatal("premise: the two paths are expected to fold to one sanitized name")
	}
	folded := store.SanitizeIndexPath(pathA)

	rowsByPath := map[string]int64{pathA: 1, pathB: 5}
	f.push("main",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: pathA, Data: buildTestParquet(t, int(rowsByPath[pathA]))},
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: pathB, Data: buildTestParquet(t, int(rowsByPath[pathB]))},
	)

	repoFiles, err := f.st.ListRepoFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("list repo files: %v", err)
	}
	var kept *store.RepoFile
	for i := range repoFiles {
		if repoFiles[i].Path == folded {
			kept = &repoFiles[i]
		}
	}
	if kept == nil {
		t.Fatalf("repo_files has no row for the folded path %q: %#v", folded, repoFiles)
	}

	// Work out which of the two raw paths repo_files actually kept, by
	// matching the blob sha git recorded for each, rather than assuming a
	// byte-order winner.
	wantRows := int64(-1)
	for _, p := range []string{pathA, pathB} {
		if kept.BlobSHA == f.blobSHA("main", p) {
			wantRows = rowsByPath[p]
		}
	}
	if wantRows < 0 {
		t.Fatalf("repo_files' blob sha %q matches neither raw path", kept.BlobSHA)
	}

	parquetFiles, err := f.st.ListParquetFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("list parquet files: %v", err)
	}
	var pf *store.ParquetFile
	for i := range parquetFiles {
		if parquetFiles[i].Path == folded {
			pf = &parquetFiles[i]
		}
	}
	if pf == nil {
		t.Fatalf("parquet_files has no row for the folded path %q: %#v", folded, parquetFiles)
	}
	if pf.NumRows != wantRows {
		t.Errorf("parquet_files row count = %d, want %d -- it must describe the same file "+
			"repo_files kept (blob %s), not whichever raw path indexParquet happened to visit last",
			pf.NumRows, wantRows, kept.BlobSHA)
	}
}
