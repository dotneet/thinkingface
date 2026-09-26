package tfcli

// `tf experiments import` (docs/dev/agent-features.md §4, P8): bring past runs
// recorded somewhere else -- a CSV export, a JSONL log -- into a project, one
// run per distinct `run` value, through the same /log and /finish endpoints
// the Python shim uses live.

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const experimentsImportUsage = `usage: tf experiments import REPO PROJECT FILE... [flags]

Import past runs into PROJECT of the dataset repository REPO (NS/NAME; created
if it does not exist). Each FILE is CSV (with a header row) or JSONL (one
object per line), chosen by extension (.csv / .jsonl / .ndjson) or --format.

Every row carries:
  run        the run name (required)
  step       an integer step (required)
  timestamp  optional: RFC 3339 or unix seconds
  ...        every other column / key is a metric; only numeric values are
             imported (empty, non-numeric, NaN and Inf cells are skipped and
             counted)

Rows are grouped per run and sorted by step. A run that already exists in the
project is refused (importing twice would duplicate its points) unless
--replace is given, which deletes it first. Nothing is sent while any run
would be refused.

Flags:
  --configs FILE         JSONL of {"run", "config", "status", "group",
                         "job_type"}: the config sent with the run's first
                         batch, its final status, and its sweep grouping
  --status S             final status of runs without a --configs status:
                         finished (default) or failed
  --replace              delete existing runs of the same name first
  --format csv|jsonl     parse every FILE as this format
  --dry-run              parse, validate and check for existing runs; send
                         nothing
  --json                 print the result as one JSON object on stdout
  --endpoint URL         server URL
  --token TOKEN          API token (or THINKINGFACE_API_KEY in the environment)
  --api-key KEY          alias of --token
  --verbose              print credential resolution to stderr
`

// importConfig is one line of --configs.
type importConfig struct {
	Run     string         `json:"run"`
	Config  map[string]any `json:"config"`
	Status  string         `json:"status"`
	Group   string         `json:"group"`
	JobType string         `json:"job_type"`
}

// importRun is everything collected for one run.
type importRun struct {
	name   string
	points []hub.IngestPoint
	keys   map[string]bool
	cfg    *importConfig
}

// importData is the parsed input of one import.
type importData struct {
	runs         []*importRun // in order of first appearance
	byName       map[string]*importRun
	skippedCells int
	// ignored names the columns that were dropped rather than imported, with
	// why, so each is reported once however many rows carry it.
	ignored map[string]string
}

func newImportData() *importData {
	return &importData{byName: map[string]*importRun{}, ignored: map[string]string{}}
}

func (d *importData) run(name string) *importRun {
	r, ok := d.byName[name]
	if !ok {
		r = &importRun{name: name, keys: map[string]bool{}}
		d.byName[name] = r
		d.runs = append(d.runs, r)
	}
	return r
}

// add records one row. A row left with no metric at all is dropped: it would
// only inflate the run's point count.
func (d *importData) add(run string, p hub.IngestPoint) {
	r := d.run(run)
	if len(p.Metrics) == 0 {
		return
	}
	for k := range p.Metrics {
		r.keys[k] = true
	}
	r.points = append(r.points, p)
}

// metricColumn decides what a non-reserved column / key is: a metric (ok),
// ignored (recorded in d.ignored), or an error.
func (d *importData) metricColumn(name string) (ok bool, err error) {
	if _, seen := d.ignored[name]; seen {
		return false, nil
	}
	switch {
	case strings.TrimSpace(name) == "":
		d.ignored[name] = "the column has no name"
		return false, nil
	case isStructuralMetricName(name):
		d.ignored[name] = "the name is reserved for the metrics table's own columns"
		return false, nil
	}
	if err := validateIngestNameLocal(name); err != nil {
		return false, fmt.Errorf("metric name %q %s", name, err)
	}
	return true, nil
}

