package tfcli

// The read / annotate half of `tf experiments` (docs/dev/agent-features.md §4):
// runs, run, wait, diff, goals, notes, annotate. import and sync live in
// experiments_import.go / experiments_sync.go, the --until language in
// experiments_until.go, and the HTTP calls in hub/experiments.go.
//
// Every subcommand takes --json, which prints the API's answer (or, for wait,
// the envelope documented in its usage) as one JSON line on stdout. Human
// output is an aligned table or a key: value block on stdout; notices go to
// stderr, so stdout stays parseable either way.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const expRunsUsage = `usage: tf experiments runs REPO PROJECT [flags]

List a project's runs as a table: name, status, last step, last update, then
one column per --columns entry. REPO is ns/name. Filtering and sorting happen
on the server.

Flags:
  --group G              only runs of sweep group G (repeatable: any of)
  --status S             only runs whose status is S: running, finished,
                         failed or stale (repeatable or comma-separated: any of)
  --tag T                only runs carrying tag T (repeatable: all of)
  --archived true|false  only archived / only unarchived runs (default: both)
  --sort SPEC            name, started_at, updated_at, last_step,
                         last:<metric>, min:<metric>, max:<metric>,
                         best:<metric> (needs a goal, see 'tf experiments
                         goals') or config:<dotted.key>
  --order asc|desc       sort direction (default asc; best: is always best first)
  --limit N              at most N runs (1..1000)
  --columns LIST         comma-separated extra columns: config:<key>,
                         last:<metric> (alias metric:), min:<metric>,
                         max:<metric>, best:<metric> (min or max by the goal),
                         group, job_type, tags, points, note.
                         Default: every metric with a goal in its best
                         direction, then the last value of up to 3 other
                         metrics. A * marks the best run of a goal metric.
  --json                 print the API response (runs, metric_goals, best)
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose
`

const expRunUsage = `usage: tf experiments run REPO PROJECT RUN [flags]

Show one run: status, step, timestamps, group, tags, note, config, and the
last / min / max of every metric.

Flags:
  --json                 print the API response ({"run": {...}})
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose
`

const expWaitUsage = `usage: tf experiments wait REPO PROJECT RUN [flags]

Block until a run satisfies a condition, using the server's long-poll
endpoint. The run does not have to exist yet: a 404 is retried every 5s, and
network or server errors with backoff, until the timeout.

Exit codes: 0 the condition holds (the run is printed), 1 timeout or the run
stopped (the last state is printed, the reason goes to stderr), 2 usage.

The wait also ends with exit 1 as soon as the run is no longer running
(finished, failed, or stale -- a crashed job that stopped logging) while
the condition does not hold and does not mention status: a wait for
step>=12000 on a run that died at step 5000 would otherwise hang until the
timeout. --ignore-stale keeps waiting instead (a stale run may come back,
and a finished run may be resumed).

Flags:
  --until EXPR           condition to wait for (default "status!=running")
  --timeout D            give up after D, e.g. 30m, 2h (default 24h; 0 = never)
  --ignore-stale         keep waiting when the run stops without meeting EXPR
  --json                 print {"run": {...}|null, "met": bool,
                         "reason": "met"|"timeout"|"stopped", "until": EXPR}
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose

Conditions:
  cond (and|or cond)...    "and" binds tighter than "or"; ( ) group
  cond := FIELD OP VALUE   OP is one of == != >= <= > <
  FIELD:
    step                   last logged step
    points                 number of logged points
    status                 running | finished | failed | stale (== / != only)
    metric:<name>          last value of a metric (alias last:<name>)
    min:<name>, max:<name> smallest / largest value so far
  Quote a metric name that contains spaces, parentheses, quotes or
  = ! < >:   metric:"val loss" < 0.2
  A comparison on a metric the run has not logged yet is false.

Examples:
  tf exp wait alice/exp ocr run-3
  tf exp wait alice/exp ocr run-3 --until 'step>=12000 or status!=running'
  tf exp wait alice/exp ocr run-3 --until 'min:val/CER < 0.05 and step >= 1000' --timeout 6h
`

const expDiffUsage = `usage: tf experiments diff REPO PROJECT [RUN ...] [flags]

Show the config keys (flattened to dotted paths) whose value differs between
runs. With no RUN, every non-archived run is compared (at most 200). "-" in
the table means the run does not have the key.

Flags:
  --include-meta         also compare the _meta and _resume subtrees
  --json                 print the API response ({"runs": [...], "keys": [...]})
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose
`

const expGoalsUsage = `usage: tf experiments goals REPO PROJECT [METRIC=min|max|none ...] [flags]

Show or set the direction each metric improves in. The goals drive the
best-run markers (runs table, Web UI) and --sort best:<metric>. With no
METRIC=..., the current goals are printed. Each METRIC=... sets one goal
("none" removes it); goals not mentioned are left alone. Setting goals needs
write access and works before the project has any run.

Flags:
  --json                 print the project ({"name", "metric_goals", ...})
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose

Example:
  tf exp goals alice/exp ocr val/CER=min acc=max old_metric=none
`

const expNotesUsage = `usage: tf experiments notes REPO PROJECT [flags]

Print a project's experiment notes ({project}/NOTES.md on the repository's
default branch), or replace them with --set. Links written as
[text](run:<run name>) become links to that run in the Web UI.

