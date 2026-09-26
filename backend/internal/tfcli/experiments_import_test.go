package tfcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseImportCSV(t *testing.T) {
	d := newImportData()
	in := "\uFEFFrun,Step,timestamp,loss,acc,,_step\n" +
		"a,2,1700000000.5,0.5,,x,9\n" +
		"a,1,2026-09-27T00:00:00Z,0.7,nan,x,9\n" +
		"b,1.0,,abc,0.9,x,9\n" +
		"a,1,,0.6,inf,x,9\n"
	if err := parseImportCSV(strings.NewReader(in), "in.csv", d); err != nil {
		t.Fatal(err)
	}
	if len(d.runs) != 2 || d.runs[0].name != "a" || d.runs[1].name != "b" {
		t.Fatalf("runs = %+v", d.runs)
	}
	a := d.runs[0]
	if len(a.points) != 3 {
		t.Fatalf("a points = %+v", a.points)
	}
	if a.points[0].Timestamp != "2023-11-14T22:13:20.5Z" || a.points[0].Metrics["loss"] != 0.5 {
		t.Errorf("first row = %+v", a.points[0])
	}
	if _, ok := a.points[0].Metrics["acc"]; ok {
		t.Error("an empty cell must be skipped")
	}
	// Cells skipped: acc "" (a/2), acc nan (a/1), loss abc (b), acc inf (a/1 dup).
	if d.skippedCells != 4 {
		t.Errorf("skipped = %d, want 4", d.skippedCells)
	}
	if _, ok := d.ignored[""]; !ok {
		t.Error("the unnamed column should be ignored")
	}
	if _, ok := d.ignored["_step"]; !ok {
		t.Error("a structural column should be ignored")
	}
	if b := d.runs[1]; b.points[0].Step != 1 || b.points[0].Metrics["acc"] != 0.9 {
		t.Errorf("b = %+v", b.points)
	}
}

func TestParseImportCSVErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no step column": "run,loss\na,1\n",
		"bad step":       "run,step,loss\na,x,1\n",
		"fractional":     "run,step,loss\na,1.5,1\n",
		"empty run":      "run,step,loss\n,1,1\n",
		"bad timestamp":  "run,step,timestamp,loss\na,1,yesterday,1\n",
		"too many cells": "run,step,loss\na,1,1,2\n",
		"duplicate run":  "run,step,Run\na,1,b\n",
	} {
		if err := parseImportCSV(strings.NewReader(in), "x.csv", newImportData()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseImportJSONL(t *testing.T) {
	d := newImportData()
	in := `{"run":"a","step":1,"timestamp":1700000000,"loss":0.5,"acc":"0.25","flag":true,"note":null,"nested":{"x":1}}

{"run":"a","step":"0","loss":1e400}
{"run":"b","step":3,"loss":0.1}
`
	if err := parseImportJSONL(strings.NewReader(in), "in.jsonl", d); err != nil {
		t.Fatal(err)
	}
	if len(d.runs) != 2 {
		t.Fatalf("runs = %+v", d.runs)
	}
	a := d.runs[0].points
	if len(a) != 1 {
		// step 0 carried only an out-of-range number -> no metric -> dropped
		t.Fatalf("a points = %+v", a)
	}
	if a[0].Metrics["loss"] != 0.5 || a[0].Metrics["acc"] != 0.25 || a[0].Timestamp != "2023-11-14T22:13:20Z" {
		t.Errorf("a[0] = %+v", a[0])
	}
	// flag (bool), note (null), nested (object), loss 1e400 (Inf).
	if d.skippedCells != 4 {
		t.Errorf("skipped = %d, want 4", d.skippedCells)
	}
	for _, bad := range []string{`{"step":1,"loss":1}`, `{"run":"a","loss":1}`, `[1,2]`, `{"run":["x"],"step":1}`} {
		if err := parseImportJSONL(strings.NewReader(bad+"\n"), "x.jsonl", newImportData()); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestImportRejectsTooManyMetricsBeforeSending(t *testing.T) {
	isolateEnv(t)
	var hdr, row strings.Builder
	hdr.WriteString("run,step")
	row.WriteString("a,1")
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&hdr, ",m%d", i)
		row.WriteString(",1")
	}
	p := writeTempFile(t, "wide.csv", hdr.String()+"\n"+row.String()+"\n")
	// No server at all: the limit must be caught before any request.
	code, _, errOut := runMain(t, []string{"experiments", "import", "alice/exp", "p", p, "--endpoint", "http://127.0.0.1:1", "--token", "t"}, "")
	if code != exitError || !strings.Contains(errOut, "at most 1000") {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
}

func TestImportBatchesAndFinishes(t *testing.T) {
	isolateEnv(t)
	f := newFakeIngest(t)
	var b strings.Builder
	b.WriteString("run,step,loss\n")
	for i := 25000 - 1; i >= 0; i-- { // reverse order: the import sorts by step
		fmt.Fprintf(&b, "big,%d,%d\n", i, i)
	}
	b.WriteString("small,0,1\n")
	data := writeTempFile(t, "runs.csv", b.String())
	configs := writeTempFile(t, "configs.jsonl",
		`{"run":"big","config":{"lr":0.01},"status":"failed","group":"sweep","job_type":"train"}`+"\n")

	var out, errOut bytes.Buffer
	code := Main([]string{"experiments", "import", "alice/exp", "my/project", data,
		"--configs", configs, "--json", "--endpoint", f.srv.URL, "--token", "t"}, nil, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, errOut.String())
	}
	if len(f.created) != 1 || f.created[0] != "alice/exp" {
		t.Errorf("created = %v", f.created)
	}
	if got := f.logSizes(); !equalInts(got, []int{10000, 10000, 5000, 1}) {
		t.Fatalf("log calls = %v", got)
	}
	first := f.logs[0]
	if first.Project != "my/project" || first.Req.Run != "big" || first.Req.Config["lr"] != 0.01 || first.Req.Group != "sweep" {
		t.Errorf("first call = %+v", first)
	}
	if first.Req.Points[0].Step != 0 || f.logs[2].Req.Points[4999].Step != 24999 {
		t.Error("points must be sent sorted by step")
	}
	if f.logs[1].Req.Config != nil {
		t.Error("only the first batch carries the config")
	}
	if len(f.finishes) != 2 || f.finishes[0].Req.Status != "failed" || f.finishes[1].Req.Status != "finished" {
		t.Errorf("finishes = %+v", f.finishes)
	}
	var res importResultJSON
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out.String())
	}
	if len(res.Runs) != 2 || res.Runs[0].Points != 25000 || res.Runs[0].Status != "failed" || res.Runs[0].Replaced {
		t.Errorf("result = %+v", res)
	}
}

