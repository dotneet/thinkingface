package tfcli

// `tf experiments sync` (docs/dev/agent-features.md §2.9, P9): replay runs the
// Python shim recorded to disk -- THINKINGFACE_MODE=offline, or the points the
// online mode spilled instead of dropping -- to the server.
//
// Each run directory holds an append-only run.jsonl and, once this command has
// touched it, a sync-state.json recording how many lines have been delivered.
// A pass replays the lines after that count, coalescing log records into /log
// calls, and persists the count after every successful call, so an
// interrupted sync resumes where it stopped (delivery is at-least-once per
// call). At the finish record it commits the staged artifacts, posts /finish,
// records the produced models and marks the directory done.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

const experimentsSyncUsage = `usage: tf experiments sync [DIR ...] [flags]

Upload runs recorded offline by thinkingface.trackio (THINKINGFACE_MODE=offline,
or points the online mode could not deliver). Each DIR is either the offline
directory holding one subdirectory per run, or one run directory (it contains
run.jsonl). Default: ./thinkingface-offline.

Progress is kept in each run directory's sync-state.json, so a sync can be
interrupted and re-run: it continues after the last delivered batch. A run
whose run.jsonl has no finish record yet is synced up to its current end and
left open; a run that reached its finish record gets its artifacts committed,
its status set and its produced models recorded, and is skipped afterwards.

On the first sync of a run the repository ("repo": null means
{you}/trackio-metrics, created if missing) and the run name are resolved with
the same resume rules as the shim: resume="never" and a taken name logs to
name-1, name-2, ...; "allow" continues the existing run; "must" requires it.

Flags:
  --watch                keep syncing every --interval until interrupted
                         (Ctrl-C), following runs that are still being written
  --interval D           pause between --watch passes (default 60s)
  --json                 print the result of each pass as one JSON line
  --endpoint URL         server URL
  --token TOKEN          API token (or THINKINGFACE_API_KEY in the environment)
  --api-key KEY          alias of --token
  --verbose              print credential resolution to stderr
`

// defaultOfflineDir is THINKINGFACE_OFFLINE_DIR's default in the shim.
const defaultOfflineDir = "thinkingface-offline"

const (
	runJSONLName     = "run.jsonl"
	syncStateName    = "sync-state.json"
	syncStateVersion = 1
)

// syncState is sync-state.json. Unknown fields written by a newer tf are
// dropped on rewrite; nothing else writes this file.
//
// Run and Repo are the resolution made on the first pass; an empty Run means
// nothing has been resolved yet. SyncedLines counts the lines delivered, and
// starts at 0 even once Run is set: line 0 is the init record, whose config
// is only delivered with the first successful /log. BaseConfig is the config
// the server already held for a run this directory continues (resume
// "allow" / "must" on an existing run); every config sent is merged over it.
type syncState struct {
	V           int            `json:"v"`
	SyncedLines int            `json:"synced_lines"`
	Run         string         `json:"run"`
	Repo        string         `json:"repo"`
	BaseConfig  map[string]any `json:"base_config,omitempty"`
	Done        bool           `json:"done"`
}

// readSyncState reads dir's sync-state.json; a missing file is the zero state.
func readSyncState(dir string) (syncState, error) {
	var st syncState
	data, err := os.ReadFile(filepath.Join(dir, syncStateName))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", syncStateName, err)
	}
	if st.SyncedLines < 0 {
		st.SyncedLines = 0
	}
	return st, nil
}

// writeSyncState replaces dir's sync-state.json atomically (temp file in the
// same directory + rename), so a crash never leaves a torn state behind.
func writeSyncState(dir string, st syncState) error {
	st.V = syncStateVersion
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".sync-state-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", syncStateName, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("write %s: %w", syncStateName, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, syncStateName)); err != nil {
		return fmt.Errorf("write %s: %w", syncStateName, err)
	}
	return nil
}

