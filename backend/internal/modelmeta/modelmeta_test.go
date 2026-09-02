package modelmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// hasWarning reports whether any of i's warnings mentions substr.
func hasWarning(i *Info, substr string) bool {
	for _, w := range i.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// fetcherFor serves ranged reads out of an in-memory file.
func fetcherFor(data []byte) (int64, Fetcher) {
	return int64(len(data)), func(_ context.Context, off, n int64) ([]byte, error) {
		if off > int64(len(data)) {
			return nil, nil
		}
		end := off + n
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return data[off:end], nil
	}
}

// buildSafetensors writes a valid safetensors file: an 8-byte header length,
// the JSON header, then zeroed tensor data.
func buildSafetensors(t *testing.T, header map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(raw)))
	buf.Write(raw)

	// Pad out to whatever the largest data offset claims, so the file is the
	// size a reader would expect.
	var end int64
	for name, entry := range header {
		if name == "__metadata__" {
			continue
		}
		offsets := entry.(map[string]any)["data_offsets"].([]int64)
		if offsets[1] > end {
			end = offsets[1]
		}
	}
	buf.Write(make([]byte, end))
	return buf.Bytes()
}

func tensorEntry(dtype string, shape []int64, start, end int64) map[string]any {
	return map[string]any{"dtype": dtype, "shape": shape, "data_offsets": []int64{start, end}}
}

func TestInspectSafetensors(t *testing.T) {
	data := buildSafetensors(t, map[string]any{
		"__metadata__": map[string]any{"format": "pt", "modelspec.title": "tiny"},
		// Listed out of file order on purpose: the reader must sort by offset.
		"model.layer.bias":   tensorEntry("F32", []int64{4}, 96, 112),
		"model.layer.weight": tensorEntry("F16", []int64{4, 8}, 0, 64),
		"model.embed":        tensorEntry("BF16", []int64{4, 4}, 64, 96),
	})

	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	if info.Format != FormatSafetensors {
		t.Errorf("format = %q, want safetensors", info.Format)
	}
	if info.NumTensors != 3 {
		t.Fatalf("num tensors = %d, want 3", info.NumTensors)
	}
	got := []string{info.Tensors[0].Name, info.Tensors[1].Name, info.Tensors[2].Name}
	want := []string{"model.layer.weight", "model.embed", "model.layer.bias"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tensor %d = %q, want %q (file order)", i, got[i], want[i])
		}
	}
	if info.Tensors[0].DType != "float16" {
		t.Errorf("dtype = %q, want float16", info.Tensors[0].DType)
	}
	if info.Tensors[0].NumParameters != 32 {
		t.Errorf("num parameters = %d, want 32", info.Tensors[0].NumParameters)
	}
	if info.NumParameters != 32+16+4 {
		t.Errorf("total parameters = %d, want %d", info.NumParameters, 32+16+4)
	}
	if info.TensorBytes != 112 {
		t.Errorf("tensor bytes = %d, want 112", info.TensorBytes)
	}
	if info.Metadata["modelspec.title"] != "tiny" {
		t.Errorf("metadata = %v, want modelspec.title=tiny", info.Metadata)
	}
	// float16 holds the most parameters, so it must sort first.
	if len(info.DTypes) != 3 || info.DTypes[0].DType != "float16" {
		t.Errorf("dtype stats = %+v, want float16 first", info.DTypes)
	}
}

func TestInspectSafetensorsRejectsBadHeader(t *testing.T) {
	tests := map[string][]byte{
		"too small":       {1, 2, 3},
		"header overruns": append(make([]byte, 0, 16), []byte{0xff, 0xff, 0, 0, 0, 0, 0, 0, '{', '}'}...),
		"header is not json": func() []byte {
			var buf bytes.Buffer
			_ = binary.Write(&buf, binary.LittleEndian, uint64(4))
			buf.WriteString("nope")
			return buf.Bytes()
		}(),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			size, fetch := fetcherFor(data)
			if _, err := Inspect(context.Background(), FormatSafetensors, size, fetch); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestInspectSafetensorsCapsMetadataSize(t *testing.T) {
	// Regression: `__metadata__` had no ceiling, so one file could park tens
	// of megabytes in the inspection cache -- times DefaultCacheEntries.
	meta := map[string]any{}
	for i := 0; i < 2000; i++ {
		meta[fmt.Sprintf("k%04d", i)] = strings.Repeat("v", 1024)
	}
	data := buildSafetensors(t, map[string]any{
		"__metadata__": meta,
		"weight":       tensorEntry("F32", []int64{2, 2}, 0, 16),
	})
	size, fetch := fetcherFor(data)

	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	total := 0
	for k, v := range info.Metadata {
		total += len(k) + len(v)
	}
	if total > maxMetadataBytes {
		t.Errorf("metadata is %d bytes, over the %d byte cap", total, maxMetadataBytes)
	}
	if len(info.Metadata) == 0 {
		t.Error("the whole metadata map was dropped, want the entries that fit")
	}
	if !hasWarning(info, "over the") {
		t.Errorf("warnings = %v, want one reporting the dropped entries", info.Warnings)
	}
	// The kept subset must not depend on map iteration order.
	again, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("second Inspect: %v", err)
	}
	if len(again.Metadata) != len(info.Metadata) {
		t.Fatalf("kept %d entries then %d: the cut is not deterministic", len(info.Metadata), len(again.Metadata))
	}
	for k, v := range info.Metadata {
		if again.Metadata[k] != v {
			t.Fatalf("entry %q survived only one of two reads: the cut is not deterministic", k)
		}
	}
	// Small metadata still comes through whole.
	plain := buildSafetensors(t, map[string]any{
		"__metadata__": map[string]any{"format": "pt"},
		"weight":       tensorEntry("F32", []int64{2, 2}, 0, 16),
	})
	size, fetch = fetcherFor(plain)
	info, err = Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Metadata["format"] != "pt" || len(info.Warnings) != 0 {
		t.Errorf("metadata = %v, warnings = %v, want an untouched map", info.Metadata, info.Warnings)
	}
}

func TestInspectSafetensorsCapsHeaderEntries(t *testing.T) {
	// Regression: every entry of the header was materialised before the
	// listing was cut down to maxTensors, so a header full of tiny tensors
	// cost several times its own size in live objects.
	header := map[string]any{}
	for i := 0; i < maxHeaderEntries+64; i++ {
		// The names sort before "__metadata__", so the fixture also proves a
		// metadata block written after the tensors is still reached.
		header[fmt.Sprintf("T%06d", i)] = tensorEntry("F32", []int64{1}, int64(i)*4, int64(i)*4+4)
	}
	header["__metadata__"] = map[string]any{"format": "pt"}
	data := buildSafetensors(t, header)
	size, fetch := fetcherFor(data)

	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != maxHeaderEntries {
		t.Errorf("num tensors = %d, want %d", info.NumTensors, maxHeaderEntries)
	}
	if len(info.Tensors) != maxTensors || !info.Truncated {
		t.Errorf("listed %d tensors (truncated=%v), want %d and truncated", len(info.Tensors), info.Truncated, maxTensors)
	}
	if !hasWarning(info, "were skipped") {
		t.Errorf("warnings = %v, want one reporting the skipped entries", info.Warnings)
	}
	if info.Metadata["format"] != "pt" {
		t.Errorf("metadata = %v, want the block that follows the tensors", info.Metadata)
	}
}

func TestInspectSafetensorsRejectsInvalidDataOffsets(t *testing.T) {
	// Regression: SizeBytes was computed as offsets[1] - offsets[0] with no
	// check on the sign or ordering, so a header with reversed or negative
	// offsets silently produced a negative tensor size instead of being
	// rejected.
	tests := map[string][2]int64{
		"reversed":       {64, 0},
		"negative start": {-8, 8},
	}
	for name, offsets := range tests {
		t.Run(name, func(t *testing.T) {
			data := buildSafetensors(t, map[string]any{
				"weight": tensorEntry("F32", []int64{2}, offsets[0], offsets[1]),
			})
			size, fetch := fetcherFor(data)
			info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.NumTensors != 0 {
				t.Errorf("num tensors = %d, want 0 (the malformed entry must be dropped, not kept with a negative size)", info.NumTensors)
			}
			if !hasWarning(info, "invalid data_offsets") {
				t.Errorf("warnings = %v, want one reporting invalid data_offsets", info.Warnings)
			}
		})
	}
}

func TestInspectSafetensorsRejectsDataOffsetsPastEndOfFile(t *testing.T) {
	// A well-ordered, non-negative span can still lie about the size of the
	// data section that follows the header; that must be caught the same way
	// as a reversed span, rather than reported as a huge tensor.
	header := map[string]any{
		"weight": tensorEntry("F32", []int64{4}, 0, 1<<40),
	}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(raw)))
	buf.Write(raw)
	buf.Write(make([]byte, 16)) // the actual file is tiny; the header's claim is not

	size, fetch := fetcherFor(buf.Bytes())
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != 0 {
		t.Errorf("num tensors = %d, want 0", info.NumTensors)
	}
	if !hasWarning(info, "invalid data_offsets") {
		t.Errorf("warnings = %v, want one reporting invalid data_offsets", info.Warnings)
	}
}

