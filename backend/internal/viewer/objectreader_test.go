package viewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/parquet-go/parquet-go"

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

// fakeParquetBytes returns n bytes that pass fetchTail's magic check without
// being a real parquet file -- enough to exercise the cache itself.
func fakeParquetBytes(n int) []byte {
	return append(bytes.Repeat([]byte("x"), n-4), []byte("PAR1")...)
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
