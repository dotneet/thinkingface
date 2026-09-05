package viewer

import (
	"bytes"
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

const (
	// minFooterProbe / maxFooterProbe bound how many bytes are pulled from
	// the end of an object in the opening round trip.
	//
	// The upper bound is 4 MiB because that is the value parquet-go's own
	// ReadBufferSize documentation recommends for network-backed readers: it
	// is large enough that a footer plus a page index normally lands in a
	// single range request, and small enough to keep per-open memory
	// bounded. The lower bound keeps small files from degenerating into a
	// read-8-bytes-then-read-the-footer round trip pair.
	minFooterProbe = 128 << 10
	maxFooterProbe = 4 << 20

	// footerProbeDivisor scales the probe with the object between those
	// bounds. The syncer calls Schema() on *every* parquet file of *every*
	// push, so a flat 4 MiB probe would mean 4 MiB of transfer and heap per
	// small file; a fraction of the object is both cheaper for small files
	// and still a rounding error on a 10 GB one.
	footerProbeDivisor = 8

	// footerTrailerSize is the fixed trailer every parquet file ends with:
	// a 4-byte little-endian footer length followed by the 4-byte magic.
	footerTrailerSize = 8

	// minParquetFileSize is the smallest byte count that can be a parquet
	// file at all: the 4-byte header magic plus the trailer above. Anything
	// shorter cannot carry a footer length, so it is rejected before the
	// trailer is read rather than indexed past the start of the buffer.
	minParquetFileSize = 4 + footerTrailerSize
)

// errNegativeOffset is returned by objectReader.ReadAt, which -- like every
// io.ReaderAt -- rejects a negative offset instead of translating it into a
// range request the object store would reject with a confusing error.
var errNegativeOffset = errors.New("viewer: ReadAt: negative offset")

// footerProbeSize returns how many bytes to read from the end of an object of
// the given size in the first round trip. It is also handed to parquet-go as
// its ReadBufferSize, so that the optimistic footer read parquet.OpenFile
// issues (ReadBufferSize bytes ending at EOF) matches the cached tail exactly
// and costs nothing.
func footerProbeSize(size int64) int64 {
	n := size / footerProbeDivisor
	if n < minFooterProbe {
		n = minFooterProbe
	}
	if n > maxFooterProbe {
		n = maxFooterProbe
	}
	if n > size {
		n = size
	}
	return n
}

// objectReader is an io.ReaderAt over one object in a storage.Storage,
// serving every read with a ranged GET instead of a whole-object download.
//
// The trailing footerProbeSize(size) bytes of the object -- footer, page
// index, and usually the last row group's pages for a small file -- are
// fetched once and held in tail, so metadata reads never reach the network
// twice. Everything before tailOffset is fetched on demand, which is what
// keeps a 10 GB parquet from ever being materialised in this process.
//
// All fields are set at construction and never mutated afterwards, so ReadAt
// is safe to call concurrently -- parquet-go does exactly that when the file
// is opened with ReadModeAsync.
type objectReader struct {
	// ctx is captured when the file is opened and reused for every range
	// request the returned *parquet.File makes, because io.ReaderAt has
	// nowhere to pass one. The *parquet.File never outlives the Schema /
	// Rows / Scan call that opened it, so its lifetime matches ctx's.
	ctx context.Context
	st  storage.Storage
	key string

	// size is the object's full length, from its cached tailEntry.
	size int64
	// tail holds the object's bytes from tailOffset to size.
	tailOffset int64
	tail       []byte
}

var _ io.ReaderAt = (*objectReader)(nil)

// ReadAt implements io.ReaderAt against the object store.
//
// It honours the io.ReaderAt contract: a read that runs into the end of the
// object returns the bytes it could read together with io.EOF, and a read
// that starts at or past the end returns (0, io.EOF). A zero-length read is
// always (0, nil) -- it costs no request and reports no error, so callers
// probing with an empty buffer cannot be told the object is short.
func (o *objectReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errNegativeOffset
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= o.size {
		return 0, io.EOF
	}

	// Clamp to the end of the object; a short read is reported as io.EOF
	// alongside the bytes that were readable.
	var err error
	want := p
	if int64(len(want)) > o.size-off {
		want = want[:o.size-off]
		err = io.EOF
	}

	n := 0
	// The part of the range that lies before the cached tail has to be
	// fetched. A range straddling tailOffset is split rather than refetched
	// in full.
	if off < o.tailOffset {
		head := want
		if int64(len(head)) > o.tailOffset-off {
			head = head[:o.tailOffset-off]
		}
		if rerr := o.readRange(head, off); rerr != nil {
			return 0, rerr
		}
		n = len(head)
	}
	// Whatever is left is inside the tail, and costs a memcpy.
	if n < len(want) {
		copy(want[n:], o.tail[off+int64(n)-o.tailOffset:])
		n = len(want)
	}
	return n, err
}

// readRange fills p from [off, off+len(p)) of the object with one range
// request. A short answer is an error: the caller has already clamped the
// range to the object's size, so anything less means a truncated transfer.
func (o *objectReader) readRange(p []byte, off int64) error {
	rc, err := o.st.GetRange(o.ctx, o.key, off, int64(len(p)))
	if err != nil {
		return fmt.Errorf("viewer: range read %s [%d,%d): %w", o.key, off, off+int64(len(p)), err)
	}
	defer rc.Close()

	if _, err := io.ReadFull(rc, p); err != nil {
		return fmt.Errorf("viewer: range read %s [%d,%d): %w", o.key, off, off+int64(len(p)), err)
	}
	return nil
}

// tailEntry is one cached object tail: everything an open needs before it
// touches the network.
type tailEntry struct {
	key string
	// size is the object's full length, cached so that a warm open does not
	// even have to Stat. Parquet objects are addressed by content hash
	// (storage.LFSKey / storage.BlobKey), so a key's bytes never change.
	size       int64
	tailOffset int64
	tail       []byte
}

// tailCache is an LRU over object tails, bounded by the total number of bytes
// held. It exists because the same object is opened over and over -- the UI
// pages through rows one request at a time and the syncer re-indexes on every
// push -- and refetching the footer each time is a round trip per open.
//
// The budget is on this process's heap, not on disk: on Cloud Run the
// container filesystem is memory-backed, so "cache it on disk" was never the
// cheaper option it looks like.
type tailCache struct {
	maxBytes int64

	mu      sync.Mutex
	bytes   int64
	order   *list.List // front = most recently used; values are *tailEntry
	entries map[string]*list.Element

	// group collapses concurrent opens of the same key into one fetch.
	group singleflight.Group
}

// newTailCache returns a tailCache holding at most maxBytes of object tails.
// A maxBytes <= 0 disables caching entirely: every open then refetches its
// tail, which is slower but never grows the heap. It is deliberately not
// read as "unlimited" -- an unbounded metadata cache is the failure this
// package exists to avoid.
func newTailCache(maxBytes int64) *tailCache {
	return &tailCache{
		maxBytes: maxBytes,
		order:    list.New(),
		entries:  make(map[string]*list.Element),
	}
}

// tailFetchTimeout bounds one cold tail fetch: a Stat plus a single ranged
// read of at most maxFooterProbe bytes. It is far past what those two storage
// round trips cost and short enough that a hung one still ends inside the
// handler deadline of every route that opens a parquet file.
const tailFetchTimeout = 30 * time.Second

// load returns the cached tail of key, fetching it if necessary.
func (c *tailCache) load(ctx context.Context, st storage.Storage, key string) (*tailEntry, error) {
	if e := c.lookup(key); e != nil {
		return e, nil
	}

	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check now that we hold the singleflight slot: another caller
		// may have finished fetching while we waited.
		if e := c.lookup(key); e != nil {
			return e, nil
		}
		// The fetch runs on a context of its own rather than the caller's.
		// singleflight collapses concurrent opens of one key into whichever
		// caller happens to execute this function, so the fetch's lifetime
		// would otherwise belong to one request while its result serves
		// several: that request going away (a user paging past a parquet
		// file) would cancel the read out from under the requests still
		// waiting on it, failing all of them at once. WithoutCancel keeps the
		// caller's values (the request id the storage layer logs with) but
		// not its cancellation, and the timeout above is what ends a fetch
		// whose storage backend has stopped answering.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tailFetchTimeout)
		defer cancel()
		e, err := fetchTail(fetchCtx, st, key)
		if err != nil {
			return nil, err
		}
		c.store(e)
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tailEntry), nil
}

