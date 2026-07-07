package wasm

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"github.com/nooga/paserati/pkg/vm"
)

// asI64 extracts an exact 64-bit integer from a wasm value. i64 values are
// carried as BigInt (paserati has no exact int64 value); anything else falls
// back through float64.
func asI64(v vm.Value) int64 {
	if v.Type() == vm.TypeBigInt {
		return v.AsBigInt().Int64()
	}
	return int64(v.ToFloat())
}

func i64Value(x int64) vm.Value { return vm.NewBigInt(new(big.Int).SetInt64(x)) }

// makeI64Add is a stateless native helper for i64.add — exact 64-bit add with
// wraparound (Go int64 overflow wraps mod 2^64). Faithful i64 arithmetic in the
// VM itself is future work; a helper per site is fine while i64 math is rare.
func makeI64Add() vm.Value {
	return vm.NewNativeFunction(2, false, "i64.add", func(args []vm.Value) (vm.Value, error) {
		return i64Value(asI64(args[0]) + asI64(args[1])), nil
	})
}

const wasmPageSize = 65536

// memory backs a module's linear memory with a real paserati ArrayBuffer and a
// set of native load/store helpers that close over its bytes. Memory ops lower
// to OpCall into these helpers: byte-addressed, little-endian, unaligned-safe —
// which sidesteps paserati's element-indexed TypedArray access.
//
// memory.grow reallocates the backing slice. Because all the helper closures
// capture the `data` variable (not a copy), reassigning it in the grow helper
// updates every helper at once. The WASI host reads through getData() for the
// same reason. The exported Buffer Value goes stale after a grow (it still points
// at the pre-grow array) — acceptable while nothing re-exports live memory.
type memory struct {
	Buffer vm.Value // the ArrayBuffer, for future exposure/exporting

	// getData returns the live backing slice, following memory.grow reallocations.
	getData func() []byte

	grow vm.Value // memory.grow(delta) -> old page count (or -1)

	loadI32 vm.Value
	load8U  vm.Value
	load8S  vm.Value
	load16U vm.Value
	load16S vm.Value
	loadI64 vm.Value
	loadF32 vm.Value
	loadF64 vm.Value
	// Narrow i64 loads: read N bytes, sign/zero-extend to a BigInt i64.
	loadI64_8S  vm.Value
	loadI64_8U  vm.Value
	loadI64_16S vm.Value
	loadI64_16U vm.Value
	loadI64_32S vm.Value
	loadI64_32U vm.Value
	storeI32    vm.Value
	store8      vm.Value
	store16     vm.Value
	storeI64    vm.Value
	storeF32    vm.Value
	storeF64    vm.Value
	// Narrow i64 stores: write the low N bytes of a BigInt i64.
	storeI64_8  vm.Value
	storeI64_16 vm.Value
	storeI64_32 vm.Value
	bulkCopy    vm.Value
	bulkFill    vm.Value
	size        vm.Value
}

