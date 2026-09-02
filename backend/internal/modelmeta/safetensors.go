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

// errHeaderTooDeep stops a scan that has passed maxHeaderDepth. It is fatal
// rather than warned about: the scan is left inside a value it will not walk
// out of, so nothing after it can be trusted.
var errHeaderTooDeep = fmt.Errorf("modelmeta: safetensors header nests more than %d levels deep", maxHeaderDepth)

// maxSafetensorsHeader bounds the JSON header we are willing to read. Real
// headers are a few hundred kilobytes even for very large shards; anything
// past this is a corrupt or hostile file.
//
// It stays this generous because the reference safetensors implementation
// accepts headers up to 100 MB, and refusing a file that `safetensors` itself
// reads would break the compatibility this package exists to provide. The cost
// of a large header is bounded by how it is read rather than by how large it
// is allowed to be: see headerScanner, maxHeaderEntries and maxHeaderDepth.
const maxSafetensorsHeader = 64 << 20

// maxHeaderDepth bounds how deeply the header may nest.
//
// The scan walks the header as a token stream, and json.Decoder.Token keeps
// one stack entry per container it is inside, with no ceiling of its own. A
// 64 MiB header that is nothing but `"extra":[[[[...]]]]` therefore grows that
// stack to millions of entries and costs a gigabyte to walk past a value the
// reader only wants to throw away. (Unmarshalling the value instead was
// bounded by encoding/json's own nesting limit, which Token does not apply --
// so the ceiling has to be applied here.)
//
// A real header nests three deep: the object, one tensor record, its shape
// array. Metadata values are read whole by rawValue rather than walked, so
// they answer to encoding/json's limit and to maxMetadataBytes, not to this.
const maxHeaderDepth = 32

// maxHeaderEntries bounds how many tensor records we take out of the header.
// A 64 MiB header can name over a million tensors, and every one of them costs
// a name string, a shape slice and a record to sort -- roughly ten times the
// header's own size in live objects at the peak. The limit is a wide multiple
// of maxTensors, so the listing is already truncated long before it bites; the
// biggest real checkpoints are two orders of magnitude below it.
const maxHeaderEntries = maxTensors * 4

// safetensorsScanInterval is how many JSON tokens the header scan reads
// between context checks.
const safetensorsScanInterval = 4096