--set is an optimistic write: tf reads the current notes first and the server
refuses the write (exit 1) if they changed in between. For a read-modify-
write across two invocations, read with --json and pass its blob_sha back
with --base-sha; --force overwrites whatever is there.

Flags:
  --set FILE|-           replace the notes with FILE's content ("-" = stdin)
  --base-sha SHA         with --set: the blob_sha the edit is based on
                         ("" = the notes must not exist yet)
  --force                with --set: overwrite without checking
  -m, --message MSG      with --set: commit message
  --json                 print the API response (path, content, exists,
                         blob_sha, commit_sha)
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose
`

const expAnnotateUsage = `usage: tf experiments annotate REPO PROJECT RUN [flags]

Change a run's hand-maintained metadata. Only what a flag names changes.

Flags:
  --note TEXT            set the run's note (Markdown; "" clears it)
  --note-file FILE|-     set the note from a file ("-" = stdin)
  --tag T                replace the tag set with the --tag values (repeatable)
  --clear-tags           remove every tag
  --add-tag T            add a tag, keeping the others (repeatable)
  --remove-tag T         remove a tag, keeping the others (repeatable)
  --archive              hide the run from default listings
  --unarchive            show it again
  --json                 print the API response ({"run": {...}})
  --endpoint URL / --token TOKEN / --api-key KEY / --verbose
