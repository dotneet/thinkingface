// Package modelmeta reads the *metadata* of model checkpoints -- safetensors
// files and PyTorch `torch.save` archives -- without ever downloading the
// weights.
//
// Both formats keep their structure in a small header at a known place: a
// safetensors file starts with a JSON header, and a PyTorch checkpoint is a
// zip archive whose `data.pkl` member holds the pickled object graph. Reading
// those means a handful of ranged reads over an object that may be hundreds of
// gigabytes, so an inspection costs about as much as opening a small file.
//
// Results are content-addressed (by LFS OID or git blob hash) and cached, so a
// second visit to the same file answers from memory.
package modelmeta

import (
	"context"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// The shapes an inspection produces are sent to clients verbatim, so they are
// declared once in apitypes -- the package the TypeScript types are generated
// from -- and aliased here.
type (
	// Format names a supported checkpoint container.
	Format = apitypes.ModelFormat
	// Tensor is one named tensor in a checkpoint.
	Tensor = apitypes.ModelTensor
	// DTypeStat aggregates the tensors sharing one dtype.
	DTypeStat = apitypes.ModelDTypeStat
	// Info is everything the viewer knows about a checkpoint file.
	Info = apitypes.ModelInfo
)

const (
	FormatSafetensors = apitypes.ModelFormatSafetensors
	FormatPyTorch     = apitypes.ModelFormatPyTorch
)

// maxTensors bounds the tensor list in a response. Totals are always computed
// over every tensor; only the listing is cut off, with Truncated set.
const maxTensors = 4096

// maxTensorNameRunes and maxDTypeRunes bound the two strings a Tensor copies
// out of the file verbatim. Neither has a length of its own: a safetensors
// header names its tensors with the JSON object's keys and keeps an
// unrecognised dtype code as written, and a PyTorch checkpoint builds both out
// of pickled strings -- so a single 64 MiB header can be one tensor whose name
// is 64 MiB, and that name then sits in the inspection cache for as long as
// the entry lives. Real names are a few dozen characters
// ("model.layers.31.self_attn.q_proj.weight" is 39) and real dtype codes fewer
// than ten, so both limits are generous by an order of magnitude.
const (
	maxTensorNameRunes = 128
	maxDTypeRunes      = 32
)

// maxDTypeStats bounds the per-dtype breakdown. There is one bucket per
// distinct dtype, so the file decides how many buckets there are: a header
// naming a different dtype for every tensor would put tens of thousands of
// them in a cached Info. A real checkpoint uses one or two. The totals are
// computed over every tensor either way, so only the breakdown is cut.
const maxDTypeStats = 64

// maxMetadataBytes bounds the embedded metadata an Info carries, counting keys
// and values. Neither container format bounds it on its own: a safetensors
// `__metadata__` block may be as large as the header limit allows (64 MiB), so
// without a ceiling a few hostile files would pin gigabytes in the inspection
// cache, which only counts entries. Real checkpoints carry a few kilobytes.
const maxMetadataBytes = 256 << 10

// maxShapeDims bounds how many dimensions one tensor's shape may carry.
// PyTorch cannot build a tensor with more than 64 dimensions, so this never
// truncates a shape a real checkpoint could hold -- but neither container
// format bounds the list on its own, and a Tensor's Shape was the one
// file-controlled part of an Info with no ceiling at all. A safetensors header
// may be 64 MiB, and `"shape":[1,1,1,...]` fills it with thirty million
// dimensions for a single tensor; the slice built from it then sits in the
// inspection cache for as long as the entry lives.
//
// Both readers apply it while they are building the shape rather than
// afterwards -- headerScanner.scanInts counts the elements past the limit but
// keeps none of them, and torchReducer.toShape stops copying at it -- so an
// oversized shape is never held even briefly, and no truncated slice keeps an
// oversized backing array alive.
const maxShapeDims = 64

// Fetcher returns the bytes in [off, off+n) of the file being inspected.
// Implementations may return fewer bytes only at end of file.
type Fetcher func(ctx context.Context, off, n int64) ([]byte, error)

// FormatFor classifies a repository path by extension, returning "" for
// anything this package cannot read.
func FormatFor(filePath string) Format {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".safetensors":
		return FormatSafetensors
	case ".bin", ".pt", ".pth", ".ckpt":
		return FormatPyTorch
	default:
		return ""
	}
}

// Inspect reads the metadata of a checkpoint of the given format. size is the
// file's real size (the LFS object's size, not the pointer's).
func Inspect(ctx context.Context, format Format, size int64, fetch Fetcher) (*Info, error) {
	switch format {
	case FormatSafetensors:
		return inspectSafetensors(ctx, size, fetch)
	case FormatPyTorch:
		return inspectPyTorch(ctx, size, fetch)
	default:
		return nil, fmt.Errorf("modelmeta: %q is not an inspectable checkpoint format", format)
	}
}