// safetensorsEntry is one tensor's record in the JSON header. It is filled in
// by headerScanner.scanEntry rather than by json.Unmarshal, so the field names
// live there rather than in struct tags.
type safetensorsEntry struct {
	DType       string
	Shape       []int64
	DataOffsets [2]int64
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

	// The header is read as a token stream rather than unmarshalled entry by
	// entry: the map of raw messages an unmarshal builds would hold every
	// entry's bytes alongside the records built from them, and -- because one
	// json.Decode is a single uninterruptible call -- a header that is almost
	// entirely one tensor's shape spent seconds inside it with the caller's
	// deadline long since blown. Scanning tokens gives both the context check
	// and the shape ceiling somewhere to live.
	s := newHeaderScanner(ctx, raw)
	open, err := s.token()
	if err != nil {
		return nil, s.wrapErr(err)
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
	// Every one of these counts a class of bad entry rather than warning about
	// it by name: an entry the reader rejects is never added to placedTensors,
	// so it never reaches the maxHeaderEntries cut-off, and a header made
	// entirely of rejected entries would otherwise put a million warnings --
	// each quoting a file-controlled name -- into an Info that is then cached.
	unreadable := 0
	badOffsets := 0
	overEntries := 0
	overDims := 0
	overCounts := 0
	// dataSize bounds data_offsets: they are byte offsets into the region that
	// follows the header, so a well-formed entry can never name a span past
	// the end of the file it came from.
	dataSize := size - 8 - int64(headerLen)

	for s.more() {
		key, err := s.token()
		if err != nil {
			return nil, s.wrapErr(err)
		}
		name, _ := key.(string)
		if name == "__metadata__" {
			metadata, err := decodeSafetensorsMetadata(s, info)
			if err != nil {
				return nil, s.wrapErr(err)
			}
			info.Metadata = metadata
			continue
		}
		if len(placedTensors) >= maxHeaderEntries {
			if err := s.skipValue(); err != nil {
				return nil, s.wrapErr(err)
			}
			overEntries++
			continue
		}
		entry, dims, ok, err := s.scanEntry()
		if err != nil {
			return nil, s.wrapErr(err)
		}
		if !ok {
			// The value was well-formed JSON but not a usable record, and the
			// scan is still positioned on the next key: skip the one tensor
			// and keep reading the rest of the header.
			unreadable++
			continue
		}
		// A reversed or out-of-range span would otherwise turn into a negative
		// SizeBytes (the subtraction below never checks its sign) or silently
		// inflate the file's reported tensor bytes past its own size, so it is
		// rejected the same way an unreadable entry is.
		if entry.DataOffsets[0] < 0 || entry.DataOffsets[1] < entry.DataOffsets[0] || entry.DataOffsets[1] > dataSize {
			badOffsets++
			continue
		}
		if dims > len(entry.Shape) {
			overDims++
		}
		numParams, exact := numElements(entry.Shape)
		if !exact {
			overCounts++
		}
		placedTensors = append(placedTensors, placed{
			tensor: Tensor{
				Name:          name,
				DType:         safetensorsDType(entry.DType),
				Shape:         entry.Shape,
				NumParameters: numParams,
				// The header carries the exact byte span, which beats
				// guessing from the dtype width.
				SizeBytes: entry.DataOffsets[1] - entry.DataOffsets[0],
			},
			offset: entry.DataOffsets[0],
		})
	}
	if unreadable > 0 {
		warn(info, "%d tensors have an unreadable header entry and were skipped", unreadable)
	}
	if badOffsets > 0 {
		warn(info, "%d tensors declare invalid data_offsets for a %d byte data section and were skipped",
			badOffsets, dataSize)
	}
	if overEntries > 0 {
		// Unlike the usual listing cut-off, this one also leaves the totals
		// short, so it is reported rather than only flagged as Truncated.
		warn(info, "header names more than %d tensors; %d were skipped and are not counted in the totals",
			maxHeaderEntries, overEntries)
	}
	if overDims > 0 {
		warn(info, "%d tensors declare a shape with more than %d dimensions; those shapes were truncated and their parameter counts are not reliable",
			overDims, maxShapeDims)
	}
	if overCounts > 0 {
		warn(info, "%d tensors declare a shape whose element count does not fit a 64-bit integer; their parameter counts are not reliable",
			overCounts)
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

// headerScanner reads the safetensors header as a stream of JSON tokens. It
// exists for three things a plain Decode cannot do: check the caller's context
// part-way through a value, stop collecting a shape once it has more
// dimensions than any tensor can have, so an oversized one is never allocated
// even briefly, and refuse a value that nests past maxHeaderDepth -- which
// Decode gets for free from encoding/json and Token does not.
type headerScanner struct {
	ctx    context.Context
	dec    *json.Decoder
	tokens int
	// depth is how many containers the scan is currently inside, and tooDeep
	// records that it once passed maxHeaderDepth.
	depth   int
	tooDeep bool
}

func newHeaderScanner(ctx context.Context, raw []byte) *headerScanner {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers arrive as json.Number rather than float64: a shape dimension or
	// a data offset can be larger than a float64 holds exactly, and rounding
	// one silently is worse than reporting the entry as unreadable.
	dec.UseNumber()
	return &headerScanner{ctx: ctx, dec: dec}
}

// token reads one JSON token, checking the context every
// safetensorsScanInterval tokens. Checking on every token would put a channel
// read in the hot path of a loop that is otherwise tens of nanoseconds a step;
// a few thousand tokens is a fraction of a millisecond.
//
// Nesting is counted here rather than in drain so that every path through the
// scanner -- the top-level loop, a tensor record, a shape array, an unknown
// field being skipped -- answers to one ceiling.
func (s *headerScanner) token() (json.Token, error) {
	if s.tokens++; s.tokens%safetensorsScanInterval == 0 {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
	}
	tok, err := s.dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'), json.Delim('['):
		s.depth++
		if s.depth > maxHeaderDepth {
			s.tooDeep = true
			return nil, errHeaderTooDeep
		}
	case json.Delim('}'), json.Delim(']'):
		s.depth--
	}
	return tok, nil
}

func (s *headerScanner) more() bool { return s.dec.More() }

// fatal reports whether the last failure has to stop the whole scan rather
// than being downgraded to a warning: either the caller stopped waiting, or
// the header nested past maxHeaderDepth and the scan can no longer say where
// in the file it is.
func (s *headerScanner) fatal() bool { return s.ctx.Err() != nil || s.tooDeep }

// wrapErr labels a scan failure. A context error means the caller stopped
// waiting, which must not be reported as a malformed file -- and must stay
// unwrapped enough for errors.Is to find it. An over-deep header is not
// invalid JSON either, so it is reported as itself.
func (s *headerScanner) wrapErr(err error) error {
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		return fmt.Errorf("modelmeta: read safetensors header: %w", ctxErr)
	}
	if errors.Is(err, errHeaderTooDeep) {
		return err
	}
	return fmt.Errorf("modelmeta: safetensors header is not valid JSON: %w", err)
}

// drain consumes the rest of a container whose opening delimiter has already
// been read. Its own counter only tracks when the container ends; the ceiling
// on how deep it may go lives in token.
func (s *headerScanner) drain() error {
	depth := 1
	for {
		tok, err := s.token()
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

// skipValue consumes exactly one value and throws it away, whatever shape it
// has.
func (s *headerScanner) skipValue() error {
	tok, err := s.token()
	if err != nil {
		return err
	}
	if _, isDelim := tok.(json.Delim); isDelim {
		return s.drain()
	}
	return nil
}

// rawValue consumes one value as the bytes it was written as, for the metadata
// block where the original text is what gets stored. This is the one path that
// does not go through token, so the value's nesting answers to encoding/json's
// own limit rather than to maxHeaderDepth, and its size to maxMetadataBytes.
func (s *headerScanner) rawValue() (json.RawMessage, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	var value json.RawMessage
	if err := s.dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// scanInts reads an array of integers, keeping at most limit of them and
// reporting how many it saw. bad means the value was not an array of integers
// and the record it belongs to is unusable; the value is consumed either way,
// so the scan is left positioned on whatever follows it.
func (s *headerScanner) scanInts(limit int) (vals []int64, count int, bad bool, err error) {
	tok, err := s.token()
	if err != nil {
		return nil, 0, false, err
	}
	if tok != json.Delim('[') {
		if _, isDelim := tok.(json.Delim); isDelim {
			return nil, 0, true, s.drain()
		}
		return nil, 0, true, nil // a scalar, already consumed whole
	}
	for {
		tok, err := s.token()
		if err != nil {
			return nil, 0, false, err
		}
		if tok == json.Delim(']') {
			return vals, count, bad, nil
		}
		num, isNum := tok.(json.Number)
		if !isNum {
			if _, isDelim := tok.(json.Delim); isDelim {
				if err := s.drain(); err != nil {
					return nil, 0, false, err
				}
			}
			bad = true
			continue
		}
		n, convErr := num.Int64()
		if convErr != nil {
			bad = true
			continue
		}
		count++
		// Past the limit the elements are still counted and consumed, but
		// nothing is kept: this is what stops a header from turning a
		// thirty-million-entry shape into memory the reader has to hold.
		if len(vals) < limit {
			vals = append(vals, n)
		}
	}
}

// scanEntry reads one tensor record, consuming exactly one JSON value whatever
// that value turns out to be, so a malformed entry costs the entry rather than
// the rest of the header. dims is how many dimensions the record declared,
// which may be more than the shape it returns. ok is false for a value that is
// well-formed JSON but not a usable record; a returned error is fatal.
func (s *headerScanner) scanEntry() (entry safetensorsEntry, dims int, ok bool, err error) {
	tok, err := s.token()
	if err != nil {
		return entry, 0, false, err
	}
	if tok != json.Delim('{') {
		if _, isDelim := tok.(json.Delim); isDelim {
			return entry, 0, false, s.drain()
		}
		return entry, 0, false, nil // a scalar, already consumed whole
	}
	bad := false
	for {
		tok, err := s.token()
		if err != nil {
			return entry, 0, false, err
		}
		if tok == json.Delim('}') {
			return entry, dims, !bad, nil
		}
		field, _ := tok.(string)
		switch field {
		case "dtype":
			value, err := s.token()
			if err != nil {
				return entry, 0, false, err
			}
			if str, isStr := value.(string); isStr {
				entry.DType = str
				break
			}
			if _, isDelim := value.(json.Delim); isDelim {
				if err := s.drain(); err != nil {
					return entry, 0, false, err
				}
			}
			bad = true
		case "shape":
			vals, count, isBad, err := s.scanInts(maxShapeDims)
			if err != nil {
				return entry, 0, false, err
			}
			entry.Shape, dims = vals, count
			bad = bad || isBad
		case "data_offsets":
			// Only the first two are meaningful; extras are ignored rather
			// than treated as an error, which is what unmarshalling into a
			// [2]int64 used to do.
			vals, _, isBad, err := s.scanInts(2)
			if err != nil {
				return entry, 0, false, err
			}
			copy(entry.DataOffsets[:], vals)
			bad = bad || isBad
		default:
			if err := s.skipValue(); err != nil {
				return entry, 0, false, err
			}
		}
	}
}

// decodeSafetensorsMetadata flattens `__metadata__`, consuming exactly that
// one value from s. The spec says the values are strings, but files in the
// wild carry numbers and nested objects too, so anything non-string is
// rendered as compact JSON.
//
// The map is left as summarize finds it; capMetadata is what enforces the
// size ceiling, so both readers answer to one limit. A returned error is
// always the caller's context: a malformed block is warned about and the scan
// carries on, since the tensors after it are still worth reading.
func decodeSafetensorsMetadata(s *headerScanner, info *Info) (map[string]string, error) {
	out := map[string]string{}
	open, err := s.token()
	if err != nil {
		if s.fatal() {
			return out, err
		}
		warn(info, "__metadata__ is unreadable: %v", err)
		return out, nil
	}
	if open != json.Delim('{') {
		warn(info, "__metadata__ is not an object")
		// A scalar has already been consumed whole; an array still has to be
		// drained so the header scan resumes on the next key.
		if open == json.Delim('[') {
			if err := s.drain(); err != nil && s.fatal() {
				return out, err
			}
		}
		return out, nil
	}
	for s.more() {
		key, err := s.token()
		if err != nil {
			if s.fatal() {
				return out, err
			}
			warn(info, "__metadata__ is unreadable: %v", err)
			return out, nil
		}
		name, _ := key.(string)
		value, err := s.rawValue()
		if err != nil {
			if s.fatal() {
				return out, err
			}
			warn(info, "__metadata__ is unreadable: %v", err)
			return out, nil
		}
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			out[name] = text
			continue
		}
		out[name] = string(value)
	}
	// Consume the closing brace so the caller's scan stays in step.
	if _, err := s.token(); err != nil {
		if s.fatal() {
			return out, err
		}
		warn(info, "__metadata__ is unreadable: %v", err)
	}
	return out, nil
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
