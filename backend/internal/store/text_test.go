package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestSanitizeJSONRaw(t *testing.T) {
	t.Run("a clean document is returned as the same slice", func(t *testing.T) {
		in := []byte(`{"name":"café","n":1.50}`)
		got := sanitizeJSONRaw(in)
		if &got[0] != &in[0] {
			t.Fatalf("sanitizeJSONRaw copied a document it had no reason to touch")
		}
	})
	t.Run("nil stays nil", func(t *testing.T) {
		if got := sanitizeJSONRaw(nil); got != nil {
			t.Fatalf("sanitizeJSONRaw(nil) = %q, want nil", got)
		}
	})
	t.Run("nul escapes go, in keys and values alike", func(t *testing.T) {
		got := sanitizeJSONRaw([]byte(`[{"name":"a\u0000b","k\u0000":"v"}]`))
		var v []map[string]string
		if err := json.Unmarshal(got, &v); err != nil {
			t.Fatalf("result %q does not parse: %v", got, err)
		}
		if len(v) != 1 || v[0]["name"] != "ab" || v[0]["k"] != "v" {
			t.Fatalf("sanitizeJSONRaw = %s, want the NULs dropped", got)
		}
	})
	t.Run("invalid utf-8 becomes the replacement character", func(t *testing.T) {
		got := sanitizeJSONRaw([]byte("{\"name\":\"caf\xe9\"}"))
		if !utf8.Valid(got) || string(got) != `{"name":"caf�"}` {
			t.Fatalf("sanitizeJSONRaw = %q", got)
		}
	})
	t.Run("numbers survive the round trip as written", func(t *testing.T) {
		got := sanitizeJSONRaw([]byte(`{"a":"\u0000","n":1.50,"big":12345678901234567890}`))
		if string(got) != `{"a":"","big":12345678901234567890,"n":1.50}` {
			t.Fatalf("sanitizeJSONRaw = %s", got)
		}
	})
	t.Run("a document that does not parse is left for the database", func(t *testing.T) {
		in := []byte(`{"a":"\u0000"`)
		if got := sanitizeJSONRaw(in); string(got) != string(in) {
			t.Fatalf("sanitizeJSONRaw = %q, want the input back", got)
		}
	})
}

// sanitizeText folds a whole run of invalid bytes into one U+FFFD, so two
// paths git keeps apart can come out as the same name. The second row then hit
// the (repo_id, ref, path) primary key and failed ReplaceRepoFiles on both
// engines -- on every retry, so the sync job parked and the repository's index
// froze, which is the very outcome the fold was introduced to prevent.
func TestIntegrationReplaceRepoFilesSurvivesPathsThatFoldTogether(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "latin1", "dataset", nil)

		if sanitizeText("Gr\xf6\xdfe.csv") != sanitizeText("Gr\xfc\xdfe.csv") {
			t.Fatal("premise: the two paths are expected to fold to one name")
		}
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", []RepoFile{
			{Path: "Gr\xf6\xdfe.csv", Size: 1, BlobSHA: "aa"},
			{Path: "Gr\xfc\xdfe.csv", Size: 2, BlobSHA: "bb"},
			{Path: "plain.csv", Size: 3, BlobSHA: "cc"},
		}); err != nil {
			t.Fatalf("ReplaceRepoFiles with two paths that fold together: %v", err)
		}
		files, err := s.ListRepoFiles(ctx, repo.ID, "main")
		if err != nil {
			t.Fatalf("ListRepoFiles: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("files = %#v, want the folded name once plus plain.csv", files)
		}
		// Byte order decides, and the first one git lists is the one kept.
		for _, file := range files {
			if file.Path == sanitizeText("Gr\xf6\xdfe.csv") && file.BlobSHA != "aa" {
				t.Errorf("kept %+v, want the first of the colliding paths", file)
			}
		}
	})
}

// A parquet column named "a\u0000b" is valid JSON and a valid column name, and
// PostgreSQL's JSONB refuses it (22P05). UpsertParquetFile failed on it, which
// failed the sync on every push of the repository.
func TestIntegrationParquetSchemaSurvivesNUL(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "nulcol", "dataset", nil)

		if err := s.UpsertParquetFile(ctx, repo.ID, "main", "train.parquet", 1, 1,
			json.RawMessage(`[{"name":"a\u0000b","type":"INT64"},{"name":"c","type":"BYTE_ARRAY"}]`)); err != nil {
			t.Fatalf("UpsertParquetFile with a NUL in a column name: %v", err)
		}
		files, err := s.ListParquetFiles(ctx, repo.ID, "main")
		if err != nil || len(files) != 1 {
			t.Fatalf("ListParquetFiles = %#v, %v", files, err)
		}
		var cols []map[string]string
		if err := json.Unmarshal(files[0].Schema, &cols); err != nil {
			t.Fatalf("stored schema %s does not parse: %v", files[0].Schema, err)
		}
		if len(cols) != 2 || cols[0]["name"] != "ab" || cols[1]["name"] != "c" {
			t.Fatalf("stored schema = %s, want the NUL dropped and the column order kept", files[0].Schema)
		}
	})
}

