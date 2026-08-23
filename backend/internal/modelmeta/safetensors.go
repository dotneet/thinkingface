package modelmeta

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxSafetensorsHeader bounds the JSON header we are willing to read. Real
// headers are a few hundred kilobytes even for very large shards; anything
// past this is a corrupt or hostile file.
const maxSafetensorsHeader = 64 << 20

// safetensorsEntry is one tensor's record in the JSON header.
type safetensorsEntry struct {
	DType       string   `json:"dtype"`
	Shape       []int64  `json:"shape"`
	DataOffsets [2]int64 `json:"data_offsets"`
}

// inspectSafetensors reads the leading `<u64 header length><JSON header>` of a
// safetensors file. Two ranged reads, regardless of the file's size.
func inspectSafetensors(ctx context.Context, size int64, fetch Fetcher) (*Info, error) {
	if size < 8 {
		return nil, fmt.Errorf("modelmeta: file is %d bytes, too small to be safetensors", size)
	}
	lenBuf, err := fetch(ctx, 0, 8)
	if err != nil {
		return nil, fmt.Errorf("modelmeta: read safetensors header length: %w", err)
	}
	if len(lenBuf) < 8 {
		return nil, fmt.Errorf("modelmeta: short read on safetensors header length")
	}
	headerLen := binary.LittleEndian.Uint64(lenBuf)
	if headerLen == 0 || headerLen > uint64(size-8) {
		return nil, fmt.Errorf("modelmeta: safetensors header length %d does not fit a %d byte file", headerLen, size)
	}
	if headerLen > maxSafetensorsHeader {
		return nil, fmt.Errorf("modelmeta: safetensors header is %d bytes, over the %d byte limit", headerLen, int64(maxSafetensorsHeader))
	}

	raw, err := fetch(ctx, 8, int64(headerLen))
	if err != nil {
		return nil, fmt.Errorf("modelmeta: read safetensors header: %w", err)
	}
	if int64(len(raw)) < int64(headerLen) {
		return nil, fmt.Errorf("modelmeta: short read on safetensors header")
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
	}

	info := &Info{Format: FormatSafetensors, HeaderBytes: int64(headerLen) + 8}
	type placed struct {
		tensor Tensor
		offset int64
	}
	placedTensors := make([]placed, 0, len(doc))

	for name, rawEntry := range doc {
		if name == "__metadata__" {
			info.Metadata = decodeSafetensorsMetadata(rawEntry, info)
			continue
		}
		var entry safetensorsEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			warn(info, "tensor %q has an unreadable header entry: %v", name, err)
			continue
		}
		placedTensors = append(placedTensors, placed{
			tensor: Tensor{
				Name:          name,
				DType:         safetensorsDType(entry.DType),
				Shape:         entry.Shape,
				NumParameters: numElements(entry.Shape),
				// The header carries the exact byte span, which beats
				// guessing from the dtype width.
				SizeBytes: entry.DataOffsets[1] - entry.DataOffsets[0],
			},
			offset: entry.DataOffsets[0],
		})
	}

	// The header is a JSON object, so map iteration order is arbitrary. File
	// order (by data offset) is what a reader expects to see.
	sort.Slice(placedTensors, func(a, b int) bool {
		if placedTensors[a].offset != placedTensors[b].offset {
			return placedTensors[a].offset < placedTensors[b].offset
		}
		return placedTensors[a].tensor.Name < placedTensors[b].tensor.Name
	})
	tensors := make([]Tensor, len(placedTensors))
	for i, p := range placedTensors {
		tensors[i] = p.tensor
	}

	summarize(info, tensors)
	return info, nil
}

// decodeSafetensorsMetadata flattens `__metadata__`. The spec says the values
// are strings, but files in the wild carry numbers and nested objects too, so
// anything non-string is rendered as compact JSON.
func decodeSafetensorsMetadata(raw json.RawMessage, info *Info) map[string]string {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		warn(info, "__metadata__ is not an object: %v", err)
		return map[string]string{}
	}
	out := make(map[string]string, len(doc))
	for k, v := range doc {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	return out
}

// safetensorsDType maps safetensors dtype codes onto the same neutral names
// the PyTorch reader produces, so the UI only ever sees one vocabulary.
func safetensorsDType(code string) string {
	switch code {
	case "BOOL":
		return "bool"
	case "U8":
		return "uint8"
	case "I8":
		return "int8"
	case "F8_E5M2":
		return "float8_e5m2"
	case "F8_E4M3":
		return "float8_e4m3fn"
	case "I16":
		return "int16"
	case "U16":
		return "uint16"
	case "F16":
		return "float16"
	case "BF16":
		return "bfloat16"
	case "I32":
		return "int32"
	case "U32":
		return "uint32"
	case "F32":
		return "float32"
	case "F64":
		return "float64"
	case "I64":
		return "int64"
	case "U64":
		return "uint64"
	case "":
		return "unknown"
	default:
		// Unknown codes stay visible rather than being flattened away.
		return strings.ToLower(code)
	}
}