// offlineRecord is one run.jsonl line. Every record type's fields share the
// struct; fields a type does not use stay zero, and unknown fields are
// ignored.
type offlineRecord struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	// init
	Repo    *string `json:"repo"`
	Project string  `json:"project"`
	Run     string  `json:"run"`
	Resume  string  `json:"resume"`
	Group   string  `json:"group"`
	JobType string  `json:"job_type"`
	// init, and log when it changed. null / absent means "send no config":
	// the shim leaves it out of a spilled record once the online mode
	// delivered it.
	Config map[string]any `json:"config"`

	// log
	Points []offlinePoint `json:"points"`

	// artifact
	Name string `json:"name"`
	Path string `json:"path"`

	// model
	RepoID   string `json:"repo_id"`
	Revision string `json:"revision"`

	// finish
	Status string `json:"status"`
}

type offlinePoint struct {
	Step      json.Number                `json:"step"`
	Timestamp json.RawMessage            `json:"timestamp"`
	Metrics   map[string]json.RawMessage `json:"metrics"`
}

// readRunJSONL reads the complete lines of run.jsonl. Line i of the file is
// records[i-1]; a blank line is a record with Type "". Reading stops at a
// line without its trailing newline (still being written) and at a line that
// is not valid JSON (the writer died mid-line): both end the file for the
// reader. A complete, well-formed line that does not fit the record shape
// (`"step": true`, a bare number, ...) is skipped with a warning and becomes
// an inert Type "" record, so the cursor moves past it instead of the
// directory stalling on it forever.
//
// warn receives the 0-based index of the line concerned, so the caller can
// stay quiet about lines an earlier pass already reported.
func readRunJSONL(path string, warn func(idx int, msg string)) ([]offlineRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []offlineRecord
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break // a partial last line: not ours to read yet
		}
		line := bytes.TrimSpace(data[:nl])
		data = data[nl+1:]
		var rec offlineRecord
		if len(line) > 0 {
			if err := json.Unmarshal(line, &rec); err != nil {
				var syntaxErr *json.SyntaxError
				if errors.As(err, &syntaxErr) {
					if len(bytes.TrimSpace(data)) > 0 {
						warn(len(recs), fmt.Sprintf("line %d does not parse (%v); it and everything after it are ignored", len(recs)+1, err))
					}
					break
				}
				// Valid JSON of the wrong shape: the writer finished the
				// line, so what follows is intact. Skip just this one.
				warn(len(recs), fmt.Sprintf("skipping line %d: %v", len(recs)+1, err))
				rec = offlineRecord{}
			}
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// syncDirResult is what one pass did for one run directory.
type syncDirResult struct {
	Dir               string `json:"dir"`
	Repo              string `json:"repo,omitempty"`
	Project           string `json:"project,omitempty"`
	Run               string `json:"run,omitempty"`
	SyncedLines       int    `json:"synced_lines"`
	Points            int    `json:"points"` // sent during this pass
	Done              bool   `json:"done"`
	AlreadyDone       bool   `json:"already_done,omitempty"`
	Status            string `json:"status,omitempty"` // final status, once done
	ArtifactsUploaded int    `json:"artifacts_uploaded,omitempty"`
	ArtifactsSkipped  int    `json:"artifacts_skipped,omitempty"` // same content already on the server
	Error             string `json:"error,omitempty"`
}

// offlineSyncer holds what is shared across the run directories of a pass and
// across --watch passes: the client, the caller's identity (resolved at most
// once, and only when a run says "repo": null) and the repositories already
// known to exist.
type offlineSyncer struct {
	client   *hub.Client
	endpoint string
	stderr   io.Writer
	username string
	repos    map[string]bool
}

func newOfflineSyncer(client *hub.Client, stderr io.Writer) *offlineSyncer {
	return &offlineSyncer{client: client, endpoint: client.Endpoint(), stderr: stderr, repos: map[string]bool{}}
}

func (s *offlineSyncer) warnf(dir, format string, args ...any) {
	fmt.Fprintf(s.stderr, "tf: warning: %s: %s\n", dir, fmt.Sprintf(format, args...))
}

// isDefaultRepo reports whether the init record's repo is null / empty,
// i.e. "resolve {whoami}/trackio-metrics at sync time".
func isDefaultRepo(repo *string) bool {
	return repo == nil || strings.TrimSpace(*repo) == ""
}

// resolveRepo turns the init record's repo into a Ref ("repo": null means
// {whoami}/trackio-metrics). It does not touch the repository itself; see
// ensureRepo.
func (s *offlineSyncer) resolveRepo(ctx context.Context, repo *string) (hub.Ref, error) {
	id := ""
	if !isDefaultRepo(repo) {
		id = strings.TrimSpace(*repo)
	} else {
		if s.username == "" {
			u, err := s.client.Whoami(ctx)
			if err != nil {
				return hub.Ref{}, err
			}
			s.username = u.Name
		}
		id = s.username + "/trackio-metrics"
	}
	return parseExpRepoArg(id)
}

func (s *offlineSyncer) ensureRepo(ctx context.Context, ref hub.Ref) error {
	if s.repos[ref.ID()] {
		return nil
	}
	_, created, err := ensureExpRepo(ctx, s.client, ref, false)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(s.stderr, "tf: created %s\n", ref)
	}
	s.repos[ref.ID()] = true
	return nil
}

