package modelmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// A `torch.save` checkpoint is a zip archive whose `data.pkl` member holds the
// pickled object graph; the weights live in sibling members that are never
// read here. Files written before PyTorch 1.6 use the legacy layout instead: a
// short run of pickles followed by raw storage bytes.

const (
	// maxPickleFile bounds the `data.pkl` member we will decode.
	maxPickleFile = 64 << 20
	// legacyPrefix is how far into a legacy checkpoint the object pickle may
	// start. The pickles that precede it are tiny, so this is generous.
	legacyPrefix = 64 << 20
	// maxLegacyLoads is how many leading pickles we try before giving up on
	// finding the object graph.
	maxLegacyLoads = 5
	// maxCollectedTensors stops a pathological graph from exhausting memory.
	// Real checkpoints are three orders of magnitude below this.
	maxCollectedTensors = 200_000
	// maxMetadataEntries bounds the scalar values reported alongside weights.
	maxMetadataEntries = 128
	// maxWalkDepth stops recursion in a deeply nested or cyclic graph.
	maxWalkDepth = 8
	// maxSequenceItems bounds how far into a list this reader descends.
	maxSequenceItems = 1024
)

// torchTensor is a tensor recovered from a rebuild call: everything the
// pickle records about it except the bytes.
type torchTensor struct {
	DType string
	Shape []int64
}

func inspectPyTorch(ctx context.Context, size int64, fetch Fetcher) (*Info, error) {
	if size < 4 {
		return nil, fmt.Errorf("modelmeta: file is %d bytes, too small to be a checkpoint", size)
	}
	src := newRangeReaderAt(ctx, size, fetch)

	magic := make([]byte, 4)
	if _, err := src.ReadAt(magic, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("modelmeta: read checkpoint magic: %w", err)
	}
	if bytes.HasPrefix(magic, []byte("PK\x03\x04")) {
		return inspectTorchZip(src, size)
	}
	return inspectTorchLegacy(src, size)
}

// inspectTorchZip reads only the archive's central directory and its
// `data.pkl` member, so the cost is a few ranged reads no matter how large
// the weights are.
func inspectTorchZip(src *rangeReaderAt, size int64) (*Info, error) {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return nil, fmt.Errorf("modelmeta: read checkpoint archive: %w", err)
	}
	member := findPickleMember(zr)
	if member == nil {
		return nil, fmt.Errorf("modelmeta: checkpoint archive has no data.pkl member")
	}
	if member.UncompressedSize64 > maxPickleFile {
		return nil, fmt.Errorf("modelmeta: %s is %d bytes, over the %d byte limit",
			member.Name, member.UncompressedSize64, int64(maxPickleFile))
	}

	rc, err := member.Open()
	if err != nil {
		return nil, fmt.Errorf("modelmeta: open %s: %w", member.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxPickleFile))
	if err != nil {
		return nil, fmt.Errorf("modelmeta: read %s: %w", member.Name, err)
	}

	root, err := newUnpickler(bytes.NewReader(data), torchReduce).load()
	if err != nil {
		return nil, fmt.Errorf("modelmeta: decode %s: %w", member.Name, err)
	}

	info := &Info{Format: FormatPyTorch, HeaderBytes: int64(len(data))}
	buildTorchInfo(info, root)
	return info, nil
}

// findPickleMember picks the archive's object pickle. torch names it
// `<archive>/data.pkl`; the shallowest match wins so a nested copy cannot
// shadow the real one.
func findPickleMember(zr *zip.Reader) *zip.File {
	var best *zip.File
	for _, f := range zr.File {
		if path.Base(f.Name) != "data.pkl" {
			continue
		}
		if best == nil || strings.Count(f.Name, "/") < strings.Count(best.Name, "/") {
			best = f
		}
	}
	return best
}

// inspectTorchLegacy handles pre-1.6 checkpoints, where the object pickle is
// the fourth of a run of pickles at the head of the file.
func inspectTorchLegacy(src *rangeReaderAt, size int64) (*Info, error) {
	n := size
	if n > legacyPrefix {
		n = legacyPrefix
	}
	u := newUnpickler(io.NewSectionReader(src, 0, n), torchReduce)

	var fallback any
	for i := 0; i < maxLegacyLoads; i++ {
		root, err := u.load()
		if err != nil {
			break
		}
		// The magic number, protocol version and sys_info come first; the
		// object graph is whichever pickle actually carries tensors.
		info := &Info{Format: FormatPyTorch, HeaderBytes: n}
		buildTorchInfo(info, root)
		if info.NumTensors > 0 {
			return info, nil
		}
		if fallback == nil && isContainer(root) {
			fallback = root
		}
	}

	if fallback != nil {
		info := &Info{Format: FormatPyTorch, HeaderBytes: n}
		buildTorchInfo(info, fallback)
		warn(info, "no tensors found: this file does not look like a PyTorch checkpoint")
		return info, nil
	}
	return nil, fmt.Errorf("modelmeta: file is neither a zip checkpoint nor a legacy torch pickle")
}

