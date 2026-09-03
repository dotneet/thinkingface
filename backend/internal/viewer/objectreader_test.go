package viewer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// countingStorage records what a Reader actually asks the object store for.
// It is the instrument behind the tests that prove this package no longer
// downloads whole objects: every read has to show up here as a bounded range,
// and a call to Get (whole-object read) is a regression by definition.
type countingStorage struct {
	*memStorage

	rangeCalls atomic.Int64
	rangeBytes atomic.Int64
	statCalls  atomic.Int64
	getCalls   atomic.Int64
}

func newCountingStorage() *countingStorage {
	return &countingStorage{memStorage: newMemStorage()}
}

func (c *countingStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	c.getCalls.Add(1)
	return c.memStorage.Get(ctx, key)
}

func (c *countingStorage) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	rc, err := c.memStorage.GetRange(ctx, key, offset, length)
	if err != nil {
		return nil, err
	}
	c.rangeCalls.Add(1)
	if length < 0 {
		info, serr := c.memStorage.Stat(ctx, key)
		if serr == nil {
			length = info.Size - offset
		}
	}
	c.rangeBytes.Add(length)
	return rc, nil
}

func (c *countingStorage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	c.statCalls.Add(1)
	return c.memStorage.Stat(ctx, key)
}

func (c *countingStorage) reset() {
	c.rangeCalls.Store(0)
	c.rangeBytes.Store(0)
	c.statCalls.Store(0)
	c.getCalls.Store(0)
}

var _ storage.Storage = (*countingStorage)(nil)

// --- objectReader: the io.ReaderAt contract ---

// newTestObjectReader puts data at key and returns a reader over it whose
// cached tail is the last tail bytes.
func newTestObjectReader(t *testing.T, st storage.Storage, key string, data []byte, tail int) *objectReader {
	t.Helper()
	if tail > len(data) {
		tail = len(data)
	}
	if putter, ok := st.(interface {
		Put(ctx context.Context, key string, r io.Reader, contentType string) error
	}); ok {
		if err := putter.Put(context.Background(), key, bytes.NewReader(data), "application/octet-stream"); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	return &objectReader{
		ctx:        context.Background(),
		st:         st,
		key:        key,
		size:       int64(len(data)),
		tailOffset: int64(len(data) - tail),
		tail:       append([]byte(nil), data[len(data)-tail:]...),
	}
}

func TestObjectReader_ReadAtBoundaries(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}

	st := newCountingStorage()
	// A 30-byte tail: reads below offset 70 must go to storage, reads at or
	// above it must not.
	r := newTestObjectReader(t, st, "obj", data, 30)

	t.Run("whole object", func(t *testing.T) {
		buf := make([]byte, 100)
		n, err := r.ReadAt(buf, 0)
		if n != 100 || err != nil {
			t.Fatalf("ReadAt(100, 0) = (%d, %v), want (100, nil)", n, err)
		}
		if !bytes.Equal(buf, data) {
			t.Errorf("ReadAt returned wrong bytes")
		}
	})

	t.Run("straddles the cached tail", func(t *testing.T) {
		buf := make([]byte, 20)
		n, err := r.ReadAt(buf, 60)
		if n != 20 || err != nil {
			t.Fatalf("ReadAt(20, 60) = (%d, %v), want (20, nil)", n, err)
		}
		if !bytes.Equal(buf, data[60:80]) {
			t.Errorf("ReadAt(20, 60) returned wrong bytes")
		}
	})

	t.Run("past the end is short plus EOF", func(t *testing.T) {
		buf := make([]byte, 20)
		n, err := r.ReadAt(buf, 90)
		if n != 10 {
			t.Errorf("n = %d, want 10", n)
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want io.EOF", err)
		}
		if !bytes.Equal(buf[:n], data[90:]) {
			t.Errorf("ReadAt(20, 90) returned wrong bytes")
		}
	})

	t.Run("exactly to the end has no error", func(t *testing.T) {
		buf := make([]byte, 10)
		n, err := r.ReadAt(buf, 90)
		if n != 10 || err != nil {
			t.Fatalf("ReadAt(10, 90) = (%d, %v), want (10, nil)", n, err)
		}
	})

	t.Run("offset at or past size is EOF", func(t *testing.T) {
		buf := make([]byte, 4)
		for _, off := range []int64{100, 101, 1 << 40} {
			n, err := r.ReadAt(buf, off)
			if n != 0 || !errors.Is(err, io.EOF) {
				t.Errorf("ReadAt(4, %d) = (%d, %v), want (0, io.EOF)", off, n, err)
			}
		}
	})

	t.Run("zero-length read never fails", func(t *testing.T) {
		for _, off := range []int64{0, 50, 100, 1000} {
			n, err := r.ReadAt(nil, off)
			if n != 0 || err != nil {
				t.Errorf("ReadAt(0, %d) = (%d, %v), want (0, nil)", off, n, err)
			}
		}
	})

	t.Run("negative offset is an error", func(t *testing.T) {
		buf := make([]byte, 4)
		n, err := r.ReadAt(buf, -1)
		if n != 0 || !errors.Is(err, errNegativeOffset) {
			t.Errorf("ReadAt(4, -1) = (%d, %v), want (0, errNegativeOffset)", n, err)
		}
	})
}

