// A parquet logical type is what tells a reader how to render the bytes under
// it. These tests pin the two annotations this package used to drop on the
// floor: TIMESTAMP's isAdjustedToUTC, which decides whether the value is an
// instant or a bare wall clock, and UUID, which has a canonical text form.

package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// buildLogicalTypeParquet writes a one-row parquet whose columns carry the
// annotations under test. The generic writer has no Go type that maps onto a
// tz-naive TIMESTAMP, and what matters here is the annotation rather than the
// data, so the schema is built by hand.
func buildLogicalTypeParquet(t *testing.T, micros int64, uuidBytes []byte) []byte {
	t.Helper()
	group := parquet.Group{
		"instant": parquet.TimestampAdjusted(parquet.Microsecond, true),
		"naive":   parquet.TimestampAdjusted(parquet.Microsecond, false),
		"id":      parquet.UUID(),
	}
	// parquet.Group orders its fields by name: id, instant, naive.
	row := parquet.Row{
		parquet.FixedLenByteArrayValue(uuidBytes).Level(0, 0, 0),
		parquet.Int64Value(micros).Level(0, 0, 1),
		parquet.Int64Value(micros).Level(0, 0, 2),
	}

	var buf bytes.Buffer
	w := parquet.NewWriter(&buf, parquet.NewSchema("logical", group))
	if _, err := w.WriteRows([]parquet.Row{row}); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func readLogicalTypeRow(t *testing.T, data []byte) map[string]any {
	t.Helper()
	st := newMemStorage()
	const key = "lfs/lo/gi/logical.parquet"
	putParquet(t, st, key, data)

	res, err := newTestReader(t, st).Rows(context.Background(), key, 0, 1, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(res.Rows))
	}
	return res.Rows[0]
}

// A TIMESTAMP with isAdjustedToUTC=false is a wall clock with no zone -- what
// pandas writes for a tz-naive datetime64, so most timestamp columns in the
// wild. Printing it with a "Z" claimed a UTC offset the file never stated and
// made it indistinguishable from a genuinely UTC column.
func TestRows_TimestampHonoursIsAdjustedToUTC(t *testing.T) {
	ts := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	row := readLogicalTypeRow(t, buildLogicalTypeParquet(t, ts.UnixMicro(), make([]byte, 16)))

	if got, want := row["instant"], "2021-01-01T00:00:00Z"; got != want {
		t.Errorf("instant = %#v, want %q", got, want)
	}
	if got, want := row["naive"], "2021-01-01T00:00:00"; got != want {
		t.Errorf("naive = %#v, want %q (no zone: the column does not have one)", got, want)
	}
}

// The units are orthogonal to the zone, and a fractional second still has to
// survive both renderings.
func TestTimestampToString(t *testing.T) {
	micros := parquet.Int64Value(time.Date(2021, 1, 1, 0, 0, 0, 500_000_000, time.UTC).UnixMicro())
	millis := parquet.Int64Value(time.Date(2021, 1, 1, 0, 0, 0, 500_000_000, time.UTC).UnixMilli())
	nanos := parquet.Int64Value(time.Date(2021, 1, 1, 0, 0, 0, 500_000_000, time.UTC).UnixNano())

	cases := []struct {
		name     string
		value    parquet.Value
		unit     parquet.TimeUnit
		adjusted bool
		want     string
	}{
		{"micros utc", micros, parquet.Microsecond, true, "2021-01-01T00:00:00.5Z"},
		{"micros local", micros, parquet.Microsecond, false, "2021-01-01T00:00:00.5"},
		{"millis utc", millis, parquet.Millisecond, true, "2021-01-01T00:00:00.5Z"},
		{"millis local", millis, parquet.Millisecond, false, "2021-01-01T00:00:00.5"},
		{"nanos utc", nanos, parquet.Nanosecond, true, "2021-01-01T00:00:00.5Z"},
		{"nanos local", nanos, parquet.Nanosecond, false, "2021-01-01T00:00:00.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := timestampToString(tc.value, tc.unit.TimeUnit(), tc.adjusted)
			if got != tc.want {
				t.Errorf("timestampToString = %q, want %q", got, tc.want)
			}
		})
	}
}

// A UUID column used to come back as base64 while the schema panel next to it
// announced "UUID".
func TestRows_UUIDIsCanonical(t *testing.T) {
	id := []byte{
		0x12, 0x3e, 0x45, 0x67, 0xe8, 0x9b, 0x12, 0xd3,
		0xa4, 0x56, 0x42, 0x66, 0x14, 0x17, 0x40, 0x00,
	}
	row := readLogicalTypeRow(t, buildLogicalTypeParquet(t, 0, id))

	if got, want := row["id"], "123e4567-e89b-12d3-a456-426614174000"; got != want {
		t.Errorf("id = %#v, want %q", got, want)
	}
}

