package viewer

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/deprecated"
	"github.com/parquet-go/parquet-go/format"
)

// julianDayOfUnixEpoch is the Julian day number of 1970-01-01, used to
// decode the legacy INT96 timestamp encoding.
const julianDayOfUnixEpoch = 2440588

// columnMeta builds the exported Column metadata for a schema field, which
// may be a leaf (primitive) column or a group (nested struct/list/map).
// features maps top-level column names to their `datasets` feature
// rendering hint (see parquetFeatureHints); a nil map or a name absent from
// it yields Feature == "".
func columnMeta(f parquet.Field, features map[string]string) Column {
	if f.Leaf() {
		t := f.Type()
		return Column{
			Name: f.Name(),
			// Kind().String() reports the physical on-disk type (e.g.
			// "INT64", "BYTE_ARRAY"); t.String() would instead describe
			// certain logical annotations (e.g. Go int64 fields default to
			// a signed 64-bit INT logical type, whose Type.String() is
			// "INT(64,true)"), which belongs in LogicalType, not Type.
			Type:        t.Kind().String(),
			LogicalType: logicalTypeString(t.LogicalType()),
			Optional:    f.Optional(),
			Repeated:    f.Repeated(),
			Feature:     features[f.Name()],
		}
	}
	return Column{
		Name:        f.Name(),
		Type:        "GROUP",
		LogicalType: groupLogicalTypeString(f),
		Optional:    f.Optional(),
		Repeated:    f.Repeated(),
		Feature:     features[f.Name()],
	}
}

// groupLogicalTypeString recognizes the standard 3-level LIST and MAP group
// encodings and reports "LIST" / "MAP" accordingly; any other group shape
// (a plain nested struct) reports "".
func groupLogicalTypeString(f parquet.Node) string {
	fields := f.Fields()
	if len(fields) == 1 && fields[0].Name() == "list" && fields[0].Repeated() {
		inner := fields[0].Fields()
		if len(inner) == 1 && inner[0].Name() == "element" {
			return "LIST"
		}
	}
	if len(fields) == 1 && fields[0].Name() == "key_value" && fields[0].Repeated() {
		inner := fields[0].Fields()
		if len(inner) == 2 {
			hasKey, hasValue := false, false
			for _, x := range inner {
				switch x.Name() {
				case "key":
					hasKey = true
				case "value":
					hasValue = true
				}
			}
			if hasKey && hasValue {
				return "MAP"
			}
		}
	}
	return ""
}

// logicalTypeString renders a parquet logical type annotation as a short
// human-readable string, e.g. "STRING", "TIMESTAMP(MICROS)", "DECIMAL(9,2)".
// It returns "" when no logical type is set.
func logicalTypeString(lt *format.LogicalType) string {
	if lt == nil || lt.Value == nil {
		return ""
	}
	switch v := lt.Value.(type) {
	case *format.StringType:
		return "STRING"
	case *format.EnumType:
		return "ENUM"
	case *format.JsonType:
		return "JSON"
	case *format.BsonType:
		return "BSON"
	case *format.UUIDType:
		return "UUID"
	case *format.DateType:
		return "DATE"
	case *format.DecimalType:
		return v.String()
	case *format.TimeType:
		return "TIME(" + timeUnitName(v.Unit) + ")"
	case *format.TimestampType:
		return "TIMESTAMP(" + timeUnitName(v.Unit) + ")"
	case *format.IntType:
		return v.String()
	case *format.ListType:
		return "LIST"
	case *format.MapType:
		return "MAP"
	case *format.NullType:
		return "NULL"
	default:
		return lt.String()
	}
}

func timeUnitName(u format.TimeUnit) string {
	if u.Value == nil {
		return ""
	}
	return u.Value.String()
}