// ------------------------------------------------------------- torch fixtures

// pickleWriter emits the opcode sequence `torch.save` produces, so the tests
// exercise the same stream shape as a real checkpoint.
type pickleWriter struct{ buf bytes.Buffer }

func (p *pickleWriter) proto()      { p.buf.Write([]byte{0x80, 0x02}) }
func (p *pickleWriter) mark()       { p.buf.WriteByte('(') }
func (p *pickleWriter) tuple()      { p.buf.WriteByte('t') }
func (p *pickleWriter) emptyTuple() { p.buf.WriteByte(')') }
func (p *pickleWriter) emptyDict()  { p.buf.WriteByte('}') }
func (p *pickleWriter) reduce()     { p.buf.WriteByte('R') }
func (p *pickleWriter) setItems()   { p.buf.WriteByte('u') }
func (p *pickleWriter) persID()     { p.buf.WriteByte('Q') }
func (p *pickleWriter) boolean(b bool) {
	if b {
		p.buf.WriteByte(0x88)
	} else {
		p.buf.WriteByte(0x89)
	}
}
func (p *pickleWriter) stop() { p.buf.WriteByte('.') }

func (p *pickleWriter) global(module, name string) {
	p.buf.WriteByte('c')
	p.buf.WriteString(module + "\n" + name + "\n")
}

func (p *pickleWriter) str(s string) {
	p.buf.WriteByte(0x8c)
	p.buf.WriteByte(byte(len(s)))
	p.buf.WriteString(s)
}

func (p *pickleWriter) int1(n int) { p.buf.Write([]byte{'K', byte(n)}) }

// put and get are BINPUT / BINGET: the memo, which is how a pickle names a
// value it has already written and so how it can point a value at itself.
func (p *pickleWriter) put(slot byte) { p.buf.Write([]byte{'q', slot}) }
func (p *pickleWriter) get(slot byte) { p.buf.Write([]byte{'h', slot}) }

// memoize is MEMOIZE (opcode 0x94): a single byte that memos the stack's top
// without popping it, so it can be repeated indefinitely against one pushed
// value.
func (p *pickleWriter) memoize() { p.buf.WriteByte(0x94) }

func (p *pickleWriter) ints(values ...int) {
	p.mark()
	for _, v := range values {
		p.int1(v)
	}
	p.tuple()
}

// tensor emits `torch._utils._rebuild_tensor_v2(storage, 0, size, stride,
// False, OrderedDict())`.
func (p *pickleWriter) tensor(storageClass, key string, shape ...int) {
	p.global("torch._utils", "_rebuild_tensor_v2")
	p.mark()

	p.mark() // the storage persistent id tuple
	p.str("storage")
	p.global("torch", storageClass)
	p.str(key)
	p.str("cpu")
	p.int1(1)
	p.tuple()
	p.persID()

	p.int1(0) // storage offset
	p.ints(shape...)
	p.ints(1) // stride, unused by the reader
	p.boolean(false)
	p.emptyDict()

	p.tuple()
	p.reduce()
}

// buildTorchPickle writes {"state_dict": OrderedDict{...}, "epoch": 7}.
func buildTorchPickle() []byte {
	p := &pickleWriter{}
	p.proto()
	p.emptyDict()
	p.mark()

	p.str("state_dict")
	p.global("collections", "OrderedDict")
	p.emptyTuple()
	p.reduce()
	p.mark()
	p.str("layer.weight")
	p.tensor("FloatStorage", "0", 4, 8)
	p.str("layer.bias")
	p.tensor("HalfStorage", "1", 8)
	p.setItems()

	p.str("epoch")
	p.int1(7)

	p.setItems()
	p.stop()
	return p.buf.Bytes()
}

// buildCyclicTorchPickle writes a dict whose every value is the dict itself:
// the dict is memoised, then read back out of the memo once per entry.
// `torch.save` never produces this, but a handcrafted file can, and it is the
// shape that turns a depth-limited walk into an N^depth explosion.
func buildCyclicTorchPickle(entries int) []byte {
	p := &pickleWriter{}
	p.proto()
	p.emptyDict()
	p.put(1)
	p.mark()
	for i := 0; i < entries; i++ {
		p.str(fmt.Sprintf("k%d", i))
		p.get(1)
	}
	p.setItems()
	p.stop()
	return p.buf.Bytes()
}