func isContainer(v any) bool {
	switch v.(type) {
	case *pickleDict, *pickleList, pickleTuple, *pickleObject:
		return true
	default:
		return false
	}
}

// torchReduce recognises the handful of `torch._utils` rebuild functions that
// carry a tensor's dtype and shape. Everything else falls through to an inert
// placeholder, so an unfamiliar checkpoint degrades to "fewer tensors found"
// rather than an error.
func torchReduce(class pickleGlobal, args []any) (any, bool) {
	switch {
	case class.Module == "collections" && class.Name == "OrderedDict":
		// Built empty and then filled by SETITEMS.
		return newPickleDict(), true

	case class.Name == "_rebuild_tensor_v2" || class.Name == "_rebuild_tensor":
		// (storage, storage_offset, size, stride, ...)
		if len(args) < 3 {
			return nil, false
		}
		return &torchTensor{DType: storageDType(args[0]), Shape: toShape(args[2])}, true

	case class.Name == "_rebuild_tensor_v3":
		// Same prefix as v2, plus an explicit dtype at the end.
		if len(args) < 3 {
			return nil, false
		}
		t := &torchTensor{DType: storageDType(args[0]), Shape: toShape(args[2])}
		if len(args) >= 7 {
			if dtype := torchDTypeName(args[6]); dtype != "" {
				t.DType = dtype
			}
		}
		return t, true

	case class.Name == "_rebuild_meta_tensor_no_storage":
		// (dtype, size, stride, requires_grad) -- no storage to read from.
		if len(args) < 2 {
			return nil, false
		}
		return &torchTensor{DType: torchDTypeName(args[0]), Shape: toShape(args[1])}, true

	case strings.HasPrefix(class.Name, "_rebuild_parameter"),
		class.Name == "_rebuild_from_type_v2",
		class.Name == "_rebuild_wrapper_subclass":
		// Wrappers around a tensor: report the tensor they wrap.
		if t := firstTensor(args, 0); t != nil {
			return t, true
		}
		return nil, false
	}
	return nil, false
}

// firstTensor finds the tensor nested somewhere in a rebuild call's arguments.
func firstTensor(args []any, depth int) *torchTensor {
	if depth > 3 {
		return nil
	}
	for _, a := range args {
		switch v := a.(type) {
		case *torchTensor:
			return v
		case pickleTuple:
			if t := firstTensor(v, depth+1); t != nil {
				return t
			}
		case *pickleList:
			if t := firstTensor(v.Items, depth+1); t != nil {
				return t
			}
		}
	}
	return nil
}

// storageDType reads the dtype out of a storage persistent id, whose second
// element names the storage class, e.g. torch.FloatStorage.
func storageDType(v any) string {
	id, ok := v.(picklePersID)
	if !ok {
		return "unknown"
	}
	parts, ok := id.Value.(pickleTuple)
	if !ok || len(parts) < 2 {
		return "unknown"
	}
	if name := torchStorageDTypes[globalName(parts[1])]; name != "" {
		return name
	}
	return "unknown"
}

func globalName(v any) string {
	if g, ok := v.(pickleGlobal); ok {
		return g.Name
	}
	return asString(v)
}

// torchDTypeName maps a `torch.float32`-style global onto our neutral names.
func torchDTypeName(v any) string {
	g, ok := v.(pickleGlobal)
	if !ok {
		return ""
	}
	switch g.Name {
	case "float", "float32":
		return "float32"
	case "double", "float64":
		return "float64"
	case "half", "float16":
		return "float16"
	case "long", "int64":
		return "int64"
	case "int", "int32":
		return "int32"
	case "short", "int16":
		return "int16"
	default:
		return g.Name
	}
}

// torchStorageDTypes maps torch storage classes onto dtype names shared with
// the safetensors reader.
var torchStorageDTypes = map[string]string{
	"FloatStorage":         "float32",
	"DoubleStorage":        "float64",
	"HalfStorage":          "float16",
	"BFloat16Storage":      "bfloat16",
	"LongStorage":          "int64",
	"IntStorage":           "int32",
	"ShortStorage":         "int16",
	"CharStorage":          "int8",
	"ByteStorage":          "uint8",
	"BoolStorage":          "bool",
	"ComplexFloatStorage":  "complex64",
	"ComplexDoubleStorage": "complex128",
	"Float8_e4m3fnStorage": "float8_e4m3fn",
	"Float8_e5m2Storage":   "float8_e5m2",
	"QInt8Storage":         "qint8",
	"QUInt8Storage":        "quint8",
	"QInt32Storage":        "qint32",
}