// convertLeafValue converts a single decoded parquet.Value from a leaf
// column into the JSON-safe Go representation described in the package
// contract. It always returns a value that encoding/json can marshal.
func convertLeafValue(typ parquet.Type, v parquet.Value) any {
	if v.IsNull() {
		return nil
	}

	if lt := typ.LogicalType(); lt != nil && lt.Value != nil {
		switch lv := lt.Value.(type) {
		case *format.DecimalType:
			return decimalToFloat64(typ, v, lv.Precision, lv.Scale)
		case *format.DateType:
			return dateToString(v)
		case *format.TimestampType:
			return timestampToString(v, lv.Unit)
		case *format.StringType, *format.EnumType, *format.JsonType:
			return byteArrayToStringOrBase64(v.ByteArray())
		}
	}

	switch typ.Kind() {
	case parquet.Boolean:
		return v.Boolean()
	case parquet.Int32:
		return int64(v.Int32())
	case parquet.Int64:
		return v.Int64()
	case parquet.Int96:
		return int96ToTime(v.Int96()).Format(time.RFC3339Nano)
	case parquet.Float:
		return safeFloat(float64(v.Float()))
	case parquet.Double:
		return safeFloat(v.Double())
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return base64.StdEncoding.EncodeToString(v.ByteArray())
	default:
		return nil
	}
}

// safeFloat returns f as a JSON-safe any, substituting nil for NaN and
// infinities since encoding/json cannot represent them.
func safeFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

func byteArrayToStringOrBase64(b []byte) any {
	if utf8.Valid(b) {
		return string(b)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// maxDecimalScale is the largest DECIMAL scale this package will honour.
//
// A file's precision and scale come straight out of its footer and nothing --
// not the spec's readers, not parquet-go -- checks them, so they are attacker
// input: a few hundred bytes of parquet can declare DECIMAL(9, 1000000000)
// and every single cell then asks for 10^1000000000 as a big.Int. Measured on
// this code, 10^1e7 costs 0.67 s and 16 MB and 10^1e8 costs 26 s and 159 MB,
// multiplied by rows x columns -- one `GET .../parquet/rows` is enough to stop
// the server, and viewer.Scan drags the experiments indexer in with it.
//
// 76 is not arbitrary: it is the widest decimal a 256-bit unscaled value can
// express (Arrow's Decimal256 ceiling), so it still admits every decimal a
// real writer emits -- Spark, Hive and pandas stop at 38 -- while capping the
// exponentiation at ~256 bits, which is free. Anything past it is not a
// decimal anyone wrote, so it is reported as null rather than computed.
const maxDecimalScale = 76

// decimalPrec is the working precision of the big.Float division below: wide
// enough that a full-width unscaled value survives it, since the result is
// truncated to a float64 anyway.
const decimalPrec = 256

// decimalScaleFactors holds 10^scale for every scale in range, built once at
// init instead of per cell (the whole point of the bound above). big.Float
// operations never mutate their operands, so sharing these across the
// goroutines serving different requests is safe.
var decimalScaleFactors = func() [maxDecimalScale + 1]*big.Float {
	var out [maxDecimalScale + 1]*big.Float
	for i := range out {
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(i)), nil)
		out[i] = new(big.Float).SetPrec(decimalPrec).SetInt(pow)
	}
	return out
}()

// validDecimalAnnotation reports whether a DECIMAL annotation is one the
// parquet spec allows (LogicalTypes.md: a positive precision, and a scale that
// is neither negative nor larger than the precision) and small enough to
// evaluate. A negative scale is deliberately rejected rather than clamped: it
// is not legal parquet, and no writer produces one -- Spark's and Arrow's
// decimal types both refuse it -- so a file carrying one is malformed, not a
// dialect to support.
func validDecimalAnnotation(precision, scale int32) bool {
	return precision > 0 && scale >= 0 && scale <= precision && scale <= maxDecimalScale
}

// decimalToFloat64 applies a DECIMAL logical type's scale to the physical
// value, returning a float64 (or nil for NaN/Inf, for an annotation outside
// the bounds above, and for unsupported physical representations).
func decimalToFloat64(typ parquet.Type, v parquet.Value, precision, scale int32) any {
	if !validDecimalAnnotation(precision, scale) {
		return nil
	}
	switch typ.Kind() {
	case parquet.Int32:
		return safeFloat(float64(v.Int32()) / math.Pow10(int(scale)))
	case parquet.Int64:
		return safeFloat(float64(v.Int64()) / math.Pow10(int(scale)))
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return safeFloat(decimalBytesToFloat64(v.ByteArray(), scale))
	default:
		return nil
	}
}

