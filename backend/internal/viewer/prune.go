package viewer

import (
	"bytes"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// Predicate restricts one top-level column so Scan can skip whole row groups
// whose statistics prove they hold no matching row.
//
// A predicate is a *hint*, never a filter: Scan still hands every row of a
// row group it decided to read to the callback, including rows the predicate
// would reject. Callers must apply their own per-row test -- what a predicate
// buys is not having to decode the pages of row groups that cannot possibly
// contribute.
//
// The zero value matches everything. Set AnyOf for a string column, Min/Max
// for an integer one; setting both is allowed but only the restriction that
// matches the column's physical type is used.
type Predicate struct {
	// Column is the top-level column name the restriction applies to. A
	// column that does not exist in the file simply disables the predicate.
	Column string

	// AnyOf accepts rows whose Column equals one of these strings. Only used
	// for BYTE_ARRAY / FIXED_LEN_BYTE_ARRAY columns. An empty slice means
	// "no string restriction"; use a non-nil empty slice at your own risk --
	// it is treated as no restriction rather than "match nothing", so that a
	// caller that forgot to populate it does not silently lose data.
	AnyOf []string

	// Min and Max bound an integer column inclusively (nil = unbounded).
	// Only used for INT32 / INT64 columns.
	Min *int64
	Max *int64
}

// empty reports whether p restricts nothing.
func (p Predicate) empty() bool {
	return p.Column == "" || (len(p.AnyOf) == 0 && p.Min == nil && p.Max == nil)
}

// resolvedPredicate is a Predicate bound to one file: the leaf column it
// applies to has been located, and its accepted values converted into the
// parquet values the column's statistics are compared against.
type resolvedPredicate struct {
	chunkIndex int
	kind       parquet.Kind
	typ        parquet.Type
	unsigned   bool     // INT(32,false)/INT(64,false), incl. legacy UINT_8/16/32/64
	anyOf      [][]byte // sorted-irrelevant; compared against [min,max]
	min, max   *int64
}

// isUnsignedInt reports whether typ carries the INT logical type with
// IsSigned false -- what parquet-go maps both a genuine INT(n,false)
// annotation and the legacy UINT_8/16/32/64 converted types onto (see
// intType.LogicalType in parquet-go). Everything else, including a plain
// INT32/INT64 with no annotation, is treated as signed.
func isUnsignedInt(typ parquet.Type) bool {
	lt := typ.LogicalType()
	if lt == nil {
		return false
	}
	it, ok := lt.Value.(*format.IntType)
	return ok && !it.IsSigned
}

// resolvePredicates binds preds to pf's schema, dropping the ones that cannot
// prune this particular file (unknown column, non-leaf column, or a
// restriction that does not fit the column's physical type). Returning fewer
// predicates than it was given is the normal fallback: a file whose columns
// or statistics we cannot reason about is simply read in full.
func resolvePredicates(pf *parquet.File, preds []Predicate) []resolvedPredicate {
	if len(preds) == 0 {
		return nil
	}
	root := pf.Root()
	var out []resolvedPredicate
	for _, p := range preds {
		if p.empty() {
			continue
		}
		col := root.Column(p.Column)
		if col == nil || !col.Leaf() || col.Index() < 0 {
			continue
		}
		typ := col.Type()
		rp := resolvedPredicate{chunkIndex: col.Index(), kind: typ.Kind(), typ: typ}
		switch rp.kind {
		case parquet.ByteArray, parquet.FixedLenByteArray:
			if len(p.AnyOf) == 0 {
				continue
			}
			for _, v := range p.AnyOf {
				rp.anyOf = append(rp.anyOf, []byte(v))
			}
		case parquet.Int32, parquet.Int64:
			if p.Min == nil && p.Max == nil {
				continue
			}
			rp.min, rp.max = p.Min, p.Max
			rp.unsigned = isUnsignedInt(typ)
		default:
			continue
		}
		out = append(out, rp)
	}
	return out
}

// keepRowGroup reports whether rg has to be read. It answers true whenever it
// cannot prove otherwise -- a chunk without a column index (parquet files
// written before the page index existed, or with it skipped), a statistic
// whose type it does not understand -- so pruning can only ever remove work,
// never rows.
func keepRowGroup(rg parquet.RowGroup, preds []resolvedPredicate) bool {
	if len(preds) == 0 {
		return true
	}
	chunks := rg.ColumnChunks()
	for _, p := range preds {
		if p.chunkIndex >= len(chunks) {
			return true
		}
		lo, hi, ok := chunkBounds(chunks[p.chunkIndex], p.typ)
		if !ok {
			// No usable statistics: either the column index is missing, or
			// every page in this group is null. A group that is entirely
			// null cannot match a value predicate, but distinguishing the
			// two is not worth a wrong answer -- read it.
			return true
		}
		if !p.overlaps(lo, hi) {
			return false
		}
	}
	return true
}

// chunkBounds folds a column chunk's per-page min/max statistics into one
// [min, max] range for the whole chunk. It reports false when the chunk has
// no column index, or when every one of its pages is null.
func chunkBounds(chunk parquet.ColumnChunk, typ parquet.Type) (lo, hi parquet.Value, ok bool) {
	index, err := chunk.ColumnIndex()
	if err != nil || index == nil {
		return lo, hi, false
	}
	for i := range index.NumPages() {
		if index.NullPage(i) {
			continue
		}
		pageMin, pageMax := index.MinValue(i), index.MaxValue(i)
		if pageMin.IsNull() || pageMax.IsNull() {
			continue
		}
		if !ok {
			lo, hi, ok = pageMin, pageMax, true
			continue
		}
		if typ.Compare(pageMin, lo) < 0 {
			lo = pageMin
		}
		if typ.Compare(pageMax, hi) > 0 {
			hi = pageMax
		}
	}
	return lo, hi, ok
}

// overlaps reports whether the predicate can be satisfied by some value in
// [lo, hi].
func (p resolvedPredicate) overlaps(lo, hi parquet.Value) bool {
	switch p.kind {
	case parquet.ByteArray, parquet.FixedLenByteArray:
		loB, hiB := lo.ByteArray(), hi.ByteArray()
		for _, want := range p.anyOf {
			if bytes.Compare(want, loB) >= 0 && bytes.Compare(want, hiB) <= 0 {
				return true
			}
		}
		return false
	case parquet.Int32:
		if p.unsigned {
			return uintRangeOverlaps(uint64(uint32(lo.Int32())), uint64(uint32(hi.Int32())), p.min, p.max)
		}
		return intRangeOverlaps(int64(lo.Int32()), int64(hi.Int32()), p.min, p.max)
	case parquet.Int64:
		if p.unsigned {
			return uintRangeOverlaps(uint64(lo.Int64()), uint64(hi.Int64()), p.min, p.max)
		}
		return intRangeOverlaps(lo.Int64(), hi.Int64(), p.min, p.max)
	default:
		return true
	}
}

// uintRangeOverlaps is intRangeOverlaps for a column whose logical type is
// unsigned. lo/hi come from the row group's statistics reinterpreted as
// unsigned (see the overlaps cases above); min/max are the predicate's bounds,
// still *int64 as the Predicate type declares them. A negative bound can never
// match an unsigned column: Max<0 rejects every row, Min<0 restricts nothing
// (every unsigned value already satisfies "at least a negative number").
func uintRangeOverlaps(lo, hi uint64, min, max *int64) bool {
	if max != nil {
		if *max < 0 || lo > uint64(*max) {
			return false
		}
	}
	if min != nil && *min > 0 && hi < uint64(*min) {
		return false
	}
	return true
}

func intRangeOverlaps(lo, hi int64, min, max *int64) bool {
	if max != nil && lo > *max {
		return false
	}
	if min != nil && hi < *min {
		return false
	}
	return true
}
