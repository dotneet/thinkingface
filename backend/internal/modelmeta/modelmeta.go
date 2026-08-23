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

// summarize fills in the aggregate fields of i from a complete tensor list
// and truncates the listing to maxTensors.
//
// Info is an alias of a type declared in apitypes, which cannot carry methods
// from this package, so this and warn take their receiver as an argument.
func summarize(i *Info, tensors []Tensor) {
	stats := map[string]*DTypeStat{}
	for _, t := range tensors {
		i.NumParameters += t.NumParameters
		i.TensorBytes += t.SizeBytes
		st, ok := stats[t.DType]
		if !ok {
			st = &DTypeStat{DType: t.DType}
			stats[t.DType] = st
		}
		st.NumTensors++
		st.NumParameters += t.NumParameters
		st.SizeBytes += t.SizeBytes
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

	if len(tensors) > maxTensors {
		tensors = tensors[:maxTensors]
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
}

// warn records a recoverable problem on i.
func warn(i *Info, format string, args ...any) {
	i.Warnings = append(i.Warnings, fmt.Sprintf(format, args...))
}

// numElements multiplies a shape, treating an empty shape as a single scalar.
func numElements(shape []int64) int64 {
	n := int64(1)
	for _, d := range shape {
		if d < 0 {
			return 0
		}
		n *= d
	}
	return n
}
