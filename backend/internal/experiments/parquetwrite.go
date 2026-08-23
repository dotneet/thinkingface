package experiments

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// IngestIDColumn carries the exp_points row id of a point that reached the
// parquet through the native ingest API (route B, docs/dev/thinkingface-design.md
// §8). It is what makes a flush idempotent: the process can die between
// writing the commit and deleting the rows, and the next attempt recognises
// the points it already wrote instead of appending them twice.
//
// Rows written by trackio's own export (route A) simply leave it null, and
// every reader ignores it -- it is listed in structuralColumns, so it is
// never mistaken for a metric.
const IngestIDColumn = "_ingest_id"

// colKind is the value shape one column of the metrics file holds. It is kept
// separate from the parquet node so an existing file's exact annotation can be
// preserved while the encoder still only has a handful of cases to cover.
type colKind int

const (
	colString colKind = iota
	colInt32
	colInt64
	colFloat
	colDouble
	colBool
	colTimestamp
)

// flushColumn is one column of the metrics parquet being written.
type flushColumn struct {
	name     string
	kind     colKind
	unit     parquet.TimeUnit // colTimestamp only
	node     parquet.Node     // leaf node, before the Optional() wrapper
	optional bool
}

// unsupportedColumnError reports a column in an existing metrics.parquet whose
// shape this package cannot reproduce. A flush that hits one is abandoned
// rather than rewriting the file into something different from what its author
// pushed; the points stay in the database and the operator sees the warning.
type unsupportedColumnError struct {
	Column string
	Reason string
}

func (e *unsupportedColumnError) Error() string {
	return fmt.Sprintf("metrics parquet column %q cannot be rewritten: %s", e.Column, e.Reason)
}

// columnFromSchema maps a column of an existing metrics.parquet onto the
// description needed to write it out again.
func columnFromSchema(c viewer.Column) (flushColumn, error) {
	out := flushColumn{name: c.Name, optional: c.Optional}
	switch {
	case c.Repeated:
		return out, &unsupportedColumnError{c.Name, "repeated columns are not supported"}
	case c.Type == "GROUP":
		return out, &unsupportedColumnError{c.Name, "nested groups are not supported"}
	}

	logical := c.LogicalType
	switch {
	case logical == "STRING" || logical == "ENUM" || logical == "JSON":
		out.kind, out.node = colString, parquet.String()
		return out, nil
	case strings.HasPrefix(logical, "TIMESTAMP("):
		unit := timeUnitFromLogical(logical)
		out.kind, out.unit, out.node = colTimestamp, unit, parquet.Timestamp(unit)
		return out, nil
	case strings.HasPrefix(logical, "INT("):
		// INT(8|16|32,…) still lives in an INT32 column; only the annotation
		// differs, and reproducing the annotation verbatim is not worth a
		// case per width. The physical type below decides the storage.
	case logical != "":
		return out, &unsupportedColumnError{c.Name, "logical type " + logical + " is not supported"}
	}

	switch c.Type {
	case "BOOLEAN":
		out.kind, out.node = colBool, parquet.Leaf(parquet.BooleanType)
	case "INT32":
		out.kind, out.node = colInt32, parquet.Leaf(parquet.Int32Type)
	case "INT64":
		out.kind, out.node = colInt64, parquet.Leaf(parquet.Int64Type)
	case "FLOAT":
		out.kind, out.node = colFloat, parquet.Leaf(parquet.FloatType)
	case "DOUBLE":
		out.kind, out.node = colDouble, parquet.Leaf(parquet.DoubleType)
	case "BYTE_ARRAY":
		// No STRING annotation: the viewer hands these back base64-encoded,
		// so writing them again would change their bytes.
		return out, &unsupportedColumnError{c.Name, "unannotated BYTE_ARRAY is not supported"}
	default:
		return out, &unsupportedColumnError{c.Name, "physical type " + c.Type + " is not supported"}
	}
	return out, nil
}

// timeUnitFromLogical reads the unit out of the viewer's "TIMESTAMP(MILLIS)"
// rendering, defaulting to microseconds the way convertLeafValue does.
func timeUnitFromLogical(logical string) parquet.TimeUnit {
	switch {
	case strings.Contains(logical, "MILLIS"):
		return parquet.Millisecond
	case strings.Contains(logical, "NANOS"):
		return parquet.Nanosecond
	default:
		return parquet.Microsecond
	}
}

