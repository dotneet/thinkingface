package modelmeta

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// This file implements just enough of the pickle virtual machine to walk the
// object graph `torch.save` writes. It never imports, constructs or calls
// anything: GLOBAL only records the module/name it names, and REDUCE hands
// that name to a callback (see torch.go) that recognises the handful of
// `torch._utils` rebuild functions. Unknown callables become inert
// placeholders. Reading a checkpoint therefore cannot execute code the way
// `torch.load` can.

// pickleGlobal names a Python attribute, e.g. torch._utils._rebuild_tensor_v2.
type pickleGlobal struct {
	Module string
	Name   string
}

func (g pickleGlobal) String() string { return g.Module + "." + g.Name }

// pickleDict is an insertion-ordered mapping; both dict and OrderedDict use
// it, since state dicts rely on their key order being meaningful.
type pickleDict struct {
	Keys   []any
	Values []any
	index  map[string]int
}

func newPickleDict() *pickleDict { return &pickleDict{index: map[string]int{}} }

func (d *pickleDict) set(key, value any) {
	if k, ok := key.(string); ok {
		if at, exists := d.index[k]; exists {
			d.Values[at] = value
			return
		}
		d.index[k] = len(d.Keys)
	}
	d.Keys = append(d.Keys, key)
	d.Values = append(d.Values, value)
}

// pickleList is a mutable list; APPEND(S) writes through the pointer.
type pickleList struct{ Items []any }

// pickleTuple is an immutable sequence.
type pickleTuple []any

// pickleObject is any reconstruction this reader does not model, kept so a
// caller can still see what class was involved.
type pickleObject struct {
	Class pickleGlobal
	Args  []any
	State any
}

// picklePersID is the value of a persistent id -- in a torch archive, the
// tuple that points at a storage record.
type picklePersID struct{ Value any }

// maxPickleStack stops a malformed stream from growing the stack forever.
const maxPickleStack = 1 << 20

// maxPickleMemoEntries bounds the memo table the same way push() bounds the
// stack. MEMOIZE (opcode 0x94) is a single byte that peeks the stack's top
// without popping it, so a stream can push one value and then repeat MEMOIZE
// forever without ever tripping the stack limit above -- every repetition
// only grows the memo. A map[uint64]any entry costs around 45 bytes, so an
// unbounded memo let a file well within maxPickleFile's 64 MiB limit force
// gigabytes of live heap out of a stream of single-byte opcodes that
// compress to almost nothing, the same trick a zip bomb uses. The limit
// mirrors maxPickleStack: a legitimate stream never needs to remember more
// distinct values than it could ever hold on the stack at once.
const maxPickleMemoEntries = maxPickleStack

// maxPickleMarks bounds the mark stack for the same reason: MARK ('(') is
// also a single byte that never touches the value stack, so it needs a limit
// of its own rather than inheriting the one on u.stack.
const maxPickleMarks = maxPickleStack

// reducer turns a REDUCE/NEWOBJ of a known callable into a value. Returning
// false leaves the unpickler to build a generic pickleObject.
type reducer func(class pickleGlobal, args []any) (any, bool)

type unpickler struct {
	r      *bufio.Reader
	stack  []any
	marks  []int
	memo   map[uint64]any
	reduce reducer
}

func newUnpickler(r io.Reader, reduce reducer) *unpickler {
	return &unpickler{r: bufio.NewReaderSize(r, 64<<10), memo: map[uint64]any{}, reduce: reduce}
}

func (u *unpickler) push(v any) error {
	if len(u.stack) >= maxPickleStack {
		return fmt.Errorf("pickle: stack overflow")
	}
	u.stack = append(u.stack, v)
	return nil
}

func (u *unpickler) pop() (any, error) {
	if len(u.stack) == 0 {
		return nil, fmt.Errorf("pickle: pop from empty stack")
	}
	v := u.stack[len(u.stack)-1]
	u.stack = u.stack[:len(u.stack)-1]
	return v, nil
}

// memoPut records a memo table entry for BINPUT / LONG_BINPUT / MEMOIZE /
// PUT, enforcing maxPickleMemoEntries so none of them can grow the map
// without bound (see the constant's comment for why they need their own
// limit rather than sharing the stack's). Protocol 0 pickles legitimately
// reuse memo slot numbers within a stream, so only entries that actually
// grow the table count against the limit -- an overwrite of an existing key
// is always allowed.
func (u *unpickler) memoPut(key uint64, value any) error {
	if _, exists := u.memo[key]; !exists && len(u.memo) >= maxPickleMemoEntries {
		return fmt.Errorf("pickle: memo table exceeds %d entries", maxPickleMemoEntries)
	}
	u.memo[key] = value
	return nil
}

