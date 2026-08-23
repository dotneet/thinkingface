package modelmeta

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// chunkSize is the granularity of ranged reads made on behalf of an
// io.ReaderAt. archive/zip walks the central directory in many small reads;
// serving those from megabyte-sized chunks keeps the number of round trips to
// object storage in the single digits.
const chunkSize = 1 << 20

// maxCachedChunks bounds the memory one inspection may hold.
const maxCachedChunks = 24

// rangeReaderAt adapts a Fetcher to io.ReaderAt, caching the chunks it has
// already pulled. It is used for a single inspection and then thrown away.
type rangeReaderAt struct {
	ctx   context.Context
	fetch Fetcher
	size  int64

	mu     sync.Mutex
	chunks map[int64][]byte
	order  []int64
	// fetched counts the bytes actually pulled from storage, for logging.
	fetched int64
}

func newRangeReaderAt(ctx context.Context, size int64, fetch Fetcher) *rangeReaderAt {
	return &rangeReaderAt{ctx: ctx, fetch: fetch, size: size, chunks: map[int64][]byte{}}
}

func (r *rangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("modelmeta: negative offset %d", off)
	}
	if off >= r.size {
		return 0, io.EOF
	}
	read := 0
	for read < len(p) {
		pos := off + int64(read)
		if pos >= r.size {
			return read, io.EOF
		}
		chunk, err := r.chunkAt(pos / chunkSize)
		if err != nil {
			return read, err
		}
		start := pos % chunkSize
		if start >= int64(len(chunk)) {
			return read, io.EOF
		}
		n := copy(p[read:], chunk[start:])
		read += n
	}
	return read, nil
}

func (r *rangeReaderAt) chunkAt(index int64) ([]byte, error) {
	r.mu.Lock()
	if chunk, ok := r.chunks[index]; ok {
		r.mu.Unlock()
		return chunk, nil
	}
	r.mu.Unlock()

	off := index * chunkSize
	n := int64(chunkSize)
	if off+n > r.size {
		n = r.size - off
	}
	data, err := r.fetch(r.ctx, off, n)
	if err != nil {
		return nil, fmt.Errorf("modelmeta: read %d bytes at %d: %w", n, off, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.chunks[index]; !ok {
		r.chunks[index] = data
		r.order = append(r.order, index)
		// Plain FIFO eviction: zip parsing sweeps the file, so the oldest
		// chunk is also the one least likely to come back.
		for len(r.order) > maxCachedChunks {
			delete(r.chunks, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.fetched += int64(len(data))
	return data, nil
}

// BytesFetched reports how much was pulled from storage, so callers can log
// what an inspection actually cost.
func (r *rangeReaderAt) BytesFetched() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetched
}