// newMemory allocates the linear memory, applies active data segments, and
// builds the helper functions.
func newMemory(m *Module) (*memory, error) {
	pages := int(m.Memory.Min)
	buf := vm.NewArrayBuffer(pages * wasmPageSize)
	data := buf.AsArrayBuffer().GetData()

	for _, seg := range m.Data {
		if seg.Offset < 0 || seg.Offset+len(seg.Bytes) > len(data) {
			return nil, fmt.Errorf("data segment [%d,%d) exceeds memory of %d bytes",
				seg.Offset, seg.Offset+len(seg.Bytes), len(data))
		}
		copy(data[seg.Offset:], seg.Bytes)
	}

	ea := func(args []vm.Value, size int) (int, error) {
		addr := int(int32(args[0].ToFloat()))
		off := int(int32(args[1].ToFloat()))
		e := addr + off
		if e < 0 || e+size > len(data) {
			return 0, fmt.Errorf("memory access at %d (size %d) out of bounds [0,%d)", e, size, len(data))
		}
		return e, nil
	}
	load := func(size int, signed bool) func([]vm.Value) (vm.Value, error) {
		return func(args []vm.Value) (vm.Value, error) {
			e, err := ea(args, size)
			if err != nil {
				return vm.Undefined, err
			}
			var u uint32
			switch size {
			case 1:
				u = uint32(data[e])
			case 2:
				u = uint32(binary.LittleEndian.Uint16(data[e:]))
			default:
				u = binary.LittleEndian.Uint32(data[e:])
			}
			var v int32
			if signed {
				switch size {
				case 1:
					v = int32(int8(u))
				case 2:
					v = int32(int16(u))
				default:
					v = int32(u)
				}
			} else {
				v = int32(u)
			}
			return vm.Number(float64(v)), nil
		}
	}
	store := func(size int) func([]vm.Value) (vm.Value, error) {
		// Args are (addr, val, offset); the effective address is addr+offset.
		return func(args []vm.Value) (vm.Value, error) {
			addr := int(int32(args[0].ToFloat()))
			off := int(int32(args[2].ToFloat()))
			e := addr + off
			if e < 0 || e+size > len(data) {
				return vm.Undefined, fmt.Errorf("memory store at %d (size %d) out of bounds [0,%d)", e, size, len(data))
			}
			u := uint32(int32(args[1].ToFloat()))
			switch size {
			case 1:
				data[e] = byte(u)
			case 2:
				binary.LittleEndian.PutUint16(data[e:], uint16(u))
			default:
				binary.LittleEndian.PutUint32(data[e:], u)
			}
			return vm.Undefined, nil
		}
	}

	// Store effective-address for the wide stores, whose args are (addr, val,
	// offset) rather than the loads' (addr, offset).
	storeAddr := func(args []vm.Value, size int) (int, error) {
		e := int(int32(args[0].ToFloat())) + int(int32(args[2].ToFloat()))
		if e < 0 || e+size > len(data) {
			return 0, fmt.Errorf("memory store at %d (size %d) out of bounds [0,%d)", e, size, len(data))
		}
		return e, nil
	}
	loadI64 := func(args []vm.Value) (vm.Value, error) {
		e, err := ea(args, 8)
		if err != nil {
			return vm.Undefined, err
		}
		return i64Value(int64(binary.LittleEndian.Uint64(data[e:]))), nil
	}
	// Narrow i64 loads: read N bytes, sign/zero-extend to i64 (BigInt).
	loadI64N := func(size int, signed bool) func([]vm.Value) (vm.Value, error) {
		return func(args []vm.Value) (vm.Value, error) {
			e, err := ea(args, size)
			if err != nil {
				return vm.Undefined, err
			}
			var u uint64
			switch size {
			case 1:
				u = uint64(data[e])
			case 2:
				u = uint64(binary.LittleEndian.Uint16(data[e:]))
			default:
				u = uint64(binary.LittleEndian.Uint32(data[e:]))
			}
			var v int64
			if signed {
				switch size {
				case 1:
					v = int64(int8(u))
				case 2:
					v = int64(int16(u))
				default:
					v = int64(int32(u))
				}
			} else {
				v = int64(u)
			}
			return i64Value(v), nil
		}
	}
	// Narrow i64 stores: write the low N bytes of the i64 (BigInt) value.
	storeI64N := func(size int) func([]vm.Value) (vm.Value, error) {
		return func(args []vm.Value) (vm.Value, error) {
			e, err := storeAddr(args, size)
			if err != nil {
				return vm.Undefined, err
			}
			u := uint64(asI64(args[1]))
			switch size {
			case 1:
				data[e] = byte(u)
			case 2:
				binary.LittleEndian.PutUint16(data[e:], uint16(u))
			default:
				binary.LittleEndian.PutUint32(data[e:], uint32(u))
			}
			return vm.Undefined, nil
		}
	}
	loadF64 := func(args []vm.Value) (vm.Value, error) {
		e, err := ea(args, 8)
		if err != nil {
			return vm.Undefined, err
		}
		return vm.Number(math.Float64frombits(binary.LittleEndian.Uint64(data[e:]))), nil
	}
	loadF32 := func(args []vm.Value) (vm.Value, error) {
		e, err := ea(args, 4)
		if err != nil {
			return vm.Undefined, err
		}
		return vm.Number(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[e:])))), nil
	}
	storeI64 := func(args []vm.Value) (vm.Value, error) {
		e, err := storeAddr(args, 8)
		if err != nil {
			return vm.Undefined, err
		}
		binary.LittleEndian.PutUint64(data[e:], uint64(asI64(args[1])))
		return vm.Undefined, nil
	}
	storeF64 := func(args []vm.Value) (vm.Value, error) {
		e, err := storeAddr(args, 8)
		if err != nil {
			return vm.Undefined, err
		}
		binary.LittleEndian.PutUint64(data[e:], math.Float64bits(args[1].ToFloat()))
		return vm.Undefined, nil
	}
	storeF32 := func(args []vm.Value) (vm.Value, error) {
		e, err := storeAddr(args, 4)
		if err != nil {
			return vm.Undefined, err
		}
		binary.LittleEndian.PutUint32(data[e:], math.Float32bits(float32(args[1].ToFloat())))
		return vm.Undefined, nil
	}

	bulkCopy := func(args []vm.Value) (vm.Value, error) { // dst, src, n
		dst := int(int32(args[0].ToFloat()))
		src := int(int32(args[1].ToFloat()))
		n := int(int32(args[2].ToFloat()))
		if n < 0 || dst < 0 || src < 0 || dst+n > len(data) || src+n > len(data) {
			return vm.Undefined, fmt.Errorf("memory.copy out of bounds")
		}
		copy(data[dst:dst+n], data[src:src+n]) // Go's copy is memmove-safe for overlap
		return vm.Undefined, nil
	}
	bulkFill := func(args []vm.Value) (vm.Value, error) { // dst, val, n
		dst := int(int32(args[0].ToFloat()))
		val := byte(int32(args[1].ToFloat()))
		n := int(int32(args[2].ToFloat()))
		if n < 0 || dst < 0 || dst+n > len(data) {
			return vm.Undefined, fmt.Errorf("memory.fill out of bounds")
		}
		for i := 0; i < n; i++ {
			data[dst+i] = val
		}
		return vm.Undefined, nil
	}

	// memory.grow(delta): reallocate to old+delta pages (zero-filled), reassigning
	// the captured `data` so every helper follows. Returns the previous page count
	// per the wasm spec, or -1 on an (unbounded here) failure. Max is honoured when
	// declared.
	maxPages := -1
	if m.Memory.HasMax {
		maxPages = int(m.Memory.Max)
	}
	grow := func(args []vm.Value) (vm.Value, error) {
		delta := int(int32(args[0].ToFloat()))
		old := len(data) / wasmPageSize
		if delta < 0 {
			return vm.Number(-1), nil
		}
		newPages := old + delta
		if maxPages >= 0 && newPages > maxPages {
			return vm.Number(-1), nil
		}
		grown := make([]byte, newPages*wasmPageSize)
		copy(grown, data)
		data = grown
		return vm.Number(float64(old)), nil
	}

	return &memory{
		Buffer:  buf,
		getData: func() []byte { return data },
		grow:    vm.NewNativeFunction(1, false, "mem.grow", grow),
		loadI32: vm.NewNativeFunction(2, false, "mem.load_i32", load(4, true)),
		load8U:  vm.NewNativeFunction(2, false, "mem.load8_u", load(1, false)),
		load8S:  vm.NewNativeFunction(2, false, "mem.load8_s", load(1, true)),
		load16U: vm.NewNativeFunction(2, false, "mem.load16_u", load(2, false)),
		load16S: vm.NewNativeFunction(2, false, "mem.load16_s", load(2, true)),
		loadI64: vm.NewNativeFunction(2, false, "mem.load_i64", loadI64),
		loadF32: vm.NewNativeFunction(2, false, "mem.load_f32", loadF32),
		loadF64: vm.NewNativeFunction(2, false, "mem.load_f64", loadF64),

		loadI64_8S:  vm.NewNativeFunction(2, false, "mem.load_i64_8s", loadI64N(1, true)),
		loadI64_8U:  vm.NewNativeFunction(2, false, "mem.load_i64_8u", loadI64N(1, false)),
		loadI64_16S: vm.NewNativeFunction(2, false, "mem.load_i64_16s", loadI64N(2, true)),
		loadI64_16U: vm.NewNativeFunction(2, false, "mem.load_i64_16u", loadI64N(2, false)),
		loadI64_32S: vm.NewNativeFunction(2, false, "mem.load_i64_32s", loadI64N(4, true)),
		loadI64_32U: vm.NewNativeFunction(2, false, "mem.load_i64_32u", loadI64N(4, false)),
		storeI64_8:  vm.NewNativeFunction(3, false, "mem.store_i64_8", storeI64N(1)),
		storeI64_16: vm.NewNativeFunction(3, false, "mem.store_i64_16", storeI64N(2)),
		storeI64_32: vm.NewNativeFunction(3, false, "mem.store_i64_32", storeI64N(4)),
		storeI32:    vm.NewNativeFunction(3, false, "mem.store_i32", store(4)),
		store8:      vm.NewNativeFunction(3, false, "mem.store8", store(1)),
		store16:     vm.NewNativeFunction(3, false, "mem.store16", store(2)),
		storeI64:    vm.NewNativeFunction(3, false, "mem.store_i64", storeI64),
		storeF32:    vm.NewNativeFunction(3, false, "mem.store_f32", storeF32),
		storeF64:    vm.NewNativeFunction(3, false, "mem.store_f64", storeF64),
		bulkCopy:    vm.NewNativeFunction(3, false, "mem.copy", bulkCopy),
		bulkFill:    vm.NewNativeFunction(3, false, "mem.fill", bulkFill),
		size: vm.NewNativeFunction(0, false, "mem.size", func([]vm.Value) (vm.Value, error) {
			return vm.Number(float64(len(data) / wasmPageSize)), nil
		}),
	}, nil
}

