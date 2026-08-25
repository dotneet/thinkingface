package api

import (
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func TestRunArtifactDir(t *testing.T) {
	if got := RunArtifactDir("sentiment", "run-42"); got != "sentiment/artifacts/run-42" {
		t.Fatalf("RunArtifactDir = %q", got)
	}
	// A run name may contain slashes (ingest only forbids control characters),
	// and the nesting is kept rather than flattened.
	if got := RunArtifactDir("proj", "sweep/lr-0.1"); got != "proj/artifacts/sweep/lr-0.1" {
		t.Fatalf("RunArtifactDir with a nested run = %q", got)
	}
}

func TestSafeRunArtifactDir(t *testing.T) {
	cases := []struct {
		name    string
		project string
		run     string
		want    string
		safe    bool
	}{
		{"plain", "proj", "run-1", "proj/artifacts/run-1", true},
		{"nested run", "proj", "a/b", "proj/artifacts/a/b", true},
		{"run climbs out", "proj", "..", "", false},
		{"run climbs further out", "proj", "../../etc", "", false},
		{"empty run", "proj", "", "", false},
		{"project climbs out", "..", "run-1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, safe := safeRunArtifactDir(tc.project, tc.run)
			if safe != tc.safe {
				t.Fatalf("safeRunArtifactDir(%q, %q) safe = %v, want %v (dir %q)",
					tc.project, tc.run, safe, tc.safe, got)
			}
			if safe && got != tc.want {
				t.Fatalf("dir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeRunModels(t *testing.T) {
	in := []apitypes.ExpRunModelInput{
		{RepoID: "  alice/bert-ja  ", Revision: " abc123 "},
		{RepoID: "alice/bert-ja"},
		// An exact repeat of the first entry collapses; the same repository at
		// a different revision does not.
		{RepoID: "alice/bert-ja", Revision: "abc123"},
	}
	got, err := normalizeRunModels(in)
	if err != nil {
		t.Fatalf("normalizeRunModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Raw != "alice/bert-ja@abc123" || got[0].Namespace != "alice" ||
		got[0].Name != "bert-ja" || got[0].Revision != "abc123" {
		t.Fatalf("first entry = %+v", got[0])
	}
	if got[1].Raw != "alice/bert-ja" || got[1].Revision != "" {
		t.Fatalf("second entry = %+v", got[1])
	}
}

func TestNormalizeRunModelsRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []apitypes.ExpRunModelInput
	}{
		{"no namespace", []apitypes.ExpRunModelInput{{RepoID: "bert-ja"}}},
		{"empty", []apitypes.ExpRunModelInput{{RepoID: ""}}},
		{"empty name", []apitypes.ExpRunModelInput{{RepoID: "alice/"}}},
		{"path traversal", []apitypes.ExpRunModelInput{{RepoID: "alice/../etc"}}},
		{"three segments", []apitypes.ExpRunModelInput{{RepoID: "a/b/c"}}},
		{"control char in revision", []apitypes.ExpRunModelInput{{RepoID: "a/b", Revision: "x\x00y"}}},
		{"oversized revision", []apitypes.ExpRunModelInput{
			{RepoID: "a/b", Revision: strings.Repeat("x", maxRevisionBytes+1)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeRunModels(tc.in); err == nil {
				t.Fatalf("normalizeRunModels(%+v) accepted the input", tc.in)
			}
		})
	}

	tooMany := make([]apitypes.ExpRunModelInput, maxRunModels+1)
	for i := range tooMany {
		tooMany[i] = apitypes.ExpRunModelInput{RepoID: "alice/m", Revision: string(rune('a' + i%26))}
	}
	if _, err := normalizeRunModels(tooMany); err == nil {
		t.Fatal("normalizeRunModels accepted more than the cap")
	}
}

func TestNormalizeRunModelsEmptyListIsValid(t *testing.T) {
	// "clear the list" has to be expressible: it is how a re-run that no
	// longer pushes a model stops claiming it.
	got, err := normalizeRunModels([]apitypes.ExpRunModelInput{})
	if err != nil || len(got) != 0 {
		t.Fatalf("normalizeRunModels(empty) = %+v, %v", got, err)
	}
}

// ----------------------------------------------------------- stale detection

func TestDeriveRunStatus(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		stored    string
		updatedAt time.Time
		want      apitypes.RunStatus
	}{
		{"fresh running", "running", now.Add(-time.Minute), apitypes.RunStatusRunning},
		{"running updated just now", "running", now, apitypes.RunStatusRunning},
		// The boundary belongs to the optimistic side: exactly the window old
		// is still running, one instant past it is stale.
		{"running exactly at the window", "running", now.Add(-runStaleAfter), apitypes.RunStatusRunning},
		{"running one nanosecond past", "running", now.Add(-runStaleAfter - time.Nanosecond), apitypes.RunStatusStale},
		{"running long past", "running", now.Add(-72 * time.Hour), apitypes.RunStatusStale},
		// Terminal states are untouched no matter how old they are: the age of
		// a finished run says nothing about it.
		{"old finished", "finished", now.Add(-72 * time.Hour), apitypes.RunStatusFinished},
		{"old failed", "failed", now.Add(-72 * time.Hour), apitypes.RunStatusFailed},
		// An unknown status is passed through rather than reinterpreted.
		{"unknown status", "queued", now.Add(-72 * time.Hour), apitypes.RunStatus("queued")},
		// Nothing to judge against, so the row keeps what it claims.
		{"running with no timestamp", "running", time.Time{}, apitypes.RunStatusRunning},
		// A clock skew that puts the update in the future must not read as
		// stale (now.Sub is negative there).
		{"running in the future", "running", now.Add(time.Hour), apitypes.RunStatusRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveRunStatus(tc.stored, tc.updatedAt, now); got != tc.want {
				t.Fatalf("deriveRunStatus(%q, %v) = %q, want %q", tc.stored, tc.updatedAt, got, tc.want)
			}
		})
	}
}

// The derived status must never be storable: ingest accepts only the three
// lifecycle states, and "stale" is an answer the server computes, not a claim
// a client may make.
func TestStaleIsNotAnIngestStatus(t *testing.T) {
	if validRunStatus(string(apitypes.RunStatusStale)) {
		t.Fatal("validRunStatus accepted the derived stale status")
	}
}

// toExpRuns is the single funnel every ExpRun-returning endpoint goes through,
// so the derivation is pinned here rather than once per handler.
func TestToExpRunsDerivesStale(t *testing.T) {
	old := time.Now().Add(-2 * runStaleAfter)
	fresh := time.Now().Add(-time.Second)
	rows := []store.ExpRun{
		{Name: "dead", Status: "running", UpdatedAt: old},
		{Name: "alive", Status: "running", UpdatedAt: fresh},
		{Name: "done", Status: "finished", UpdatedAt: old},
		{Name: "broke", Status: "failed", UpdatedAt: old},
	}
	got := toExpRuns(rows, nil)
	want := map[string]apitypes.RunStatus{
		"dead":  apitypes.RunStatusStale,
		"alive": apitypes.RunStatusRunning,
		"done":  apitypes.RunStatusFinished,
		"broke": apitypes.RunStatusFailed,
	}
	if len(got) != len(want) {
		t.Fatalf("toExpRuns returned %d runs, want %d", len(got), len(want))
	}
	for _, run := range got {
		if run.Status != want[run.Name] {
			t.Errorf("run %q status = %q, want %q", run.Name, run.Status, want[run.Name])
		}
		// The timestamp itself is reported unchanged: the UI needs it to say
		// how long ago the run was last heard from.
		if run.UpdatedAt.IsZero() {
			t.Errorf("run %q lost its updated_at", run.Name)
		}
	}
}
