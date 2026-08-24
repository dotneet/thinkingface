package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// memStorage is a minimal in-memory implementation of storage.Storage, used
// so these tests do not depend on GCS or the fake-gcs-server emulator.
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStorage() *memStorage {
	return &memStorage{objects: make(map[string][]byte)}
}

func (m *memStorage) SupportsSignedURL() bool { return false }

func (m *memStorage) SignedGetURL(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) SignedPutURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *memStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// GetWithGeneration / PutIfGeneration exist only to satisfy storage.Storage:
// the viewer reads immutable parquet objects and never uses the CAS path.
func (m *memStorage) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	rc, err := m.Get(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return rc, 1, nil
}

func (m *memStorage) PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error) {
	return 0, errors.New("memStorage: conditional writes not supported")
}

func (m *memStorage) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	if offset < 0 || offset > int64(len(data)) {
		return nil, errors.New("memStorage: offset out of range")
	}
	end := int64(len(data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func (m *memStorage) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(data)), Updated: time.Now()}, nil
}

func (m *memStorage) Copy(ctx context.Context, srcKey, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	m.objects[dstKey] = data
	return nil
}

func (m *memStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStorage) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []storage.ObjectInfo
	for k, data := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.ObjectInfo{Key: k, Size: int64(len(data)), Updated: time.Now()})
		}
	}
	return out, nil
}

func (m *memStorage) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*memStorage)(nil)

// --- test helpers ---

func newTestReader(t *testing.T, st storage.Storage) *Reader {
	t.Helper()
	return New(st, 8<<20)
}

func buildParquet[T any](t *testing.T, rows []T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[T](&buf)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func putParquet(t *testing.T, st *memStorage, key string, data []byte) {
	t.Helper()
	if err := st.Put(context.Background(), key, bytes.NewReader(data), "application/octet-stream"); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

// --- 1. basic types (int64, float64, string, bool, optional/null) ---

type basicRow struct {
	ID       int64   `parquet:"id"`
	Score    float64 `parquet:"score"`
	Name     string  `parquet:"name"`
	Active   bool    `parquet:"active"`
	Nickname *string `parquet:"nickname"`
}

func TestSchemaAndRows_BasicTypes(t *testing.T) {
	nick := "ace"
	rows := []basicRow{
		{ID: 1, Score: 1.5, Name: "alice", Active: true, Nickname: &nick},
		{ID: 2, Score: 2.5, Name: "bob", Active: false, Nickname: nil},
	}
	data := buildParquet(t, rows)

	st := newMemStorage()
	const key = "lfs/ba/si/basic.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	sch, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if sch.NumRows != 2 {
		t.Errorf("NumRows = %d, want 2", sch.NumRows)
	}
	if sch.NumRowGroups != 1 {
		t.Errorf("NumRowGroups = %d, want 1", sch.NumRowGroups)
	}
	if sch.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", sch.SizeBytes, len(data))
	}

	byName := map[string]Column{}
	for _, c := range sch.Columns {
		byName[c.Name] = c
	}
	for _, name := range []string{"id", "score", "name", "active", "nickname"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing column %q in schema", name)
		}
	}
	if byName["nickname"].Optional != true {
		t.Errorf("nickname column should be optional")
	}
	if byName["id"].Optional {
		t.Errorf("id column should not be optional")
	}
	if byName["id"].Type != "INT64" {
		t.Errorf("id.Type = %q, want INT64", byName["id"].Type)
	}
	if byName["name"].LogicalType != "STRING" {
		t.Errorf("name.LogicalType = %q, want STRING", byName["name"].LogicalType)
	}

	res, err := r.Rows(ctx, key, 0, 10, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if res.NumRows != 2 {
		t.Errorf("Rows.NumRows = %d, want 2", res.NumRows)
	}
	if res.Offset != 0 {
		t.Errorf("Rows.Offset = %d, want 0", res.Offset)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("len(Rows.Rows) = %d, want 2", len(res.Rows))
	}

	row0 := res.Rows[0]
	if v, ok := row0["id"].(int64); !ok || v != 1 {
		t.Errorf("row0[id] = %#v, want int64(1)", row0["id"])
	}
	if v, ok := row0["score"].(float64); !ok || v != 1.5 {
		t.Errorf("row0[score] = %#v, want float64(1.5)", row0["score"])
	}
	if v, ok := row0["name"].(string); !ok || v != "alice" {
		t.Errorf("row0[name] = %#v, want \"alice\"", row0["name"])
	}
	if v, ok := row0["active"].(bool); !ok || v != true {
		t.Errorf("row0[active] = %#v, want true", row0["active"])
	}
	if v, ok := row0["nickname"].(string); !ok || v != "ace" {
		t.Errorf("row0[nickname] = %#v, want \"ace\"", row0["nickname"])
	}

	row1 := res.Rows[1]
	if row1["nickname"] != nil {
		t.Errorf("row1[nickname] = %#v, want nil", row1["nickname"])
	}

	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("json.Marshal(res): %v", err)
	}
}