// stringColumn / int64Column / doubleColumn / timestampColumn describe the
// columns this package creates when a metrics file (or one of its columns)
// does not exist yet. Everything except run_name is optional, because a point
// only carries the metrics its logger passed on that step.
func stringColumn(name string, optional bool) flushColumn {
	return flushColumn{name: name, kind: colString, node: parquet.String(), optional: optional}
}

func int64Column(name string) flushColumn {
	return flushColumn{name: name, kind: colInt64, node: parquet.Leaf(parquet.Int64Type), optional: true}
}

func doubleColumn(name string) flushColumn {
	return flushColumn{name: name, kind: colDouble, node: parquet.Leaf(parquet.DoubleType), optional: true}
}

func timestampColumn(name string) flushColumn {
	return flushColumn{
		name: name, kind: colTimestamp, unit: parquet.Millisecond,
		node: parquet.Timestamp(parquet.Millisecond), optional: true,
	}
}

// encode converts one cell into the parquet value for this column. It reports
// false for a value the column cannot hold, which the writer turns into a null
// -- dropping one cell is always better than failing a whole flush over a
// value some other writer put in the file.
func (c flushColumn) encode(v any) (parquet.Value, bool) {
	if v == nil {
		return parquet.Value{}, false
	}
	switch c.kind {
	case colString:
		return parquet.ByteArrayValue([]byte(cellString(v))), true
	case colBool:
		switch t := v.(type) {
		case bool:
			return parquet.BooleanValue(t), true
		default:
			if f, ok := toFloat(v); ok {
				return parquet.BooleanValue(f != 0), true
			}
		}
	case colInt32:
		if n, ok := toInt(v); ok {
			return parquet.Int32Value(int32(n)), true
		}
	case colInt64:
		if n, ok := toInt(v); ok {
			return parquet.Int64Value(n), true
		}
	case colFloat:
		if f, ok := toFloat(v); ok {
			return parquet.FloatValue(float32(f)), true
		}
	case colDouble:
		if f, ok := toFloat(v); ok {
			return parquet.DoubleValue(f), true
		}
	case colTimestamp:
		if ts, ok := toTime(v); ok {
			return parquet.Int64Value(timestampTicks(ts, c.unit)), true
		}
	}
	return parquet.Value{}, false
}

func timestampTicks(ts time.Time, unit parquet.TimeUnit) int64 {
	switch unit {
	case parquet.Millisecond:
		return ts.UnixMilli()
	case parquet.Nanosecond:
		return ts.UnixNano()
	default:
		return ts.UnixMicro()
	}
}

// cellString renders a value for a STRING column. Timestamps use the same
// RFC3339Nano rendering the viewer produces, so a route-A file that keeps its
// timestamps as text stays internally consistent after a flush appends to it.
func cellString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return ""
		}
	}
	return fmt.Sprint(v)
}

// Row-group sizing for the metrics file. Rows are laid out one run at a time
// (see groupRowsByRun) and a row group is cut at a run boundary as soon as it
// holds minRowGroupRows, so a row group's run column covers a narrow,
// contiguous slice of the run names -- which is what lets viewer.Predicate
// throw away the row groups of runs a chart did not ask for
// (docs/dev/thinkingface-design.md §9). maxRowGroupRows keeps one very long run
// from becoming a single unskippable group again, and the minimum keeps a
// project of thousands of tiny runs from turning into thousands of row groups
// worth of footer metadata.
const (
	minRowGroupRows = 8192
	maxRowGroupRows = 65536
)

// rowGroupLayout is how writeMetricsParquet lays rows out across row groups.
// The only production value is defaultRowGroupLayout; the zero value (no run
// grouping, no row-group limit) reproduces the single-row-group shape this
// package wrote before, which is also what a route-A export from trackio
// looks like, and is what the tests and benchmarks compare against.
type rowGroupLayout struct {
	groupByRun bool
	minRows    int
	maxRows    int
}

func defaultRowGroupLayout() rowGroupLayout {
	return rowGroupLayout{groupByRun: true, minRows: minRowGroupRows, maxRows: maxRowGroupRows}
}

// writeMetricsParquet renders rows as a parquet file, laid out for the chart
// reader: rows grouped by run and cut into row groups. See groupRowsByRun for
// why the grouping is safe and minRowGroupRows for what it buys.
func writeMetricsParquet(columns []flushColumn, rows []map[string]any) ([]byte, error) {
	return writeMetricsParquetLaidOut(columns, rows, defaultRowGroupLayout())
}