// emitLoad lowers a load (1 stack arg = address) to a helper call, folding the
// static memarg offset in as a const argument.
func (g *funcGen) emitLoad(helper vm.Value, offset uint32) error {
	if g.mem == nil {
		return fmt.Errorf("memory load without a declared memory")
	}
	addr := byte(g.base + g.depth - 1)
	t := g.base + g.depth
	if t+3 > g.maxReg {
		g.maxReg = t + 3
	}
	g.loadConst(byte(t), g.c.AddConstant(helper))
	g.emit(vm.OpMove, byte(t+1), addr)
	g.loadConst(byte(t+2), g.c.AddConstant(vm.Number(float64(offset))))
	g.depth-- // pop address
	dest := g.push()
	g.emitCallOp(dest, byte(t), 2)
	return nil
}

// emitStore lowers a store (2 stack args = address, value) to a helper call.
func (g *funcGen) emitStore(helper vm.Value, offset uint32) error {
	if g.mem == nil {
		return fmt.Errorf("memory store without a declared memory")
	}
	val := byte(g.base + g.depth - 1)
	addr := byte(g.base + g.depth - 2)
	t := g.base + g.depth
	if t+4 > g.maxReg {
		g.maxReg = t + 4
	}
	g.loadConst(byte(t), g.c.AddConstant(helper))
	g.emit(vm.OpMove, byte(t+1), addr)
	g.emit(vm.OpMove, byte(t+2), val)
	g.loadConst(byte(t+3), g.c.AddConstant(vm.Number(float64(offset))))
	g.depth -= 2 // pop address + value
	dest := byte(g.base + g.depth)
	g.emitCallOp(dest, byte(t), 3) // store returns void; dest is scratch
	return nil
}