// importReservedKey maps a column / key to "run", "step" or "timestamp"
// (case-insensitively, surrounding space ignored), or "".
func importReservedKey(name string) string {
	switch k := strings.ToLower(strings.TrimSpace(name)); k {
	case "run", "step", "timestamp":
		return k
	}
	return ""
}

// parseImportValue parses one metric cell. ok=false means "skip it": empty,
// not a number, NaN or ±Inf.
func parseImportValue(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// parseImportStep parses an integer step; "10.0" is accepted as 10.
func parseImportStep(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("step is empty")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, fmt.Errorf("step %q is not an integer", s)
	}
	return int64(f), nil
}

// parseImportTimestamp accepts RFC 3339 or unix seconds (fractional allowed)
// and returns RFC 3339 in UTC, or "" for an empty cell.
func parseImportTimestamp(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("timestamp %q is neither RFC 3339 nor unix seconds", s)
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC().Format(time.RFC3339Nano), nil
}

// importFormat picks the parser for path: --format when given, else the
// extension.
func importFormat(path, flagFormat string) (string, error) {
	if flagFormat != "" {
		return flagFormat, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "csv", nil
	case ".jsonl", ".ndjson":
		return "jsonl", nil
	}
	return "", fmt.Errorf("%s: cannot tell the format from the extension; use .csv / .jsonl / .ndjson or pass --format", path)
}

// parseImportCSV reads one CSV file with a header row into d.
func parseImportCSV(r io.Reader, name string, d *importData) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // a short row is filled with empty cells below
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: empty file (a header row is required)", name)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\uFEFF") // a spreadsheet's BOM
	}
	runCol, stepCol, tsCol := -1, -1, -1
	metricCols := map[int]string{}
	seen := map[string]bool{}
	for i, h := range header {
		if key := importReservedKey(h); key != "" {
			col := map[string]*int{"run": &runCol, "step": &stepCol, "timestamp": &tsCol}[key]
			if *col >= 0 {
				return fmt.Errorf("%s: more than one %q column", name, key)
			}
			*col = i
			continue
		}
		if seen[h] {
			return fmt.Errorf("%s: duplicate column %q", name, h)
		}
		seen[h] = true
		ok, err := d.metricColumn(h)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if ok {
			metricCols[i] = h
		}
	}
	if runCol < 0 || stepCol < 0 {
		return fmt.Errorf("%s: the header must have a %q and a %q column", name, "run", "step")
	}
	cell := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return rec[i]
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		line, _ := cr.FieldPos(0)
		if len(rec) > len(header) {
			return fmt.Errorf("%s:%d: %d cells, but the header has %d columns", name, line, len(rec), len(header))
		}
		run := strings.TrimSpace(cell(rec, runCol))
		if err := validateIngestNameLocal(run); err != nil {
			return fmt.Errorf("%s:%d: run name %q %s", name, line, run, err)
		}
		step, err := parseImportStep(cell(rec, stepCol))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", name, line, err)
		}
		ts, err := parseImportTimestamp(cell(rec, tsCol))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", name, line, err)
		}
		p := hub.IngestPoint{Step: step, Timestamp: ts, Metrics: map[string]float64{}}
		for i, key := range metricCols {
			if v, ok := parseImportValue(cell(rec, i)); ok {
				p.Metrics[key] = v
			} else {
				d.skippedCells++
			}
		}
		d.add(run, p)
	}
}

// jsonScalarString renders a JSON scalar (string or number) as text; ok=false
// for anything else.
func jsonScalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	}
	return "", false
}

