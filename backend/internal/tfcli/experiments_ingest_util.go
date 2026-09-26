package tfcli

// Helpers shared by `tf experiments import` and `tf experiments sync`: the
// client-side mirror of the server's ingest validation, batching of points
// into /log calls, and the experiment repository argument.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

// ingestBatchBytes bounds the estimated JSON size of one /log call. The server
// accepts 32 MiB (maxIngestBody); a wide run -- hundreds of metrics per point
// -- would pass that long before 10 000 points, so batches are cut by size as
// well as by count, with ample headroom for the estimate being rough.
const ingestBatchBytes = 8 << 20

// validateIngestNameLocal mirrors the server's validateIngestName
// (internal/api/experiments_ingest.go): non-empty, at most 256 bytes, valid
// UTF-8, no control characters. Checking here means a bad name fails the
// command before anything is sent rather than half-way through.
func validateIngestNameLocal(name string) error {
	switch {
	case name == "":
		return errors.New("is empty")
	case len(name) > hub.IngestMaxNameBytes:
		return fmt.Errorf("is longer than %d bytes", hub.IngestMaxNameBytes)
	case !utf8.ValidString(name):
		return errors.New("is not valid UTF-8")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("contains a control character")
		}
	}
	return nil
}

// structuralMetricNames are the column names the server refuses as metric
// names because the metrics parquet uses them to describe the row
// (experiments.IsStructuralColumn, internal/experiments/layout.go). Copied
// rather than imported so the tf binary does not pull in the parquet stack.
var structuralMetricNames = map[string]bool{
	"id": true, "log_id": true, "space_id": true, "run_id": true,
	"run_name": true, "run": true, "step": true, "_step": true,
	"timestamp": true, "_timestamp": true, "created_at": true,
	"project": true, "global_step": true, "_ingest_id": true,
}

// isStructuralMetricName reports whether the server would refuse name as a
// metric because it names a structural parquet column.
func isStructuralMetricName(name string) bool { return structuralMetricNames[name] }

// estimatePointBytes is a generous estimate of one point's JSON encoding.
func estimatePointBytes(p hub.IngestPoint) int {
	n := 64 + len(p.Timestamp)
	for k := range p.Metrics {
		n += len(k) + 32
	}
	return n
}

// splitIngestBatches cuts points into /log-sized batches: at most
// hub.IngestMaxPoints points and about ingestBatchBytes each, order preserved.
func splitIngestBatches(points []hub.IngestPoint) [][]hub.IngestPoint {
	var out [][]hub.IngestPoint
	start, size := 0, 0
	for i, p := range points {
		b := estimatePointBytes(p)
		if i > start && (i-start >= hub.IngestMaxPoints || size+b > ingestBatchBytes) {
			out = append(out, points[start:i])
			start, size = i, 0
		}
		size += b
	}
	if start < len(points) {
		out = append(out, points[start:])
	}
	return out
}

// parseExpRepoArg parses the REPO argument of the experiments commands:
// "ns/name", optionally prefixed "datasets/". Experiments live in dataset
// repositories only.
func parseExpRepoArg(s string) (hub.Ref, error) {
	s = strings.Trim(strings.TrimSpace(s), "/")
	s = strings.TrimPrefix(s, "datasets/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return hub.Ref{}, fmt.Errorf("repository must be NS/NAME, got %q", s)
	}
	return hub.Ref{Kind: hub.KindDataset, Namespace: parts[0], Name: parts[1]}, nil
}

// ensureExpRepo makes sure the dataset repository exists, creating it when it
// does not (the ingest endpoints refuse to write into a missing repository).
// With dryRun nothing is created; exists reports what was found.
func ensureExpRepo(ctx context.Context, client *hub.Client, ref hub.Ref, dryRun bool) (exists, created bool, err error) {
	exists, err = client.RepoExists(ctx, ref)
	if err != nil || exists || dryRun {
		return exists, false, err
	}
	created, err = client.CreateRepo(ctx, ref)
	if err != nil {
		return false, false, err
	}
	return true, created, nil
}
