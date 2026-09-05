package viewer

import (
	"context"
	"io"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// cancelCheckingStorage fails any read issued under a cancelled context, the
// way a real network-backed storage driver does. The in-memory stub ignores
// contexts entirely, so without this the test below would pass against the
// bug it guards.
type cancelCheckingStorage struct {
	storage.Storage
}

func (s cancelCheckingStorage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	return s.Storage.Stat(ctx, key)
}

func (s cancelCheckingStorage) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Storage.GetRange(ctx, key, offset, length)
}

// The tail fetch outlives the caller that triggered it. singleflight
// collapses concurrent opens of one key into whichever caller executes the
// fetch, so running it on that caller's context lets one request going away
// (a user paging past a parquet file) cancel the read out from under the
// requests still waiting on it, failing all of them at once.
func TestTailCacheLoad_SurvivesACancelledCaller(t *testing.T) {
	inner := newMemStorage()
	putParquet(t, inner, "cancelled", fakeParquetBytes(1000))
	st := cancelCheckingStorage{Storage: inner}

	c := newTailCache(8 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.load(ctx, st, "cancelled"); err != nil {
		t.Fatalf("load with a cancelled caller: %v (the fetch must not depend on the caller's context)", err)
	}
}