// parseImportJSONL reads one JSONL file into d. Blank lines are ignored.
func parseImportJSONL(r io.Reader, name string, d *importData) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil || obj == nil {
			return fmt.Errorf("%s:%d: not a JSON object", name, line)
		}
		var (
			run, stepText string
			haveRun       bool
			haveStep      bool
			ts            string
		)
		metrics := map[string]float64{}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := obj[k]
			switch importReservedKey(k) {
			case "run":
				s, ok := jsonScalarString(v)
				if !ok {
					return fmt.Errorf("%s:%d: %q must be a string", name, line, k)
				}
				run, haveRun = strings.TrimSpace(s), true
				continue
			case "step":
				s, ok := jsonScalarString(v)
				if !ok {
					return fmt.Errorf("%s:%d: %q must be an integer", name, line, k)
				}
				stepText, haveStep = s, true
				continue
			case "timestamp":
				if v == nil {
					continue
				}
				s, ok := jsonScalarString(v)
				if !ok {
					return fmt.Errorf("%s:%d: %q must be RFC 3339 or unix seconds", name, line, k)
				}
				parsed, err := parseImportTimestamp(s)
				if err != nil {
					return fmt.Errorf("%s:%d: %w", name, line, err)
				}
				ts = parsed
				continue
			}
			ok, err := d.metricColumn(k)
			if err != nil {
				return fmt.Errorf("%s:%d: %w", name, line, err)
			}
			if !ok {
				continue
			}
			s, isScalar := jsonScalarString(v)
			if !isScalar {
				d.skippedCells++ // null, bool, object, array
				continue
			}
			if f, ok := parseImportValue(s); ok {
				metrics[k] = f
			} else {
				d.skippedCells++
			}
		}
		if !haveRun {
			return fmt.Errorf("%s:%d: missing %q", name, line, "run")
		}
		if err := validateIngestNameLocal(run); err != nil {
			return fmt.Errorf("%s:%d: run name %q %s", name, line, run, err)
		}
		if !haveStep {
			return fmt.Errorf("%s:%d: missing %q", name, line, "step")
		}
		step, err := parseImportStep(stepText)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", name, line, err)
		}
		d.add(run, hub.IngestPoint{Step: step, Timestamp: ts, Metrics: metrics})
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// parseImportConfigs reads the --configs JSONL.
func parseImportConfigs(r io.Reader, name string) (map[string]*importConfig, error) {
	out := map[string]*importConfig{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var c importConfig
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, line, err)
		}
		c.Run = strings.TrimSpace(c.Run)
		if err := validateIngestNameLocal(c.Run); err != nil {
			return nil, fmt.Errorf("%s:%d: run name %q %s", name, line, c.Run, err)
		}
		if _, dup := out[c.Run]; dup {
			return nil, fmt.Errorf("%s:%d: run %q appears more than once", name, line, c.Run)
		}
		switch c.Status {
		case "", "finished", "failed":
		default:
			return nil, fmt.Errorf("%s:%d: status must be finished or failed, got %q", name, line, c.Status)
		}
		for label, v := range map[string]string{"group": c.Group, "job_type": c.JobType} {
			if v == "" {
				continue
			}
			if err := validateIngestNameLocal(v); err != nil {
				return nil, fmt.Errorf("%s:%d: %s %q %s", name, line, label, v, err)
			}
		}
		out[c.Run] = &c
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// loadImportData parses every FILE and the --configs file, then sorts each
// run's points by step and checks the per-run metric limit. Warnings about
// ignored columns and unused configs go to stderr.
func loadImportData(files []string, format, configsPath string, stderr io.Writer) (*importData, error) {
	d := newImportData()
	for _, path := range files {
		f, err := importFormat(path, format)
		if err != nil {
			return nil, err
		}
		if err := parseImportPath(path, f, d); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(d.ignored))
	for n := range d.ignored {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stderr, "tf: warning: ignoring column %q: %s\n", n, d.ignored[n])
	}

	if configsPath != "" {
		fh, err := os.Open(configsPath)
		if err != nil {
			return nil, err
		}
		cfgs, err := parseImportConfigs(fh, configsPath)
		fh.Close()
		if err != nil {
			return nil, err
		}
		var unused []string
		for name, c := range cfgs {
			if r, ok := d.byName[name]; ok {
				r.cfg = c
			} else {
				unused = append(unused, name)
			}
		}
		sort.Strings(unused)
		for _, n := range unused {
			fmt.Fprintf(stderr, "tf: warning: --configs names run %q, which has no rows; ignoring it\n", n)
		}
	}

	for _, r := range d.runs {
		sort.SliceStable(r.points, func(i, j int) bool { return r.points[i].Step < r.points[j].Step })
		if len(r.keys) > hub.IngestMaxMetricKeys {
			return nil, fmt.Errorf("run %q has %d distinct metrics; the server allows at most %d per run",
				r.name, len(r.keys), hub.IngestMaxMetricKeys)
		}
	}
	return d, nil
}