func TestObjectReader_TailIsServedFromMemory(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	st := newCountingStorage()
	r := newTestObjectReader(t, st, "obj", data, 30)

	buf := make([]byte, 30)
	if _, err := r.ReadAt(buf, 70); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(buf, data[70:]) {
		t.Fatalf("tail read returned wrong bytes")
	}
	if got := st.rangeCalls.Load(); got != 0 {
		t.Errorf("reads inside the cached tail made %d range requests, want 0", got)
	}

	// A read straddling the boundary fetches only the part below it.
	if _, err := r.ReadAt(make([]byte, 40), 60); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if got := st.rangeCalls.Load(); got != 1 {
		t.Errorf("straddling read made %d range requests, want 1", got)
	}
	if got := st.rangeBytes.Load(); got != 10 {
		t.Errorf("straddling read fetched %d bytes, want 10 (only the part below the tail)", got)
	}
}

func TestObjectReader_ConcurrentReadAt(t *testing.T) {
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i * 7)
	}
	st := newCountingStorage()
	r := newTestObjectReader(t, st, "obj", data, 512)

	const goroutines = 32
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 32 {
				off := int64((g*97 + i*31) % len(data))
				length := 1 + (i*13)%600
				buf := make([]byte, length)
				n, err := r.ReadAt(buf, off)
				if err != nil && !errors.Is(err, io.EOF) {
					errs <- fmt.Errorf("ReadAt(%d, %d): %v", length, off, err)
					return
				}
				if !bytes.Equal(buf[:n], data[off:off+int64(n)]) {
					errs <- fmt.Errorf("ReadAt(%d, %d) returned wrong bytes", length, off)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- tailCache ---

// fakeParquetBytes returns n bytes that pass fetchTail's checks -- a trailer
// whose declared footer length fits in the object, and the magic -- without
// being a real parquet file. Enough to exercise the cache itself.
func fakeParquetBytes(n int) []byte {
	return fakeParquetBytesWithFooterLen(n, uint32(n-minParquetFileSize))
}

// fakeParquetBytesWithFooterLen is fakeParquetBytes with the declared footer
// length spelled out, so a test can craft the one the footer trailer is not
// allowed to carry.
func fakeParquetBytesWithFooterLen(n int, footerLen uint32) []byte {
	b := bytes.Repeat([]byte("x"), n)
	binary.LittleEndian.PutUint32(b[n-footerTrailerSize:], footerLen)
	copy(b[n-4:], "PAR1")
	return b
}

// TestFetchTail_RejectsNonParquet covers the check that lets openParquetFile
// skip parquet-go's own magic-header read (and the round trip it costs).
func TestFetchTail_RejectsNonParquet(t *testing.T) {
	ctx := context.Background()
	st := newMemStorage()
	putParquet(t, st, "junk", bytes.Repeat([]byte("x"), 1000))
	putParquet(t, st, "encrypted", append(bytes.Repeat([]byte("x"), 996), []byte("PARE")...))
	putParquet(t, st, "empty", nil)

	for _, tc := range []struct{ key, want string }{
		{"junk", "not a parquet file"},
		{"encrypted", "encrypted footer"},
		{"empty", "is empty"},
	} {
		_, err := fetchTail(ctx, st, tc.key)
		if err == nil {
			t.Errorf("fetchTail(%s) succeeded, want an error mentioning %q", tc.key, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("fetchTail(%s) = %v, want an error mentioning %q", tc.key, err, tc.want)
		}
	}
}

// TestFetchTail_RejectsOversizedFooterLength covers the crafted-footer denial
// of service: the 4 bytes in front of the trailing magic are the footer's
// length and parquet-go allocates exactly that many bytes before reading them,
// so an unvalidated 0xFFFFFFFF there is a 4 GiB allocation -- a fatal runtime
// error no recover can catch -- driven by a file any user can push.
func TestFetchTail_RejectsOversizedFooterLength(t *testing.T) {
	ctx := context.Background()
	st := newMemStorage()

	const size = 1000
	cases := map[string]uint32{
		"far past the end":   1 << 29, // 512 MiB, from a 1000-byte object
		"whole 32-bit range": ^uint32(0),
		"one byte too long":  size - footerTrailerSize + 1,
		"the whole object":   size, // the footer and its trailer cannot both fit
	}
	for name, footerLen := range cases {
		t.Run(name, func(t *testing.T) {
			key := "crafted-" + name
			putParquet(t, st, key, fakeParquetBytesWithFooterLen(size, footerLen))
			_, err := fetchTail(ctx, st, key)
			if err == nil {
				t.Fatalf("fetchTail accepted a footer length of %d in a %d-byte object", footerLen, size)
			}
			if !strings.Contains(err.Error(), "footer length") {
				t.Fatalf("fetchTail = %v, want an error mentioning the footer length", err)
			}
		})
	}

	// The largest footer that does fit is still accepted: the check is a
	// bound, not a new minimum size.
	putParquet(t, st, "ok", fakeParquetBytesWithFooterLen(size, size-footerTrailerSize))
	if _, err := fetchTail(ctx, st, "ok"); err != nil {
		t.Fatalf("fetchTail rejected a footer that exactly fills the object: %v", err)
	}
}

// TestFetchTail_RejectsTruncatedObject guards the read of that same trailer:
// an object too short to hold one must be rejected before it is indexed into.
func TestFetchTail_RejectsTruncatedObject(t *testing.T) {
	ctx := context.Background()
	st := newMemStorage()
	for _, n := range []int{4, 8, minParquetFileSize - 1} {
		key := fmt.Sprintf("short-%d", n)
		// Ends with the magic, so only the length check can reject it.
		putParquet(t, st, key, append(bytes.Repeat([]byte("x"), n-4), []byte("PAR1")...))
		if _, err := fetchTail(ctx, st, key); err == nil {
			t.Errorf("fetchTail accepted a %d-byte object", n)
		}
	}
}

// TestSchema_CraftedFooterAllocatesNothing is the end-to-end half: the crafted
// file goes through the same Reader the API and the syncer use, and the
// process neither allocates the declared footer nor dies trying.
func TestSchema_CraftedFooterAllocatesNothing(t *testing.T) {
	st := newMemStorage()
	const key = "lfs/cr/af/crafted.parquet"
	// 512 MiB: unmistakably an attack on a 1000-byte object, yet small enough
	// that a machine running this test survives the regression it guards.
	putParquet(t, st, key, fakeParquetBytesWithFooterLen(1000, 1<<29))

	r := New(st, 64<<20)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := r.Schema(context.Background(), key)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("Schema accepted a file whose footer length overruns it")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Errorf("rejecting a 1000-byte crafted file allocated %d bytes; want a bounded amount", grew)
	}
}

// --- the footer's other declared lengths: the page index ---

// rewriteFooter re-encodes data's footer with mutate applied to it, producing
// a file that is well-formed everywhere except where the test bent it.
func rewriteFooter(t *testing.T, data []byte, mutate func(*format.FileMetaData)) []byte {
	t.Helper()
	size := int64(binary.LittleEndian.Uint32(data[len(data)-footerTrailerSize : len(data)-4]))
	start := int64(len(data)) - (size + footerTrailerSize)

	var md format.FileMetaData
	if err := thrift.Unmarshal(new(thrift.CompactProtocol), data[start:start+size], &md); err != nil {
		t.Fatalf("decode footer: %v", err)
	}
	mutate(&md)
	enc, err := thrift.Marshal(new(thrift.CompactProtocol), &md)
	if err != nil {
		t.Fatalf("encode footer: %v", err)
	}

	out := make([]byte, 0, int(start)+len(enc)+footerTrailerSize)
	out = append(out, data[:start]...)
	out = append(out, enc...)
	var trailer [footerTrailerSize]byte
	binary.LittleEndian.PutUint32(trailer[:4], uint32(len(enc)))
	copy(trailer[4:], "PAR1")
	return append(out, trailer[:]...)
}

// TestSchema_RejectsCraftedPageIndexLengths is the footer-length hazard one
// layer in: ReadPageIndex sums every column chunk's declared ColumnIndexLength
// and OffsetIndexLength and allocates that much before reading, so a file a
// couple of kilobytes long can ask for gigabytes. The declared lengths here
// come to ~960 MiB -- unmistakably an attack on a file this size, and small
// enough that a machine running this test survives the regression.
func TestSchema_RejectsCraftedPageIndexLengths(t *testing.T) {
	base := buildRunGroupedParquet(t, pruneRuns)

	// The round trip through thrift on its own must leave a readable file, or
	// the cases below would prove nothing.
	untouched := rewriteFooter(t, base, func(*format.FileMetaData) {})
	st := newMemStorage()
	putParquet(t, st, "sane", untouched)
	if _, err := New(st, 64<<20).Schema(context.Background(), "sane"); err != nil {
		t.Fatalf("re-encoding the footer unchanged broke the file: %v", err)
	}

	cases := map[string]func(*format.FileMetaData){
		"column index length": func(md *format.FileMetaData) {
			for i := range md.RowGroups {
				for j := range md.RowGroups[i].Columns {
					md.RowGroups[i].Columns[j].ColumnIndexLength = 1 << 26
				}
			}
		},
		"offset index length": func(md *format.FileMetaData) {
			for i := range md.RowGroups {
				for j := range md.RowGroups[i].Columns {
					md.RowGroups[i].Columns[j].OffsetIndexLength = 1 << 26
				}
			}
		},
		"column index offset past the end": func(md *format.FileMetaData) {
			md.RowGroups[0].Columns[0].ColumnIndexOffset = 1 << 40
		},
		// ReadPageIndex indexes its result by the first row group's column
		// count, so a row group that disagrees walks off the end of the slice.
		"row groups disagree on their column count": func(md *format.FileMetaData) {
			c := md.RowGroups[1].Columns[0]
			md.RowGroups[1].Columns = append(md.RowGroups[1].Columns, c, c)
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := newMemStorage()
			const key = "lfs/cr/af/page-index.parquet"
			putParquet(t, st, key, rewriteFooter(t, base, mutate))

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := New(st, 64<<20).Schema(context.Background(), key)
			runtime.ReadMemStats(&after)

			if err == nil {
				t.Fatal("Schema accepted a footer whose page index cannot exist")
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
				t.Errorf("rejecting the file allocated %d bytes; want a bounded amount", grew)
			}
		})
	}
}

func TestCheckPageIndexSections(t *testing.T) {
	const size = 1000
	chunk := func(colOff, colLen, offOff, offLen int64) format.ColumnChunk {
		return format.ColumnChunk{
			ColumnIndexOffset: colOff,
			ColumnIndexLength: int32(colLen),
			OffsetIndexOffset: offOff,
			OffsetIndexLength: int32(offLen),
		}
	}
	md := func(groups ...[]format.ColumnChunk) *format.FileMetaData {
		out := &format.FileMetaData{}
		for _, g := range groups {
			out.RowGroups = append(out.RowGroups, format.RowGroup{Columns: g})
		}
		return out
	}

	cases := map[string]struct {
		md   *format.FileMetaData
		want bool // true = must be rejected
	}{
		"no row groups":     {md(), false},
		"sections fit":      {md([]format.ColumnChunk{chunk(100, 50, 200, 50)}), false},
		"no page index":     {md([]format.ColumnChunk{chunk(0, 0, 0, 0)}), false},
		"ends exactly":      {md([]format.ColumnChunk{chunk(950, 50, 0, 0)}), false},
		"one byte past":     {md([]format.ColumnChunk{chunk(950, 51, 0, 0)}), true},
		"offset index past": {md([]format.ColumnChunk{chunk(0, 0, 950, 51)}), true},
		"negative length": {&format.FileMetaData{RowGroups: []format.RowGroup{{
			Columns: []format.ColumnChunk{{ColumnIndexOffset: 100, ColumnIndexLength: -1}},
		}}}, true},
		// offset+length would wrap negative and slip past a naive
		// "offset+length <= size".
		"offset near MaxInt64": {md([]format.ColumnChunk{chunk(math.MaxInt64-10, 1<<20, 0, 0)}), true},
		// A length that fits nowhere is rejected even with the offset zeroed:
		// ReadPageIndex sums the lengths of every chunk, absent or not.
		"length without an offset":  {md([]format.ColumnChunk{chunk(1, 10, 0, 0), chunk(0, 1<<20, 0, 0)}), true},
		"row group with no columns": {md([]format.ColumnChunk{}), true},
		"column counts disagree": {md(
			[]format.ColumnChunk{chunk(100, 10, 0, 0)},
			[]format.ColumnChunk{chunk(100, 10, 0, 0), chunk(110, 10, 0, 0)},
		), true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := checkPageIndexSections(tc.md, size)
			if got := err != nil; got != tc.want {
				t.Fatalf("checkPageIndexSections = %v, want rejected=%v", err, tc.want)
			}
		})
	}
}