// buildTorchZip wraps a pickle the way torch.save does, with the weights in
// sibling members the reader must never need.
func buildTorchZip(t *testing.T, pickle []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("archive/data.pkl", pickle)
	write("archive/data/0", make([]byte, 128))
	write("archive/data/1", make([]byte, 16))
	write("archive/version", []byte("3\n"))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestInspectPyTorchZip(t *testing.T) {
	data := buildTorchZip(t, buildTorchPickle())
	size, fetch := fetcherFor(data)

	info, err := Inspect(context.Background(), FormatPyTorch, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Format != FormatPyTorch {
		t.Errorf("format = %q, want pytorch", info.Format)
	}
	if info.NumTensors != 2 {
		t.Fatalf("num tensors = %d, want 2 (%+v)", info.NumTensors, info.Tensors)
	}

	weight := info.Tensors[0]
	if weight.Name != "state_dict.layer.weight" {
		t.Errorf("name = %q, want state_dict.layer.weight", weight.Name)
	}
	if weight.DType != "float32" {
		t.Errorf("dtype = %q, want float32", weight.DType)
	}
	if len(weight.Shape) != 2 || weight.Shape[0] != 4 || weight.Shape[1] != 8 {
		t.Errorf("shape = %v, want [4 8]", weight.Shape)
	}
	if weight.SizeBytes != 32*4 {
		t.Errorf("size = %d, want 128", weight.SizeBytes)
	}
	if info.Tensors[1].DType != "float16" {
		t.Errorf("bias dtype = %q, want float16", info.Tensors[1].DType)
	}
	if info.NumParameters != 40 {
		t.Errorf("total parameters = %d, want 40", info.NumParameters)
	}
	if info.Metadata["epoch"] != "7" {
		t.Errorf("metadata = %v, want epoch=7", info.Metadata)
	}
}

func TestInspectPyTorchLegacy(t *testing.T) {
	// Pre-1.6 layout: a run of small pickles, then the object graph.
	var buf bytes.Buffer
	preamble := &pickleWriter{}
	preamble.proto()
	preamble.int1(119)
	preamble.stop()
	buf.Write(preamble.buf.Bytes())
	buf.Write(preamble.buf.Bytes())
	buf.Write(preamble.buf.Bytes())
	buf.Write(buildTorchPickle())
	buf.Write(make([]byte, 64)) // raw storage bytes the reader must ignore

	size, fetch := fetcherFor(buf.Bytes())
	info, err := Inspect(context.Background(), FormatPyTorch, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != 2 {
		t.Fatalf("num tensors = %d, want 2", info.NumTensors)
	}
}

func TestInspectPyTorchReadsOnlyTheHeader(t *testing.T) {
	// A checkpoint whose weights dwarf its header must not be downloaded: the
	// fixture reports a huge size but only serves the real bytes, so any read
	// past the archive would come back short and fail the parse.
	data := buildTorchZip(t, buildTorchPickle())
	size, fetch := fetcherFor(data)

	var fetched int64
	counted := func(ctx context.Context, off, n int64) ([]byte, error) {
		out, err := fetch(ctx, off, n)
		fetched += int64(len(out))
		return out, err
	}
	if _, err := Inspect(context.Background(), FormatPyTorch, size, counted); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if fetched > size {
		t.Errorf("fetched %d bytes for a %d byte file: the reader is over-reading", fetched, size)
	}
}

func TestInspectPyTorchCyclicPickleTerminates(t *testing.T) {
	// Regression: a dict that points at itself has no bottom, and the walk's
	// depth limit alone does not bound it -- even a 12-entry cycle is 12^8
	// (about 4x10^11) visits, so a sub-kilobyte file used to hold a request
	// open and a core busy indefinitely. The cycle here is deliberately wide:
	// a budget on visits alone still left the walk linear in the width,
	// because naming children it then refused was work nothing counted.
	data := buildTorchZip(t, buildCyclicTorchPickle(4000))
	size, fetch := fetcherFor(data)

	type result struct {
		info *Info
		err  error
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		info, err := Inspect(context.Background(), FormatPyTorch, size, fetch)
		done <- result{info: info, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Inspect: %v", got.err)
		}
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Errorf("a %d byte cyclic checkpoint took %v to inspect", len(data), elapsed)
		}
		if !hasWarning(got.info, "object graph") {
			t.Errorf("warnings = %v, want one reporting the cut-short walk", got.info.Warnings)
		}
	case <-time.After(30 * time.Second):
		// The walk is unbounded again; leaving it running is the point.
		t.Fatalf("Inspect never returned for a %d byte cyclic checkpoint", len(data))
	}
}

func TestCollectorWalkVisitsAreBudgeted(t *testing.T) {
	// The budget is a total, not a per-level one: a graph that fans out
	// legitimately still stops after maxWalkVisits nodes rather than after
	// maxWalkVisits per branch.
	root, err := newUnpickler(context.Background(), bytes.NewReader(buildCyclicTorchPickle(8)), (&torchReducer{}).reduce).load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := &collector{meta: map[string]string{}}
	c.walk("", root, 0)
	if !c.exhausted {
		t.Fatal("walk finished a cyclic graph without exhausting its budget")
	}
	if c.visits > maxWalkVisits+1 {
		t.Errorf("walk made %d visits, want at most %d", c.visits, maxWalkVisits+1)
	}
}

func TestUnpicklerMemoTableIsBounded(t *testing.T) {
	// Regression: MEMOIZE (opcode 0x94) is a single byte that memos the
	// stack's top without popping it, so a stream that pushes one value and
	// then repeats MEMOIZE has nothing else to trip -- push()'s stack limit
	// never sees it, and every repetition only grows the memo map. A run of
	// one-byte opcodes like that also compresses to almost nothing, so a
	// small uploaded file could force gigabytes of live heap. The memo table
	// must now refuse to grow past maxPickleMemoEntries.
	p := &pickleWriter{}
	p.proto()
	p.int1(1)
	for i := 0; i < maxPickleMemoEntries+10; i++ {
		p.memoize()
	}
	p.stop()

	_, err := newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load()
	if err == nil {
		t.Fatal("load: want an error once the memo table exceeds its limit, got none")
	}
	if !strings.Contains(err.Error(), "memo table exceeds") {
		t.Errorf("load error = %v, want it to mention the memo table limit", err)
	}
}

func TestUnpicklerMemoOverwriteDoesNotCountAgainstLimit(t *testing.T) {
	// BINPUT can legitimately reuse a memo slot within one stream (protocol 0
	// pickles do this routinely), so an overwrite of an existing key must not
	// be treated as growth -- only writes that add a new key count against
	// maxPickleMemoEntries.
	p := &pickleWriter{}
	p.proto()
	p.int1(1)
	p.put(0)
	p.put(0) // same slot again; the table must still have exactly one entry
	p.stop()

	u := newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce)
	if _, err := u.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(u.memo) != 1 {
		t.Errorf("memo has %d entries, want 1 (an overwrite must not grow the table)", len(u.memo))
	}
}

func TestUnpicklerMarkStackIsBounded(t *testing.T) {
	// Regression: MARK ('(') is also a single byte, and unlike PUSH it never
	// touches the value stack -- so a stream that repeats MARK alone grew
	// u.marks without ever tripping push()'s stack limit.
	p := &pickleWriter{}
	p.proto()
	for i := 0; i < maxPickleMarks+10; i++ {
		p.mark()
	}
	p.stop()

	_, err := newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load()
	if err == nil {
		t.Fatal("load: want an error once MARK opcodes exceed the limit, got none")
	}
	if !strings.Contains(err.Error(), "MARK") {
		t.Errorf("load error = %v, want it to mention MARK", err)
	}
}

func TestCollectorAddMetadata_TruncatesASCIIAtRuneLimit(t *testing.T) {
	// Regression: plain ASCII text must still be cut at exactly 512
	// characters, same as before rune-aware truncation.
	c := &collector{meta: map[string]string{}}
	c.addMetadata("note", strings.Repeat("x", 600), 0)
	want := strings.Repeat("x", 512) + "…"
	if got := c.meta["note"]; got != want {
		t.Errorf("meta[note] = %q, want 512 x's plus an ellipsis", got)
	}
}

func TestCollectorAddMetadata_TruncatesOnRuneBoundaryForMultibyteText(t *testing.T) {
	// The 512-character cut lands inside a run of 3-byte Japanese characters;
	// truncation must land on a rune boundary rather than slicing mid-character
	// and producing invalid UTF-8 (which json.Marshal would silently replace
	// with U+FFFD in the API response).
	c := &collector{meta: map[string]string{}}
	long := strings.Repeat("a", 511) + "あいうえお"
	c.addMetadata("note", long, 0)
	got := c.meta["note"]
	if !utf8.ValidString(got) {
		t.Fatalf("meta[note] = %q is not valid UTF-8", got)
	}
	want := strings.Repeat("a", 511) + "あ" + "…"
	if got != want {
		t.Errorf("meta[note] = %q, want %q", got, want)
	}
}

func TestFormatFor(t *testing.T) {
	tests := map[string]Format{
		"model.safetensors":         FormatSafetensors,
		"sub/dir/MODEL.SafeTensors": FormatSafetensors,
		"pytorch_model.bin":         FormatPyTorch,
		"last.ckpt":                 FormatPyTorch,
		"weights.pt":                FormatPyTorch,
		"weights.pth":               FormatPyTorch,
		"README.md":                 "",
		"data/train.parquet":        "",
		"noextension":               "",
	}
	for path, want := range tests {
		if got := FormatFor(path); got != want {
			t.Errorf("FormatFor(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCacheReusesInspection(t *testing.T) {
	data := buildSafetensors(t, map[string]any{
		"weight": tensorEntry("F32", []int64{2, 2}, 0, 16),
	})
	size, fetch := fetcherFor(data)

	calls := 0
	counted := func(ctx context.Context, off, n int64) ([]byte, error) {
		calls++
		return fetch(ctx, off, n)
	}

	cache := NewCache(4)
	first, err := cache.Inspect(context.Background(), "blob:abc", FormatSafetensors, size, counted)
	if err != nil {
		t.Fatalf("first Inspect: %v", err)
	}
	after := calls

	second, err := cache.Inspect(context.Background(), "blob:abc", FormatSafetensors, size, counted)
	if err != nil {
		t.Fatalf("second Inspect: %v", err)
	}
	if calls != after {
		t.Errorf("cached lookup made %d extra reads, want 0", calls-after)
	}
	if first != second {
		t.Error("cached lookup returned a different value")
	}
}

func TestCacheKeyIsQualifiedByFormat(t *testing.T) {
	// Regression: the key is a content address, but the format comes from the
	// file's *name*. The same bytes committed as both `model.bin` and
	// `model.safetensors` therefore share a key, and the first inspection used
	// to answer for the second -- here a torch checkpoint reported as a
	// successful safetensors read.
	data := buildTorchZip(t, buildTorchPickle())
	size, fetch := fetcherFor(data)
	cache := NewCache(4)

	info, err := cache.Inspect(context.Background(), "blob:abc", FormatPyTorch, size, fetch)
	if err != nil {
		t.Fatalf("Inspect as pytorch: %v", err)
	}
	if info.Format != FormatPyTorch {
		t.Fatalf("format = %q, want pytorch", info.Format)
	}
	// A zip is not a safetensors file, so reading the same bytes under the
	// other format has to fail rather than hand back the cached answer.
	if got, err := cache.Inspect(context.Background(), "blob:abc", FormatSafetensors, size, fetch); err == nil {
		t.Fatalf("Inspect as safetensors returned %+v, want an error", got)
	}
}

func TestCacheEvictsOldestEntries(t *testing.T) {
	data := buildSafetensors(t, map[string]any{"weight": tensorEntry("F32", []int64{1}, 0, 4)})
	size, fetch := fetcherFor(data)

	cache := NewCache(2)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := cache.Inspect(context.Background(), key, FormatSafetensors, size, fetch); err != nil {
			t.Fatalf("Inspect %s: %v", key, err)
		}
	}
	if _, ok := cache.lookup(cacheKey(FormatSafetensors, "a")); ok {
		t.Error("oldest entry survived eviction")
	}
	if _, ok := cache.lookup(cacheKey(FormatSafetensors, "c")); !ok {
		t.Error("newest entry was evicted")
	}
}

// emptyList and appends are EMPTY_LIST / APPENDS: how a pickle writes a list
// whose contents are pushed under a MARK.
func (p *pickleWriter) emptyList() { p.buf.WriteByte(']') }
func (p *pickleWriter) appends()   { p.buf.WriteByte('e') }

// long4 is LONG4 (opcode 0x8b): a 4-byte little-endian length followed by that
// many two's-complement bytes.
func (p *pickleWriter) long4(payload []byte) {
	p.buf.WriteByte(0x8b)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(payload)))
	p.buf.Write(n[:])
	p.buf.Write(payload)
}

// sharedShape writes a size tuple of dims entries and memoises it in slot 1,
// so every later get(1) hands the *same* tuple to another rebuild call.
func (p *pickleWriter) sharedShape(dims int) {
	p.mark()
	for i := 0; i < dims; i++ {
		p.int1(1)
	}
	p.tuple()
	p.put(1)
	p.buf.WriteByte('0') // POP: the tuple lives in the memo from here on
}

// tensorWithSharedShape emits _rebuild_tensor_v2 with its size argument read
// back out of memo slot 1 rather than written out again.
func (p *pickleWriter) tensorWithSharedShape() {
	p.global("torch._utils", "_rebuild_tensor_v2")
	p.mark()

	p.mark()
	p.str("storage")
	p.global("torch", "FloatStorage")
	p.str("0")
	p.str("cpu")
	p.int1(1)
	p.tuple()
	p.persID()

	p.int1(0)
	p.get(1) // the shared size tuple
	p.ints(1)
	p.boolean(false)
	p.emptyDict()

	p.tuple()
	p.reduce()
}

// buildSharedShapeTorchPickle writes a state_dict of `tensors` entries that all
// point at one memoised `dims`-entry size tuple.
func buildSharedShapeTorchPickle(dims, tensors int) []byte {
	p := &pickleWriter{}
	p.proto()
	p.sharedShape(dims)
	p.emptyDict()
	p.mark()
	for i := 0; i < tensors; i++ {
		p.str(fmt.Sprintf("t%d", i))
		p.tensorWithSharedShape()
	}
	p.setItems()
	p.stop()
	return p.buf.Bytes()
}

func TestInspectPyTorchSharedShapeIsBounded(t *testing.T) {
	// Regression: a size tuple in the memo can be handed to any number of
	// _rebuild_tensor_v2 calls for two bytes apiece (BINGET), and toShape
	// allocated a fresh []int64 for every one of them. Nothing else saw it --
	// the stack limit does not count values that have moved into a container,
	// and the walk's budgets only apply after the whole graph is in memory --
	// so a few hundred bytes on disk turned into hundreds of megabytes of live
	// heap, cached.
	const (
		dims    = 2000
		tensors = 2000
	)
	pickle := buildSharedShapeTorchPickle(dims, tensors)
	data := buildTorchZip(t, pickle)

	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatPyTorch, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != tensors {
		t.Fatalf("NumTensors = %d, want %d", info.NumTensors, tensors)
	}
	total := 0
	for _, tensor := range info.Tensors {
		if len(tensor.Shape) > maxShapeDims {
			t.Fatalf("tensor %q kept %d dimensions, want at most %d", tensor.Name, len(tensor.Shape), maxShapeDims)
		}
		total += len(tensor.Shape)
	}
	if total > maxTotalShapeDims {
		t.Errorf("the file's shapes hold %d dimensions in total, want at most %d", total, maxTotalShapeDims)
	}
	// The amplification is what the bound is for. Uncapped this file asked for
	// dims*tensors dimensions -- 32 MB of shape from a pickle two orders of
	// magnitude smaller, and a far worse ratio once deflate has its way with a
	// stream this repetitive. What is left must be a small multiple of the
	// input rather than a multiple of the input squared.
	if want := len(pickle); total*8 > 8*want {
		t.Errorf("shapes allocated %d bytes from a %d byte pickle (uncapped: %d bytes); the cost must stay linear in the input",
			total*8, want, dims*tensors*8)
	}
	if !hasWarning(info, "shape dimensions") {
		t.Errorf("warnings = %v, want one reporting the truncated shapes", info.Warnings)
	}
}

func TestToShapeIsBoundedPerTensorAndInTotal(t *testing.T) {
	// The per-tensor cap alone would still let a file spend an unbounded
	// amount by rebuilding a legal-looking shape over and over, so the two
	// ceilings are tested separately.
	oversized := make(pickleTuple, maxShapeDims*10)
	for i := range oversized {
		oversized[i] = int64(1)
	}

	r := &torchReducer{}
	got := r.toShape(oversized)
	if len(got) != maxShapeDims {
		t.Fatalf("toShape kept %d of %d dimensions, want %d", len(got), len(oversized), maxShapeDims)
	}
	if !r.shapeExhausted {
		t.Error("toShape truncated a shape without raising shapeExhausted")
	}

	// The same tuple over and over: legal per call, unbounded in aggregate.
	r = &torchReducer{}
	shape := oversized[:maxShapeDims]
	for i := 0; i < maxTotalShapeDims/maxShapeDims+100; i++ {
		if len(r.toShape(shape)) > maxShapeDims {
			t.Fatal("toShape returned more than maxShapeDims dimensions")
		}
	}
	if r.shapeDims > maxTotalShapeDims {
		t.Errorf("toShape handed out %d dimensions in total, want at most %d", r.shapeDims, maxTotalShapeDims)
	}
	if !r.shapeExhausted {
		t.Error("the total shape budget ran out without raising shapeExhausted")
	}
}

func TestUnpicklerSkipsOversizedLong(t *testing.T) {
	// Regression: readN allowed a LONG4 payload of up to 64 MiB, and
	// decodeLong ran big.Int.String() over it -- a quadratic base conversion
	// that spent nearly a minute on a file that deflates to a few kilobytes,
	// inside a single arithmetic call no budget or deadline could interrupt.
	// Anything past maxPickleLongBytes must be consumed without being
	// converted.
	payload := bytes.Repeat([]byte{0xAB}, maxPickleLongBytes+1)
	p := &pickleWriter{}
	p.proto()
	p.long4(payload)
	p.stop()

	root, err := newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	skipped, ok := root.(pickleOversizedLong)
	if !ok {
		t.Fatalf("load returned %T, want the oversized-long placeholder (the base conversion must be skipped)", root)
	}
	if skipped.Bytes != int64(len(payload)) {
		t.Errorf("placeholder reports %d bytes, want %d", skipped.Bytes, len(payload))
	}

	// A long inside the limit still decodes normally.
	p = &pickleWriter{}
	p.proto()
	p.long4([]byte{0x2A})
	p.stop()
	root, err = newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if root != int64(42) {
		t.Errorf("load returned %#v, want int64(42)", root)
	}
}

func TestUnpicklerSkipsOversizedTextLong(t *testing.T) {
	// Protocol 0's LONG is a line of digits with no length prefix, and
	// big.Int.SetString is quadratic the same way String() is.
	digits := strings.Repeat("9", maxPickleLongBytes+1)
	if got := parseBigDecimal(digits); got != (pickleOversizedLong{Bytes: int64(len(digits))}) {
		t.Errorf("parseBigDecimal(%d digits) = %#v, want the oversized-long placeholder", len(digits), got)
	}
	if got := parseBigDecimal("123"); got != int64(123) {
		t.Errorf("parseBigDecimal(\"123\") = %#v, want int64(123)", got)
	}
}

func TestUnpicklerHonoursContextCancellation(t *testing.T) {
	// Regression: load() never looked at the caller's context, so a request
	// deadline had no effect on a stream that stayed inside every other
	// budget. It must give up promptly instead.
	p := &pickleWriter{}
	p.proto()
	for i := 0; i < pickleCancelCheckInterval*4; i++ {
		p.int1(1)
		p.buf.WriteByte('0') // POP, so the value stack never grows
	}
	p.int1(0) // one value for STOP to return
	p.stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newUnpickler(ctx, bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("load error = %v, want it to wrap context.Canceled", err)
	}

	// An uncancelled context must not disturb the same stream.
	if _, err := newUnpickler(context.Background(), bytes.NewReader(p.buf.Bytes()), (&torchReducer{}).reduce).load(); err != nil {
		t.Fatalf("load with a live context: %v", err)
	}
}

func TestInspectPyTorchHonoursContextCancellation(t *testing.T) {
	// The same thing end to end: a cancelled context must reach the unpickler
	// through Inspect, not just through a hand-made one.
	p := &pickleWriter{}
	p.proto()
	for i := 0; i < pickleCancelCheckInterval*4; i++ {
		p.int1(1)
		p.buf.WriteByte('0') // POP
	}
	p.int1(0) // one value for STOP to return
	p.stop()
	data := buildTorchZip(t, p.buf.Bytes())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	size, fetch := fetcherFor(data)
	if _, err := Inspect(ctx, FormatPyTorch, size, fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want it to wrap context.Canceled", err)
	}
}

func TestInspectSafetensorsCapsShapeDimensions(t *testing.T) {
	// Regression: Shape was the one file-controlled part of an Info with no
	// ceiling. A header may be 64 MiB, so `"shape":[1,1,1,...]` declared tens
	// of millions of dimensions for a single tensor, and the slice built from
	// it then sat in the inspection cache.
	const dims = 200_000
	shape := make([]int64, dims)
	for i := range shape {
		shape[i] = 1
	}
	data := buildSafetensors(t, map[string]any{
		"wide": tensorEntry("F32", shape, 0, 4),
	})

	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(info.Tensors) != 1 {
		t.Fatalf("Tensors = %d, want 1", len(info.Tensors))
	}
	if got := len(info.Tensors[0].Shape); got != maxShapeDims {
		t.Errorf("kept %d of %d dimensions, want %d", got, dims, maxShapeDims)
	}
	if !hasWarning(info, "dimensions") {
		t.Errorf("warnings = %v, want one reporting the truncated shape", info.Warnings)
	}

	// A shape a real checkpoint could have is left exactly as it was.
	data = buildSafetensors(t, map[string]any{
		"normal": tensorEntry("F32", []int64{2, 3, 4}, 0, 96),
	})
	size, fetch = fetcherFor(data)
	info, err = Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := info.Tensors[0].Shape; len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Errorf("Shape = %v, want [2 3 4] untouched", got)
	}
	if len(info.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for an ordinary shape", info.Warnings)
	}
}