func (g *funcGen) emitMemSize() error {
	if g.mem == nil {
		return fmt.Errorf("memory.size without a declared memory")
	}
	t := g.base + g.depth
	if t+1 > g.maxReg {
		g.maxReg = t + 1
	}
	g.loadConst(byte(t), g.c.AddConstant(g.mem.size))
	dest := g.push()
	g.emitCallOp(dest, byte(t), 0)
	return nil
}

// emitMemGrow lowers memory.grow: pop the page delta, call the grow helper, push
// the previous page count (or -1).
func (g *funcGen) emitMemGrow() error {
	if g.mem == nil {
		return fmt.Errorf("memory.grow without a declared memory")
	}
	g.emitHelperUnop(g.mem.grow)
	return nil
}

// loadHelper / storeHelper select the native helper for an opcode. They are
// nil-safe: with no memory the returned Undefined is never used because emitLoad
// /emitStore error on g.mem == nil first.
func (g *funcGen) loadHelper(op Opcode) vm.Value {
	if g.mem == nil {
		return vm.Undefined
	}
	switch op {
	case OpI32Load8U:
		return g.mem.load8U
	case OpI32Load8S:
		return g.mem.load8S
	case OpI32Load16U:
		return g.mem.load16U
	case OpI32Load16S:
		return g.mem.load16S
	case OpI64Load:
		return g.mem.loadI64
	case OpI64Load8S:
		return g.mem.loadI64_8S
	case OpI64Load8U:
		return g.mem.loadI64_8U
	case OpI64Load16S:
		return g.mem.loadI64_16S
	case OpI64Load16U:
		return g.mem.loadI64_16U
	case OpI64Load32S:
		return g.mem.loadI64_32S
	case OpI64Load32U:
		return g.mem.loadI64_32U
	case OpF32Load:
		return g.mem.loadF32
	case OpF64Load:
		return g.mem.loadF64
	default:
		return g.mem.loadI32
	}
}

