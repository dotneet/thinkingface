package modelmeta

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxSafetensorsHeader bounds the JSON header we are willing to read. Real
// headers are a few hundred kilobytes even for very large shards; anything
// past this is a corrupt or hostile file.
const maxSafetensorsHeader = 64 << 20

// maxHeaderEntries bounds how many tensor records we take out of the header.
// A 64 MiB header can name over a million tensors, and every one of them costs
// a name string, a shape slice and a record to sort -- roughly ten times the
// header's own size in live objects at the peak. The limit is a wide multiple
// of maxTensors, so the listing is already truncated long before it bites; the
// biggest real checkpoints are two orders of magnitude below it.
const maxHeaderEntries = maxTensors * 4

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

	// The header is decoded one entry at a time rather than unmarshalled into
	// a map of raw messages: the map would hold every entry's bytes alongside
	// the records built from them, so a 64 MiB header peaked at several
	// hundred megabytes of live heap before anything was truncated.
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
	}
	if open != json.Delim('{') {
		return nil, fmt.Errorf("modelmeta: safetensors header is not a JSON object")
	}

	info := &Info{Format: FormatSafetensors, HeaderBytes: int64(headerLen) + 8}
	type placed struct {
		tensor Tensor
		offset int64
	}
	var placedTensors []placed
	overEntries := 0

	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
		}
		name, _ := key.(string)
		if name == "__metadata__" {
			info.Metadata = decodeSafetensorsMetadata(dec, info)
			continue
		}
		if len(placedTensors) >= maxHeaderEntries {
			if err := skipJSONValue(dec); err != nil {
				return nil, fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
			}
			overEntries++
			continue
		}
		var entry safetensorsEntry
		if err := dec.Decode(&entry); err != nil {
			// A type error means this one entry is malformed and the decoder
			// is still positioned on the next key; anything else has left the
			// stream unusable, so the whole header is rejected.
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				return nil, fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
			}
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
	if overEntries > 0 {
		// Unlike the usual listing cut-off, this one also leaves the totals
		// short, so it is reported rather than only flagged as Truncated.
		warn(info, "header names more than %d tensors; %d were skipped and are not counted in the totals",
			maxHeaderEntries, overEntries)
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

// decodeSafetensorsMetadata flattens `__metadata__`, consuming exactly that
// one value from dec. The spec says the values are strings, but files in the
// wild carry numbers and nested objects too, so anything non-string is
// rendered as compact JSON.
//
// The map is left as summarize finds it; capMetadata is what enforces the
// size ceiling, so both readers answer to one limit.
func decodeSafetensorsMetadata(dec *json.Decoder, info *Info) map[string]string {
	out := map[string]string{}
	open, err := dec.Token()
	if err != nil {
		warn(info, "__metadata__ is unreadable: %v", err)
		return out
	}
	if open != json.Delim('{') {
		warn(info, "__metadata__ is not an object")
		// A scalar has already been consumed whole; an array still has to be
		// drained so the header scan resumes on the next key.
		if open == json.Delim('[') {
			_ = drainJSONContainer(dec)
		}
		return out
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			warn(info, "__metadata__ is unreadable: %v", err)
			return out
		}
		name, _ := key.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			warn(info, "__metadata__ is unreadable: %v", err)
			return out
		}
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			out[name] = s
			continue
		}
		out[name] = string(value)
	}
	// Consume the closing brace so the caller's scan stays in step.
	if _, err := dec.Token(); err != nil {
		warn(info, "__metadata__ is unreadable: %v", err)
	}
	return out
}

// skipJSONValue consumes the next value from dec and throws it away. Decoding
// into a field-less struct parses the value without building anything, so
// walking past the entry cap costs no memory; a value that is not an object
// still counts as consumed, since Decode reads it whole before complaining
// about its type.
func skipJSONValue(dec *json.Decoder) error {
	var discard struct{}
	err := dec.Decode(&discard)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

// drainJSONContainer consumes the rest of a container whose opening delimiter
// has already been read, which is the one case skipJSONValue cannot handle.
func drainJSONContainer(dec *json.Decoder) error {
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth <= 0 {
			return nil
		}
	}
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