func TestInspectPyTorchWarnsOnTruncatedSequence(t *testing.T) {
	// Regression: walkSequence stopped at maxSequenceItems and said nothing.
	// 1024 is under summarize's own 4096 cut-off, so Truncated stayed false
	// too, and a checkpoint whose state_dict is a long list (any model with a
	// wide nn.Sequential) came back with a short tensor count, a short
	// parameter total and no indication that anything had been dropped.
	const entries = maxSequenceItems + 476
	p := &pickleWriter{}
	p.proto()
	p.emptyList()
	p.mark()
	for i := 0; i < entries; i++ {
		p.tensor("FloatStorage", "0", 2)
	}
	p.appends()
	p.stop()
	data := buildTorchZip(t, p.buf.Bytes())

	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatPyTorch, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != maxSequenceItems {
		t.Fatalf("NumTensors = %d, want %d (the walk still stops at the limit)", info.NumTensors, maxSequenceItems)
	}
	if !hasWarning(info, "list") {
		t.Errorf("warnings = %v, want one reporting the truncated list", info.Warnings)
	}
}

func TestNumElementsSaturates(t *testing.T) {
	// Regression: the product wrapped, so a declared shape of [2^62, 4] came
	// out as exactly 0 -- indistinguishable from an empty tensor -- and
	// [2^62, 2] came out negative and subtracted from the file's total.
	cases := []struct {
		name  string
		shape []int64
		want  int64
		ok    bool
	}{
		{"scalar", nil, 1, true},
		{"ordinary", []int64{2, 3, 4}, 24, true},
		{"wraps to zero", []int64{1 << 62, 4}, math.MaxInt64, false},
		{"wraps negative", []int64{1 << 62, 2}, math.MaxInt64, false},
		{"negative dimension", []int64{-1, 2}, 0, false},
		{"genuinely empty", []int64{0, 4}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := numElements(tc.shape)
			if got != tc.want || ok != tc.ok {
				t.Errorf("numElements(%v) = (%d, %v), want (%d, %v)", tc.shape, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestInspectSafetensorsWarnsOnOverflowingShape(t *testing.T) {
	data := buildSafetensors(t, map[string]any{
		"huge": tensorEntry("F32", []int64{1 << 62, 4}, 0, 4),
	})
	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Tensors[0].NumParameters != math.MaxInt64 {
		t.Errorf("NumParameters = %d, want it clamped to MaxInt64 rather than wrapped", info.Tensors[0].NumParameters)
	}
	if info.NumParameters < 0 {
		t.Errorf("NumParameters total = %d, want a non-negative value", info.NumParameters)
	}
	if !hasWarning(info, "64-bit integer") {
		t.Errorf("warnings = %v, want one reporting the unrepresentable element count", info.Warnings)
	}
}

func TestInspectSafetensorsHonoursContextCancellation(t *testing.T) {
	// Regression: the header was unmarshalled a value at a time, and one
	// json.Decode is a single uninterruptible call -- a header that is almost
	// entirely one tensor's shape spent seconds inside it with the caller's
	// deadline already blown. The scan must give up promptly instead.
	shape := make([]int64, 200_000)
	for i := range shape {
		shape[i] = 1
	}
	data := buildSafetensors(t, map[string]any{
		"wide": tensorEntry("F32", shape, 0, 4),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	size, fetch := fetcherFor(data)
	_, err := Inspect(ctx, FormatSafetensors, size, fetch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect error = %v, want it to wrap context.Canceled", err)
	}
	// A cancelled read is the caller giving up, not a bad file, and must not
	// be reported as one.
	if strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("Inspect error = %v, want it reported as an abandoned read rather than a malformed header", err)
	}

	// The same header still reads cleanly when nobody has given up.
	size, fetch = fetcherFor(data)
	if _, err := Inspect(context.Background(), FormatSafetensors, size, fetch); err != nil {
		t.Fatalf("Inspect with a live context: %v", err)
	}
}

func TestHeaderScannerScanIntsKeepsOnlyItsLimit(t *testing.T) {
	// The elements past the limit are counted and consumed but never kept, so
	// an oversized shape is not allocated even transiently.
	s := newHeaderScanner(context.Background(), []byte("[10,20,30,40,50]"))
	vals, count, bad, err := s.scanInts(2)
	if err != nil || bad {
		t.Fatalf("scanInts: err=%v bad=%v, want a clean read", err, bad)
	}
	if len(vals) != 2 || vals[0] != 10 || vals[1] != 20 {
		t.Errorf("kept %v, want the first two elements only", vals)
	}
	if count != 5 {
		t.Errorf("count = %d, want all 5 elements counted", count)
	}

	// A non-numeric element makes the record unusable, but the value is still
	// consumed so the scan stays in step.
	s = newHeaderScanner(context.Background(), []byte(`["a",1]`))
	if _, _, bad, err := s.scanInts(8); err != nil || !bad {
		t.Errorf("scanInts on a non-numeric array: err=%v bad=%v, want bad", err, bad)
	}
}

func TestInspectSafetensorsKeepsLargeIntegersExact(t *testing.T) {
	// The token scan reads numbers as json.Number rather than float64: a
	// dimension or offset past 2^53 would otherwise be silently rounded on the
	// way in, which is worse than reporting the entry as unreadable.
	const big = int64(1)<<53 + 1
	data := buildSafetensors(t, map[string]any{
		"weight": tensorEntry("F32", []int64{big}, 0, 4),
	})
	size, fetch := fetcherFor(data)
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := info.Tensors[0].Shape[0]; got != big {
		t.Errorf("Shape[0] = %d, want %d exactly", got, big)
	}
}

func TestInspectSafetensorsSkipsMalformedEntries(t *testing.T) {
	// The header scan reads entries token by token now, so the "one bad entry
	// is skipped, the rest of the header still reads" behaviour needs its own
	// cover: the scan has to consume exactly one value per key however that
	// value is shaped, or every later entry is lost with it.
	cases := map[string]string{
		"entry is a string":  `"nonsense"`,
		"entry is an array":  `[1,2,3]`,
		"entry is a number":  `42`,
		"shape is a string":  `{"dtype":"F32","shape":"wide","data_offsets":[0,4]}`,
		"shape holds a word": `{"dtype":"F32","shape":[2,"x"],"data_offsets":[0,4]}`,
		"dtype is an object": `{"dtype":{"a":1},"shape":[1],"data_offsets":[0,4]}`,
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			// "bad" sorts before "good", so a mishandled entry takes the good
			// one down with it rather than merely being dropped itself.
			header := fmt.Sprintf(
				`{"bad":%s,"good":{"dtype":"F32","shape":[2,2],"data_offsets":[0,16]},"__metadata__":{"format":"pt"}}`, bad)
			var buf bytes.Buffer
			_ = binary.Write(&buf, binary.LittleEndian, uint64(len(header)))
			buf.WriteString(header)
			buf.Write(make([]byte, 16))

			size, fetch := fetcherFor(buf.Bytes())
			info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.NumTensors != 1 || info.Tensors[0].Name != "good" {
				t.Fatalf("tensors = %+v, want only the well-formed one", info.Tensors)
			}
			if info.Metadata["format"] != "pt" {
				t.Errorf("metadata = %v, want the block that follows the entries", info.Metadata)
			}
			if !hasWarning(info, "unreadable header entry") {
				t.Errorf("warnings = %v, want one reporting the unreadable entry", info.Warnings)
			}
		})
	}
}

func TestInspectSafetensorsIgnoresUnknownEntryFields(t *testing.T) {
	// A field the reader has no use for must be skipped whole -- container or
	// not -- rather than confusing the scan's position.
	header := `{"weight":{"dtype":"F32","shape":[2,2],"extra":{"a":[1,2]},"data_offsets":[0,16],"note":"hi"}}`
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(header)))
	buf.WriteString(header)
	buf.Write(make([]byte, 16))

	size, fetch := fetcherFor(buf.Bytes())
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != 1 || info.Tensors[0].SizeBytes != 16 || len(info.Warnings) != 0 {
		t.Errorf("info = %+v, warnings = %v, want the entry read cleanly", info.Tensors, info.Warnings)
	}
}

// safetensorsWithHeader frames a hand-written JSON header as a safetensors
// file, with dataBytes of zeroed tensor data behind it.
func safetensorsWithHeader(header string, dataBytes int) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(header)))
	buf.WriteString(header)
	buf.Write(make([]byte, dataBytes))
	return buf.Bytes()
}

