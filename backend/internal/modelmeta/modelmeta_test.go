package modelmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	root, err := newUnpickler(bytes.NewReader(buildCyclicTorchPickle(8)), torchReduce).load()
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
