package wasm

import (
	"encoding/binary"
	"fmt"

	"github.com/nooga/paserati/pkg/vm"
)

const wasmPageSize = 65536

// memory backs a module's linear memory with a real paserati ArrayBuffer and a
// set of native load/store helpers that close over its bytes. Memory ops lower
// to OpCall into these helpers: byte-addressed, little-endian, unaligned-safe —
// which sidesteps paserati's element-indexed TypedArray access. memory.grow is
// unsupported, so the buffer never reallocates and the closed-over slice stays
// valid.
type memory struct {
	Buffer vm.Value // the ArrayBuffer, for future exposure/exporting

	loadI32  vm.Value
	load8U   vm.Value
	load8S   vm.Value
	load16U  vm.Value
	load16S  vm.Value
	storeI32 vm.Value
	store8   vm.Value
	store16  vm.Value
	size     vm.Value
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

	nbytes := len(data)
	return &memory{
		Buffer:   buf,
		loadI32:  vm.NewNativeFunction(2, false, "mem.load_i32", load(4, true)),
		load8U:   vm.NewNativeFunction(2, false, "mem.load8_u", load(1, false)),
		load8S:   vm.NewNativeFunction(2, false, "mem.load8_s", load(1, true)),
		load16U:  vm.NewNativeFunction(2, false, "mem.load16_u", load(2, false)),
		load16S:  vm.NewNativeFunction(2, false, "mem.load16_s", load(2, true)),
		storeI32: vm.NewNativeFunction(3, false, "mem.store_i32", store(4)),
		store8:   vm.NewNativeFunction(3, false, "mem.store8", store(1)),
		store16:  vm.NewNativeFunction(3, false, "mem.store16", store(2)),
		size: vm.NewNativeFunction(0, false, "mem.size", func([]vm.Value) (vm.Value, error) {
			return vm.Number(float64(nbytes / wasmPageSize)), nil
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
	default:
		return g.mem.loadI32
	}
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
	default:
		return g.mem.storeI32
	}
}

func (g *funcGen) emitCallOp(dest, funcReg, argCount byte) {
	g.emit3(vm.OpCall, dest, funcReg, argCount)
}