func TestInspectSafetensorsRejectsDeeplyNestedHeader(t *testing.T) {
	// Regression: the header is walked with json.Decoder.Token, whose stack
	// grows with the nesting it is asked to walk past and which has no depth
	// limit of its own -- unlike json.Decode, which stops at encoding/json's
	// own. A header that is nothing but `"extra":[[[...]]]` was therefore
	// accepted, at a cost of roughly twenty times its own size in memory.
	nest := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	entry := func(extra string) string {
		return fmt.Sprintf(`{"weight":{"dtype":"F32","shape":[2,2],"data_offsets":[0,16],"extra":%s}}`, extra)
	}

	// The root object and the tensor record are two levels already, so an
	// unknown field may nest maxHeaderDepth-2 further and no more.
	t.Run("at the limit", func(t *testing.T) {
		size, fetch := fetcherFor(safetensorsWithHeader(entry(nest(maxHeaderDepth-2)), 16))
		info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if info.NumTensors != 1 || len(info.Warnings) != 0 {
			t.Errorf("tensors = %d, warnings = %v, want the entry read cleanly", info.NumTensors, info.Warnings)
		}
	})

	// Objects rather than arrays, for the paths that only ever see one of the
	// two: an entry the scan is walking into, versus a value it is skipping.
	nestObjects := func(n int) string {
		return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n)
	}
	for name, header := range map[string]string{
		"in an unknown field": entry(nest(100_000)),
		"in a shape":          fmt.Sprintf(`{"weight":{"dtype":"F32","shape":%s,"data_offsets":[0,16]}}`, nest(100_000)),
		"in a tensor record":  nestObjects(100_000),
		"in the metadata":     fmt.Sprintf(`{"__metadata__":%s}`, nest(100_000)),
	} {
		t.Run(name, func(t *testing.T) {
			size, fetch := fetcherFor(safetensorsWithHeader(header, 16))
			info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
			if err == nil {
				t.Fatalf("Inspect accepted a header nesting 100000 deep: %+v", info)
			}
			if !errors.Is(err, errHeaderTooDeep) {
				t.Errorf("err = %v, want it to report the nesting limit", err)
			}
		})
	}
}

