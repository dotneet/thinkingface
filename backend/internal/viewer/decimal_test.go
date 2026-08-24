// A DECIMAL's precision and scale are read from the file's footer and nothing
// else vouches for them, so a parquet in a repository can ask this package for
// arbitrarily expensive arithmetic. These tests pin the bound that stops it.

package viewer

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

// buildDecimalParquet writes a one-row, two-column parquet by hand: the
// generic writer has no Go type that maps onto a DECIMAL, and what matters
// here is the annotation rather than the data. "attack" carries the scale
// under test, "ok" a perfectly ordinary DECIMAL(18,2) so the same file also
// proves that bounding the scale did not break decimals that are real.
func buildDecimalParquet(t *testing.T, typ parquet.Type, precision, scale int, unscaled int64) []byte {
	t.Helper()
	// parquet-go refuses an INT32 decimal wider than 9 digits, so the control
	// column follows the physical type it is written next to.
	okPrecision := 18
	if typ.Kind() == parquet.Int32 {
		okPrecision = 9
	}
	group := parquet.Group{
		"attack": parquet.Decimal(scale, precision, typ),
		"ok":     parquet.Decimal(2, okPrecision, typ),
	}
	// parquet.Group orders its fields by name, so "attack" is column 0.
	value := func() parquet.Value {
		switch typ.Kind() {
		case parquet.Int32:
			return parquet.Int32Value(int32(unscaled))
		default:
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(unscaled))
			return parquet.FixedLenByteArrayValue(b[:])
		}
	}

	var buf bytes.Buffer
	w := parquet.NewWriter(&buf, parquet.NewSchema("decimals", group))
	row := parquet.Row{value().Level(0, 0, 0), value().Level(0, 0, 1)}
	if _, err := w.WriteRows([]parquet.Row{row}); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

// readDecimalRow reads the fixture back, failing the test if it takes long
// enough to be the unbounded computation rather than an answer. The read runs
// on its own goroutine because the whole failure mode is that it does not
// return: without the timeout an unfixed decoder would hang the package's
// tests instead of reporting the regression.
func readDecimalRow(t *testing.T, data []byte, budget time.Duration) map[string]any {
	t.Helper()
	st := newMemStorage()
	const key = "lfs/de/ci/decimal.parquet"
	putParquet(t, st, key, data)
	r := newTestReader(t, st)

	type result struct {
		row map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := r.Rows(context.Background(), key, 0, 10, nil)
		if err != nil || len(res.Rows) == 0 {
			done <- result{nil, err}
			return
		}
		done <- result{res.Rows[0], nil}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Rows: %v", got.err)
		}
		if got.row == nil {
			t.Fatal("Rows returned no rows")
		}
		return got.row
	case <-time.After(budget):
		t.Fatalf("reading a %d-byte parquet took longer than %s: the DECIMAL scale is being trusted", len(data), budget)
		return nil
	}
}

// TestRows_AbsurdDecimalScaleIsRejectedNotComputed is the regression test for
// the denial of service: DECIMAL(18, 1000000000) fits in a few hundred bytes
// and used to cost a multi-hundred-megabyte big.Int per cell (10^1e8 alone
// measured 26 s and 159 MB). The cell must come back null, immediately, and
// the neighbouring real decimal must still decode.
func TestRows_AbsurdDecimalScaleIsRejectedNotComputed(t *testing.T) {
	data := buildDecimalParquet(t, parquet.FixedLenByteArrayType(8), 18, 1_000_000_000, 12345)
	if len(data) > 4096 {
		t.Fatalf("fixture is %d bytes; the point is that the attack is tiny", len(data))
	}

	row := readDecimalRow(t, data, 10*time.Second)
	if row["attack"] != nil {
		t.Errorf("attack column = %#v, want nil (scale out of range)", row["attack"])
	}
	if got, ok := row["ok"].(float64); !ok || got != 123.45 {
		t.Errorf("ok column = %#v, want 123.45", row["ok"])
	}
}

// TestRows_DecimalAnnotationBounds covers the rest of the range check. A
// negative scale is not legal parquet -- and used to *multiply* the value by
// 10^|scale| through math.Pow10 -- while a scale inside the bound must keep
// decoding exactly as before.
func TestRows_DecimalAnnotationBounds(t *testing.T) {
	cases := []struct {
		name      string
		typ       parquet.Type
		precision int
		scale     int
		unscaled  int64
		want      any
	}{
		{"negative scale", parquet.Int32Type, 9, -1, 12345, nil},
		{"scale above the precision", parquet.Int32Type, 4, 6, 12345, nil},
		{"scale past the widest decimal", parquet.FixedLenByteArrayType(8), 90, 80, 12345, nil},
		{"int32 decimal", parquet.Int32Type, 9, 3, 12345, 12.345},
		{"fixed len byte array decimal", parquet.FixedLenByteArrayType(8), 18, 4, 12345, 1.2345},
		{"widest supported scale", parquet.FixedLenByteArrayType(8), 76, 76, 1, 1e-76},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := buildDecimalParquet(t, tc.typ, tc.precision, tc.scale, tc.unscaled)
			row := readDecimalRow(t, data, 10*time.Second)
			got := row["attack"]
			if tc.want == nil {
				if got != nil {
					t.Fatalf("attack = %#v, want nil", got)
				}
				return
			}
			f, ok := got.(float64)
			if !ok {
				t.Fatalf("attack = %#v, want a float64", got)
			}
			want := tc.want.(float64)
			if diff := f - want; diff > want*1e-9 || diff < -want*1e-9 {
				t.Fatalf("attack = %v, want %v", f, want)
			}
		})
	}
}