// decimalBytesToFloat64 decodes a big-endian two's-complement integer (the
// on-disk representation of BYTE_ARRAY/FIXED_LEN_BYTE_ARRAY decimals) and
// applies the given scale, which the caller has already bounded.
func decimalBytesToFloat64(data []byte, scale int32) float64 {
	if len(data) == 0 {
		return 0
	}
	val := new(big.Int)
	if data[0]&0x80 != 0 {
		tmp := make([]byte, len(data))
		for i, b := range data {
			tmp[i] = ^b
		}
		val.SetBytes(tmp)
		val.Add(val, big.NewInt(1))
		val.Neg(val)
	} else {
		val.SetBytes(data)
	}

	f := new(big.Float).SetPrec(decimalPrec).SetInt(val)
	f.Quo(f, decimalScaleFactors[scale])
	out, _ := f.Float64()
	return out
}

// dateToString formats a DATE value (days since the Unix epoch) as
// "2006-01-02".
func dateToString(v parquet.Value) string {
	days := int64(v.Int32())
	return time.Unix(days*86400, 0).UTC().Format("2006-01-02")
}

// timestampToString formats a TIMESTAMP value as RFC3339.
func timestampToString(v parquet.Value, unit format.TimeUnit) string {
	raw := v.Int64()
	var t time.Time
	switch unit.Value.(type) {
	case *format.MilliSeconds:
		t = time.UnixMilli(raw).UTC()
	case *format.NanoSeconds:
		t = time.Unix(0, raw).UTC()
	default: // MicroSeconds, and any unrecognized/future unit.
		t = time.UnixMicro(raw).UTC()
	}
	return t.Format(time.RFC3339Nano)
}

// int96ToTime decodes the legacy INT96 timestamp encoding: the low 64 bits
// are nanoseconds within the day, and the high 32 bits are a Julian day
// number.
func int96ToTime(i deprecated.Int96) time.Time {
	julianDay := int64(i[2])
	nanosOfDay := int64(i[1])<<32 | int64(i[0])
	days := julianDay - julianDayOfUnixEpoch
	return time.Unix(days*86400, nanosOfDay).UTC()
}

// normalizeGeneric walks a value tree produced by parquet-go's generic
// (reflection-based) row reconstruction -- used as a fallback for schemas
// containing nested groups, LIST, or MAP columns -- and converts it into
// the same JSON-safe representation that convertLeafValue produces for flat
// columns. Unlike convertLeafValue it does not have access to logical type
// information for every nested leaf, so it makes a best effort: byte
// sequences that round-tripped as Go strings are kept as strings when valid
// UTF-8 and base64-encoded otherwise. Whatever the input, the result is
// always safe for encoding/json.
func normalizeGeneric(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = normalizeGeneric(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = normalizeGeneric(vv)
		}
		return out
	case bool:
		return x
	case string:
		if utf8.ValidString(x) {
			return x
		}
		return base64.StdEncoding.EncodeToString([]byte(x))
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case float32:
		return safeFloat(float64(x))
	case float64:
		return safeFloat(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case deprecated.Int96:
		return int96ToTime(x).Format(time.RFC3339Nano)
	case *big.Float:
		f, _ := x.Float64()
		return safeFloat(f)
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint())
	case reflect.Slice, reflect.Array:
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = normalizeGeneric(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		for _, k := range rv.MapKeys() {
			out[fmt.Sprint(k.Interface())] = normalizeGeneric(rv.MapIndex(k).Interface())
		}
		return out
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return normalizeGeneric(rv.Elem().Interface())
	default:
		return fmt.Sprintf("%v", v)
	}
}

// firstLeafColumn returns the first leaf column found via a depth-first walk
// of c, or nil if c has no leaves (an empty schema).
func firstLeafColumn(c *parquet.Column) *parquet.Column {
	if c == nil {
		return nil
	}
	if c.Leaf() {
		return c
	}
	for _, child := range c.Columns() {
		if lc := firstLeafColumn(child); lc != nil {
			return lc
		}
	}
	return nil
}