func TestInspectSafetensorsWarningsAreBoundedByBadEntries(t *testing.T) {
	// Regression: an entry the reader rejects is never added to the tensor
	// list, so it never counts against maxHeaderEntries -- and warning about
	// each one by name therefore put one warning per entry, each quoting a
	// name out of the file, into an Info that is then cached. Nothing in the
	// header was needed but the entries; a header full of them was a hundred
	// megabytes of warnings.
	const bad = 20_000
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < bad; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if i%2 == 0 {
			// Well-formed JSON, but not a usable record.
			fmt.Fprintf(&b, `"U%06d":"nonsense"`, i)
		} else {
			// A usable record naming a span the file does not contain.
			fmt.Fprintf(&b, `"O%06d":{"dtype":"F32","shape":[1],"data_offsets":[64,0]}`, i)
		}
	}
	b.WriteByte('}')

	size, fetch := fetcherFor(safetensorsWithHeader(b.String(), 16))
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.NumTensors != 0 {
		t.Errorf("num tensors = %d, want 0", info.NumTensors)
	}
	// One line per class of problem, however many entries were in the class.
	if len(info.Warnings) > 4 {
		t.Errorf("got %d warnings for %d bad entries, want a line per class of problem: %v",
			len(info.Warnings), bad, info.Warnings[:min(len(info.Warnings), 4)])
	}
	for _, want := range []string{
		fmt.Sprintf("%d tensors have an unreadable header entry", bad/2),
		fmt.Sprintf("%d tensors declare invalid data_offsets", bad/2),
	} {
		if !hasWarning(info, want) {
			t.Errorf("warnings = %v, want one reading %q", info.Warnings, want)
		}
	}
}

