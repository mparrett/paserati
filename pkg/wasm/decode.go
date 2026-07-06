package wasm

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Decode parses a wasm binary module into the IR. It handles the numeric +
// control-flow subset and returns a clear error on anything outside it.
func Decode(data []byte) (*Module, error) {
	r := &reader{buf: data}
	if err := r.expectHeader(); err != nil {
		return nil, err
	}

	m := &Module{}
	var funcTypeIdx []uint32 // parallel to the code section: type index per defined func

	for !r.eof() {
		secID, err := r.byte()
		if err != nil {
			return nil, err
		}
		secLen, err := r.u32()
		if err != nil {
			return nil, err
		}
		body, err := r.take(int(secLen))
		if err != nil {
			return nil, fmt.Errorf("section 0x%02x: %w", secID, err)
		}
		sr := &reader{buf: body}

		switch secID {
		case 1: // Type
			if err := decodeTypeSection(sr, m); err != nil {
				return nil, err
			}
		case 3: // Function
			idxs, err := decodeFunctionSection(sr)
			if err != nil {
				return nil, err
			}
			funcTypeIdx = idxs
		case 7: // Export
			if err := decodeExportSection(sr, m); err != nil {
				return nil, err
			}
		case 5: // Memory
			if err := decodeMemorySection(sr, m); err != nil {
				return nil, err
			}
		case 10: // Code
			if err := decodeCodeSection(sr, m, funcTypeIdx); err != nil {
				return nil, err
			}
		case 11: // Data
			if err := decodeDataSection(sr, m); err != nil {
				return nil, err
			}
		case 0: // Custom — skip (names, etc.)
		case 2: // Import
			return nil, fmt.Errorf("import section unsupported in this subset")
		default:
			// Global(6), Table(4), Element(9), etc. Skip silently; codegen
			// fails loudly if a body needs them.
		}
	}

	// Resolve each defined function's type pointer.
	for i := range m.Funcs {
		ti := m.Funcs[i].TypeIndex
		if int(ti) >= len(m.Types) {
			return nil, fmt.Errorf("func %d references type %d out of %d", i, ti, len(m.Types))
		}
		m.Funcs[i].Type = &m.Types[ti]
	}
	return m, nil
}

func decodeTypeSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		form, err := r.byte()
		if err != nil {
			return err
		}
		if form != 0x60 {
			return fmt.Errorf("type %d: expected func form 0x60, got 0x%02x", i, form)
		}
		var ft FuncType
		if ft.Params, err = r.valTypes(); err != nil {
			return err
		}
		if ft.Results, err = r.valTypes(); err != nil {
			return err
		}
		m.Types = append(m.Types, ft)
	}
	return nil
}

func decodeFunctionSection(r *reader) ([]uint32, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	idxs := make([]uint32, n)
	for i := range idxs {
		if idxs[i], err = r.u32(); err != nil {
			return nil, err
		}
	}
	return idxs, nil
}

func decodeExportSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		name, err := r.name()
		if err != nil {
			return err
		}
		kind, err := r.byte()
		if err != nil {
			return err
		}
		idx, err := r.u32()
		if err != nil {
			return err
		}
		m.Exports = append(m.Exports, Export{Name: name, Kind: kind, Index: idx})
	}
	return nil
}

func decodeMemorySection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		flags, err := r.byte()
		if err != nil {
			return err
		}
		min, err := r.u32()
		if err != nil {
			return err
		}
		mt := &MemType{Min: min}
		if flags&1 != 0 {
			if mt.Max, err = r.u32(); err != nil {
				return err
			}
			mt.HasMax = true
		}
		if m.Memory == nil { // only memory 0 is modelled
			m.Memory = mt
		}
	}
	return nil
}

func decodeDataSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		kind, err := r.u32()
		if err != nil {
			return err
		}
		if kind != 0 {
			return fmt.Errorf("data segment kind %d (passive/explicit-memory) unsupported", kind)
		}
		// Active segment, memory 0: a constant i32 offset expression.
		off, err := r.constI32Expr()
		if err != nil {
			return err
		}
		bytes, err := r.name() // length-prefixed byte vector, reused
		if err != nil {
			return err
		}
		m.Data = append(m.Data, DataSegment{Offset: off, Bytes: []byte(bytes)})
	}
	return nil
}

// constI32Expr reads a constant offset expression of the form
// `i32.const N; end` and returns N.
func (r *reader) constI32Expr() (int, error) {
	op, err := r.byte()
	if err != nil {
		return 0, err
	}
	if Opcode(op) != OpI32Const {
		return 0, fmt.Errorf("data offset expr: expected i32.const, got 0x%02x", op)
	}
	v, err := r.s64()
	if err != nil {
		return 0, err
	}
	end, err := r.byte()
	if err != nil {
		return 0, err
	}
	if Opcode(end) != OpEnd {
		return 0, fmt.Errorf("data offset expr: expected end, got 0x%02x", end)
	}
	return int(int32(v)), nil
}