// Experiment runs carry whatever a training script logged, and encoding/json
// writes a NUL in a key as \u0000 -- storable on SQLite, refused by JSONB.
func TestIntegrationExperimentJSONSurvivesNUL(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "exp-nul", "dataset", nil)
		pid, err := s.UpsertExpProject(ctx, repo.ID, "proj")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}
		runID, err := s.UpsertExpRun(ctx, pid, "run", "running",
			map[string]any{"l\x00r": 0.1}, map[string]any{"best": "x\x00"}, []string{"lo\x00ss"}, 0, 0, nil)
		if err != nil {
			t.Fatalf("UpsertExpRun with NULs: %v", err)
		}
		run, err := s.GetExpRun(ctx, pid, "run")
		if err != nil {
			t.Fatalf("GetExpRun: %v", err)
		}
		if _, ok := run.Config["lr"]; !ok || run.Summary["best"] != "x" ||
			len(run.MetricKeys) != 1 || run.MetricKeys[0] != "loss" {
			t.Fatalf("run = config %v summary %v keys %v, want the NULs dropped",
				run.Config, run.Summary, run.MetricKeys)
		}

		if err := s.InsertPoints(ctx, runID, []MetricPoint{{Step: 1, Metrics: map[string]float64{"lo\x00ss": 0.5}}}); err != nil {
			t.Fatalf("InsertPoints with a NUL in a metric name: %v", err)
		}
		points, err := s.ListProjectPoints(ctx, pid, 10)
		if err != nil || len(points) != 1 {
			t.Fatalf("ListProjectPoints = %#v, %v", points, err)
		}
		if points[0].Metrics["loss"] != 0.5 {
			t.Fatalf("point metrics = %v, want the NUL dropped from the name", points[0].Metrics)
		}
	})
}

// A webhook endpoint's reply is cut at a byte cap and can be Latin-1, binary,
// or end mid-character. PostgreSQL refused to store it (22021), so the finish
// never landed: the delivery stayed 'pending', was reclaimed when the lease
// lapsed, and was POSTed again every lease period, forever.
func TestIntegrationWebhookDeliverySurvivesAnUnstorableResponse(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		ns := f.ns(t, "alice")
		hook, err := s.CreateWebhook(ctx, ns.ID, nil, "https://example.com/h", "s", []string{"repo.push"}, true)
		if err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		id, err := s.CreateWebhookDelivery(ctx, hook.ID, "repo.push", []byte(`{"ref":"ma\u0000in"}`))
		if err != nil {
			t.Fatalf("CreateWebhookDelivery with a NUL in the payload: %v", err)
		}
		job, err := s.ClaimWebhookDelivery(ctx, time.Minute)
		if err != nil || job == nil || job.DeliveryID != id {
			t.Fatalf("ClaimWebhookDelivery = %+v, %v", job, err)
		}
		status := 500
		// A NUL, a Latin-1 byte and the first two bytes of a three-byte
		// character: everything a truncated reply can end up holding.
		body := "err\x00or caf\xe9 \xe3\x81"
		if err := s.FinishWebhookDelivery(ctx, id, false, job.Attempts, 1, &status, body, time.Minute); err != nil {
			t.Fatalf("FinishWebhookDelivery with an unstorable body: %v", err)
		}
		d, err := s.GetWebhookDelivery(ctx, id)
		if err != nil {
			t.Fatalf("GetWebhookDelivery: %v", err)
		}
		if d.Status != "failed" {
			t.Errorf("status = %q, want the finish to have landed (failed at the budget)", d.Status)
		}
		if !utf8.ValidString(d.ResponseBody) || strings.ContainsRune(d.ResponseBody, 0) {
			t.Errorf("stored body %q is not storable text", d.ResponseBody)
		}
		if strings.Contains(string(d.Payload), `\u0000`) {
			t.Errorf("stored payload %s still carries the NUL", d.Payload)
		}
	})
}

// A sync error quotes the git path it failed on, raw. PostgreSQL refused the
// non-UTF-8 ones (22021), so the job could not record its own failure: it
// stayed 'running' until the lease lapsed and was requeued to fail again.
func TestIntegrationFinishSyncJobStoresANonUTF8Error(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "errpath", "dataset", nil)
		if err := s.EnqueueSync(ctx, repo.ID, "main", "", "s1"); err != nil {
			t.Fatal(err)
		}
		j, err := s.ClaimSyncJob(ctx, testLease)
		if err != nil || j == nil {
			t.Fatalf("ClaimSyncJob = %+v, %v", j, err)
		}
		if err := s.FinishSyncJob(ctx, j, errors.New("read blob caf\xe9.txt:\x00 boom")); err != nil {
			t.Fatalf("FinishSyncJob with a non-UTF-8 error: %v", err)
		}
		got := readSyncJob(t, s, j.ID)
		if got.status != "pending" {
			t.Errorf("status = %q, want the failure recorded and the job back in the queue", got.status)
		}
		if got.lastError != "read blob caf�.txt: boom" {
			t.Errorf("last_error = %q", got.lastError)
		}
	})
}