func parseImportPath(path, format string, d *importData) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	switch format {
	case "csv":
		return parseImportCSV(fh, path, d)
	case "jsonl":
		return parseImportJSONL(fh, path, d)
	}
	return fmt.Errorf("unknown format %q", format)
}

// importRunResult is one entry of the summary.
type importRunResult struct {
	Run      string `json:"run"`
	Points   int    `json:"points"`
	Status   string `json:"status"`
	Replaced bool   `json:"replaced"`
}

type importResultJSON struct {
	Runs         []importRunResult `json:"runs"`
	SkippedCells int               `json:"skipped_cells"`
	DryRun       bool              `json:"dry_run,omitempty"`
}

// importRunOne sends one run: DELETE first when replacing, then its points in
// /log batches (the first carrying the config), then /finish.
func importRunOne(ctx context.Context, client *hub.Client, ref hub.Ref, project string, r *importRun, status string, replace bool, stderr io.Writer) error {
	var cfg *importConfig
	if r.cfg != nil {
		cfg = r.cfg
	} else {
		cfg = &importConfig{}
	}
	if replace {
		fmt.Fprintf(stderr, "tf: deleting existing run %q\n", r.name)
		if err := client.IngestDeleteRun(ctx, ref, project, r.name); err != nil && !hub.IsNotFound(err) {
			return fmt.Errorf("delete run %q: %w", r.name, err)
		}
	}
	batches := splitIngestBatches(r.points)
	if len(batches) == 0 && cfg.Config != nil {
		// No points, but a config to record: an empty batch carries it.
		batches = [][]hub.IngestPoint{{}}
	}
	sent := 0
	for i, b := range batches {
		req := hub.IngestLogRequest{
			Run: r.name, Status: "running", Group: cfg.Group, JobType: cfg.JobType, Points: b,
		}
		if i == 0 {
			req.Config = cfg.Config
		}
		if _, err := client.IngestLog(ctx, ref, project, req); err != nil {
			return fmt.Errorf("run %q: log batch %d/%d: %w", r.name, i+1, len(batches), err)
		}
		sent += len(b)
		fmt.Fprintf(stderr, "tf: %s: sent %d/%d points\n", r.name, sent, len(r.points))
	}
	if err := client.IngestFinish(ctx, ref, project, hub.IngestFinishRequest{
		Run: r.name, Status: status, Group: cfg.Group, JobType: cfg.JobType,
	}); err != nil {
		return fmt.Errorf("run %q: finish: %w", r.name, err)
	}
	return nil
}

