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
		case 2: // Import
			if err := decodeImportSection(sr, m); err != nil {
				return nil, err
			}
		case 3: // Function
			idxs, err := decodeFunctionSection(sr)
			if err != nil {
				return nil, err
			}
			funcTypeIdx = idxs
		case 4: // Table
			if err := decodeTableSection(sr, m); err != nil {
				return nil, err
			}
		case 5: // Memory
			if err := decodeMemorySection(sr, m); err != nil {
				return nil, err
			}
		case 6: // Global
			if err := decodeGlobalSection(sr, m); err != nil {
				return nil, err
			}
		case 7: // Export
			if err := decodeExportSection(sr, m); err != nil {
				return nil, err
			}
		case 9: // Element
			if err := decodeElemSection(sr, m); err != nil {
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
		case 12: // DataCount
			if m.DataCount, err = sr.u32(); err != nil {
				return nil, err
			}
		case 0: // Custom (name, producers, …) — skip
		default:
			// Start(8) and reference/extension sections. Skip; codegen fails
			// loudly if a body needs something we didn't model.
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
		seg := DataSegment{}
		switch kind {
		case 0: // active, memory 0, constant i32 offset
			if seg.Offset, err = r.constI32Expr(); err != nil {
				return err
			}
		case 1: // passive — staged for memory.init
			seg.Passive = true
		case 2: // active, explicit memory index, then offset expr
			if _, err = r.u32(); err != nil { // memory index
				return err
			}
			if seg.Offset, err = r.constI32Expr(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("data segment kind %d unsupported", kind)
		}
		bytes, err := r.name() // length-prefixed byte vector
		if err != nil {
			return err
		}
		seg.Bytes = []byte(bytes)
		m.Data = append(m.Data, seg)
	}
	return nil
}

// limits reads a limits descriptor (flag byte, min, optional max).
func (r *reader) limits() (min, max uint32, hasMax bool, err error) {
	flags, err := r.byte()
	if err != nil {
		return 0, 0, false, err
	}
	if min, err = r.u32(); err != nil {
		return 0, 0, false, err
	}
	if flags&1 != 0 {
		if max, err = r.u32(); err != nil {
			return 0, 0, false, err
		}
		hasMax = true
	}
	return min, max, hasMax, nil
}

func (r *reader) skipLimits() error {
	_, _, _, err := r.limits()
	return err
}

// constExprValue reads a global/segment init const-expression, returning the
// integer value for i32/i64.const forms (0 for others), and consuming the end.
func (r *reader) constExprValue() (int64, error) {
	op, err := r.byte()
	if err != nil {
		return 0, err
	}
	var v int64
	switch Opcode(op) {
	case OpI32Const, OpI64Const:
		if v, err = r.s64(); err != nil {
			return 0, err
		}
	case OpGlobalGet:
		if _, err = r.u32(); err != nil { // references another global; value unknown here
			return 0, err
		}
	case OpF32Const:
		if _, err = r.take(4); err != nil {
			return 0, err
		}
	case OpF64Const:
		if _, err = r.take(8); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("const expr: unexpected opcode 0x%02x", op)
	}
	end, err := r.byte()
	if err != nil {
		return 0, err
	}
	if Opcode(end) != OpEnd {
		return 0, fmt.Errorf("const expr: expected end, got 0x%02x", end)
	}
	return v, nil
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

func decodeImportSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		mod, err := r.name()
		if err != nil {
			return err
		}
		field, err := r.name()
		if err != nil {
			return err
		}
		kindByte, err := r.byte()
		if err != nil {
			return err
		}
		imp := Import{Module: mod, Field: field, Kind: ImportKind(kindByte)}
		switch ImportKind(kindByte) {
		case ImportFunc:
			if imp.TypeIndex, err = r.u32(); err != nil {
				return err
			}
			m.ImportedFuncCount++
		case ImportTable:
			if _, err = r.byte(); err != nil { // elem type
				return err
			}
			if err = r.skipLimits(); err != nil {
				return err
			}
		case ImportMemory:
			if err = r.skipLimits(); err != nil {
				return err
			}
		case ImportGlobal:
			if _, err = r.byte(); err != nil { // valtype
				return err
			}
			if _, err = r.byte(); err != nil { // mutability
				return err
			}
		default:
			return fmt.Errorf("import %q.%q: unknown kind %d", mod, field, kindByte)
		}
		m.Imports = append(m.Imports, imp)
	}
	return nil
}

func decodeTableSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		elem, err := r.byte()
		if err != nil {
			return err
		}
		min, max, hasMax, err := r.limits()
		if err != nil {
			return err
		}
		m.Tables = append(m.Tables, TableType{ElemType: elem, Min: min, Max: max, HasMax: hasMax})
	}
	return nil
}

func decodeGlobalSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		vt, err := r.byte()
		if err != nil {
			return err
		}
		mut, err := r.byte()
		if err != nil {
			return err
		}
		init, err := r.constExprValue()
		if err != nil {
			return err
		}
		m.Globals = append(m.Globals, Global{Type: ValType(vt), Mutable: mut == 1, Init: init})
	}
	return nil
}

func decodeElemSection(r *reader, m *Module) error {
	n, err := r.u32()
	if err != nil {
		return err
	}
	for i := uint32(0); i < n; i++ {
		flags, err := r.u32()
		if err != nil {
			return err
		}
		// Only the common "active, table 0, funcref, offset expr, vec funcidx"
		// form (flags 0) is modelled; TinyGo emits this.
		if flags != 0 {
			return fmt.Errorf("elem segment flags %d unsupported (only active table-0 handled)", flags)
		}
		off, err := r.constI32Expr()
		if err != nil {
			return err
		}
		count, err := r.u32()
		if err != nil {
			return err
		}
		funcs := make([]uint32, count)
		for j := range funcs {
			if funcs[j], err = r.u32(); err != nil {
				return err
			}
		}
		m.Elems = append(m.Elems, ElemSegment{TableIndex: 0, Offset: off, FuncIndices: funcs})
	}
	return nil
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
		if b == 0xfc { // bulk-memory / saturating-truncation prefix
			ins, err := decodePrefixedFC(r)
			if err != nil {
				return err
			}
			fn.Body = append(fn.Body, ins)
			continue
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
		case immBrTable:
			count, err := r.u32()
			if err != nil {
				return err
			}
			ins.Labels = make([]uint32, count)
			for j := range ins.Labels {
				if ins.Labels[j], err = r.u32(); err != nil {
					return err
				}
			}
			if ins.U32, err = r.u32(); err != nil { // default label
				return err
			}
		case immCallIndirect:
			if ins.U32, err = r.u32(); err != nil { // type index
				return err
			}
			tableIdx, err := r.u32() // table index (0 in MVP)
			if err != nil {
				return err
			}
			ins.I64 = int64(tableIdx)
		case immSelectT:
			n, err := r.u32() // result-type vector; consumed, not used
			if err != nil {
				return err
			}
			if _, err = r.take(int(n)); err != nil {
				return err
			}
		}
		fn.Body = append(fn.Body, ins)
	}
	return nil
}

// decodePrefixedFC decodes a 0xFC-prefixed instruction (bulk memory). The
// saturating-truncation ops (sub 0–7) and table ops (12+) are out of scope.
func decodePrefixedFC(r *reader) (Instr, error) {
	sub, err := r.u32()
	if err != nil {
		return Instr{}, err
	}
	switch sub {
	case 8: // memory.init dataidx, memidx(0x00)
		idx, err := r.u32()
		if err != nil {
			return Instr{}, err
		}
		if _, err := r.byte(); err != nil {
			return Instr{}, err
		}
		return Instr{Op: OpMemoryInit, U32: idx}, nil
	case 9: // data.drop dataidx
		idx, err := r.u32()
		if err != nil {
			return Instr{}, err
		}
		return Instr{Op: OpDataDrop, U32: idx}, nil
	case 10: // memory.copy memidx memidx (both 0x00)
		if _, err := r.take(2); err != nil {
			return Instr{}, err
		}
		return Instr{Op: OpMemoryCopy}, nil
	case 11: // memory.fill memidx(0x00)
		if _, err := r.byte(); err != nil {
			return Instr{}, err
		}
		return Instr{Op: OpMemoryFill}, nil
	default:
		return Instr{}, fmt.Errorf("unsupported 0xfc opcode %d", sub)
	}
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
