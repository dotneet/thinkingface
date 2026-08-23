package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const cacheFileSuffix = ".parquet"

// cachePath returns the local cache path for the object identified by key.
func (r *Reader) cachePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(r.cacheDir, hex.EncodeToString(sum[:])+cacheFileSuffix)
}

// ensureCached makes sure the object at key is present in the local cache,
// downloading it if necessary, and returns its local path and size. It
// deduplicates concurrent downloads of the same key and refreshes the
// cache entry's mtime so the LRU eviction in evict reflects recent use.
func (r *Reader) ensureCached(ctx context.Context, key string) (string, int64, error) {
	path := r.cachePath(key)

	info, err := r.st.Stat(ctx, key)
	if err != nil {
		return "", 0, fmt.Errorf("viewer: stat %s: %w", key, err)
	}

	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() == info.Size {
		touch(path)
		return path, info.Size, nil
	}

	_, err, _ = r.group.Do(key, func() (any, error) {
		// Re-check now that we hold the singleflight slot: another caller
		// may have finished downloading while we were waiting.
		if fi, statErr := os.Stat(path); statErr == nil && fi.Size() == info.Size {
			return nil, nil
		}
		return nil, r.download(ctx, key, path, info.Size)
	})
	if err != nil {
		return "", 0, err
	}

	touch(path)
	return path, info.Size, nil
}

func touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// download fetches key from storage into path, evicting older cache entries
// first if needed, and writes it atomically via a temp file + rename.
func (r *Reader) download(ctx context.Context, key, path string, size int64) error {
	r.evict(size)

	rc, err := r.st.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("viewer: get %s: %w", key, err)
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(r.cacheDir, "download-*.tmp")
	if err != nil {
		return fmt.Errorf("viewer: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return fmt.Errorf("viewer: download %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("viewer: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("viewer: rename into cache: %w", err)
	}
	return nil
}

// evict removes cache entries, oldest (by mtime) first, until the cache
// directory's total size plus needed bytes fits within maxCacheBytes. A
// non-positive maxCacheBytes disables the size limit.
func (r *Reader) evict(needed int64) {
	if r.maxCacheBytes <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := os.ReadDir(r.cacheDir)
	if err != nil {
		return
	}

	type cacheFile struct {
		path string
		size int64
		mod  time.Time
	}

	var files []cacheFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), cacheFileSuffix) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, cacheFile{filepath.Join(r.cacheDir, e.Name()), fi.Size(), fi.ModTime()})
		total += fi.Size()
	}

	if total+needed <= r.maxCacheBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	for _, f := range files {
		if total+needed <= r.maxCacheBytes {
			return
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}