func TestInspectSafetensorsCapsTensorNameAndDType(t *testing.T) {
	// A tensor's name is a key in the header's JSON object and its dtype is
	// kept as written when the code is unrecognised, so both are as long as
	// the file cares to make them -- and both are then held by a cache entry.
	name := strings.Repeat("n", 200_000)
	dtype := strings.Repeat("D", 200_000)
	header := fmt.Sprintf(`{%q:{"dtype":%q,"shape":[2,2],"data_offsets":[0,16]}}`, name, dtype)

	size, fetch := fetcherFor(safetensorsWithHeader(header, 16))
	info, err := Inspect(context.Background(), FormatSafetensors, size, fetch)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(info.Tensors) != 1 {
		t.Fatalf("tensors = %+v, want one", info.Tensors)
	}
	got := info.Tensors[0]
	if n := utf8.RuneCountInString(got.Name); n > maxTensorNameRunes+1 {
		t.Errorf("name is %d runes, want at most %d plus the ellipsis", n, maxTensorNameRunes)
	}
	if n := utf8.RuneCountInString(got.DType); n > maxDTypeRunes+1 {
		t.Errorf("dtype is %d runes, want at most %d plus the ellipsis", n, maxDTypeRunes)
	}
	if !strings.HasPrefix(got.Name, "nnnn") || !strings.HasPrefix(got.DType, "dddd") {
		t.Errorf("name = %q, dtype = %q, want the head of each kept", got.Name, got.DType)
	}
	// The breakdown is keyed by the capped code, so it cannot smuggle the
	// uncapped one back into the entry.
	if len(info.DTypes) != 1 || info.DTypes[0].DType != got.DType {
		t.Errorf("dtypes = %+v, want the one capped code %q", info.DTypes, got.DType)
	}
}

func TestCapStringCutsOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("あ", 4_000)
	got := capString(long, maxTensorNameRunes)
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("capString split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != maxTensorNameRunes+1 {
		t.Errorf("cut to %d runes, want %d plus the ellipsis", n, maxTensorNameRunes)
	}
	if short := "あいう"; capString(short, maxTensorNameRunes) != short {
		t.Errorf("capString cut a string that was already short enough")
	}
	// Bytes are not runes: a value under the rune limit but over it in bytes
	// must come back whole.
	if fits := strings.Repeat("あ", maxDTypeRunes); capString(fits, maxDTypeRunes) != fits {
		t.Errorf("capString cut a value that was exactly at the rune limit")
	}
}

func TestSummarizeTruncatedListingDropsTheOversizedBackingArray(t *testing.T) {
	// Regression: the listing was cut down with tensors[:maxTensors], which
	// keeps the original array -- every tensor past the cut-off and every
	// name string it points at -- alive for as long as the cached entry lives.
	tensors := make([]Tensor, maxTensors*3)
	for i := range tensors {
		tensors[i] = Tensor{Name: fmt.Sprintf("t%06d", i), DType: "float32", NumParameters: 1, SizeBytes: 4}
	}
	info := &Info{}
	summarize(info, tensors)

	if !info.Truncated || len(info.Tensors) != maxTensors {
		t.Fatalf("listed %d tensors (truncated=%v), want %d and truncated", len(info.Tensors), info.Truncated, maxTensors)
	}
	if info.NumTensors != maxTensors*3 || info.NumParameters != int64(maxTensors*3) {
		t.Errorf("totals = %d tensors / %d params, want them to cover every tensor", info.NumTensors, info.NumParameters)
	}
	if c := cap(info.Tensors); c > 2*maxTensors {
		t.Errorf("cap(Tensors) = %d for a listing of %d, want the dropped tensors released", c, len(info.Tensors))
	}
}

func TestSummarizeCapsTheDTypeBreakdown(t *testing.T) {
	// One bucket per distinct dtype means the file decides how many buckets a
	// cached entry holds, so the breakdown is cut the way the listing is.
	tensors := make([]Tensor, maxDTypeStats*4)
	var wantParams int64
	for i := range tensors {
		tensors[i] = Tensor{
			Name:          fmt.Sprintf("t%06d", i),
			DType:         fmt.Sprintf("made-up-%d", i),
			NumParameters: int64(i),
		}
		wantParams += int64(i)
	}
	info := &Info{}
	summarize(info, tensors)

	if len(info.DTypes) != maxDTypeStats {
		t.Errorf("broke out %d dtypes, want %d", len(info.DTypes), maxDTypeStats)
	}
	if c := cap(info.DTypes); c > 2*maxDTypeStats {
		t.Errorf("cap(DTypes) = %d for %d buckets, want the dropped ones released", c, len(info.DTypes))
	}
	if info.NumParameters != wantParams {
		t.Errorf("params = %d, want %d: the totals cover every tensor even when the breakdown does not",
			info.NumParameters, wantParams)
	}
	if !hasWarning(info, "distinct dtypes") {
		t.Errorf("warnings = %v, want one reporting the cut breakdown", info.Warnings)
	}
}