// summarize fills in the aggregate fields of i from a complete tensor list and
// truncates the listing to maxTensors.
//
// It is also where every string a Tensor copies out of the file is cut down to
// its ceiling. Both readers funnel through here, so this is the one place that
// has to hold for the claim DefaultCacheEntries makes about how large a cached
// entry can be.
//
// Info is an alias of a type declared in apitypes, which cannot carry methods
// from this package, so this and warn take their receiver as an argument.
func summarize(i *Info, tensors []Tensor) {
	stats := map[string]*DTypeStat{}
	for idx := range tensors {
		t := &tensors[idx]
		// Both of these come straight out of the file, so they are cut down to
		// their ceilings before anything holds on to them -- including the
		// buckets below, which are keyed by the capped dtype so that two
		// absurdly long codes cannot open two absurdly large buckets.
		t.Name = capString(t.Name, maxTensorNameRunes)
		t.DType = capString(t.DType, maxDTypeRunes)
		// Saturating, for the same reason numElements is: a clamped per-tensor
		// count must not wrap the file's total back around into a small or
		// negative number on the way out.
		i.NumParameters = addSaturating(i.NumParameters, t.NumParameters)
		i.TensorBytes = addSaturating(i.TensorBytes, t.SizeBytes)
		st, ok := stats[t.DType]
		if !ok {
			st = &DTypeStat{DType: t.DType}
			stats[t.DType] = st
		}
		st.NumTensors++
		st.NumParameters = addSaturating(st.NumParameters, t.NumParameters)
		st.SizeBytes = addSaturating(st.SizeBytes, t.SizeBytes)
	}
	i.NumTensors = len(tensors)

	i.DTypes = make([]DTypeStat, 0, len(stats))
	for _, st := range stats {
		i.DTypes = append(i.DTypes, *st)
	}
	// Biggest contributor first; ties by name so the order is stable.
	sort.Slice(i.DTypes, func(a, b int) bool {
		if i.DTypes[a].NumParameters != i.DTypes[b].NumParameters {
			return i.DTypes[a].NumParameters > i.DTypes[b].NumParameters
		}
		return i.DTypes[a].DType < i.DTypes[b].DType
	})
	if len(i.DTypes) > maxDTypeStats {
		warn(i, "checkpoint declares %d distinct dtypes; only the %d largest are broken out",
			len(i.DTypes), maxDTypeStats)
		// Copied rather than re-sliced, for the same reason the listing below
		// is.
		i.DTypes = append([]DTypeStat(nil), i.DTypes[:maxDTypeStats]...)
	}

	if len(tensors) > maxTensors {
		// A copy, not a re-slice: a re-slice keeps the whole backing array --
		// every Tensor past the cut-off, and every name string it points at --
		// alive for as long as the cached Info holds the truncated listing.
		tensors = append([]Tensor(nil), tensors[:maxTensors]...)
		i.Truncated = true
	}
	i.Tensors = tensors
	if i.Tensors == nil {
		i.Tensors = []Tensor{}
	}
	if i.Metadata == nil {
		i.Metadata = map[string]string{}
	}
	if i.Warnings == nil {
		i.Warnings = []string{}
	}
	capMetadata(i)
}

// capMetadata drops entries once i.Metadata passes maxMetadataBytes. Keys are
// considered in sorted order rather than map order so the surviving subset is
// the same on every read of the same file.
func capMetadata(i *Info) {
	total := 0
	for k, v := range i.Metadata {
		total += len(k) + len(v)
	}
	if total <= maxMetadataBytes {
		return
	}

	keys := make([]string, 0, len(i.Metadata))
	for k := range i.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	kept := make(map[string]string, len(keys))
	used := 0
	for _, k := range keys {
		size := len(k) + len(i.Metadata[k])
		if used+size > maxMetadataBytes {
			break
		}
		kept[k] = i.Metadata[k]
		used += size
	}
	warn(i, "metadata is %d bytes, over the %d byte limit; %d of %d entries were dropped",
		total, maxMetadataBytes, len(i.Metadata)-len(kept), len(i.Metadata))
	i.Metadata = kept
}

// capString cuts s down to at most n runes, marking the cut so a truncated
// value cannot be read as a short one.
//
// The result is always a fresh string. Returning s[:k] would leave the whole
// original alive behind the short value it was cut down to, which is the point
// of the cut: these strings are held by a cache entry, not just by the reader.
func capString(s string, n int) string {
	// A rune is at least a byte, so anything this short is already inside the
	// limit and needs no counting at all.
	if len(s) <= n {
		return s
	}
	// Cut on bytes before counting runes: s may be megabytes long, and
	// []rune(s) on it would allocate four times its size only to throw nearly
	// all of it away. No rune is wider than four bytes, so the first n runes
	// are always inside the first 4n.
	head := s
	if len(head) > 4*n {
		head = head[:4*n]
	}
	cut := truncateRunes(head, n)
	if cut == s {
		return s
	}
	return cut + "…"
}

// warn records a recoverable problem on i.
func warn(i *Info, format string, args ...any) {
	i.Warnings = append(i.Warnings, fmt.Sprintf(format, args...))
}

// numElements multiplies a shape, treating an empty shape as a single scalar.
// ok is false when the shape names a negative dimension, or when the product
// does not fit an int64 and has been clamped to math.MaxInt64.
//
// The multiply used to wrap silently, which is worse than a wrong number: a
// declared shape of [2^62, 4] came out as exactly 0 -- indistinguishable from
// a genuinely empty tensor -- and [2^62, 2] came out negative, which then
// subtracted from the file's reported parameter total. Saturating keeps the
// count obviously implausible instead of quietly plausible, and callers turn
// !ok into a warning.
func numElements(shape []int64) (int64, bool) {
	n := int64(1)
	ok := true
	for _, d := range shape {
		if d < 0 {
			return 0, false
		}
		n, ok = mulSaturating(n, d)
		if !ok {
			return n, false
		}
	}
	return n, ok
}

// mulSaturating multiplies two non-negative values, clamping at math.MaxInt64
// rather than wrapping. ok is false when the product was clamped.
func mulSaturating(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a < 0 || b < 0 {
		return 0, false
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64, false
	}
	return a * b, true
}

// addSaturating adds two non-negative values, clamping at math.MaxInt64.
func addSaturating(a, b int64) int64 {
	if a < 0 || b < 0 {
		return a + b
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