// resolveRunName applies the shim's resume rules to the run name. prev is the
// existing run's config when the run is continued, nil otherwise.
func (s *offlineSyncer) resolveRunName(ctx context.Context, dir string, ref hub.Ref, project, name, resume string) (string, map[string]any, error) {
	runs, err := s.client.IngestListRuns(ctx, ref, project)
	if err != nil {
		return "", nil, err
	}
	taken := make(map[string]bool, len(runs))
	var existing *apitypes.ExpRun
	for i := range runs {
		taken[runs[i].Name] = true
		if runs[i].Name == name {
			existing = &runs[i]
		}
	}
	mode := strings.ToLower(strings.TrimSpace(resume))
	switch mode {
	case "", "never", "allow", "must":
	default:
		s.warnf(dir, "unknown resume mode %q; treating it as \"never\"", resume)
		mode = "never"
	}
	switch mode {
	case "must":
		if existing == nil {
			return "", nil, fmt.Errorf("resume=\"must\" but run %q does not exist in project %q of %s", name, project, ref.ID())
		}
		return name, existing.Config, nil
	case "allow":
		if existing != nil {
			return name, existing.Config, nil
		}
		return name, nil, nil
	}
	if existing == nil {
		return name, nil, nil
	}
	unique := name
	for i := 1; taken[unique]; i++ {
		unique = fmt.Sprintf("%s-%d", name, i)
	}
	s.warnf(dir, "run %q already exists in project %q and resume=\"never\"; syncing to %q instead", name, project, unique)
	return unique, nil, nil
}

// mergeConfig overlays cur on prev (cur wins), which is what continuing a run
// does to its config. prev is the config the server held when the run was
// resolved (syncState.BaseConfig), so every config a continued run sends --
// not just the first -- keeps the keys the earlier attempt recorded.
func mergeConfig(prev, cur map[string]any) map[string]any {
	if len(prev) == 0 {
		return cur
	}
	out := make(map[string]any, len(prev)+len(cur))
	for k, v := range prev {
		out[k] = v
	}
	for k, v := range cur {
		out[k] = v
	}
	return out
}