// --- 1b. feature hints from the parquet's own "huggingface" key-value
//         metadata, which `datasets` writes as {"info":{"features":{...}}} ---

type featureRow struct {
	Image struct {
		Bytes []byte  `parquet:"bytes"`
		Path  *string `parquet:"path"`
	} `parquet:"image"`
	Label int64  `parquet:"label"`
	Text  string `parquet:"text"`
}

func buildFeatureParquet(t *testing.T) []byte {
	t.Helper()
	rows := []featureRow{
		{Label: 0, Text: "a"},
		{Label: 1, Text: "b"},
	}
	rows[0].Image.Bytes = []byte{1, 2, 3}
	rows[1].Image.Bytes = []byte{4, 5, 6}

	const kv = `{"info":{"features":{
		"image": {"_type": "Image"},
		"label": {"_type": "ClassLabel", "names": ["neg", "pos"]},
		"text": {"dtype": "string", "_type": "Value"}
	}}}`

	var buf bytes.Buffer
	w := parquet.NewGenericWriter[featureRow](&buf, parquet.KeyValueMetadata("huggingface", kv))
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func TestSchemaAndRows_FeatureHints(t *testing.T) {
	data := buildFeatureParquet(t)

	st := newMemStorage()
	const key = "lfs/fe/at/feature.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	wantFeature := map[string]string{
		"image": "image",
		"label": "classlabel",
		"text":  "",
	}

	sch, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	schByName := map[string]Column{}
	for _, c := range sch.Columns {
		schByName[c.Name] = c
	}
	for name, want := range wantFeature {
		c, ok := schByName[name]
		if !ok {
			t.Fatalf("Schema: missing column %q", name)
		}
		if c.Feature != want {
			t.Errorf("Schema: column %q Feature = %q, want %q", name, c.Feature, want)
		}
	}

	res, err := r.Rows(ctx, key, 0, 10, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	rowsByName := map[string]Column{}
	for _, c := range res.Columns {
		rowsByName[c.Name] = c
	}
	for name, want := range wantFeature {
		c, ok := rowsByName[name]
		if !ok {
			t.Fatalf("Rows: missing column %q", name)
		}
		if c.Feature != want {
			t.Errorf("Rows: column %q Feature = %q, want %q", name, c.Feature, want)
		}
	}

	// A file with no "huggingface" key-value metadata gets no hints at all.
	plainRows := []basicRow{{ID: 1, Score: 1, Name: "x", Active: true}}
	plainData := buildParquet(t, plainRows)
	const plainKey = "lfs/pl/ai/plain.parquet"
	putParquet(t, st, plainKey, plainData)
	plainSch, err := r.Schema(ctx, plainKey)
	if err != nil {
		t.Fatalf("Schema (plain): %v", err)
	}
	for _, c := range plainSch.Columns {
		if c.Feature != "" {
			t.Errorf("plain file: column %q Feature = %q, want \"\"", c.Name, c.Feature)
		}
	}
}

// --- 2. NaN / +Inf / -Inf must marshal to null, not error ---

type floatRow struct {
	ID    int64   `parquet:"id"`
	Value float64 `parquet:"value"`
}

func TestRows_NaNAndInf(t *testing.T) {
	rows := []floatRow{
		{ID: 1, Value: 1.5},
		{ID: 2, Value: math.NaN()},
		{ID: 3, Value: math.Inf(1)},
		{ID: 4, Value: math.Inf(-1)},
	}
	data := buildParquet(t, rows)

	st := newMemStorage()
	const key = "lfs/na/ni/nan.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	sch, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if sch.NumRows != 4 {
		t.Fatalf("NumRows = %d, want 4", sch.NumRows)
	}

	res, err := r.Rows(ctx, key, 0, 10, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 4 {
		t.Fatalf("len(Rows.Rows) = %d, want 4", len(res.Rows))
	}

	want := []any{1.5, nil, nil, nil}
	for i, w := range want {
		got := res.Rows[i]["value"]
		if w == nil {
			if got != nil {
				t.Errorf("row %d: value = %#v, want nil", i, got)
			}
			continue
		}
		if got != w {
			t.Errorf("row %d: value = %#v, want %#v", i, got, w)
		}
	}

	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal(res) failed (NaN/Inf leaked?): %v", err)
	}
	if strings.Contains(string(encoded), "NaN") || strings.Contains(string(encoded), "Inf") {
		t.Errorf("encoded JSON contains NaN/Inf literal: %s", encoded)
	}
}

