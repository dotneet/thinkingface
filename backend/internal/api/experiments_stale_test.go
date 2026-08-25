// HTTP-level tests for the derived "stale" run status.
//
// The unit tests in experiments_pure_test.go pin the rule; these pin that
// every route which hands out an apitypes.ExpRun applies it, because the whole
// point of deriving instead of storing is that no route may be left behind.

package api

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// backdateRun rewinds one run's updated_at, the only way to reach the stale
// window without sleeping through it. Opened as a second raw connection to the
// same SQLite file rather than reaching into store's unexported db field, the
// same way addOrgMember (archive_test.go) does it.
func (f *expFixture) backdateRun(run string, age time.Duration) {
	f.t.Helper()
	dsn := "file:" + f.dbPath + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		f.t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	// The column is stored as the text the sqlite dialect writes; format it the
	// same way so the driver reads it back as a UTC time.
	stamp := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05.000")
	res, err := db.Exec(`UPDATE exp_runs SET updated_at = ? WHERE name = ?`, stamp, run)
	if err != nil {
		f.t.Fatalf("backdate run: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		f.t.Fatalf("backdate run %q touched %d rows, want 1", run, n)
	}
}

// runFromAnnotation reads a run back through the PATCH route, which returns
// the row it just wrote. The route rejects a body that asks for nothing, so it
// re-asserts the archived flag these runs already carry -- a no-op write, and
// one that leaves updated_at alone (the annotation statement does not touch
// it), which is what makes it usable as a read here.
func (f *expFixture) runFromAnnotation(t *testing.T, token, run string) apitypes.ExpRun {
	t.Helper()
	resp := f.do("PATCH", "/api/v1/experiments/alice/exp/proj/runs/"+run, token,
		map[string]any{"archived": false})
	if resp.status() != 200 {
		t.Fatalf("annotate status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.ExpRunAnnotationResponse
	resp.json(t, &body)
	return body.Run
}

func TestStaleRunIsDerivedOnEveryRoute(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	// Three runs that all last checked in long ago; only the one still
	// claiming to run may change.
	for _, name := range []string{"dead", "done", "broke"} {
		f.logBatch(tok, map[string]any{
			"run": name, "status": "running",
			"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
		})
	}
	f.logBatch(tok, map[string]any{"run": "done", "status": "finished", "points": []map[string]any{}})
	f.logBatch(tok, map[string]any{"run": "broke", "status": "failed", "points": []map[string]any{}})
	// ...and one that logged a moment ago and must keep reading as running.
	f.logBatch(tok, map[string]any{
		"run": "alive", "status": "running",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})

	for _, name := range []string{"dead", "done", "broke"} {
		f.backdateRun(name, runStaleAfter+time.Hour)
	}

	want := map[string]apitypes.RunStatus{
		"dead":  apitypes.RunStatusStale,
		"alive": apitypes.RunStatusRunning,
		"done":  apitypes.RunStatusFinished,
		"broke": apitypes.RunStatusFailed,
	}
	for name, status := range want {
		if got := f.runNamed(t, tok, name).Status; got != status {
			t.Errorf("listing: run %q status = %q, want %q", name, got, status)
		}
		if got := f.runFromAnnotation(t, tok, name).Status; got != status {
			t.Errorf("annotation response: run %q status = %q, want %q", name, got, status)
		}
	}
}

// A stale run that starts logging again is live once more: nothing was written
// when it went stale, so nothing has to be undone.
func TestStaleRunRecoversOnNextLog(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{
		"run": "slow", "status": "running",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})
	f.backdateRun("slow", runStaleAfter+time.Hour)
	if got := f.runNamed(t, tok, "slow").Status; got != apitypes.RunStatusStale {
		t.Fatalf("status = %q, want stale after backdating", got)
	}

	f.logBatch(tok, map[string]any{
		"run": "slow", "status": "running",
		"points": []map[string]any{point(2, map[string]any{"loss": 0.4})},
	})
	if got := f.runNamed(t, tok, "slow").Status; got != apitypes.RunStatusRunning {
		t.Fatalf("status = %q, want running once the run logged again", got)
	}
}

// A run just inside the window is not stale. Together with the nanosecond case
// in TestDeriveRunStatus this pins the boundary end to end.
func TestRunJustInsideTheWindowIsNotStale(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{
		"run": "slow", "status": "running",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})
	f.backdateRun("slow", runStaleAfter-time.Minute)
	if got := f.runNamed(t, tok, "slow").Status; got != apitypes.RunStatusRunning {
		t.Fatalf("status = %q, want running just inside the window", got)
	}
}