func TestImportRefusesExistingRun(t *testing.T) {
	isolateEnv(t)
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "b", nil)
	data := writeTempFile(t, "runs.jsonl", `{"run":"a","step":0,"loss":1}`+"\n"+`{"run":"b","step":0,"loss":1}`+"\n")

	code, _, errOut := runMain(t, []string{"experiments", "import", "alice/exp", "p", data, "--endpoint", f.srv.URL, "--token", "t"}, "")
	if code != exitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, `"b"`) && !strings.Contains(errOut, ": b") {
		t.Errorf("stderr should name the existing run: %s", errOut)
	}
	if len(f.logs)+len(f.finishes)+len(f.deletes) != 0 {
		t.Error("nothing may be sent while a run would be refused")
	}
}

func TestImportReplace(t *testing.T) {
	isolateEnv(t)
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "b", nil)
	data := writeTempFile(t, "runs.ndjson", `{"run":"b","step":0,"loss":1}`+"\n")

	var out, errOut bytes.Buffer
	code := Main([]string{"experiments", "import", "alice/exp", "p", data, "--replace", "--json", "--endpoint", f.srv.URL, "--token", "t"}, nil, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, errOut.String())
	}
	if len(f.deletes) != 1 || f.deletes[0] != "p/b" {
		t.Errorf("deletes = %v", f.deletes)
	}
	if len(f.logs) != 1 || len(f.finishes) != 1 {
		t.Errorf("logs=%d finishes=%d", len(f.logs), len(f.finishes))
	}
	var res importResultJSON
	_ = json.Unmarshal(out.Bytes(), &res)
	if len(res.Runs) != 1 || !res.Runs[0].Replaced {
		t.Errorf("result = %+v", res)
	}
}

func TestImportDryRunSendsNothing(t *testing.T) {
	isolateEnv(t)
	f := newFakeIngest(t)
	data := writeTempFile(t, "runs.csv", "run,step,loss\na,0,1\n")
	code, out, errOut := runMain(t, []string{"experiments", "import", "alice/exp", "p", data, "--dry-run", "--endpoint", f.srv.URL, "--token", "t"}, "")
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, errOut)
	}
	if len(f.created)+len(f.logs)+len(f.finishes) != 0 {
		t.Error("--dry-run must not write anything")
	}
	if !strings.Contains(out, "would import 1 run") {
		t.Errorf("stdout = %q", out)
	}
}

func TestImportUsageErrors(t *testing.T) {
	isolateEnv(t)
	for _, args := range [][]string{
		{"experiments", "import", "alice/exp", "p"},
		{"experiments", "import", "alice", "p", "x.csv"},
		{"experiments", "import", "alice/exp", "p", "x.csv", "--status", "running"},
	} {
		if code, _, _ := runMain(t, args, ""); code != exitUsage {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
	if code, out, _ := runMain(t, []string{"experiments", "import", "--help"}, ""); code != exitOK || !strings.Contains(out, "tf experiments import") {
		t.Errorf("--help: %d %q", code, out)
	}
}