// writeMetricsParquetLaidOut is writeMetricsParquet with the row-group layout
// spelled out. Columns are sorted by name because parquet.Group orders its
// fields that way, and the row values must be handed to the writer in
// column-index order.
func writeMetricsParquetLaidOut(columns []flushColumn, rows []map[string]any, layout rowGroupLayout) ([]byte, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("write metrics parquet: no columns")
	}
	ordered := append([]flushColumn(nil), columns...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })

	group := parquet.Group{}
	names := make(map[string]bool, len(ordered))
	for _, c := range ordered {
		node := c.node
		if c.optional {
			node = parquet.Optional(node)
		}
		group[c.name] = node
		names[c.name] = true
	}

	cuts := make([]bool, len(rows))
	if layout.groupByRun {
		rows, cuts = groupRowsByRun(runColumn(names), rows)
	}
	if layout.maxRows <= 0 {
		layout.maxRows = len(rows) + 1
	}

	var buf bytes.Buffer
	w := parquet.NewWriter(&buf, parquet.NewSchema("metrics", group), parquet.Compression(&parquet.Snappy))

	batch := make([]parquet.Row, 0, min(len(rows), layout.maxRows))
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := w.WriteRows(batch); err != nil {
			return fmt.Errorf("write metrics parquet rows: %w", err)
		}
		batch = batch[:0]
		// Flush ends the current row group; the next WriteRows starts a new
		// one. An empty writer buffer makes it a no-op, so the last call
		// before Close costs nothing.
		if err := w.Flush(); err != nil {
			return fmt.Errorf("flush metrics parquet row group: %w", err)
		}
		return nil
	}

	for i, row := range rows {
		if (cuts[i] && len(batch) >= layout.minRows) || len(batch) >= layout.maxRows {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		values := make(parquet.Row, len(ordered))
		for j, c := range ordered {
			value, ok := c.encode(row[c.name])
			switch {
			case ok && c.optional:
				values[j] = value.Level(0, 1, j)
			case ok:
				values[j] = value.Level(0, 0, j)
			case c.optional:
				values[j] = parquet.NullValue().Level(0, 0, j)
			default:
				// A required column with no usable value: write the type's
				// zero rather than a null the schema forbids.
				values[j] = zeroValue(c).Level(0, 0, j)
			}
		}
		batch = append(batch, values)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close metrics parquet: %w", err)
	}
	return buf.Bytes(), nil
}

// groupRowsByRun reorders rows so that every run's rows are contiguous, and
// reports which positions start a new run. Runs come out in name order; a row
// whose run cannot be read (no run column, a null, a non-string) is treated as
// one more run named "" and lands first.
//
// **The relative order of one run's rows is preserved exactly.** That is not a
// nicety, it is the correctness condition for three readers, all of which are
// per-run and none of which cares how runs are ordered relative to each other:
//
//   - Series() resolves two values logged at the same step by taking the later
//     one in collection order (docs/dev/thinkingface-design.md §8), which for the
//     parquet half *is* file order.
//   - A file with no usable step column charts rows against their position
//     within their run (series.go's counters).
//   - IndexRepo's per-run summary is the last value it saw for each key.
//
// Sorting *within* a run -- by step, say -- would break the third of those
// (a resumed run's summary would jump from "last logged" to "highest step"),
// so it is deliberately not done: run grouping alone is what the row-group
// pruning needs.
func groupRowsByRun(runCol string, rows []map[string]any) ([]map[string]any, []bool) {
	cuts := make([]bool, len(rows))
	if len(rows) == 0 {
		return rows, cuts
	}
	if runCol == "" {
		cuts[0] = true
		return rows, cuts
	}

	buckets := map[string][]map[string]any{}
	names := make([]string, 0, 16)
	for _, row := range rows {
		name := toString(row[runCol])
		if _, ok := buckets[name]; !ok {
			names = append(names, name)
		}
		buckets[name] = append(buckets[name], row)
	}
	if len(names) == 1 {
		cuts[0] = true
		return rows, cuts
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(rows))
	for _, name := range names {
		cuts[len(out)] = true
		out = append(out, buckets[name]...)
	}
	return out, cuts
}

func zeroValue(c flushColumn) parquet.Value {
	switch c.kind {
	case colString:
		return parquet.ByteArrayValue(nil)
	case colBool:
		return parquet.BooleanValue(false)
	case colInt32:
		return parquet.Int32Value(0)
	case colFloat:
		return parquet.FloatValue(0)
	case colDouble:
		return parquet.DoubleValue(0)
	default: // colInt64, colTimestamp
		return parquet.Int64Value(0)
	}
}