func decodeCodeSection(r *reader, m *Module, funcTypeIdx []uint32) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	if int(n) != len(funcTypeIdx) {
		return fmt.Errorf("code section has %d bodies but function section declared %d", n, len(funcTypeIdx))
	}
	for i := uint32(0); i < n; i++ {
		size, err := r.u32()
		if err != nil {
			return err
		}
		bodyBytes, err := r.take(int(size))
		if err != nil {
			return err
		}
		fn := Func{TypeIndex: funcTypeIdx[i]}
		if err := decodeFuncBody(&reader{buf: bodyBytes}, &fn); err != nil {
			return fmt.Errorf("func %d body: %w", i, err)
		}
		m.Funcs = append(m.Funcs, fn)
	}
	return nil
}

func decodeFuncBody(r *reader, fn *Func) error {
	// Local declarations: repeated (count, valtype), run-length encoded.
	groups, err := r.u32()
	if err != nil {
		return err
	}
	for g := uint32(0); g < groups; g++ {
		count, err := r.u32()
		if err != nil {
			return err
		}
		vt, err := r.byte()
		if err != nil {
			return err
		}
		for c := uint32(0); c < count; c++ {
			fn.Locals = append(fn.Locals, ValType(vt))
		}
	}

	// Instructions until the body is fully consumed. The trailing byte is the
	// function's implicit `end`; we keep it in the stream.
	for !r.eof() {
		b, err := r.byte()
		if err != nil {
			return err
		}
		op := Opcode(b)
		kind, ok := immediateKind(op)
		if !ok {
			return fmt.Errorf("unsupported opcode 0x%02x (%s)", b, op)
		}
		ins := Instr{Op: op}
		switch kind {
		case immNone:
		case immU32:
			if ins.U32, err = r.u32(); err != nil {
				return err
			}
		case immBlockType:
			if ins.I64, err = r.blockType(); err != nil {
				return err
			}
		case immI32Const:
			v, err := r.s64() // s32 fits; wasm encodes it as a signed LEB
			if err != nil {
				return err
			}
			ins.I64 = v
		case immI64Const:
			if ins.I64, err = r.s64(); err != nil {
				return err
			}
		case immF32Const:
			bs, err := r.take(4)
			if err != nil {
				return err
			}
			ins.F64 = float64(math.Float32frombits(binary.LittleEndian.Uint32(bs)))
		case immF64Const:
			bs, err := r.take(8)
			if err != nil {
				return err
			}
			ins.F64 = math.Float64frombits(binary.LittleEndian.Uint64(bs))
		case immMemarg:
			if _, err = r.u32(); err != nil { // align (ignored)
				return err
			}
			if ins.U32, err = r.u32(); err != nil { // offset
				return err
			}
		case immMemory:
			if _, err = r.byte(); err != nil { // memory index (only 0 modelled)
				return err
			}
		}
		fn.Body = append(fn.Body, ins)
	}
	return nil
}

// --- low-level cursor ---

type reader struct {
	buf []byte
	pos int
}

func (r *reader) eof() bool { return r.pos >= len(r.buf) }

func (r *reader) byte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, fmt.Errorf("unexpected end of input")
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, fmt.Errorf("unexpected end of input (wanted %d bytes)", n)
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *reader) expectHeader() error {
	hdr, err := r.take(8)
	if err != nil {
		return fmt.Errorf("truncated header")
	}
	if hdr[0] != 0x00 || hdr[1] != 0x61 || hdr[2] != 0x73 || hdr[3] != 0x6d {
		return fmt.Errorf("bad magic: not a wasm module")
	}
	if v := binary.LittleEndian.Uint32(hdr[4:]); v != 1 {
		return fmt.Errorf("unsupported wasm version %d", v)
	}
	return nil
}

// u32 reads an unsigned LEB128 (used for u32-range values).
func (r *reader) u32() (uint32, error) {
	var result uint32
	var shift uint
	for {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 35 {
			return 0, fmt.Errorf("u32 LEB128 overflow")
		}
	}
}

// s64 reads a signed LEB128.
func (r *reader) s64() (int64, error) {
	var result int64
	var shift uint
	var b byte
	var err error
	for {
		b, err = r.byte()
		if err != nil {
			return 0, err
		}
		result |= int64(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
		if shift >= 64 {
			return 0, fmt.Errorf("s64 LEB128 overflow")
		}
	}
	// Sign-extend if the sign bit of the last group is set.
	if shift < 64 && b&0x40 != 0 {
		result |= -1 << shift
	}
	return result, nil
}

// blockType reads a block signature immediate: 0x40 (empty), a single valtype
// byte, or a positive s33 type index. We store the raw signed value.
func (r *reader) blockType() (int64, error) {
	return r.s64()
}

func (r *reader) valTypes() ([]ValType, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	out := make([]ValType, n)
	for i := range out {
		b, err := r.byte()
		if err != nil {
			return nil, err
		}
		out[i] = ValType(b)
	}
	return out, nil
}

func (r *reader) name() (string, error) {
	n, err := r.u32()
	if err != nil {
		return "", err
	}
	bs, err := r.take(int(n))
	if err != nil {
		return "", err
	}
	return string(bs), nil
}