`

// runExperimentsQuery implements every `tf experiments` subcommand except
// import and sync.
func runExperimentsQuery(sub string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch sub {
	case "runs":
		return runExpRuns(args, stdout, stderr)
	case "run":
		return runExpRun(args, stdout, stderr)
	case "wait":
		return runExpWait(args, stdout, stderr)
	case "diff":
		return runExpDiff(args, stdout, stderr)
	case "goals":
		return runExpGoals(args, stdout, stderr)
	case "notes":
		return runExpNotes(args, stdin, stdout, stderr)
	case "annotate":
		return runExpAnnotate(args, stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "tf experiments: unknown subcommand %q\n", sub)
	fmt.Fprint(stderr, experimentsUsage)
	return exitUsage
}

// experimentsSubUsage returns the usage text of one query subcommand, for
// `tf experiments help <sub>`.
func experimentsSubUsage(sub string) (string, bool) {
	switch sub {
	case "runs":
		return expRunsUsage, true
	case "run":
		return expRunUsage, true
	case "wait":
		return expWaitUsage, true
	case "diff":
		return expDiffUsage, true
	case "goals":
		return expGoalsUsage, true
	case "notes":
		return expNotesUsage, true
	case "annotate":
		return expAnnotateUsage, true
	}
	return "", false
}

// ------------------------------------------------------------ shared setup

// expCmd is the flag set and output plumbing every query subcommand shares.
type expCmd struct {
	name    string // "runs", "wait", ...
	usage   string
	fs      *flag.FlagSet
	cf      *commonFlags
	jsonOut bool
	stdout  io.Writer
	stderr  io.Writer

	endpoint string // set by client()
	repoNS   string // set by repo()
	repoName string
}

func newExpCmd(name, usage string, stdout, stderr io.Writer) *expCmd {
	c := &expCmd{name: name, usage: usage, stdout: stdout, stderr: stderr}
	c.fs = flag.NewFlagSet("experiments "+name, flag.ContinueOnError)
	c.fs.SetOutput(io.Discard)
	c.cf = addCommonFlags(c.fs)
	c.fs.BoolVar(&c.jsonOut, "json", false, "print JSON")
	return c
}

// parse parses args and checks the positional count. When it returns
// ok == false the command is over and code is its exit code (help, usage).
// maxPos < 0 means unbounded.
func (c *expCmd) parse(args []string, minPos, maxPos int) (pos []string, code int, ok bool) {
	if hasHelpFlag(args) {
		fmt.Fprint(c.stdout, c.usage)
		return nil, exitOK, false
	}
	pos, err := parseInterspersed(c.fs, args)
	if err != nil {
		return nil, c.usageError(err.Error()), false
	}
	if len(pos) < minPos || (maxPos >= 0 && len(pos) > maxPos) {
		return nil, c.usageError("wrong number of arguments"), false
	}
	return pos, exitOK, true
}

func (c *expCmd) usageError(msg string) int {
	fmt.Fprintf(c.stderr, "tf experiments %s: %s\n", c.name, msg)
	fmt.Fprint(c.stderr, c.usage)
	return exitUsage
}

// repo parses the REPO argument: "ns/name", or "datasets/ns/name".
func (c *expCmd) repo(arg string) bool {
	s := strings.TrimPrefix(arg, "datasets/")
	ns, name, ok := strings.Cut(s, "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		c.usageError(fmt.Sprintf("REPO must be ns/name, got %q", arg))
		return false
	}
	c.repoNS, c.repoName = ns, name
	return true
}

// client resolves credentials and builds a hub client. A token is optional:
// public experiment repositories are readable anonymously, and a write
// without one fails with the server's 401 and a `tf login` hint.
func (c *expCmd) client() (*hub.Client, bool) {
	resolved, err := resolveCreds(c.cf, c.stderr)
	if err != nil {
		fmt.Fprintf(c.stderr, "tf: %s\n", err)
		return nil, false
	}
	c.endpoint = resolved.Endpoint
	return hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent())), true
}

// fail reports a hub error and returns the exit code.
func (c *expCmd) fail(err error) int {
	fmt.Fprintf(c.stderr, "tf: %s\n", describeExpError(err, c.endpoint, c.repoNS+"/"+c.repoName))
	return exitError
}

// describeExpError is describeHubError with experiment-specific wording for
// the statuses whose generic message would mislead (a 403 on a read, a 404).
func describeExpError(err error, endpoint, repo string) string {
	var herr *hub.Error
	if errors.As(err, &herr) {
		switch herr.Status {
		case http.StatusForbidden:
			if herr.Type == "token_restricted" && herr.Message != "" {
				return herr.Message
			}
			if herr.Method == http.MethodGet {
				if herr.Message != "" {
					return herr.Message
				}
				return fmt.Sprintf("you do not have access to %s", repo)
			}
			return fmt.Sprintf("you do not have write access to %s", repo)
		case http.StatusNotFound:
			if herr.Message != "" {
				return herr.Message
			}
			return fmt.Sprintf("not found (is %s an experiment repository?)", repo)
		}
	}
	return describeHubError(err, endpoint, repo)
}

// printJSON writes v as one JSON line on stdout.
func (c *expCmd) printJSON(v any) int { return writeJSONLine(c.stdout, c.stderr, v) }

// ---------------------------------------------------------------------- runs

func runExpRuns(args []string, stdout, stderr io.Writer) int {
	c := newExpCmd("runs", expRunsUsage, stdout, stderr)
	var groups, tags sliceFlag
	statuses := sliceFlag{split: true}
	var archived, sortSpec, order, columns string
	var limit int
	c.fs.Var(&groups, "group", "sweep group (repeatable)")
	c.fs.Var(&statuses, "status", "derived status (repeatable)")
	c.fs.Var(&tags, "tag", "tag (repeatable)")
	c.fs.StringVar(&archived, "archived", "", "true|false")
	c.fs.StringVar(&sortSpec, "sort", "", "sort key")
	c.fs.StringVar(&order, "order", "", "asc|desc")
	c.fs.IntVar(&limit, "limit", 0, "max runs")
	c.fs.StringVar(&columns, "columns", "", "extra columns")
	pos, code, ok := c.parse(args, 2, 2)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	q := hub.RunQuery{
		Groups: groups.values, Statuses: statuses.values, Tags: tags.values,
		Sort: sortSpec, Order: order, Limit: limit,
	}
	if archived != "" {
		b, err := strconv.ParseBool(archived)
		if err != nil {
			return c.usageError(fmt.Sprintf("--archived must be true or false, got %q", archived))
		}
		q.Archived = &b
	}
	if limit < 0 {
		return c.usageError("--limit must not be negative")
	}
	var cols []runColumn
	if columns != "" {
		var err error
		if cols, err = parseRunColumns(columns); err != nil {
			return c.usageError(err.Error())
		}
	}

	client, ok := c.client()
	if !ok {
		return exitError
	}
	resp, err := client.ListRuns(context.Background(), c.repoNS, c.repoName, pos[1], q)
	if err != nil {
		return c.fail(err)
	}
	if c.jsonOut {
		return c.printJSON(resp)
	}
	if len(resp.Runs) == 0 {
		fmt.Fprintf(stderr, "tf: no runs in %s/%s/%s match\n", c.repoNS, c.repoName, pos[1])
		return exitOK
	}
	if cols == nil {
		var hidden int
		cols, hidden = defaultRunColumns(resp)
		if hidden > 0 {
			fmt.Fprintf(stderr, "tf: %d more metric(s) not shown; pick them with --columns last:<metric>,...\n", hidden)
		}
	}
	printRunsTable(stdout, resp, cols, time.Now())
	return exitOK
}

// runColumn is one extra column of the runs table.
type runColumn struct {
	kind string // config, last, min, max, best, group, job_type, tags, points, note
	key  string // metric name or config path
	spec string // as written, used as the header
}

// parseRunColumns parses --columns. Commas separate entries, so a metric or
// config key containing a comma cannot be named here (--json has it all).
func parseRunColumns(spec string) ([]runColumn, error) {
	var cols []runColumn
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kind, key, hasKey := strings.Cut(part, ":")
		kind = strings.ToLower(kind)
		switch kind {
		case "metric":
			kind = "last"
			fallthrough
		case "config", "last", "min", "max", "best":
			if !hasKey || key == "" {
				return nil, fmt.Errorf("column %q needs a name after the colon", part)
			}
		case "group", "job_type", "tags", "points", "note":
			if hasKey {
				return nil, fmt.Errorf("column %q takes no name", part)
			}
		default:
			return nil, fmt.Errorf("unknown column %q (want config:, last:, min:, max:, best:, group, job_type, tags, points or note)", part)
		}
		cols = append(cols, runColumn{kind: kind, key: key, spec: part})
	}
	if len(cols) == 0 {
		return nil, errors.New("--columns is empty")
	}
	return cols, nil
}

// maxDefaultLastColumns bounds the last: columns the default layout adds.
const maxDefaultLastColumns = 3

// defaultRunColumns is the layout without --columns: the group when any run
// has one, each goal metric in its best direction, then the last value of up
// to three other metrics (alphabetically, skipping _-prefixed system keys).
// hidden counts the metrics left out.
func defaultRunColumns(resp *apitypes.ExpRunListResponse) (cols []runColumn, hidden int) {
	for _, r := range resp.Runs {
		if r.Group != "" {
			cols = append(cols, runColumn{kind: "group", spec: "group"})
			break
		}
	}
	goalKeys := make([]string, 0, len(resp.MetricGoals))
	for m := range resp.MetricGoals {
		goalKeys = append(goalKeys, m)
	}
	sort.Strings(goalKeys)
	seen := map[string]bool{}
	for _, m := range goalKeys {
		g := string(resp.MetricGoals[m])
		if g != "min" && g != "max" {
			continue
		}
		cols = append(cols, runColumn{kind: g, key: m, spec: g + ":" + m})
		seen[m] = true
	}
	var others []string
	for _, r := range resp.Runs {
		for _, m := range r.MetricKeys {
			if !seen[m] && !strings.HasPrefix(m, "_") {
				seen[m] = true
				others = append(others, m)
			}
		}
		for m := range r.Summary {
			if !seen[m] && !strings.HasPrefix(m, "_") {
				seen[m] = true
				others = append(others, m)
			}
		}
	}
	sort.Strings(others)
	for i, m := range others {
		if i >= maxDefaultLastColumns {
			hidden = len(others) - maxDefaultLastColumns
			break
		}
		cols = append(cols, runColumn{kind: "last", key: m, spec: "last:" + m})
	}
	return cols, hidden
}

// cell renders one column for one run; marked reports that the value is the
// best run of a goal metric in the goal's direction.
func (col runColumn) cell(r *apitypes.ExpRun, resp *apitypes.ExpRunListResponse) (text string, marked bool) {
	kind := col.kind
	if kind == "best" {
		switch resp.MetricGoals[col.key] {
		case apitypes.MetricGoalMin:
			kind = "min"
		case apitypes.MetricGoalMax:
			kind = "max"
		default:
			kind = "last"
		}
	}
	switch kind {
	case "group":
		return orDash(r.Group), false
	case "job_type":
		return orDash(r.JobType), false
	case "tags":
		return orDash(strings.Join(r.Tags, ",")), false
	case "points":
		return strconv.FormatInt(r.NumPoints, 10), false
	case "note":
		return orDash(firstLine(r.Note, 40)), false
	case "config":
		v, ok := configLookup(r.Config, col.key)
		if !ok {
			return "-", false
		}
		return formatValue(v), false
	}
	var m map[string]float64
	switch kind {
	case "last":
		m = r.Summary
	case "min":
		m = r.SummaryMin
	case "max":
		m = r.SummaryMax
	}
	v, ok := m[col.key]
	if !ok {
		return "-", false
	}
	best := resp.Best[col.key] == r.Name && string(resp.MetricGoals[col.key]) == kind
	return formatFloat(v), best
}

// printRunsTable renders the runs table.
func printRunsTable(w io.Writer, resp *apitypes.ExpRunListResponse, cols []runColumn, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := []string{"NAME", "STATUS", "STEP", "UPDATED"}
	for _, col := range cols {
		header = append(header, col.spec)
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	anyMarked := false
	for i := range resp.Runs {
		r := &resp.Runs[i]
		status := string(r.Status)
		if r.Archived {
			status += ",archived"
		}
		row := []string{sanitizeCell(r.Name), status, strconv.FormatInt(r.LastStep, 10), formatAge(r.UpdatedAt, now)}
		for _, col := range cols {
			text, marked := col.cell(r, resp)
			if marked {
				text += " *"
				anyMarked = true
			}
			row = append(row, sanitizeCell(text))
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
	if anyMarked {
		fmt.Fprintln(w, "* best run for the metric's goal")
	}
}

// ----------------------------------------------------------------------- run

func runExpRun(args []string, stdout, stderr io.Writer) int {
	c := newExpCmd("run", expRunUsage, stdout, stderr)
	pos, code, ok := c.parse(args, 3, 3)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	client, ok := c.client()
	if !ok {
		return exitError
	}
	run, err := client.GetRun(context.Background(), c.repoNS, c.repoName, pos[1], pos[2], hub.WaitOpts{})
	if err != nil {
		return c.fail(err)
	}
	if c.jsonOut {
		return c.printJSON(apitypes.ExpRunResponse{Run: *run})
	}
	printRun(stdout, run, time.Now())
	return exitOK
}

// printRun renders one run as a key: value block followed by its config and
// a metrics table.
func printRun(w io.Writer, r *apitypes.ExpRun, now time.Time) {
	row := func(label, value string) { fmt.Fprintf(w, "%-10s %s\n", label+":", value) }
	row("name", r.Name)
	status := string(r.Status)
	if r.HeartbeatSecs > 0 {
		status += fmt.Sprintf(" (heartbeat %ds)", r.HeartbeatSecs)
	}
	row("status", status)
	row("step", fmt.Sprintf("%d (%d points)", r.LastStep, r.NumPoints))
	if r.StartedAt != nil {
		row("started", r.StartedAt.UTC().Format(time.RFC3339))
	}
	row("updated", fmt.Sprintf("%s (%s)", r.UpdatedAt.UTC().Format(time.RFC3339), formatAge(r.UpdatedAt, now)))
	if r.Group != "" || r.JobType != "" {
		row("group", strings.TrimSuffix(r.Group+" / "+r.JobType, " / "))
	}
	if len(r.Tags) > 0 {
		row("tags", strings.Join(r.Tags, ", "))
	}
	if r.Archived {
		row("archived", "yes")
	}
	if r.IsBaseline {
		row("baseline", "yes")
	}
	for _, m := range r.Models {
		ref := m.RepoID
		if m.Revision != "" {
			ref += "@" + m.Revision
		}
		if !m.Exists {
			ref += " (not found)"
		}
		row("model", ref)
	}
	if r.Note != "" {
		fmt.Fprintln(w, "note:")
		for _, line := range strings.Split(r.Note, "\n") {
			fmt.Fprintln(w, "  "+line)
		}
	}
	if len(r.Config) > 0 {
		fmt.Fprintln(w, "config:")
		flat := map[string]any{}
		flattenConfig("", r.Config, flat)
		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, k := range keys {
			fmt.Fprintf(tw, "  %s\t%s\n", sanitizeCell(k), sanitizeCell(formatValue(flat[k])))
		}
		_ = tw.Flush()
	}
	metrics := map[string]bool{}
	for _, m := range r.MetricKeys {
		metrics[m] = true
	}
	for m := range r.Summary {
		metrics[m] = true
	}
	if len(metrics) > 0 {
		names := make([]string, 0, len(metrics))
		for m := range metrics {
			names = append(names, m)
		}
		sort.Strings(names)
		fmt.Fprintln(w, "metrics:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tLAST\tMIN\tMAX")
		get := func(m map[string]float64, k string) string {
			if v, ok := m[k]; ok {
				return formatFloat(v)
			}
			return "-"
		}
		for _, k := range names {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", sanitizeCell(k), get(r.Summary, k), get(r.SummaryMin, k), get(r.SummaryMax, k))
		}
		_ = tw.Flush()
	}
}

// ---------------------------------------------------------------------- wait

// Timing of `tf experiments wait`; variables so tests can shrink them.
var (
	// waitPollWindow is how long each long poll asks the server to hold
	// the request (the server caps it at 60s; staying under that leaves
	// headroom for proxies with a 60s idle timeout).
	waitPollWindow = 55 * time.Second
	// waitNotFoundInterval is the retry cadence while the run does not exist.
	waitNotFoundInterval = 5 * time.Second
	// waitMinInterval is the least time between two polls that brought no
	// change, so a server that ignores ?wait= is not hammered.
	waitMinInterval = 2 * time.Second
	// waitInitialBackoff / waitMaxBackoff bound the retry delay after a
	// network error or a 5xx.
	waitInitialBackoff = time.Second
	waitMaxBackoff     = 30 * time.Second
)

// Why a wait ended.
const (
	waitReasonMet     = "met"
	waitReasonTimeout = "timeout"
	waitReasonStopped = "stopped"
)

// runWaitResult is how a wait ended; it is also what `wait --json` prints.
type runWaitResult struct {
	// Run is the last state seen; nil when the run never appeared.
	Run    *apitypes.ExpRun `json:"run"`
	Met    bool             `json:"met"`
	Reason string           `json:"reason"`
	Until  string           `json:"until"`
}

// runWaiter waits for one run to satisfy a condition. It is the engine
// behind `tf experiments wait` and is meant to be reused as is by other
// front ends (tf mcp's wait_for_run).
type runWaiter struct {
	Client                 *hub.Client
	NS, Name, Project, Run string
	Until                  untilExpr
	UntilText              string        // as the user wrote it, echoed in the result
	Timeout                time.Duration // 0 = no timeout
	IgnoreStale            bool
	// Logf, when set, receives notices worth showing a human (waiting for
	// the run to appear, retrying after an error). May be nil.
	Logf func(format string, args ...any)
}

func (w *runWaiter) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

// sleepCtx sleeps for d or until ctx is done, reporting whether it slept
// the whole way.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Wait polls until the condition holds, the timeout passes, or (unless
// IgnoreStale) the run stops running without meeting a condition that does
// not mention status. Only a failure that retrying cannot fix -- 401, 403,
// 400, the parent ctx being cancelled -- is returned as an error; a timeout
// is a result.
func (w *runWaiter) Wait(parent context.Context) (runWaitResult, error) {
	res := runWaitResult{Until: w.UntilText}
	ctx := parent
	var deadline time.Time
	if w.Timeout > 0 {
		deadline = time.Now().Add(w.Timeout)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(parent, deadline)
		defer cancel()
	}
	timedOut := func() (runWaitResult, error) {
		if err := parent.Err(); err != nil {
			return res, err
		}
		res.Reason = waitReasonTimeout
		return res, nil
	}

	var last *apitypes.ExpRun
	backoff := waitInitialBackoff
	notFoundLogged := false
	for {
		var opts hub.WaitOpts
		if last != nil {
			opts = hub.WaitOpts{Wait: waitPollWindow, Since: last.UpdatedAt, Status: string(last.Status)}
			if !deadline.IsZero() {
				if left := time.Until(deadline); left < opts.Wait {
					opts.Wait = left
				}
			}
		}
		start := time.Now()
		run, err := w.Client.GetRun(ctx, w.NS, w.Name, w.Project, w.Run, opts)
		if err != nil {
			if ctx.Err() != nil {
				return timedOut()
			}
			var herr *hub.Error
			var delay time.Duration
			switch {
			case hub.IsNotFound(err):
				if !notFoundLogged {
					w.logf("run %q does not exist yet; checking every %s", w.Run, waitNotFoundInterval)
					notFoundLogged = true
				}
				delay = waitNotFoundInterval
				backoff = waitInitialBackoff
			case errors.As(err, &herr) && herr.Status < 500 && herr.Status != http.StatusTooManyRequests:
				return res, err
			default:
				w.logf("%s; retrying in %s", err, backoff)
				delay = backoff
				backoff = min(backoff*2, waitMaxBackoff)
			}
			if !sleepCtx(ctx, delay) {
				return timedOut()
			}
			continue
		}
		backoff = waitInitialBackoff
		changed := last == nil || !run.UpdatedAt.Equal(last.UpdatedAt) || run.Status != last.Status
		last = run
		res.Run = run
		if w.Until.eval(run) {
			res.Met, res.Reason = true, waitReasonMet
			return res, nil
		}
		if run.Status != apitypes.RunStatusRunning && !w.IgnoreStale && !w.Until.mentionsStatus() {
			res.Reason = waitReasonStopped
			return res, nil
		}
		if !changed {
			if !sleepCtx(ctx, waitMinInterval-time.Since(start)) {
				return timedOut()
			}
		}
	}
}

func runExpWait(args []string, stdout, stderr io.Writer) int {
	c := newExpCmd("wait", expWaitUsage, stdout, stderr)
	var untilText string
	var timeout time.Duration
	var ignoreStale bool
	c.fs.StringVar(&untilText, "until", "status!=running", "condition")
	c.fs.DurationVar(&timeout, "timeout", 24*time.Hour, "give up after")
	c.fs.BoolVar(&ignoreStale, "ignore-stale", false, "keep waiting on a stopped run")
	pos, code, ok := c.parse(args, 3, 3)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	if timeout < 0 {
		return c.usageError("--timeout must not be negative")
	}
	until, err := parseUntil(untilText)
	if err != nil {
		var uerr *untilError
		if errors.As(err, &uerr) {
			return c.usageError(fmt.Sprintf("invalid --until: %s\n%s", uerr, uerr.caret()))
		}
		return c.usageError("invalid --until: " + err.Error())
	}
	client, ok := c.client()
	if !ok {
		return exitError
	}
	waiter := &runWaiter{
		Client: client, NS: c.repoNS, Name: c.repoName, Project: pos[1], Run: pos[2],
		Until: until, UntilText: untilText, Timeout: timeout, IgnoreStale: ignoreStale,
		Logf: func(format string, args ...any) { fmt.Fprintf(stderr, "tf: "+format+"\n", args...) },
	}
	res, err := waiter.Wait(context.Background())
	if err != nil {
		return c.fail(err)
	}
	if c.jsonOut {
		if code := c.printJSON(res); code != exitOK {
			return code
		}
	} else if res.Run != nil {
		printRun(stdout, res.Run, time.Now())
	}
	switch res.Reason {
	case waitReasonMet:
		return exitOK
	case waitReasonStopped:
		fmt.Fprintf(stderr, "tf: run %q is %s and does not satisfy %q; it is not going to (pass --ignore-stale to keep waiting)\n",
			res.Run.Name, res.Run.Status, untilText)
	default:
		if res.Run == nil {
			fmt.Fprintf(stderr, "tf: timed out after %s: run %q never appeared\n", timeout, pos[2])
		} else {
			fmt.Fprintf(stderr, "tf: timed out after %s waiting for %q\n", timeout, untilText)
		}
	}
	return exitError
}

// ---------------------------------------------------------------------- diff

func runExpDiff(args []string, stdout, stderr io.Writer) int {
	c := newExpCmd("diff", expDiffUsage, stdout, stderr)
	var includeMeta bool
	c.fs.BoolVar(&includeMeta, "include-meta", false, "compare _meta and _resume too")
	pos, code, ok := c.parse(args, 2, -1)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	client, ok := c.client()
	if !ok {
		return exitError
	}
	resp, err := client.ConfigDiff(context.Background(), c.repoNS, c.repoName, pos[1], pos[2:], includeMeta)
	if err != nil {
		return c.fail(err)
	}
	if c.jsonOut {
		return c.printJSON(resp)
	}
	switch {
	case len(resp.Runs) == 0:
		fmt.Fprintln(stderr, "tf: no runs to compare")
		return exitOK
	case len(resp.Keys) == 0:
		fmt.Fprintf(stdout, "no config differences between %d run(s)\n", len(resp.Runs))
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	header := []string{"KEY"}
	for _, r := range resp.Runs {
		header = append(header, sanitizeCell(r))
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, k := range resp.Keys {
		row := []string{sanitizeCell(k.Key)}
		for _, r := range resp.Runs {
			v, ok := k.Values[r]
			if !ok {
				row = append(row, "-")
				continue
			}
			row = append(row, sanitizeCell(formatValue(v)))
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
	return exitOK
}

// --------------------------------------------------------------------- goals

func runExpGoals(args []string, stdout, stderr io.Writer) int {
	c := newExpCmd("goals", expGoalsUsage, stdout, stderr)
	pos, code, ok := c.parse(args, 2, -1)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	project := pos[1]
	var goals map[string]string
	if len(pos) > 2 {
		goals = map[string]string{}
		for _, a := range pos[2:] {
			// The last "=" splits: a metric name may contain one, a goal
			// never does.
			i := strings.LastIndex(a, "=")
			if i <= 0 {
				return c.usageError(fmt.Sprintf("expected METRIC=min|max|none, got %q", a))
			}
			metric, goal := a[:i], strings.ToLower(a[i+1:])
			switch goal {
			case "min", "max":
			case "none", "":
				goal = ""
			default:
				return c.usageError(fmt.Sprintf("goal for %q must be min, max or none, got %q", metric, a[i+1:]))
			}
			goals[metric] = goal
		}
	}
	client, ok := c.client()
	if !ok {
		return exitError
	}
	ctx := context.Background()
	var proj *apitypes.ExpProject
	if goals != nil {
		p, err := client.UpdateProject(ctx, c.repoNS, c.repoName, project, goals)
		if err != nil {
			return c.fail(err)
		}
		proj = p
	} else {
		repo, err := client.GetExperimentRepo(ctx, c.repoNS, c.repoName)
		if err != nil {
			return c.fail(err)
		}
		for i := range repo.Projects {
			if repo.Projects[i].Name == project {
				proj = &repo.Projects[i]
				break
			}
		}
		if proj == nil {
			// No row yet: no runs and no goals. Not an error -- goals can
			// be declared before the first run.
			proj = &apitypes.ExpProject{Name: project}
		}
	}
	if proj.MetricGoals == nil {
		proj.MetricGoals = map[string]apitypes.MetricGoal{}
	}
	if c.jsonOut {
		return c.printJSON(proj)
	}
	if len(proj.MetricGoals) == 0 {
		fmt.Fprintf(stderr, "tf: no metric goals set for %s\n", project)
		return exitOK
	}
	metrics := make([]string, 0, len(proj.MetricGoals))
	for m := range proj.MetricGoals {
		metrics = append(metrics, m)
	}
	sort.Strings(metrics)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "METRIC\tGOAL")
	for _, m := range metrics {
		fmt.Fprintf(tw, "%s\t%s\n", sanitizeCell(m), proj.MetricGoals[m])
	}
	_ = tw.Flush()
	return exitOK
}

// --------------------------------------------------------------------- notes

func runExpNotes(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newExpCmd("notes", expNotesUsage, stdout, stderr)
	var setFrom, message, baseSHA string
	var force bool
	c.fs.StringVar(&setFrom, "set", "", "replace from FILE or -")
	c.fs.StringVar(&baseSHA, "base-sha", "", "blob sha the edit is based on")
	c.fs.BoolVar(&force, "force", false, "overwrite without checking")
	c.fs.StringVar(&message, "m", "", "commit message")
	c.fs.StringVar(&message, "message", "", "commit message")
	pos, code, ok := c.parse(args, 2, 2)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	project := pos[1]
	set := map[string]bool{}
	c.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["set"] && (set["base-sha"] || set["force"] || set["m"] || set["message"]) {
		return c.usageError("--base-sha, --force and --message only apply with --set")
	}
	if set["set"] && setFrom == "" {
		return c.usageError("--set needs a FILE or -")
	}
	if force && set["base-sha"] {
		return c.usageError("--force and --base-sha are mutually exclusive")
	}

	var content string
	if set["set"] {
		b, err := readInput(setFrom, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
		content = string(b)
	}

	client, ok := c.client()
	if !ok {
		return exitError
	}
	ctx := context.Background()
	if !set["set"] {
		notes, err := client.GetNotes(ctx, c.repoNS, c.repoName, project)
		if err != nil {
			return c.fail(err)
		}
		if c.jsonOut {
			return c.printJSON(notes)
		}
		if !notes.Exists {
			fmt.Fprintf(stderr, "tf: %s has no notes yet (%s)\n", project, notes.Path)
			return exitOK
		}
		fmt.Fprint(stdout, notes.Content)
		if !strings.HasSuffix(notes.Content, "\n") && notes.Content != "" {
			fmt.Fprintln(stdout)
		}
		return exitOK
	}

	var base *string
	switch {
	case force:
	case set["base-sha"]:
		base = &baseSHA
	default:
		cur, err := client.GetNotes(ctx, c.repoNS, c.repoName, project)
		if err != nil {
			return c.fail(err)
		}
		sha := cur.BlobSHA
		base = &sha
	}
	notes, err := client.PutNotes(ctx, c.repoNS, c.repoName, project, content, base, message)
	if err != nil {
		if hub.IsConflict(err) {
			fmt.Fprintln(stderr, "tf: the notes changed since they were read (someone else edited them); read them again, or pass --force to overwrite")
			return exitError
		}
		return c.fail(err)
	}
	if c.jsonOut {
		return c.printJSON(notes)
	}
	fmt.Fprintf(stdout, "Updated %s (commit %s)\n", notes.Path, shortOID(notes.CommitSHA))
	return exitOK
}

// readInput reads a whole file, or stdin for "-".
func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ------------------------------------------------------------------ annotate

func runExpAnnotate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newExpCmd("annotate", expAnnotateUsage, stdout, stderr)
	var note, noteFile string
	var tags, addTags, removeTags sliceFlag
	var clearTags, archive, unarchive bool
	c.fs.StringVar(&note, "note", "", "note text")
	c.fs.StringVar(&noteFile, "note-file", "", "note file or -")
	c.fs.Var(&tags, "tag", "replace tags (repeatable)")
	c.fs.BoolVar(&clearTags, "clear-tags", false, "remove every tag")
	c.fs.Var(&addTags, "add-tag", "add a tag (repeatable)")
	c.fs.Var(&removeTags, "remove-tag", "remove a tag (repeatable)")
	c.fs.BoolVar(&archive, "archive", false, "archive the run")
	c.fs.BoolVar(&unarchive, "unarchive", false, "unarchive the run")
	pos, code, ok := c.parse(args, 3, 3)
	if !ok {
		return code
	}
	if !c.repo(pos[0]) {
		return exitUsage
	}
	project, runName := pos[1], pos[2]
	set := map[string]bool{}
	c.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var req apitypes.ExpRunAnnotationRequest
	switch {
	case set["note"] && set["note-file"]:
		return c.usageError("--note and --note-file are mutually exclusive")
	case set["note"]:
		req.Note = &note
	case set["note-file"]:
		b, err := readInput(noteFile, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "tf: %s\n", err)
			return exitError
		}
		s := string(b)
		req.Note = &s
	}
	replaceTags := set["tag"] || clearTags
	editTags := len(addTags.values) > 0 || len(removeTags.values) > 0
	if replaceTags && editTags {
		return c.usageError("--tag / --clear-tags replace the tag set; they cannot be combined with --add-tag / --remove-tag")
	}
	if set["tag"] && clearTags {
		return c.usageError("--tag and --clear-tags are mutually exclusive")
	}
	if replaceTags {
		t := append([]string{}, tags.values...)
		req.Tags = &t
	}
	if archive && unarchive {
		return c.usageError("--archive and --unarchive are mutually exclusive")
	}
	if archive || unarchive {
		req.Archived = &archive
	}
	if req.Note == nil && req.Tags == nil && req.Archived == nil && !editTags {
		return c.usageError("nothing to change; pass --note, --note-file, --tag, --clear-tags, --add-tag, --remove-tag, --archive or --unarchive")
	}

	client, ok := c.client()
	if !ok {
		return exitError
	}
	ctx := context.Background()
	if editTags {
		// Read-modify-write: the API replaces the tag list wholesale. A
		// concurrent tag edit between the read and the write is lost; tags
		// are hand-maintained labels, so that window is accepted.
		cur, err := client.GetRun(ctx, c.repoNS, c.repoName, project, runName, hub.WaitOpts{})
		if err != nil {
			return c.fail(err)
		}
		t := editTagList(cur.Tags, addTags.values, removeTags.values)
		req.Tags = &t
	}
	run, err := client.AnnotateRun(ctx, c.repoNS, c.repoName, project, runName, req)
	if err != nil {
		return c.fail(err)
	}
	if c.jsonOut {
		return c.printJSON(apitypes.ExpRunAnnotationResponse{Run: *run})
	}
	printRun(stdout, run, time.Now())
	return exitOK
}

// editTagList applies --add-tag / --remove-tag to cur, keeping its order and
// appending new tags at the end.
func editTagList(cur, add, remove []string) []string {
	drop := map[string]bool{}
	for _, t := range remove {
		drop[strings.TrimSpace(t)] = true
	}
	out := []string{}
	have := map[string]bool{}
	for _, t := range cur {
		if !drop[t] && !have[t] {
			out = append(out, t)
			have[t] = true
		}
	}
	for _, t := range add {
		t = strings.TrimSpace(t)
		if t != "" && !drop[t] && !have[t] {
			out = append(out, t)
			have[t] = true
		}
	}
	return out
}

// ---------------------------------------------------------------- formatting

// formatFloat renders a metric value compactly: up to 6 significant digits,
// no exponent for ordinary magnitudes (12000, not 1.2e+04).
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// formatValue renders a config value: strings bare, numbers via
// formatFloat, anything else as JSON.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		if x == float64(int64(x)) && x < 1e15 && x > -1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// configLookup finds key in a run's config: the literal key first (a config
// may use dotted keys itself), then a dotted path through nested objects.
func configLookup(cfg map[string]any, key string) (any, bool) {
	if v, ok := cfg[key]; ok {
		return v, true
	}
	var cur any = cfg
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// flattenConfig flattens nested objects into dotted keys.
func flattenConfig(prefix string, v any, out map[string]any) {
	m, ok := v.(map[string]any)
	if !ok || (len(m) == 0 && prefix != "") {
		out[prefix] = v
		return
	}
	for k, child := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		flattenConfig(key, child, out)
	}
}

// formatAge renders how long ago t was: "12s ago", "5m ago", "3h ago",
// "2d ago", then a date past a week.
func formatAge(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.UTC().Format("2006-01-02")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// firstLine is s's first line, cut to n runes.
func firstLine(s string, n int) string {
	s, _, cut := strings.Cut(s, "\n")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	if cut {
		return s + " …"
	}
	return s
}

// sanitizeCell keeps a table cell on one line and out of tabwriter's column
// logic: tabs and newlines in a name or value would split or misalign it.
func sanitizeCell(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', '\v', '\f':
			return ' '
		}
		return r
	}, s)
}