// --- 3. multiple row groups: offset/limit must select the right rows,
//        including a window that spans a row-group boundary ---

type numRow struct {
	ID int64 `parquet:"id"`
}

const (
	testNumRowGroups = 5
	testRowsPerGroup = 20
	testTotalNumRows = testNumRowGroups * testRowsPerGroup
)

func buildMultiGroupParquet(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[numRow](&buf)
	for g := 0; g < testNumRowGroups; g++ {
		batch := make([]numRow, testRowsPerGroup)
		for i := range batch {
			batch[i] = numRow{ID: int64(g*testRowsPerGroup + i)}
		}
		if _, err := w.Write(batch); err != nil {
			t.Fatalf("write batch %d: %v", g, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush batch %d: %v", g, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func TestRows_MultipleRowGroups(t *testing.T) {
	data := buildMultiGroupParquet(t)

	st := newMemStorage()
	const key = "lfs/mu/lt/multi.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	sch, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if sch.NumRowGroups != testNumRowGroups {
		t.Fatalf("NumRowGroups = %d, want %d", sch.NumRowGroups, testNumRowGroups)
	}
	if sch.NumRows != testTotalNumRows {
		t.Fatalf("NumRows = %d, want %d", sch.NumRows, testTotalNumRows)
	}

	// Offset lands strictly inside row group 2 (rows [40,60)); take 5 rows.
	res, err := r.Rows(ctx, key, 45, 5, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 5 {
		t.Fatalf("len(Rows.Rows) = %d, want 5", len(res.Rows))
	}
	for i, row := range res.Rows {
		want := int64(45 + i)
		if got, _ := row["id"].(int64); got != want {
			t.Errorf("row %d: id = %v, want %d", i, row["id"], want)
		}
	}

	// Window spans the row-group boundary between group 0 and group 1 (at row 20).
	res2, err := r.Rows(ctx, key, 15, 15, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res2.Rows) != 15 {
		t.Fatalf("len(Rows.Rows) = %d, want 15", len(res2.Rows))
	}
	for i, row := range res2.Rows {
		want := int64(15 + i)
		if got, _ := row["id"].(int64); got != want {
			t.Errorf("row %d: id = %v, want %d", i, row["id"], want)
		}
	}

	// Offset beyond the end of the file returns no rows, no error.
	res3, err := r.Rows(ctx, key, testTotalNumRows+10, 5, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res3.Rows) != 0 {
		t.Fatalf("len(Rows.Rows) = %d, want 0", len(res3.Rows))
	}
}

// --- 4. columns filter: only requested columns come back ---

func TestRows_ColumnsFilter(t *testing.T) {
	nick := "ace"
	rows := []basicRow{
		{ID: 1, Score: 1.5, Name: "alice", Active: true, Nickname: &nick},
		{ID: 2, Score: 2.5, Name: "bob", Active: false},
	}
	data := buildParquet(t, rows)

	st := newMemStorage()
	const key = "lfs/co/ls/cols.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	res, err := r.Rows(ctx, key, 0, 10, []string{"name", "id"})
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Columns) != 2 {
		t.Fatalf("len(Columns) = %d, want 2", len(res.Columns))
	}
	for _, row := range res.Rows {
		if len(row) != 2 {
			t.Fatalf("row has %d keys, want 2: %#v", len(row), row)
		}
		if _, ok := row["id"]; !ok {
			t.Errorf("row missing id: %#v", row)
		}
		if _, ok := row["name"]; !ok {
			t.Errorf("row missing name: %#v", row)
		}
	}

	if _, err := r.Rows(ctx, key, 0, 10, []string{"does_not_exist"}); err == nil {
		t.Errorf("Rows with unknown column: want error, got nil")
	}
}

// --- 5. Scan visits every row, in order, and stops on the callback's error ---

func TestScan_AllRowsInOrder(t *testing.T) {
	data := buildMultiGroupParquet(t)

	st := newMemStorage()
	const key = "lfs/sc/an/scan.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	var got []int64
	err := r.Scan(ctx, key, ScanRequest{}, func(row map[string]any) error {
		id, ok := row["id"].(int64)
		if !ok {
			t.Fatalf("row id not int64: %#v", row)
		}
		got = append(got, id)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != testTotalNumRows {
		t.Fatalf("scanned %d rows, want %d", len(got), testTotalNumRows)
	}
	for i, v := range got {
		if v != int64(i) {
			t.Fatalf("row %d out of order: id = %d", i, v)
		}
	}
}

func TestScan_StopsOnCallbackError(t *testing.T) {
	data := buildMultiGroupParquet(t)

	st := newMemStorage()
	const key = "lfs/sc/er/scan_err.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	stopErr := errors.New("stop scanning")
	count := 0
	err := r.Scan(ctx, key, ScanRequest{}, func(row map[string]any) error {
		count++
		if count == 3 {
			return stopErr
		}
		return nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("Scan error = %v, want %v", err, stopErr)
	}
	if count != 3 {
		t.Fatalf("callback invoked %d times, want 3", count)
	}
}

// --- 6. trackio-shaped metrics parquet ---

type trackioRow struct {
	ID        int64     `parquet:"id"`
	Timestamp time.Time `parquet:"timestamp"`
	RunName   string    `parquet:"run_name"`
	Step      int64     `parquet:"step"`
	Loss      float64   `parquet:"loss"`
	Accuracy  float64   `parquet:"accuracy"`
}

func TestTrackioMetricsFormat(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	rows := []trackioRow{
		{ID: 1, Timestamp: t0, RunName: "run-1", Step: 1, Loss: 0.5, Accuracy: 0.80},
		{ID: 2, Timestamp: t0.Add(time.Minute), RunName: "run-1", Step: 2, Loss: 0.4, Accuracy: 0.85},
	}
	data := buildParquet(t, rows)

	st := newMemStorage()
	const key = "lfs/tr/ac/metrics.parquet"
	putParquet(t, st, key, data)

	r := newTestReader(t, st)
	ctx := context.Background()

	res, err := r.Rows(ctx, key, 0, 10, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("len(Rows.Rows) = %d, want 2", len(res.Rows))
	}

	row0 := res.Rows[0]
	if v, _ := row0["run_name"].(string); v != "run-1" {
		t.Errorf("run_name = %#v, want \"run-1\"", row0["run_name"])
	}
	if v, _ := row0["step"].(int64); v != 1 {
		t.Errorf("step = %#v, want int64(1)", row0["step"])
	}
	if v, _ := row0["loss"].(float64); v != 0.5 {
		t.Errorf("loss = %#v, want 0.5", row0["loss"])
	}
	if v, _ := row0["accuracy"].(float64); v != 0.80 {
		t.Errorf("accuracy = %#v, want 0.80", row0["accuracy"])
	}

	ts, ok := row0["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp is %T, want string", row0["timestamp"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", ts, err)
	}
	if !parsed.Equal(t0) {
		t.Errorf("timestamp = %v, want %v", parsed, t0)
	}

	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("json.Marshal(res): %v", err)
	}
}