func runExperimentsImport(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("experiments import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	var (
		configsPath, status, format string
		replace, dryRun, jsonOut    bool
	)
	fs.StringVar(&configsPath, "configs", "", "JSONL of per-run config/status/group/job_type")
	fs.StringVar(&status, "status", "finished", "final status of runs without a --configs status")
	fs.StringVar(&format, "format", "", "csv or jsonl")
	fs.BoolVar(&replace, "replace", false, "delete existing runs of the same name first")
	fs.BoolVar(&dryRun, "dry-run", false, "validate without sending anything")
	fs.BoolVar(&jsonOut, "json", false, "print the result as JSON")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, experimentsImportUsage)
		return exitOK
	}
	usageErr := func(msg string) int {
		if msg != "" {
			fmt.Fprintf(stderr, "tf: %s\n", msg)
		}
		fmt.Fprint(stderr, experimentsImportUsage)
		return exitUsage
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageErr(err.Error())
	}
	if len(pos) < 3 {
		return usageErr("experiments import needs REPO, PROJECT and at least one FILE")
	}
	ref, err := parseExpRepoArg(pos[0])
	if err != nil {
		return usageErr(err.Error())
	}
	project := pos[1]
	if err := validateIngestNameLocal(project); err != nil {
		return usageErr("project name " + err.Error())
	}
	if status != "finished" && status != "failed" {
		return usageErr(fmt.Sprintf("--status must be finished or failed, got %q", status))
	}
	switch format {
	case "", "csv", "jsonl":
	case "ndjson":
		format = "jsonl"
	default:
		return usageErr(fmt.Sprintf("--format must be csv or jsonl, got %q", format))
	}

	data, err := loadImportData(pos[2:], format, configsPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	if len(data.runs) == 0 {
		fmt.Fprintln(stderr, "tf: the input has no rows")
		return exitError
	}

	resolved, err := resolveCreds(cf, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		return exitError
	}
	if resolved.Token == "" {
		fmt.Fprintln(stderr, "tf: "+notLoggedInHint)
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent()))
	fail := func(err error) int {
		fmt.Fprintf(stderr, "tf: %s\n", describeHubError(err, resolved.Endpoint, ref.Namespace))
		return exitError
	}

	exists, created, err := ensureExpRepo(ctx, client, ref, dryRun)
	if err != nil {
		return fail(err)
	}
	switch {
	case created:
		fmt.Fprintf(stderr, "tf: created %s\n", ref)
	case !exists && dryRun:
		fmt.Fprintf(stderr, "tf: %s does not exist; it would be created\n", ref)
	}
	existing := map[string]bool{}
	if exists {
		if existing, err = client.IngestListRunNames(ctx, ref, project); err != nil {
			return fail(err)
		}
	}
	var conflicts []string
	for _, r := range data.runs {
		if existing[r.name] {
			conflicts = append(conflicts, r.name)
		}
	}
	if len(conflicts) > 0 && !replace {
		fmt.Fprintf(stderr, "tf: %d run(s) already exist in %s project %q: %s\n",
			len(conflicts), ref, project, strings.Join(conflicts, ", "))
		fmt.Fprintln(stderr, "tf: nothing was imported; pass --replace to delete and re-import them")
		return exitError
	}

	result := importResultJSON{Runs: []importRunResult{}, SkippedCells: data.skippedCells, DryRun: dryRun}
	for _, r := range data.runs {
		st := status
		if r.cfg != nil && r.cfg.Status != "" {
			st = r.cfg.Status
		}
		res := importRunResult{Run: r.name, Points: len(r.points), Status: st, Replaced: existing[r.name]}
		if !dryRun {
			if err := importRunOne(ctx, client, ref, project, r, st, existing[r.name], stderr); err != nil {
				if len(result.Runs) > 0 {
					done := make([]string, 0, len(result.Runs))
					for _, d := range result.Runs {
						done = append(done, d.Run)
					}
					fmt.Fprintf(stderr, "tf: already imported: %s\n", strings.Join(done, ", "))
				}
				return fail(err)
			}
		}
		result.Runs = append(result.Runs, res)
	}

	if jsonOut {
		if err := json.NewEncoder(stdout).Encode(&result); err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
		return exitOK
	}
	printImportSummary(stdout, ref, project, &result)
	return exitOK
}

func printImportSummary(w io.Writer, ref hub.Ref, project string, res *importResultJSON) {
	total := 0
	for _, r := range res.Runs {
		total += r.Points
	}
	verb := "imported"
	if res.DryRun {
		verb = "would import"
	}
	fmt.Fprintf(w, "%s %d run(s), %d point(s) into %s project %q\n", verb, len(res.Runs), total, ref, project)
	for _, r := range res.Runs {
		extra := ""
		if r.Replaced {
			extra = "  (replaced)"
		}
		fmt.Fprintf(w, "  %s  %d points  %s%s\n", r.Run, r.Points, r.Status, extra)
	}
	if res.SkippedCells > 0 {
		fmt.Fprintf(w, "skipped %d empty or non-numeric cell(s)\n", res.SkippedCells)
	}
}
