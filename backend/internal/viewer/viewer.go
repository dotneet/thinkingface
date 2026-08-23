// Package viewer reads Parquet files stored behind a storage.Storage
// backend and exposes their schema and row data as plain Go values that are
// always safe to pass to encoding/json.
//
// Reader downloads Parquet files into a local disk cache (an LRU keyed by
// object key) before reading them with github.com/parquet-go/parquet-go, a
// pure-Go Parquet implementation -- no cgo or external processes involved.
// Two kinds of row-group skipping keep callers from paying for data they did
// not ask for. Rows() skips by *position*: row groups entirely outside the
// requested [offset, offset+limit) window are never decoded. Scan() skips by
// *value*: a caller-supplied Predicate is checked against each row group's
// min/max statistics, so a scan for a handful of runs only decodes the row
// groups whose statistics say those runs might be in them. On top of both,
// requested columns are read column-oriented wherever the schema allows it.
package viewer

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/parquet-go/parquet-go"
	"golang.org/x/sync/singleflight"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// Column describes one column of a parquet schema. A column description is
// sent to clients verbatim, so it is declared once in apitypes -- the package
// the TypeScript types are generated from -- and aliased here.
type Column = apitypes.ParquetColumn

// Schema describes a parquet file's top-level columns and file-level
// metadata.
type Schema struct {
	Columns      []Column `json:"columns"`
	NumRows      int64    `json:"num_rows"`
	NumRowGroups int      `json:"num_row_groups"`
	Compression  string   `json:"compression"`
	SizeBytes    int64    `json:"size_bytes"`
}

// Rows is a page of decoded rows from a parquet file.
type Rows struct {
	// Columns describes the columns actually present in Rows, in the order
	// requested by the caller (or file order, when no columns were
	// requested).
	Columns []Column `json:"columns"`
	// Rows holds one JSON-safe map per row, keyed by column name.
	Rows []map[string]any `json:"rows"`
	// NumRows is the total number of rows in the file (not just this page).
	NumRows int64 `json:"num_rows"`
	// Offset is the row offset this page started at.
	Offset int64 `json:"offset"`
}

// Reader reads parquet files referenced by object keys in a storage.Storage
// backend, transparently caching downloaded files on local disk.
//
// A Reader is safe for concurrent use by multiple goroutines.
type Reader struct {
	st            storage.Storage
	cacheDir      string
	maxCacheBytes int64

	group singleflight.Group
	mu    sync.Mutex // guards cache eviction bookkeeping
}

// New returns a Reader that downloads parquet files referenced by st into
// cacheDir (created if necessary), evicting the least-recently-used cache
// entries once the directory's total size would exceed maxCacheBytes. A
// maxCacheBytes <= 0 disables the size limit.
func New(st storage.Storage, cacheDir string, maxCacheBytes int64) *Reader {
	_ = os.MkdirAll(cacheDir, 0o755)
	return &Reader{st: st, cacheDir: cacheDir, maxCacheBytes: maxCacheBytes}
}

// openParquetFile ensures key is cached locally and opens it. The returned
// *os.File must be closed by the caller once done with the *parquet.File
// (which holds it as its backing reader).
func (r *Reader) openParquetFile(ctx context.Context, key string) (*parquet.File, *os.File, error) {
	path, size, err := r.ensureCached(ctx, key)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("viewer: open cache file for %s: %w", key, err)
	}

	pf, err := parquet.OpenFile(f, size)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("viewer: open parquet file %s: %w", key, err)
	}

	return pf, f, nil
}

// Schema returns the schema and file-level metadata of the parquet object
// at key.
func (r *Reader) Schema(ctx context.Context, key string) (*Schema, error) {
	pf, f, err := r.openParquetFile(ctx, key)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := pf.Schema().Fields()
	features := parquetFeatureHints(pf)
	cols := make([]Column, len(fields))
	for i, field := range fields {
		cols[i] = columnMeta(field, features)
	}

	compression := "UNCOMPRESSED"
	if lc := firstLeafColumn(pf.Root()); lc != nil {
		if codec := lc.Compression(); codec != nil {
			compression = codec.String()
		}
	}

	return &Schema{
		Columns:      cols,
		NumRows:      pf.NumRows(),
		NumRowGroups: len(pf.RowGroups()),
		Compression:  compression,
		SizeBytes:    pf.Size(),
	}, nil
}

// Rows reads up to limit rows starting at offset from the parquet object at
// key. When columns is non-empty, only those top-level columns are read;
// otherwise all columns are read. Whole row groups outside [offset,
// offset+limit) are skipped without decoding their pages.
func (r *Reader) Rows(ctx context.Context, key string, offset int64, limit int, columns []string) (*Rows, error) {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}

	pf, f, err := r.openParquetFile(ctx, key)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	plan, err := newRowPlan(pf, columns)
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, limit)
	remainingSkip := offset
	remainingTake := int64(limit)

	for _, rg := range pf.RowGroups() {
		if remainingTake <= 0 {
			break
		}
		n := rg.NumRows()
		if remainingSkip >= n {
			remainingSkip -= n
			continue
		}

		groupOffset := remainingSkip
		groupTake := n - groupOffset
		if groupTake > remainingTake {
			groupTake = remainingTake
		}

		rows, err := plan.rowsFor(rg)
		if err != nil {
			return nil, err
		}
		got, err := readGroupRows(rows, plan, groupOffset, groupTake, func(m map[string]any) error {
			out = append(out, m)
			return nil
		})
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("viewer: close row reader: %w", closeErr)
		}

		remainingTake -= got
		remainingSkip = 0
	}

	return &Rows{
		Columns: plan.columns,
		Rows:    out,
		NumRows: pf.NumRows(),
		Offset:  offset,
	}, nil
}

// ScanRequest narrows what one Scan has to decode.
type ScanRequest struct {
	// Columns lists the top-level columns to read; empty reads every column.
	Columns []string

	// Predicates prune whole row groups whose statistics prove they hold no
	// matching row. They never filter the rows Scan emits -- a row group that
	// survives pruning is delivered in full -- so the callback still has to
	// apply its own per-row test. See Predicate.
	Predicates []Predicate
}

// Scan streams the rows of the parquet object at key to fn, in file order.
// Rows are processed in small batches rather than loaded into memory all at
// once. If fn returns an error, Scan stops and returns that error.
//
// req.Columns restricts which columns are decoded and req.Predicates lets
// whole row groups be skipped on their min/max statistics; both are pure
// optimisations, and a file that supports neither (no page index, unknown
// column) is simply read in full.
func (r *Reader) Scan(ctx context.Context, key string, req ScanRequest, fn func(row map[string]any) error) error {
	pf, f, err := r.openParquetFile(ctx, key)
	if err != nil {
		return err
	}
	defer f.Close()

	plan, err := newRowPlan(pf, req.Columns)
	if err != nil {
		return err
	}
	preds := resolvePredicates(pf, req.Predicates)

	for _, rg := range pf.RowGroups() {
		n := rg.NumRows()
		if n == 0 {
			continue
		}
		if !keepRowGroup(rg, preds) {
			continue
		}

		rows, err := plan.rowsFor(rg)
		if err != nil {
			return err
		}
		_, err = readGroupRows(rows, plan, 0, n, fn)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("viewer: close row reader: %w", closeErr)
		}
	}

	return nil
}