func TestTailCache_ReusesAndEvicts(t *testing.T) {
	ctx := context.Background()
	st := newCountingStorage()
	blob := fakeParquetBytes(1000)
	for _, key := range []string{"a", "b", "c"} {
		putParquet(t, st.memStorage, key, blob)
	}

	// Each tail is the whole 1000-byte object (below minFooterProbe), so a
	// budget of 2500 holds two of them.
	c := newTailCache(2500)

	first, err := c.load(ctx, st, "a")
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	if first.size != 1000 || first.tailOffset != 0 || len(first.tail) != 1000 {
		t.Fatalf("entry a = {size:%d off:%d tail:%d}, want {1000 0 1000}", first.size, first.tailOffset, len(first.tail))
	}
	if got := st.rangeCalls.Load(); got != 1 {
		t.Fatalf("cold load made %d range requests, want 1", got)
	}

	st.reset()
	if _, err := c.load(ctx, st, "a"); err != nil {
		t.Fatalf("reload a: %v", err)
	}
	if st.rangeCalls.Load() != 0 || st.statCalls.Load() != 0 {
		t.Errorf("warm load hit storage: %d ranges, %d stats; want 0 and 0",
			st.rangeCalls.Load(), st.statCalls.Load())
	}

	// b fits alongside a; c pushes the least recently used (a) out.
	if _, err := c.load(ctx, st, "b"); err != nil {
		t.Fatalf("load b: %v", err)
	}
	if _, err := c.load(ctx, st, "c"); err != nil {
		t.Fatalf("load c: %v", err)
	}
	if c.bytes > c.maxBytes {
		t.Errorf("cache holds %d bytes, over its %d budget", c.bytes, c.maxBytes)
	}
	if _, ok := c.entries["a"]; ok {
		t.Errorf("a should have been evicted as least recently used")
	}
	for _, key := range []string{"b", "c"} {
		if _, ok := c.entries[key]; !ok {
			t.Errorf("%s should still be cached", key)
		}
	}
}

