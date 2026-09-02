package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ascii is untouched", "README.md", "README.md"},
		{"valid utf-8 is untouched", "モデル/café.txt", "モデル/café.txt"},
		{"latin-1 bytes become the replacement character", "caf\xe9.txt", "caf�.txt"},
		{"a lone continuation byte is replaced", "\x80", "�"},
		{"nul is dropped rather than replaced", "a\x00b", "ab"},
		{"both at once", "caf\xe9\x00.txt", "caf�.txt"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeText(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("sanitizeText(%q) = %q, which is not valid UTF-8", tt.in, got)
			}
		})
	}
}

func TestSanitizeJSONValueWalksTheWholeDocument(t *testing.T) {
	in := map[string]any{
		"license":  "caf\xe9",
		"nul":      "a\x00b",
		"tags":     []any{"ok", "b\xffd"},
		"nested":   map[string]any{"k\xe9y": "v\xe9"},
		"number":   3,
		"boolean":  true,
		"nilvalue": nil,
	}
	want := map[string]any{
		"license":  "caf�",
		"nul":      "ab",
		"tags":     []any{"ok", "b�d"},
		"nested":   map[string]any{"k�y": "v�"},
		"number":   3,
		"boolean":  true,
		"nilvalue": nil,
	}
	got := sanitizeJSONMap(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeJSONMap = %#v, want %#v", got, want)
	}
	// The caller's map must survive intact: the syncer parses one card and
	// hands it to two writers.
	if in["license"] != "caf\xe9" {
		t.Errorf("the input map was mutated: %#v", in)
	}
}

// A push from a machine whose file names are not UTF-8 used to fail the sync
// job on PostgreSQL (SQLSTATE 22021 out of COPY) and succeed on SQLite. Five
// retries later the job parked, and that repository's file index, search entry
// and blobs/ publication froze at the previous push -- permanently, for one
// `café.txt` created on a Latin-1 workstation.
func TestIntegrationIndexWritesSurviveNonUTF8AndNUL(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "model", "model", nil)

		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", []RepoFile{
			{Path: "caf\xe9.txt", Size: 3, BlobSHA: "aa"},
			{Path: "plain.txt", Size: 4, BlobSHA: "bb"},
		}); err != nil {
			t.Fatalf("ReplaceRepoFiles with a non-UTF-8 path: %v", err)
		}
		files, err := s.ListRepoFiles(ctx, repo.ID, "main")
		if err != nil {
			t.Fatalf("ListRepoFiles: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("files = %#v, want both indexed", files)
		}
		for _, file := range files {
			if !utf8.ValidString(file.Path) {
				t.Errorf("stored path %q is not valid UTF-8", file.Path)
			}
		}

		card := map[string]any{
			"license": "mit\x00",
			"tags":    []any{"nlp", "b\xe9ta"},
		}
		if err := s.UpdateRepoIndex(ctx, repo.ID, "abc", 7, card, "descri\xe9tion", false); err != nil {
			t.Fatalf("UpdateRepoIndex with a NUL and a non-UTF-8 byte: %v", err)
		}
		got, err := s.GetRepoByID(ctx, repo.ID)
		if err != nil {
			t.Fatalf("GetRepoByID: %v", err)
		}
		if got.Card["license"] != "mit" {
			t.Errorf("card license = %#v, want the NUL dropped", got.Card["license"])
		}
		if !utf8.ValidString(got.Description) {
			t.Errorf("description %q is not valid UTF-8", got.Description)
		}

		if err := s.ReplaceRepoLineage(ctx, repo.ID, []LineageEdge{{
			Kind: LineageKindBaseModel, Raw: "bob/b\xe9se", Namespace: "bob", Name: "b\xe9se",
		}}); err != nil {
			t.Fatalf("ReplaceRepoLineage with a non-UTF-8 target: %v", err)
		}
		edges, err := s.ListRepoLineage(ctx, repo.ID)
		if err != nil {
			t.Fatalf("ListRepoLineage: %v", err)
		}
		if len(edges) != 1 {
			t.Fatalf("edges = %#v, want the edge stored", edges)
		}
		if !utf8.ValidString(edges[0].Raw) {
			t.Errorf("stored lineage raw %q is not valid UTF-8", edges[0].Raw)
		}
	})
}

// The parquet index was the one write on that path that still let a raw git
// path through. On PostgreSQL the INSERT itself failed, which parked the sync
// job and froze the whole repository's index -- the same class of fault the
// rest of this file exists to prevent. On SQLite it succeeded and broke
// ListParquetFiles' `f.path = p.path` join instead, so the dataset viewer
// showed the file as zero bytes.
func TestIntegrationParquetIndexSurvivesNonUTF8Paths(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "dataset", "dataset", nil)

		const rawPath = "caf\xe9/train.parquet"
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", []RepoFile{
			{Path: rawPath, Size: 4096, BlobSHA: "aa"},
		}); err != nil {
			t.Fatalf("ReplaceRepoFiles: %v", err)
		}
		if err := s.UpsertParquetFile(ctx, repo.ID, "main", rawPath, 10, 1,
			json.RawMessage(`[{"name":"x"}]`)); err != nil {
			t.Fatalf("UpsertParquetFile with a non-UTF-8 path: %v", err)
		}

		files, err := s.ListParquetFiles(ctx, repo.ID, "main")
		if err != nil {
			t.Fatalf("ListParquetFiles: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("parquet files = %#v, want the file indexed", files)
		}
		if !utf8.ValidString(files[0].Path) {
			t.Errorf("stored path %q is not valid UTF-8", files[0].Path)
		}
		// The join against repo_files is what carries the real size, and it
		// only lands because both sides were folded the same way.
		if files[0].Size != 4096 {
			t.Errorf("size = %d, want 4096 -- the repo_files join missed", files[0].Size)
		}
		// And the same fold is what lets the syncer recognise its own row
		// again, rather than deleting it as stale on the very next push.
		if files[0].Path != SanitizeIndexPath(rawPath) {
			t.Errorf("stored path = %q, want %q", files[0].Path, SanitizeIndexPath(rawPath))
		}
	})
}