// torchDTypeBytes is the width of each dtype, used to size tensors the pickle
// does not measure for us. A missing entry leaves SizeBytes at 0.
var torchDTypeBytes = map[string]int64{
	"float64": 8, "int64": 8, "uint64": 8, "complex128": 16,
	"float32": 4, "int32": 4, "uint32": 4, "complex64": 8, "qint32": 4,
	"float16": 2, "bfloat16": 2, "int16": 2, "uint16": 2,
	"int8": 1, "uint8": 1, "bool": 1, "qint8": 1, "quint8": 1,
	"float8_e4m3fn": 1, "float8_e5m2": 1,
}

// toShape reads a size tuple, which pickle may present as a tuple or a list.
func toShape(v any) []int64 {
	items := toSlice(v)
	shape := make([]int64, 0, len(items))
	for _, item := range items {
		if n, ok := item.(int64); ok {
			shape = append(shape, n)
		}
	}
	return shape
}

// buildTorchInfo walks the decoded graph, pulling out tensors and the scalar
// values stored beside them (epoch, global_step, and friends).
func buildTorchInfo(info *Info, root any) {
	c := &collector{meta: map[string]string{}}
	c.walk("", root, 0)
	if c.overflowed {
		warn(info, "checkpoint holds more than %d tensors; the rest were skipped", maxCollectedTensors)
	}
	if _, isObject := root.(*pickleObject); isObject {
		warn(info, "this checkpoint stores a pickled object rather than a plain state_dict")
	}
	info.Metadata = c.meta
	summarize(info, c.tensors)
}

type collector struct {
	tensors    []Tensor
	meta       map[string]string
	overflowed bool
}

func (c *collector) walk(prefix string, v any, depth int) {
	if depth > maxWalkDepth || c.overflowed {
		return
	}
	switch node := v.(type) {
	case *torchTensor:
		c.addTensor(prefix, node)
	case *pickleDict:
		for i, key := range node.Keys {
			c.walk(joinKey(prefix, keyString(key, i)), node.Values[i], depth+1)
		}
	case *pickleList:
		c.walkSequence(prefix, node.Items, depth)
	case pickleTuple:
		c.walkSequence(prefix, node, depth)
	case *pickleObject:
		// A pickled module keeps its parameters in its __setstate__ payload.
		c.walk(prefix, node.State, depth+1)
	case picklePersID, pickleGlobal, nil:
		// Storage pointers and class references carry no metadata of their own.
	default:
		c.addMetadata(prefix, node, depth)
	}
}

func (c *collector) walkSequence(prefix string, items []any, depth int) {
	for i, item := range items {
		if i >= maxSequenceItems {
			return
		}
		c.walk(joinKey(prefix, strconv.Itoa(i)), item, depth+1)
	}
}

func (c *collector) addTensor(name string, t *torchTensor) {
	if len(c.tensors) >= maxCollectedTensors {
		c.overflowed = true
		return
	}
	if name == "" {
		name = "<tensor>"
	}
	n := numElements(t.Shape)
	dtype := t.DType
	if dtype == "" {
		dtype = "unknown"
	}
	c.tensors = append(c.tensors, Tensor{
		Name:          name,
		DType:         dtype,
		Shape:         t.Shape,
		NumParameters: n,
		SizeBytes:     n * torchDTypeBytes[dtype],
	})
}

// addMetadata records the scalars sitting near the top of a checkpoint. Deeply
// nested values are skipped: they are model internals, not metadata.
func (c *collector) addMetadata(name string, v any, depth int) {
	if name == "" || depth > 3 || len(c.meta) >= maxMetadataEntries {
		return
	}
	var text string
	switch value := v.(type) {
	case string:
		text = value
	case int64:
		text = strconv.FormatInt(value, 10)
	case float64:
		text = strconv.FormatFloat(value, 'g', -1, 64)
	case bool:
		text = strconv.FormatBool(value)
	default:
		return
	}
	if cut := truncateRunes(text, 512); cut != text {
		text = cut + "…"
	}
	c.meta[name] = text
}

// truncateRunes returns the first n runes of s, or s unchanged when it
// already has n or fewer. Cutting by rune count rather than slicing by byte
// index never splits a multi-byte character in two, which would otherwise
// leave an invalid UTF-8 tail that json.Marshal silently replaces with
// U+FFFD on the way out. 512 is a character count, not a byte count: nothing
// downstream requires a byte ceiling on a metadata value, and a byte-based
// cap would give a checkpoint's Japanese metadata roughly a third as many
// characters as English for the same limit.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func keyString(key any, index int) string {
	switch k := key.(type) {
	case string:
		return k
	case int64:
		return strconv.FormatInt(k, 10)
	case []byte:
		return string(k)
	default:
		return strconv.Itoa(index)
	}
}