// convertOfflinePoints turns one log record's points into wire points,
// dropping what the server would refuse (with a warning, once per name) so a
// single bad value cannot block the directory forever.
func convertOfflinePoints(pts []offlinePoint, warnOnce func(key, msg string)) []hub.IngestPoint {
	out := make([]hub.IngestPoint, 0, len(pts))
	for _, p := range pts {
		step, err := parseImportStep(p.Step.String())
		if err != nil {
			warnOnce("step:"+p.Step.String(), fmt.Sprintf("skipping a point: %v", err))
			continue
		}
		ip := hub.IngestPoint{Step: step, Metrics: make(map[string]float64, len(p.Metrics))}
		if ts := bytes.TrimSpace(p.Timestamp); len(ts) > 0 && !bytes.Equal(ts, []byte("null")) {
			var text string
			if ts[0] == '"' {
				_ = json.Unmarshal(ts, &text)
			} else {
				text = string(ts)
			}
			if parsed, err := parseImportTimestamp(text); err == nil {
				ip.Timestamp = parsed
			}
		}
		for k, raw := range p.Metrics {
			if err := validateIngestNameLocal(k); err != nil {
				warnOnce("metric:"+k, fmt.Sprintf("dropping metric %q: the name %s", k, err))
				continue
			}
			if isStructuralMetricName(k) {
				warnOnce("metric:"+k, fmt.Sprintf("dropping metric %q: the name is reserved", k))
				continue
			}
			// A pointer, so that null stays "nothing logged" instead of
			// decoding as a measured 0.
			var v *float64
			if json.Unmarshal(raw, &v) != nil || v == nil {
				continue // null or non-numeric: logged nothing for this key
			}
			ip.Metrics[k] = *v
		}
		out = append(out, ip)
	}
	return out
}