func TestTailCache_DisabledStillWorks(t *testing.T) {
	ctx := context.Background()
	st := newCountingStorage()
	putParquet(t, st.memStorage, "a", fakeParquetBytes(1000))

	c := newTailCache(0)
	for range 2 {
		e, err := c.load(ctx, st, "a")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(e.tail) != 1000 {
			t.Fatalf("tail = %d bytes, want 1000", len(e.tail))
		}
	}
	if got := st.rangeCalls.Load(); got != 2 {
		t.Errorf("with caching off, 2 loads made %d range requests, want 2", got)
	}
	if c.bytes != 0 || len(c.entries) != 0 {
		t.Errorf("disabled cache retained %d bytes in %d entries", c.bytes, len(c.entries))
	}
}

func TestTailCache_EntryLargerThanBudgetIsNotCached(t *testing.T) {
	ctx := context.Background()
	st := newCountingStorage()
	putParquet(t, st.memStorage, "a", fakeParquetBytes(1000))

	c := newTailCache(100)
	if _, err := c.load(ctx, st, "a"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.bytes != 0 || len(c.entries) != 0 {
		t.Fatalf("cache exceeded its %d-byte budget: %d bytes in %d entries", c.maxBytes, c.bytes, len(c.entries))
	}
}

func TestFooterProbeSize(t *testing.T) {
	cases := []struct {
		size int64
		want int64
	}{
		{size: 512, want: 512},                 // smaller than the floor: the whole object
		{size: 1 << 20, want: minFooterProbe},  // small file: the floor
		{size: 10 << 20, want: (10 << 20) / 8}, // scales with the object
		{size: 10 << 30, want: maxFooterProbe}, // 10 GB: capped, not proportional
		{size: minFooterProbe, want: minFooterProbe},
	}
	for _, tc := range cases {
		if got := footerProbeSize(tc.size); got != tc.want {
			t.Errorf("footerProbeSize(%d) = %d, want %d", tc.size, got, tc.want)
		}
	}
}

// --- the point of this package: metadata reads must not download the file ---

type wideRow struct {
	ID    int64   `parquet:"id"`
	Score float64 `parquet:"score"`
	Name  string  `parquet:"name"`
}

// buildLargeParquet returns a parquet file of at least minBytes, split into
// several row groups so that it has a page index worth reading.
func buildLargeParquet(t *testing.T, minBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[wideRow](&buf, parquet.MaxRowsPerRowGroup(20000))

	batch := make([]wideRow, 5000)
	for id := int64(0); buf.Len() < minBytes; id += int64(len(batch)) {
		for i := range batch {
			batch[i] = wideRow{
				ID:    id + int64(i),
				Score: float64(id+int64(i)) * 1.5,
				// Unique, incompressible-ish text so the file actually grows
				// instead of collapsing into a dictionary.
				Name: fmt.Sprintf("row-%012d-%s", id+int64(i), "abcdefghijklmnopqrstuvwxyz0123456789"),
			}
		}
		if _, err := w.Write(batch); err != nil {
			t.Fatalf("write rows: %v", err)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

// TestSchemaDoesNotDownloadTheWholeObject is the regression test this package
// was rewritten for. Reading a 10 MB file's schema used to transfer all 10 MB
// (into a memory-backed cache directory, on Cloud Run); a 10 GB dataset --
// which this hub is expected to hold -- took the process down with it, on
// every push, because the syncer indexes every parquet file it sees.
func TestSchemaDoesNotDownloadTheWholeObject(t *testing.T) {
	data := buildLargeParquet(t, 10<<20)
	size := int64(len(data))

	st := newCountingStorage()
	const key = "lfs/bi/gg/big.parquet"
	putParquet(t, st.memStorage, key, data)

	r := New(st, 64<<20)
	ctx := context.Background()

	st.reset()
	sch, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if sch.SizeBytes != size {
		t.Fatalf("SizeBytes = %d, want %d", sch.SizeBytes, size)
	}
	if len(sch.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(sch.Columns))
	}
	if sch.NumRowGroups < 2 {
		t.Fatalf("test file has %d row groups, want several", sch.NumRowGroups)
	}

	coldCalls := st.rangeCalls.Load()
	coldBytes := st.rangeBytes.Load()
	t.Logf("cold Schema() on a %.1f MiB file: %d range requests, %d stat, %.2f MiB read (%.1f%% of the file)",
		float64(size)/(1<<20), coldCalls, st.statCalls.Load(),
		float64(coldBytes)/(1<<20), 100*float64(coldBytes)/float64(size))

	if got := st.getCalls.Load(); got != 0 {
		t.Errorf("Schema() called Get (whole-object read) %d times; it must only use GetRange", got)
	}
	// The budget: one probe of the object's tail, plus at most a couple of
	// small reads (the 4-byte magic header, a page index that fell outside
	// the probe). Anything close to the file size means the download is back.
	if coldBytes > footerProbeSize(size)+(1<<20) {
		t.Errorf("Schema() read %d bytes of a %d-byte file; want no more than the %d-byte footer probe plus slack",
			coldBytes, size, footerProbeSize(size))
	}
	if coldBytes*4 > size {
		t.Errorf("Schema() read %d bytes, more than a quarter of the %d-byte file", coldBytes, size)
	}
	if coldCalls > 4 {
		t.Errorf("Schema() made %d range requests, want a handful", coldCalls)
	}

	// Opening the same object again must be free: the tail is cached.
	st.reset()
	if _, err := r.Schema(ctx, key); err != nil {
		t.Fatalf("second Schema: %v", err)
	}
	if st.rangeCalls.Load() != 0 || st.statCalls.Load() != 0 {
		t.Errorf("warm Schema() hit storage: %d range requests, %d stats; want 0 and 0",
			st.rangeCalls.Load(), st.statCalls.Load())
	}
}

// TestRowsReadsOnlyTheRequestedPage checks the other half: a 50-row preview
// of a large file reads a small fraction of it, and only the columns asked
// for.
func TestRowsReadsOnlyTheRequestedPage(t *testing.T) {
	data := buildLargeParquet(t, 10<<20)
	size := int64(len(data))

	st := newCountingStorage()
	const key = "lfs/bi/gg/big.parquet"
	putParquet(t, st.memStorage, key, data)

	r := New(st, 64<<20)
	ctx := context.Background()

	// Warm the metadata cache so the measurement below is page reads only.
	if _, err := r.Schema(ctx, key); err != nil {
		t.Fatalf("Schema: %v", err)
	}

	st.reset()
	res, err := r.Rows(ctx, key, 0, 50, []string{"id"})
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 50 {
		t.Fatalf("got %d rows, want 50", len(res.Rows))
	}
	if v, ok := res.Rows[0]["id"].(int64); !ok || v != 0 {
		t.Errorf("row0[id] = %#v, want int64(0)", res.Rows[0]["id"])
	}

	read := st.rangeBytes.Load()
	t.Logf("50-row preview of a %.1f MiB file: %d range requests, %.2f MiB read (%.1f%%)",
		float64(size)/(1<<20), st.rangeCalls.Load(), float64(read)/(1<<20), 100*float64(read)/float64(size))

	if got := st.getCalls.Load(); got != 0 {
		t.Errorf("Rows() called Get (whole-object read) %d times", got)
	}
	// A single column's pages for 50 rows measure well under 1% of the file;
	// an eighth leaves an order of magnitude of headroom and still fails
	// loudly if a change starts pulling whole column chunks or the file.
	if read > size/8 {
		t.Errorf("a 50-row preview read %d bytes of a %d-byte file; want at most %d",
			read, size, size/8)
	}
}