func (g *funcGen) memBulkCopy() vm.Value {
	if g.mem == nil {
		return vm.Undefined
	}
	return g.mem.bulkCopy
}

func (g *funcGen) memBulkFill() vm.Value {
	if g.mem == nil {
		return vm.Undefined
	}
	return g.mem.bulkFill
}

func (g *funcGen) storeHelper(op Opcode) vm.Value {
	if g.mem == nil {
		return vm.Undefined
	}
	switch op {
	case OpI32Store8:
		return g.mem.store8
	case OpI32Store16:
		return g.mem.store16
	case OpI64Store:
		return g.mem.storeI64
	case OpI64Store8:
		return g.mem.storeI64_8
	case OpI64Store16:
		return g.mem.storeI64_16
	case OpI64Store32:
		return g.mem.storeI64_32
	case OpF32Store:
		return g.mem.storeF32
	case OpF64Store:
		return g.mem.storeF64
	default:
		return g.mem.storeI32
	}
}

// emitMemBulk lowers a 3-operand void bulk-memory op (memory.copy/fill) to a
// helper call over the top three stack values.
func (g *funcGen) emitMemBulk(helper vm.Value) error {
	if g.mem == nil {
		return fmt.Errorf("bulk memory op without a declared memory")
	}
	a := byte(g.base + g.depth - 3)
	b := byte(g.base + g.depth - 2)
	c := byte(g.base + g.depth - 1)
	t := g.base + g.depth
	if t+4 > g.maxReg {
		g.maxReg = t + 4
	}
	g.loadConst(byte(t), g.c.AddConstant(helper))
	g.emit(vm.OpMove, byte(t+1), a)
	g.emit(vm.OpMove, byte(t+2), b)
	g.emit(vm.OpMove, byte(t+3), c)
	g.depth -= 3
	dest := byte(g.base + g.depth)
	g.emitCallOp(dest, byte(t), 3) // void
	return nil
}

// emitHelperBinop lowers a two-operand op to a native helper call: stage
// [helper, a, b] above the stack, call, and push the single result.
func (g *funcGen) emitHelperBinop(helper vm.Value) {
	b := byte(g.base + g.depth - 1)
	a := byte(g.base + g.depth - 2)
	t := g.base + g.depth
	if t+3 > g.maxReg {
		g.maxReg = t + 3
	}
	g.loadConst(byte(t), g.c.AddConstant(helper))
	g.emit(vm.OpMove, byte(t+1), a)
	g.emit(vm.OpMove, byte(t+2), b)
	g.depth -= 2
	dest := g.push()
	g.emitCallOp(dest, byte(t), 2)
}

// emitHelperUnop lowers a one-operand op to a native helper call: stage
// [helper, a] above the stack, call, and push the single result.
func (g *funcGen) emitHelperUnop(helper vm.Value) {
	a := byte(g.base + g.depth - 1)
	t := g.base + g.depth
	if t+2 > g.maxReg {
		g.maxReg = t + 2
	}
	g.loadConst(byte(t), g.c.AddConstant(helper))
	g.emit(vm.OpMove, byte(t+1), a)
	g.depth-- // pop operand
	dest := g.push()
	g.emitCallOp(dest, byte(t), 1)
}

func (g *funcGen) emitCallOp(dest, funcReg, argCount byte) {
	g.emit3(vm.OpCall, dest, funcReg, argCount)
}