// offlineArtifactName normalises an artifact name the way the shim does
// (_artifacts.normalize_artifact_name): forward slashes, no empty / "."
// segments, never "..".
func offlineArtifactName(name string) (string, error) {
	cleaned := strings.Trim(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"), "/")
	var parts []string
	for _, p := range strings.Split(cleaned, "/") {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("artifact name %q climbs out of the run directory", name)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "", errors.New("artifact name is empty")
	}
	return strings.Join(parts, "/"), nil
}

// offlineArtifacts builds the artifact list from every artifact record up to
// (not including) index end. A later record for the same name wins; a file
// that is missing on disk is skipped with a warning.
func (s *offlineSyncer) offlineArtifacts(dir string, recs []offlineRecord, end int) []hub.RunArtifact {
	byName := map[string]int{}
	var arts []hub.RunArtifact
	for _, rec := range recs[:end] {
		if rec.Type != "artifact" {
			continue
		}
		name, err := offlineArtifactName(rec.Name)
		if err != nil {
			s.warnf(dir, "skipping artifact: %v", err)
			continue
		}
		rel := rec.Path
		if rel == "" {
			rel = "artifacts/" + name
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dir, full); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(rec.Path) {
			s.warnf(dir, "skipping artifact %q: its path %q is outside the run directory", name, rec.Path)
			continue
		}
		fi, err := os.Stat(full)
		if err != nil || !fi.Mode().IsRegular() {
			s.warnf(dir, "skipping artifact %q: %s is missing or not a regular file", name, rel)
			continue
		}
		a := hub.RunArtifact{Name: name, File: hub.LocalFile{
			Size: fi.Size(),
			Open: func() (io.ReadCloser, error) { return os.Open(full) },
		}}
		if i, ok := byName[name]; ok {
			arts[i] = a
			continue
		}
		byName[name] = len(arts)
		arts = append(arts, a)
	}
	return arts
}

// offlineModels is every model record up to end, duplicates removed.
func offlineModels(recs []offlineRecord, end int) []apitypes.ExpRunModelInput {
	seen := map[apitypes.ExpRunModelInput]bool{}
	var out []apitypes.ExpRunModelInput
	for _, rec := range recs[:end] {
		if rec.Type != "model" || strings.TrimSpace(rec.RepoID) == "" {
			continue
		}
		m := apitypes.ExpRunModelInput{RepoID: strings.TrimSpace(rec.RepoID), Revision: rec.Revision}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// finishStatus normalises a finish record's status: "finished" or "failed",
// anything else (with a warning) and nothing at all meaning "finished".
func finishStatus(raw string, warnOnce func(key, msg string)) string {
	status := strings.TrimSpace(raw)
	switch status {
	case "finished", "failed":
		return status
	case "":
		return "finished"
	default:
		warnOnce("status", fmt.Sprintf("unknown finish status %q; recording the run as finished", status))
		return "finished"
	}
}

// syncDir runs one pass over one run directory. It never panics on bad input:
// every problem ends up in the result's Error, with the state left at the
// last successful call.
func (s *offlineSyncer) syncDir(ctx context.Context, dir string) (res syncDirResult) {
	res.Dir = dir
	var (
		ref         hub.Ref
		repoID      string // where this run goes, as soon as that is known
		defaultRepo bool   // the init record said "repo": null
	)
	fail := func(err error) syncDirResult {
		res.Error = syncErrorText(err, s.endpoint, repoID, defaultRepo)
		return res
	}
	st, err := readSyncState(dir)
	if err != nil {
		return fail(err)
	}
	res.SyncedLines, res.Run, res.Repo = st.SyncedLines, st.Run, st.Repo
	if st.Done {
		res.Done, res.AlreadyDone = true, true
		return res
	}

	warned := map[string]bool{}
	warnOnce := func(key, msg string) {
		if !warned[key] {
			warned[key] = true
			s.warnf(dir, "%s", msg)
		}
	}
	recs, err := readRunJSONL(filepath.Join(dir, runJSONLName), func(idx int, m string) {
		if idx >= st.SyncedLines { // an earlier pass already reported the rest
			s.warnf(dir, "%s: %s", runJSONLName, m)
		}
	})
	if err != nil {
		return fail(err)
	}
	if len(recs) == 0 {
		return res // the writer has not finished its first line yet
	}
	init := recs[0]
	if init.Type != "init" {
		return fail(fmt.Errorf("%s does not start with an init record", runJSONLName))
	}
	if err := validateIngestNameLocal(init.Project); err != nil {
		return fail(fmt.Errorf("init record: project name %q %s", init.Project, err))
	}
	if err := validateIngestNameLocal(init.Run); err != nil {
		return fail(fmt.Errorf("init record: run name %q %s", init.Run, err))
	}
	if init.V > 1 {
		warnOnce("version", fmt.Sprintf("%s is format version %d; this tf knows version 1 and syncs what it understands", runJSONLName, init.V))
	}
	defaultRepo = isDefaultRepo(init.Repo)
	res.Project = init.Project
	project := init.Project
	group, jobType := strings.TrimSpace(init.Group), strings.TrimSpace(init.JobType)
	for label, v := range map[string]string{"group": group, "job_type": jobType} {
		if v != "" && validateIngestNameLocal(v) != nil {
			warnOnce(label, fmt.Sprintf("ignoring the init record's invalid %s %q", label, v))
			if label == "group" {
				group = ""
			} else {
				jobType = ""
			}
		}
	}

	if st.Run == "" || st.Repo == "" {
		// First sync: decide where this run goes, and record the decision
		// before anything is sent -- a crash after the first /log must not
		// make the next attempt pick "name-1" for the run it already created.
		// The cursor stays at 0: line 0 is the init record, and its config is
		// only delivered by the first /log that succeeds.
		if ref, err = s.resolveRepo(ctx, init.Repo); err != nil {
			return fail(err)
		}
		repoID = ref.ID()
		if err := s.ensureRepo(ctx, ref); err != nil {
			return fail(err)
		}
		name, base, err := s.resolveRunName(ctx, dir, ref, project, init.Run, init.Resume)
		if err != nil {
			return fail(err)
		}
		st = syncState{SyncedLines: 0, Run: name, Repo: ref.ID(), BaseConfig: base}
		if err := writeSyncState(dir, st); err != nil {
			return fail(err)
		}
	} else if ref, err = parseExpRepoArg(st.Repo); err != nil {
		return fail(fmt.Errorf("%s: %w", syncStateName, err))
	}
	repoID = ref.ID()
	res.SyncedLines, res.Run, res.Repo = st.SyncedLines, st.Run, st.Repo
	if st.SyncedLines > len(recs) {
		warnOnce("shrunk", fmt.Sprintf("%s records %d synced lines but %s has only %d complete lines; nothing to do",
			syncStateName, st.SyncedLines, runJSONLName, len(recs)))
		return res
	}

	// A run whose file already holds its finish record is replayed at that
	// final status rather than "running": a spill of a run whose online
	// /finish went through must not move it finished -> running -> finished
	// (a second run.finished webhook, and a window where it looks live).
	finishAt := -1
	for i := 1; i < len(recs); i++ {
		if recs[i].Type == "finish" {
			finishAt = i
			break
		}
	}
	end, logStatus, status := len(recs), "running", ""
	if finishAt >= 0 {
		status = finishStatus(recs[finishAt].Status, warnOnce)
		end, logStatus = finishAt, status
		if finishAt+1 < len(recs) {
			warnOnce("after-finish", fmt.Sprintf("ignoring %d line(s) after the finish record", len(recs)-finishAt-1))
		}
	}

	var (
		batch         []hub.IngestPoint
		batchBytes    int
		pendingConfig map[string]any
	)
	// flush sends the pending batch (and config) and, once the server has it,
	// records that every line before cursor is delivered.
	flush := func(cursor int) error {
		if len(batch) > 0 || pendingConfig != nil {
			req := hub.IngestLogRequest{
				Run: st.Run, Status: logStatus, Config: pendingConfig,
				Group: group, JobType: jobType, Points: batch,
			}
			if _, err := s.client.IngestLog(ctx, ref, project, req); err != nil {
				return err
			}
			res.Points += len(batch)
			batch, batchBytes, pendingConfig = nil, 0, nil
		}
		if cursor != st.SyncedLines {
			st.SyncedLines = cursor
			if err := writeSyncState(dir, st); err != nil {
				return err
			}
			res.SyncedLines = cursor
		}
		return nil
	}

	for i := st.SyncedLines; i < end; i++ {
		rec := recs[i]
		switch rec.Type {
		case "init":
			if i > 0 {
				warnOnce("init-again", fmt.Sprintf("ignoring a second init record at line %d", i+1))
				continue
			}
			// Not delivered yet (the cursor is still on it): its config
			// travels with the first call.
			if rec.Config != nil {
				pendingConfig = mergeConfig(st.BaseConfig, rec.Config)
			}
		case "log":
			pts := convertOfflinePoints(rec.Points, warnOnce)
			recBytes := 0
			for _, p := range pts {
				recBytes += estimatePointBytes(p)
			}
			// Everything before line i is whole in the batch: send it first
			// if this record would push the call over a limit.
			if len(batch) > 0 && (len(batch)+len(pts) > hub.IngestMaxPoints || batchBytes+recBytes > ingestBatchBytes) {
				if err := flush(i); err != nil {
					return fail(err)
				}
			}
			if rec.Config != nil {
				pendingConfig = mergeConfig(st.BaseConfig, rec.Config)
			}
			chunks := splitIngestBatches(pts)
			// A record too big for one call on its own goes out in pieces;
			// the cursor only moves past it with the last one.
			for len(chunks) > 1 {
				batch = chunks[0]
				chunks = chunks[1:]
				if err := flush(i); err != nil {
					return fail(err)
				}
			}
			if len(chunks) == 1 {
				batch = append(batch, chunks[0]...)
				for _, p := range chunks[0] {
					batchBytes += estimatePointBytes(p)
				}
			}
		case "artifact", "model", "":
			// Collected from the whole file at finish; a blank line is inert.
		default:
			warnOnce("type:"+rec.Type, fmt.Sprintf("ignoring unknown record type %q", rec.Type))
		}
	}

	// Deliver what there is. Without a finish record the run is left open;
	// a run that logged nothing still gets its init config here, as a
	// config-only call.
	if err := flush(end); err != nil {
		return fail(err)
	}
	if finishAt < 0 {
		return res
	}

	// The finish sequence. Each step is safe to repeat, which is what a crash
	// before the state write below leads to: artifacts whose content is
	// already committed are skipped, /finish and the model list are
	// idempotent.
	arts := s.offlineArtifacts(dir, recs, finishAt)
	if len(arts) > 0 {
		ar, err := s.client.UploadRunArtifacts(ctx, ref, project, st.Run, arts, nil)
		if err != nil {
			return fail(fmt.Errorf("commit artifacts: %w", err))
		}
		res.ArtifactsUploaded, res.ArtifactsSkipped = len(ar.Uploaded), len(ar.Skipped)
	}
	if err := s.client.IngestFinish(ctx, ref, project, hub.IngestFinishRequest{
		Run: st.Run, Status: status, Group: group, JobType: jobType,
	}); err != nil {
		return fail(fmt.Errorf("finish: %w", err))
	}
	// After /finish, which guarantees the run row exists even for a run that
	// logged no points (the same order the shim uses).
	if models := offlineModels(recs, finishAt); len(models) > 0 {
		if err := s.client.IngestSetRunModels(ctx, ref, project, st.Run, models); err != nil {
			return fail(fmt.Errorf("record produced models: %w", err))
		}
	}
	st.SyncedLines, st.Done = finishAt+1, true
	if err := writeSyncState(dir, st); err != nil {
		return fail(err)
	}
	res.SyncedLines, res.Done, res.Status = st.SyncedLines, true, status
	return res
}

// findRunDirs expands the DIR arguments into run directories: an argument
// holding run.jsonl is one; otherwise each of its subdirectories that holds
// one, sorted by name. A missing default directory is simply empty.
func findRunDirs(args []string, defaulted bool) (dirs []string, errs []syncDirResult) {
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			if defaulted && errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, syncDirResult{Dir: arg, Error: err.Error()})
			continue
		}
		if !fi.IsDir() {
			errs = append(errs, syncDirResult{Dir: arg, Error: "not a directory"})
			continue
		}
		if isFile(filepath.Join(arg, runJSONLName)) {
			add(arg)
			continue
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			errs = append(errs, syncDirResult{Dir: arg, Error: err.Error()})
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() && isFile(filepath.Join(arg, e.Name(), runJSONLName)) {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(arg, n))
		}
	}
	return dirs, errs
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// syncPass is one pass over every run directory. Errors in one directory do
// not stop the others.
func (s *offlineSyncer) syncPass(ctx context.Context, args []string, defaulted bool) []syncDirResult {
	dirs, results := findRunDirs(args, defaulted)
	for _, d := range dirs {
		if ctx.Err() != nil {
			results = append(results, syncDirResult{Dir: d, Error: "interrupted"})
			continue
		}
		results = append(results, s.syncDir(ctx, d))
	}
	return results
}

func syncFailed(results []syncDirResult) bool {
	for _, r := range results {
		if r.Error != "" {
			return true
		}
	}
	return false
}

// printSyncResults reports a pass. quietDone leaves out directories that were
// already done before the pass (every --watch pass after the first).
func printSyncResults(stdout, stderr io.Writer, results []syncDirResult, jsonOut, quietDone bool) {
	if jsonOut {
		out := struct {
			Runs []syncDirResult `json:"runs"`
		}{Runs: results}
		if out.Runs == nil {
			out.Runs = []syncDirResult{}
		}
		_ = json.NewEncoder(stdout).Encode(&out)
		for _, r := range results {
			if r.Error != "" {
				fmt.Fprintf(stderr, "tf: sync %s: %s\n", r.Dir, r.Error)
			}
		}
		return
	}
	for _, r := range results {
		switch {
		case r.Error != "":
			fmt.Fprintf(stderr, "tf: sync %s: %s\n", r.Dir, r.Error)
		case r.AlreadyDone:
			if !quietDone {
				fmt.Fprintf(stdout, "%s: already synced to %s (%s)\n", r.Dir, r.Repo, r.Run)
			}
		case r.Run == "":
			fmt.Fprintf(stdout, "%s: nothing to sync yet\n", r.Dir)
		default:
			state := "open, waiting for more"
			if r.Done {
				state = "done, " + r.Status
			}
			extra := ""
			if r.ArtifactsUploaded+r.ArtifactsSkipped > 0 {
				extra = fmt.Sprintf(", %d artifact(s) committed, %d already up to date", r.ArtifactsUploaded, r.ArtifactsSkipped)
			}
			fmt.Fprintf(stdout, "%s -> %s %s/%s: sent %d point(s)%s (%s)\n",
				r.Dir, r.Repo, r.Project, r.Run, r.Points, extra, state)
		}
	}
}

func runExperimentsSync(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("experiments sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	var (
		watch, jsonOut bool
		interval       time.Duration
	)
	fs.BoolVar(&watch, "watch", false, "keep syncing until interrupted")
	fs.DurationVar(&interval, "interval", 60*time.Second, "pause between --watch passes")
	fs.BoolVar(&jsonOut, "json", false, "print each pass as JSON")

	if hasHelpFlag(args) {
		fmt.Fprint(stdout, experimentsSyncUsage)
		return exitOK
	}
	dirs, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "tf: %s\n", err)
		fmt.Fprint(stderr, experimentsSyncUsage)
		return exitUsage
	}
	if interval <= 0 {
		fmt.Fprintln(stderr, "tf: --interval must be positive")
		return exitUsage
	}
	defaulted := len(dirs) == 0
	if defaulted {
		dirs = []string{defaultOfflineDir}
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
	s := newOfflineSyncer(client, stderr)

	if !watch {
		results := s.syncPass(ctx, dirs, defaulted)
		if len(results) == 0 && !jsonOut {
			fmt.Fprintf(stdout, "no offline runs found in %s\n", strings.Join(dirs, ", "))
		}
		printSyncResults(stdout, stderr, results, jsonOut, false)
		if syncFailed(results) {
			return exitError
		}
		return exitOK
	}

	failed := false
	for pass := 0; ; pass++ {
		results := s.syncPass(ctx, dirs, defaulted)
		if ctx.Err() != nil {
			// Interrupted mid-pass. Every state file already reflects exactly
			// what the server acknowledged, so stopping here loses nothing.
			break
		}
		failed = syncFailed(results)
		printSyncResults(stdout, stderr, results, jsonOut, pass > 0)
		select {
		case <-ctx.Done():
		case <-time.After(interval):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if failed {
		return exitError
	}
	return exitOK
}

// syncErrorText renders a failure for the result: the usual `tf login` hint
// for a 401, the full error for anything but a 403 (hub errors already name
// the failing call). A 403 names the repository the run was resolved to --
// with "repo": null that is {you}/trackio-metrics, which a token restricted
// to other repositories cannot write, and the user never typed that name --
// and, for such a run, points at THINKINGFACE_REPO.
func syncErrorText(err error, endpoint, repo string, defaultRepo bool) string {
	if hub.IsForbidden(err) && repo != "" {
		msg := "you do not have write access to " + repo
		var herr *hub.Error
		if errors.As(err, &herr) && herr.Message != "" {
			msg += " (" + herr.Message + ")"
		}
		if defaultRepo {
			msg += "; the run was recorded without a repository, so it syncs to " + repo +
				" -- set THINKINGFACE_REPO=ns/name when recording to choose one this token can write"
		}
		return msg
	}
	if hub.IsUnauthorized(err) || hub.IsForbidden(err) {
		return describeHubError(err, endpoint, "")
	}
	return err.Error()
}
