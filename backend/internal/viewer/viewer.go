// Package viewer reads Parquet files stored behind a storage.Storage
// backend and exposes their schema and row data as plain Go values that are
// always safe to pass to encoding/json.
//
// Reader never downloads an object. It opens each file through an
// io.ReaderAt that turns every read into a ranged GET against the object
// store (objectreader.go), and hands that to
// github.com/parquet-go/parquet-go, a pure-Go Parquet implementation -- no
// cgo or external processes involved. That is not a micro-optimisation: this
// hub serves datasets well past 10 GB, the scratch filesystem on Cloud Run is
// memory-backed, and Schema() is called on every parquet file of every push
// by the syncer. Downloading a file to answer "what are its columns?" meant
// one 10 GB dataset could take the process down without anyone opening a
// browser.
//
// Round trips are what a ranged design has to pay for instead, so they are
// budgeted deliberately: parquet-go is opened with OptimisticRead so the
// footer arrives in one large read rather than a chain of small ones, that
// read is sized to match the object tail Reader has already cached in
// memory (an LRU across keys, so repeated opens of the same file are free),
// and pages are prefetched asynchronously. The page index is deliberately
// *not* skipped -- prune.go reads its min/max statistics.
//
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

	"github.com/parquet-go/parquet-go"

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
// backend, over ranged reads, holding only their metadata in memory.
//
// A Reader is safe for concurrent use by multiple goroutines.
type Reader struct {
	st   storage.Storage
	meta *tailCache
}

// New returns a Reader that reads the parquet objects in st through range
// requests, keeping at most metadataCacheBytes of parquet metadata (object
// tails: footer and page index) on the heap across keys. The bound is a heap
// bound, not a disk one -- nothing is written to the filesystem. A
// metadataCacheBytes <= 0 turns the cache off rather than making it
// unbounded.
func New(st storage.Storage, metadataCacheBytes int64) *Reader {
	return &Reader{st: st, meta: newTailCache(metadataCacheBytes)}
}

// openParquetFile opens key for reading over range requests. The returned
// *parquet.File owns no OS resources, so there is nothing for the caller to
// close; it must not outlive ctx, which every range request it makes is
// issued under.
func (r *Reader) openParquetFile(ctx context.Context, key string) (*parquet.File, error) {
	entry, err := r.meta.load(ctx, r.st, key)
	if err != nil {
		return nil, err
	}

	rd := &objectReader{
		ctx:        ctx,
		st:         r.st,
		key:        key,
		size:       entry.size,
		tailOffset: entry.tailOffset,
		tail:       entry.tail,
	}

	pf, err := parquet.OpenFile(rd, entry.size,
		// Read the footer region in one large read instead of an 8-byte
		// probe followed by a second read, and size that read to the tail
		// already cached in memory so it costs no request at all.
		parquet.OptimisticRead(true),
		parquet.ReadBufferSize(int(footerProbeSize(entry.size))),
		// Prefetch pages in the background: with a network round trip
		// between pages, waiting for each one in turn dominates.
		parquet.FileReadMode(parquet.ReadModeAsync),
		// Nothing here probes bloom filters, and reading their headers costs
		// a seek per column chunk. The page index is *not* skipped: prune.go
		// prunes row groups on the min/max statistics it carries.
		parquet.SkipBloomFilters(true),
		// The header magic was already checked against the cached tail in
		// fetchTail; letting parquet-go re-read it would cost a range request
		// per open, including opens the tail cache would otherwise answer
		// without touching the network at all.
		parquet.SkipMagicBytes(true),
	)
	if err != nil {
		return nil, fmt.Errorf("viewer: open parquet file %s: %w", key, err)
	}

	return pf, nil
}

// Schema returns the schema and file-level metadata of the parquet object
// at key.
func (r *Reader) Schema(ctx context.Context, key string) (*Schema, error) {
	pf, err := r.openParquetFile(ctx, key)
	if err != nil {
		return nil, err
	}

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

	pf, err := r.openParquetFile(ctx, key)
	if err != nil {
		return nil, err
	}

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

		got, err := plan.readRange(rg, groupOffset, groupTake, func(m map[string]any) error {
			out = append(out, m)
			return nil
		})
		if err != nil {
			return nil, err
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
	pf, err := r.openParquetFile(ctx, key)
	if err != nil {
		return err
	}

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

		if _, err := plan.readRange(rg, 0, n, fn); err != nil {
			return err
		}
	}

	return nil
}