// popMark returns everything pushed since the most recent MARK.
func (u *unpickler) popMark() ([]any, error) {
	if len(u.marks) == 0 {
		return nil, fmt.Errorf("pickle: no MARK on the stack")
	}
	at := u.marks[len(u.marks)-1]
	u.marks = u.marks[:len(u.marks)-1]
	if at > len(u.stack) {
		return nil, fmt.Errorf("pickle: corrupt MARK")
	}
	items := make([]any, len(u.stack)-at)
	copy(items, u.stack[at:])
	u.stack = u.stack[:at]
	return items, nil
}

func (u *unpickler) readN(n int64) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("pickle: negative length %d", n)
	}
	if n > maxPickleBytes {
		return nil, fmt.Errorf("pickle: refusing to read a %d byte value", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(u.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// maxPickleBytes bounds one length-prefixed value.
const maxPickleBytes = 64 << 20

func (u *unpickler) readLine() (string, error) {
	line, err := u.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (u *unpickler) readUint(size int) (uint64, error) {
	buf, err := u.readN(int64(size))
	if err != nil {
		return 0, err
	}
	var v uint64
	for i := size - 1; i >= 0; i-- {
		v = v<<8 | uint64(buf[i])
	}
	return v, nil
}

// load runs the machine until STOP and returns the value left on the stack.
func (u *unpickler) load() (any, error) {
	u.stack = u.stack[:0]
	u.marks = u.marks[:0]
	for {
		op, err := u.r.ReadByte()
		if err != nil {
			return nil, err
		}
		done, err := u.step(op)
		if err != nil {
			return nil, fmt.Errorf("pickle: opcode %q: %w", string(rune(op)), err)
		}
		if done {
			return u.pop()
		}
	}
}

// step executes one opcode, reporting whether STOP was reached.
func (u *unpickler) step(op byte) (bool, error) {
	switch op {
	case '.': // STOP
		return true, nil

	case 0x80: // PROTO
		_, err := u.r.ReadByte()
		return false, err
	case 0x95: // FRAME
		_, err := u.readUint(8)
		return false, err

	case '(': // MARK
		if len(u.marks) >= maxPickleMarks {
			return false, fmt.Errorf("pickle: more than %d MARK opcodes", maxPickleMarks)
		}
		u.marks = append(u.marks, len(u.stack))
		return false, nil
	case '1': // POP_MARK
		_, err := u.popMark()
		return false, err
	case '0': // POP
		_, err := u.pop()
		return false, err
	case '2': // DUP
		v, err := u.pop()
		if err != nil {
			return false, err
		}
		if err := u.push(v); err != nil {
			return false, err
		}
		return false, u.push(v)

	case 'N': // NONE
		return false, u.push(nil)
	case 0x88: // NEWTRUE
		return false, u.push(true)
	case 0x89: // NEWFALSE
		return false, u.push(false)

	case 'J': // BININT
		v, err := u.readUint(4)
		if err != nil {
			return false, err
		}
		return false, u.push(int64(int32(v)))
	case 'K': // BININT1
		v, err := u.readUint(1)
		if err != nil {
			return false, err
		}
		return false, u.push(int64(v))
	case 'M': // BININT2
		v, err := u.readUint(2)
		if err != nil {
			return false, err
		}
		return false, u.push(int64(v))
	case 'I': // INT (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		switch line {
		case "01":
			return false, u.push(true)
		case "00":
			return false, u.push(false)
		}
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return false, err
		}
		return false, u.push(n)
	case 'L': // LONG (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		return false, u.push(parseBigDecimal(strings.TrimSuffix(line, "L")))
	case 0x8a, 0x8b: // LONG1, LONG4
		width := 1
		if op == 0x8b {
			width = 4
		}
		n, err := u.readUint(width)
		if err != nil {
			return false, err
		}
		buf, err := u.readN(int64(n))
		if err != nil {
			return false, err
		}
		return false, u.push(decodeLong(buf))

	case 'G': // BINFLOAT (big-endian double)
		buf, err := u.readN(8)
		if err != nil {
			return false, err
		}
		return false, u.push(math.Float64frombits(binary.BigEndian.Uint64(buf)))
	case 'F': // FLOAT (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		f, err := strconv.ParseFloat(line, 64)
		if err != nil {
			return false, err
		}
		return false, u.push(f)

	case 'X', 0x8c, 0x8d: // BINUNICODE, SHORT_BINUNICODE, BINUNICODE8
		size := map[byte]int{'X': 4, 0x8c: 1, 0x8d: 8}[op]
		n, err := u.readUint(size)
		if err != nil {
			return false, err
		}
		buf, err := u.readN(int64(n))
		if err != nil {
			return false, err
		}
		return false, u.push(string(buf))
	case 'V': // UNICODE (text, raw-unicode-escape)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		return false, u.push(line)
	case 'T', 'U': // BINSTRING, SHORT_BINSTRING
		size := 4
		if op == 'U' {
			size = 1
		}
		n, err := u.readUint(size)
		if err != nil {
			return false, err
		}
		buf, err := u.readN(int64(n))
		if err != nil {
			return false, err
		}
		return false, u.push(string(buf))
	case 'S': // STRING (text, quoted)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		if s, err := strconv.Unquote(line); err == nil {
			return false, u.push(s)
		}
		return false, u.push(strings.Trim(line, "'\""))
	case 'B', 'C', 0x8e, 0x96: // BINBYTES, SHORT_BINBYTES, BINBYTES8, BYTEARRAY8
		size := map[byte]int{'B': 4, 'C': 1, 0x8e: 8, 0x96: 8}[op]
		n, err := u.readUint(size)
		if err != nil {
			return false, err
		}
		buf, err := u.readN(int64(n))
		if err != nil {
			return false, err
		}
		return false, u.push(buf)

	case ')': // EMPTY_TUPLE
		return false, u.push(pickleTuple{})
	case 't': // TUPLE
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		return false, u.push(pickleTuple(items))
	case 0x85, 0x86, 0x87: // TUPLE1, TUPLE2, TUPLE3
		n := int(op-0x85) + 1
		if len(u.stack) < n {
			return false, fmt.Errorf("pickle: TUPLE%d needs %d values", n, n)
		}
		items := make(pickleTuple, n)
		copy(items, u.stack[len(u.stack)-n:])
		u.stack = u.stack[:len(u.stack)-n]
		return false, u.push(items)

	case ']': // EMPTY_LIST
		return false, u.push(&pickleList{})
	case 'l': // LIST
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		return false, u.push(&pickleList{Items: items})
	case 'a': // APPEND
		value, err := u.pop()
		if err != nil {
			return false, err
		}
		list, err := u.pop()
		if err != nil {
			return false, err
		}
		if l, ok := list.(*pickleList); ok {
			l.Items = append(l.Items, value)
		}
		return false, u.push(list)
	case 'e', 0x90: // APPENDS, ADDITEMS
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: APPENDS with no target")
		}
		if l, ok := u.stack[len(u.stack)-1].(*pickleList); ok {
			l.Items = append(l.Items, items...)
		}
		return false, nil

	case '}': // EMPTY_DICT
		return false, u.push(newPickleDict())
	case 0x8f: // EMPTY_SET
		return false, u.push(&pickleList{})
	case 0x91: // FROZENSET
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		return false, u.push(&pickleList{Items: items})
	case 'd': // DICT
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		d := newPickleDict()
		for i := 0; i+1 < len(items); i += 2 {
			d.set(items[i], items[i+1])
		}
		return false, u.push(d)
	case 's': // SETITEM
		value, err := u.pop()
		if err != nil {
			return false, err
		}
		key, err := u.pop()
		if err != nil {
			return false, err
		}
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: SETITEM with no target")
		}
		if d, ok := u.stack[len(u.stack)-1].(*pickleDict); ok {
			d.set(key, value)
		}
		return false, nil
	case 'u': // SETITEMS
		items, err := u.popMark()
		if err != nil {
			return false, err
		}
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: SETITEMS with no target")
		}
		if d, ok := u.stack[len(u.stack)-1].(*pickleDict); ok {
			for i := 0; i+1 < len(items); i += 2 {
				d.set(items[i], items[i+1])
			}
		}
		return false, nil

	case 'q', 'r', 0x94: // BINPUT, LONG_BINPUT, MEMOIZE
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: memo write with an empty stack")
		}
		value := u.stack[len(u.stack)-1]
		var key uint64
		switch op {
		case 'q':
			k, err := u.readUint(1)
			if err != nil {
				return false, err
			}
			key = k
		case 'r':
			k, err := u.readUint(4)
			if err != nil {
				return false, err
			}
			key = k
		default:
			key = uint64(len(u.memo))
		}
		return false, u.memoPut(key, value)
	case 'p': // PUT (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		key, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
		if err != nil {
			return false, err
		}
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: PUT with an empty stack")
		}
		return false, u.memoPut(key, u.stack[len(u.stack)-1])
	case 'h', 'j': // BINGET, LONG_BINGET
		size := 1
		if op == 'j' {
			size = 4
		}
		key, err := u.readUint(size)
		if err != nil {
			return false, err
		}
		return false, u.push(u.memo[key])
	case 'g': // GET (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		key, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
		if err != nil {
			return false, err
		}
		return false, u.push(u.memo[key])

	case 'c': // GLOBAL
		module, err := u.readLine()
		if err != nil {
			return false, err
		}
		name, err := u.readLine()
		if err != nil {
			return false, err
		}
		return false, u.push(pickleGlobal{Module: module, Name: name})
	case 0x93: // STACK_GLOBAL
		name, err := u.pop()
		if err != nil {
			return false, err
		}
		module, err := u.pop()
		if err != nil {
			return false, err
		}
		return false, u.push(pickleGlobal{Module: asString(module), Name: asString(name)})

	case 'R': // REDUCE
		args, err := u.pop()
		if err != nil {
			return false, err
		}
		class, err := u.pop()
		if err != nil {
			return false, err
		}
		return false, u.push(u.call(class, toSlice(args)))
	case 0x81: // NEWOBJ
		args, err := u.pop()
		if err != nil {
			return false, err
		}
		class, err := u.pop()
		if err != nil {
			return false, err
		}
		return false, u.push(u.call(class, toSlice(args)))
	case 0x92: // NEWOBJ_EX
		_, err := u.pop() // kwargs
		if err != nil {
			return false, err
		}
		args, err := u.pop()
		if err != nil {
			return false, err
		}
		class, err := u.pop()
		if err != nil {
			return false, err
		}
		return false, u.push(u.call(class, toSlice(args)))
	case 'i', 'o': // INST, OBJ
		var class pickleGlobal
		if op == 'i' {
			module, err := u.readLine()
			if err != nil {
				return false, err
			}
			name, err := u.readLine()
			if err != nil {
				return false, err
			}
			class = pickleGlobal{Module: module, Name: name}
		}
		args, err := u.popMark()
		if err != nil {
			return false, err
		}
		if op == 'o' && len(args) > 0 {
			if g, ok := args[0].(pickleGlobal); ok {
				class, args = g, args[1:]
			}
		}
		return false, u.push(u.call(class, args))

	case 'b': // BUILD
		state, err := u.pop()
		if err != nil {
			return false, err
		}
		if len(u.stack) == 0 {
			return false, fmt.Errorf("pickle: BUILD with no target")
		}
		switch target := u.stack[len(u.stack)-1].(type) {
		case *pickleObject:
			target.State = state
		case *pickleDict:
			if src, ok := state.(*pickleDict); ok {
				for i, k := range src.Keys {
					target.set(k, src.Values[i])
				}
			}
		}
		return false, nil

	case 'Q': // BINPERSID
		id, err := u.pop()
		if err != nil {
			return false, err
		}
		return false, u.push(picklePersID{Value: id})
	case 'P': // PERSID (text)
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		return false, u.push(picklePersID{Value: line})

	default:
		return false, fmt.Errorf("pickle: unsupported opcode 0x%02x", op)
	}
}

