package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Every name segment is path-escaped: a project or run may contain "/", a
// space, "%" or non-ASCII, and each must arrive as one segment.
func TestIngestURLsEscapeEverySegment(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if r.URL.EscapedPath() == "/api/v1/experiments/alice/corpus/a%2Fb%20c%25%C3%A9/runs" {
				_, _ = w.Write([]byte(`{"runs":[{"name":"x"}]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"ok":true,"accepted":0}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "t")
	ctx := t.Context()
	project, run := "a/b c%é", "sweep/seed 1"

	names, err := c.IngestListRunNames(ctx, testRef(), project)
	if err != nil || !names["x"] {
		t.Fatalf("list names = %v, %v", names, err)
	}
	if r, err := c.IngestGetRun(ctx, testRef(), project, run); err != nil || r != nil {
		t.Fatalf("get missing run = %v, %v; want nil, nil", r, err)
	}
	if _, err := c.IngestLog(ctx, testRef(), project, IngestLogRequest{Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := c.IngestFinish(ctx, testRef(), project, IngestFinishRequest{Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := c.IngestDeleteRun(ctx, testRef(), project, run); err != nil {
		t.Fatal(err)
	}
	if err := c.IngestSetRunModels(ctx, testRef(), project, run, nil); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/experiments/alice/corpus/a%2Fb%20c%25%C3%A9"
	want := []string{
		"GET " + base + "/runs",
		"GET " + base + "/runs/sweep%2Fseed%201",
		"POST " + base + "/log",
		"POST " + base + "/finish",
		"DELETE " + base + "/runs/sweep%2Fseed%201",
		"PATCH " + base + "/runs/sweep%2Fseed%201",
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d = %s, want %s", i, seen[i], want[i])
		}
	}
}

func TestIngestLogRefusesOversizedBatch(t *testing.T) {
	c := New("http://127.0.0.1:1", "t")
	_, err := c.IngestLog(t.Context(), testRef(), "p", IngestLogRequest{Run: "r", Points: make([]IngestPoint, IngestMaxPoints+1)})
	if err == nil {
		t.Fatal("expected an error before any request")
	}
}

// A run artifact is skipped only when its content is already on the server.
// Checkpoints of one model are routinely the same size, so a same-sized new
// version must be committed (regular and LFS paths alike), while a retry of
// an identical upload commits nothing.
func TestUploadRunArtifactsComparesContentNotSize(t *testing.T) {
	h := newFakeHub(t)
	c := h.client()
	ctx := t.Context()
	arts := func(notes, model string) []RunArtifact {
		return []RunArtifact{
			{Name: "notes.txt", File: localFile("", []byte(notes))},
			{Name: "ckpt/model.bin", File: localFile("", []byte(model))}, // *.bin routes through LFS
		}
	}

	res, err := c.UploadRunArtifacts(ctx, testRef(), "p", "r", arts("v1-notes", "weights-v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uploaded) != 2 || len(res.Skipped) != 0 || res.Commit == nil {
		t.Fatalf("first upload = %+v", res)
	}

	// Same sizes, different bytes: both must travel again.
	res, err = c.UploadRunArtifacts(ctx, testRef(), "p", "r", arts("v2-notes", "weights-v2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uploaded) != 2 || len(res.Skipped) != 0 || res.Commit == nil {
		t.Fatalf("same-size new versions = %+v, want both uploaded", res)
	}
	h.mu.Lock()
	notes := h.files[RunArtifactPath("p", "r", "notes.txt")]
	model := h.files[RunArtifactPath("p", "r", "ckpt/model.bin")]
	h.mu.Unlock()
	wantNotes, _ := GitBlobSHA1(strings.NewReader("v2-notes"), 8)
	wantModel, _, _ := SHA256Hex(strings.NewReader("weights-v2"))
	if notes.blobOID != wantNotes || model.lfsOID != wantModel {
		t.Errorf("server holds %+v / %+v, want the v2 content", notes, model)
	}

	// Identical content: nothing committed, both reported as already present.
	_, commitsBefore := h.counts()
	res, err = c.UploadRunArtifacts(ctx, testRef(), "p", "r", arts("v2-notes", "weights-v2"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uploaded) != 0 || len(res.Skipped) != 2 || res.Commit != nil {
		t.Errorf("identical retry = %+v, want both skipped", res)
	}
	if _, commits := h.counts(); commits != commitsBefore {
		t.Errorf("an identical retry committed (%d -> %d)", commitsBefore, commits)
	}
}
