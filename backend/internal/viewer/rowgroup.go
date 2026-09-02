package viewer

import (
	"errors"
	"fmt"
	"io"

	"github.com/parquet-go/parquet-go"
)

// rowPlan resolves which top-level columns to read from a parquet file and
// how to read them efficiently.
//
// When every selected column is a non-repeated leaf ("fast" path), rows are
// read straight off the leaf columns' pages: parquet-go's page decoder
// already emits exactly one Value per row for such columns, so no
// definition/repetition-level bookkeeping is needed and only the requested
// columns' pages are ever decoded.
//
// Otherwise (a selected column is a nested group, LIST, MAP, or a legacy
// repeated leaf) rowPlan falls back to parquet-go's generic, reflection
// based row reconstruction for the whole row, then extracts and normalizes
// the requested columns. This path is correct but reads every column's
// pages; it is not expected to be exercised by typical flat datasets.
type rowPlan struct {
	columns []Column // output column metadata, in the caller's requested order (or file order)
	names   []string // selected top-level field names, same order as columns
	fast    bool

	// Fast-path only. Group.Fields() (used to build projSchema) sorts
	// fields alphabetically, so sortedNames/sortedCols is the order rows
	// will actually be produced in -- which need not match names/columns.
	// Row output is a map, so this reordering is invisible to callers.
	projSchema  *parquet.Schema
	sortedNames []string
	sortedCols  []*parquet.Column

	// Fallback-path only.
	fullSchema *parquet.Schema
}

// newRowPlan resolves want (top-level column names, or nil/empty for all
// columns) against pf's schema.
func newRowPlan(pf *parquet.File, want []string) (*rowPlan, error) {
	root := pf.Root()
	fileFields := pf.Schema().Fields()

	byName := make(map[string]parquet.Field, len(fileFields))
	for _, f := range fileFields {
		byName[f.Name()] = f
	}

	var selected []string
	if len(want) == 0 {
		selected = make([]string, len(fileFields))
		for i, f := range fileFields {
			selected[i] = f.Name()
		}
	} else {
		seen := make(map[string]bool, len(want))
		for _, name := range want {
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, ok := byName[name]; !ok {
				return nil, fmt.Errorf("viewer: unknown column %q", name)
			}
			selected = append(selected, name)
		}
	}

	features := parquetFeatureHints(pf)
	cols := make([]Column, len(selected))
	fast := true
	for i, name := range selected {
		f := byName[name]
		cols[i] = columnMeta(f, features)
		if !f.Leaf() || f.Repeated() {
			fast = false
		}
	}

	plan := &rowPlan{
		columns:    cols,
		names:      selected,
		fast:       fast,
		fullSchema: pf.Schema(),
	}

	if fast {
		nodeMap := parquet.Group{}
		colByName := make(map[string]*parquet.Column, len(selected))
		for _, name := range selected {
			col := root.Column(name)
			nodeMap[name] = col
			colByName[name] = col
		}
		plan.projSchema = parquet.NewSchema("row", nodeMap)
		fields := plan.projSchema.Fields()
		plan.sortedNames = make([]string, len(fields))
		plan.sortedCols = make([]*parquet.Column, len(fields))
		for i, f := range fields {
			plan.sortedNames[i] = f.Name()
			plan.sortedCols[i] = colByName[f.Name()]
		}
	}

	return plan, nil
}

// rowsFor returns a row reader scoped to rg. Callers must Close it.
func (p *rowPlan) rowsFor(rg parquet.RowGroup) (parquet.Rows, error) {
	if !p.fast {
		return rg.Rows(), nil
	}

	allChunks := rg.ColumnChunks()
	chunks := make([]parquet.ColumnChunk, len(p.sortedCols))
	for i, col := range p.sortedCols {
		idx := col.Index()
		if idx < 0 || idx >= len(allChunks) {
			return nil, fmt.Errorf("viewer: column %q has no chunk in row group", p.sortedNames[i])
		}
		chunks[i] = allChunks[idx]
	}

	proj := &projectedRowGroup{
		numRows: rg.NumRows(),
		schema:  p.projSchema,
		chunks:  chunks,
	}
	return proj.Rows(), nil
}