func (c *tailCache) lookup(key string) *tailEntry {
	if c.maxBytes <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.order.MoveToFront(el)
	return el.Value.(*tailEntry)
}

// store inserts e, evicting least-recently-used entries until the cache fits
// its budget again. An entry that on its own exceeds the budget is not
// cached: unlike the disk cache this replaced, the budget is never exceeded
// "just this once".
func (c *tailCache) store(e *tailEntry) {
	if c.maxBytes <= 0 {
		return
	}
	n := int64(len(e.tail))
	if n > c.maxBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[e.key]; ok {
		c.bytes -= int64(len(el.Value.(*tailEntry).tail))
		c.order.Remove(el)
		delete(c.entries, e.key)
	}
	for c.bytes+n > c.maxBytes {
		back := c.order.Back()
		if back == nil {
			break
		}
		old := back.Value.(*tailEntry)
		c.order.Remove(back)
		delete(c.entries, old.key)
		c.bytes -= int64(len(old.tail))
	}

	c.entries[e.key] = c.order.PushFront(e)
	c.bytes += n
}

// fetchTail stats key and reads its trailing footerProbeSize bytes. That is
// two requests against the object store, and the only two a cold Schema()
// needs beyond the 4-byte magic header check.
func fetchTail(ctx context.Context, st storage.Storage, key string) (*tailEntry, error) {
	info, err := st.Stat(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("viewer: stat %s: %w", key, err)
	}
	if info.Size <= 0 {
		return nil, fmt.Errorf("viewer: %s is empty", key)
	}
	if info.Size < minParquetFileSize {
		return nil, fmt.Errorf("viewer: %s is not a parquet file (only %d bytes)", key, info.Size)
	}

	n := footerProbeSize(info.Size)
	off := info.Size - n
	buf := make([]byte, n)

	rc, err := st.GetRange(ctx, key, off, n)
	if err != nil {
		return nil, fmt.Errorf("viewer: read footer of %s: %w", key, err)
	}
	defer rc.Close()
	if _, err := io.ReadFull(rc, buf); err != nil {
		return nil, fmt.Errorf("viewer: read footer of %s: %w", key, err)
	}

	// Validate the file's magic here, from bytes already in hand, rather than
	// letting parquet.OpenFile read the 4-byte header at offset 0: that read
	// is a whole round trip against the object store, and it is paid on every
	// open, including the ones the tail cache otherwise answers for free.
	// Opening with SkipMagicBytes is only safe because of this check --
	// notably, parquet-go dereferences a nil decryption config on a "PARE"
	// (encrypted-footer) file when the header check is skipped, so an
	// encrypted file has to be rejected before it gets there.
	switch {
	case bytes.HasSuffix(buf, []byte("PAR1")):
	case bytes.HasSuffix(buf, []byte("PARE")):
		return nil, fmt.Errorf("viewer: %s has an encrypted footer, which is not supported", key)
	default:
		return nil, fmt.Errorf("viewer: %s is not a parquet file (bad footer magic)", key)
	}

	// The 4 bytes in front of that magic are the footer's length, and they are
	// the one number in a parquet file that a writer controls completely.
	// parquet.OpenFile allocates a buffer of exactly that length *before* it
	// reads the bytes -- and therefore before the range read can discover they
	// are not there -- so a footer length of 0xFFFFFFFF is a 4 GiB
	// make([]byte, n) driven by four attacker-chosen bytes. An allocation that
	// size is a fatal runtime error rather than a panic, so no recover
	// middleware catches it, and the syncer and the experiments indexer reach
	// this code from background workers where there is no request to fail
	// anyway: the process goes down. Validating the length here, against bytes
	// already in hand, is what keeps that allocation bounded by the object's
	// real size.
	//
	// Only a length past the cached tail can allocate at all -- a footer of up
	// to len(tail)-8 bytes is sliced straight out of the buffer parquet-go
	// already read, because openParquetFile hands it footerProbeSize as its
	// ReadBufferSize and that is exactly len(tail) -- but the check is applied
	// to every file, since a footer that overruns the object is malformed
	// whichever side of the tail boundary it falls on.
	footerSize := int64(binary.LittleEndian.Uint32(buf[len(buf)-footerTrailerSize : len(buf)-4]))
	if footerSize+footerTrailerSize > info.Size {
		return nil, fmt.Errorf("viewer: %s is not a parquet file (footer length %d does not fit in %d bytes)",
			key, footerSize, info.Size)
	}

	return &tailEntry{key: key, size: info.Size, tailOffset: off, tail: buf}, nil
}