// call applies the reducer to a named callable, falling back to an inert
// placeholder for anything it does not recognise.
func (u *unpickler) call(class any, args []any) any {
	g, ok := class.(pickleGlobal)
	if !ok {
		return &pickleObject{Args: args}
	}
	if u.reduce != nil {
		if v, handled := u.reduce(g, args); handled {
			return v
		}
	}
	return &pickleObject{Class: g, Args: args}
}

// toSlice normalises whatever REDUCE popped as its argument tuple.
func toSlice(v any) []any {
	switch t := v.(type) {
	case pickleTuple:
		return t
	case *pickleList:
		return t.Items
	case nil:
		return nil
	default:
		return []any{v}
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// decodeLong reads pickle's little-endian two's complement integer.
func decodeLong(buf []byte) any {
	if len(buf) == 0 {
		return int64(0)
	}
	be := make([]byte, len(buf))
	for i, b := range buf {
		be[len(buf)-1-i] = b
	}
	negative := be[0]&0x80 != 0
	n := new(big.Int).SetBytes(be)
	if negative {
		// Subtract 2^(8*len) to recover the signed value.
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(buf))))
	}
	if n.IsInt64() {
		return n.Int64()
	}
	return n.String()
}

func parseBigDecimal(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if n, ok := new(big.Int).SetString(s, 10); ok {
		return n.String()
	}
	return s
}