// readRange reads take rows out of rg, starting skip rows in, converting each
// one and handing it to emit. It returns how many rows were emitted.
//
// It owns the row reader's lifetime so that callers do not each have to
// repeat the close dance: the reader is always closed, but a close failure is
// only reported when the read itself succeeded -- surfacing it over a real
// read error would replace the useful diagnosis with the cleanup's.
func (p *rowPlan) readRange(rg parquet.RowGroup, skip, take int64, emit func(map[string]any) error) (int64, error) {
	rows, err := p.rowsFor(rg)
	if err != nil {
		return 0, err
	}
	got, err := readGroupRows(rows, p, skip, take, emit)
	closeErr := rows.Close()
	if err != nil {
		return got, err
	}
	if closeErr != nil {
		return got, fmt.Errorf("viewer: close row reader: %w", closeErr)
	}
	return got, nil
}

// convertRow turns a raw parquet.Row read from rowsFor's reader into a
// JSON-safe map keyed by column name.
func (p *rowPlan) convertRow(row parquet.Row) (map[string]any, error) {
	if p.fast {
		m := make(map[string]any, len(p.sortedNames))
		for i, name := range p.sortedNames {
			if i >= len(row) {
				m[name] = nil
				continue
			}
			m[name] = convertLeafValue(p.sortedCols[i].Type(), row[i])
		}
		return m, nil
	}

	raw := map[string]any{}
	if err := p.fullSchema.Reconstruct(&raw, row); err != nil {
		return nil, fmt.Errorf("viewer: reconstruct row: %w", err)
	}
	out := make(map[string]any, len(p.names))
	for _, name := range p.names {
		out[name] = normalizeGeneric(raw[name])
	}
	return out, nil
}

// projectedRowGroup is a parquet.RowGroup restricted to a subset of the
// underlying row group's column chunks, so that reading its Rows() only
// decodes the pages of the selected columns.
type projectedRowGroup struct {
	numRows int64
	schema  *parquet.Schema
	chunks  []parquet.ColumnChunk
}

func (g *projectedRowGroup) NumRows() int64                          { return g.numRows }
func (g *projectedRowGroup) ColumnChunks() []parquet.ColumnChunk     { return g.chunks }
func (g *projectedRowGroup) Schema() *parquet.Schema                 { return g.schema }
func (g *projectedRowGroup) SortingColumns() []parquet.SortingColumn { return nil }
func (g *projectedRowGroup) Rows() parquet.Rows                      { return parquet.NewRowGroupRowReader(g) }

// readGroupRows seeks rows to skip, reads up to take rows in small batches,
// converts each one, and passes it to emit. It returns the number of rows
// actually emitted before running out of rows or hitting an error.
func readGroupRows(rows parquet.Rows, plan *rowPlan, skip, take int64, emit func(map[string]any) error) (int64, error) {
	if take <= 0 {
		return 0, nil
	}
	if err := rows.SeekToRow(skip); err != nil {
		return 0, fmt.Errorf("viewer: seek to row %d: %w", skip, err)
	}

	const batchSize int64 = 256
	bufLen := batchSize
	if take < bufLen {
		bufLen = take
	}
	buf := make([]parquet.Row, bufLen)

	var total int64
	for total < take {
		want := take - total
		if want > batchSize {
			want = batchSize
		}

		n, readErr := rows.ReadRows(buf[:want])
		for i := int64(0); i < int64(n); i++ {
			m, err := plan.convertRow(buf[i])
			if err != nil {
				return total, err
			}
			if err := emit(m); err != nil {
				return total, err
			}
			total++
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, fmt.Errorf("viewer: read rows: %w", readErr)
		}
		if n == 0 {
			return total, nil
		}
	}
	return total, nil
}