// A UUID annotation on something that is not 16 bytes is not a UUID, whatever
// the footer says; it falls back to the encoding every other opaque byte
// array gets rather than being padded into a plausible-looking lie.
func TestUUIDToString_WrongWidthFallsBackToBase64(t *testing.T) {
	for _, b := range [][]byte{nil, {0x01}, bytes.Repeat([]byte{0xff}, 15), bytes.Repeat([]byte{0xff}, 17)} {
		got := uuidToString(b)
		if len(got) == 36 && got[8] == '-' {
			t.Errorf("uuidToString(%d bytes) = %q, want the base64 fallback", len(b), got)
		}
	}
}

// unsignedRow is a flat schema with UINT32/UINT64 columns (Go uint32/uint64
// fields map onto parquet's INT(32,false)/INT(64,false) logical type -- see
// schema.go's reflect.Uint32/Uint64 cases in parquet-go), read through the
// "fast" leaf path (rowPlan.convertRow -> convertLeafValue).
type unsignedRow struct {
	U32 uint32 `parquet:"u32"`
	U64 uint64 `parquet:"u64"`
}

// A uint32 at or above 1<<31 and a uint64 at or above 1<<63 both set the sign
// bit of their physical INT32/INT64 storage. Reading them back as signed
// (int64(v.Int32()) / v.Int64()) used to hand back a negative number for a
// column that is never negative.
func TestRows_UnsignedIntsAreNotNegative(t *testing.T) {
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[unsignedRow](&buf)
	rows := []unsignedRow{
		{U32: 3_000_000_000, U64: 18_000_000_000_000_000_000}, // both > the signed max of their physical width
		{U32: 0, U64: 0},
	}
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	st := newMemStorage()
	const key = "lfs/un/si/unsigned.parquet"
	putParquet(t, st, key, buf.Bytes())

	res, err := newTestReader(t, st).Rows(context.Background(), key, 0, 2, nil)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(res.Rows))
	}

	row := res.Rows[0]
	if got, want := row["u32"], int64(3_000_000_000); got != want {
		t.Errorf("u32 = %#v (%T), want %#v", got, got, want)
	}
	if got, want := row["u64"], uint64(18_000_000_000_000_000_000); got != want {
		t.Errorf("u64 = %#v (%T), want %#v", got, got, want)
	}

	// encoding/json must round-trip both as positive numerals, not silently
	// truncate or reject them.
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "-") {
		t.Errorf("json encoding of an unsigned row went negative: %s", encoded)
	}
	if !strings.Contains(string(encoded), "3000000000") || !strings.Contains(string(encoded), "18000000000000000000") {
		t.Errorf("json encoding lost precision: %s", encoded)
	}
}

// unsignedIntValue backs the logical-type branch of convertLeafValue. The
// inputs are built through a variable (not a constant expression) so the
// uint32/uint64 -> int32/int64 conversion reinterprets bits, the same way
// v.Int32()/v.Int64() hands back whatever bit pattern is physically stored,
// rather than the compiler rejecting an out-of-range constant conversion.
func TestUnsignedIntValue(t *testing.T) {
	u32 := uint32(3_000_000_000)
	if got, want := unsignedIntValue(parquet.Int32, parquet.Int32Value(int32(u32))), int64(3_000_000_000); got != want {
		t.Errorf("Int32 unsigned = %#v, want %#v", got, want)
	}
	u64 := uint64(18_000_000_000_000_000_000)
	if got, want := unsignedIntValue(parquet.Int64, parquet.Int64Value(int64(u64))), uint64(18_000_000_000_000_000_000); got != want {
		t.Errorf("Int64 unsigned = %#v, want %#v", got, want)
	}
}

// normalizeGeneric hits the same reflect.Uint* branch for unsigned columns
// nested under a LIST/MAP/struct, where parquet-go's generic reconstruction
// hands back native Go uint8/16/32/64 values.
func TestNormalizeGeneric_UnsignedIntsAreNotNegative(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"uint8", uint8(200), uint64(200)},
		{"uint16", uint16(60_000), uint64(60_000)},
		{"uint32 above int32 max", uint32(3_000_000_000), uint64(3_000_000_000)},
		{"uint64 above int64 max", uint64(18_000_000_000_000_000_000), uint64(18_000_000_000_000_000_000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeGeneric(tc.in); got != tc.want {
				t.Errorf("normalizeGeneric(%#v) = %#v (%T), want %#v", tc.in, got, got, tc.want)
			}
		})
	}
}
