// Package wasm decodes a subset of the WebAssembly binary format into a small
// typed IR, as the front end for the wasm→paserati-bytecode experiment
// (see docs/wasm-interop-design.md). Phase 1 covers the numeric + control-flow
// subset: i32/i64/f32/f64 numeric ops, locals, block/loop/if/br/br_if, call,
// return. Memory, tables, imports, SIMD, and threads are intentionally absent
// and decode as an "unsupported opcode" error.
package wasm

import "fmt"

// ValType is a wasm value type (the numeric types only, for now).
type ValType byte

const (
	I32 ValType = 0x7f
	I64 ValType = 0x7e
	F32 ValType = 0x7d
	F64 ValType = 0x7c
)

func (t ValType) String() string {
	switch t {
	case I32:
		return "i32"
	case I64:
		return "i64"
	case F32:
		return "f32"
	case F64:
		return "f64"
	default:
		return fmt.Sprintf("valtype(0x%02x)", byte(t))
	}
}

// FuncType is a function signature: params → results.
type FuncType struct {
	Params  []ValType
	Results []ValType
}

// Func is a defined function: its signature, extra locals (beyond params), and
// a flat instruction stream. block/loop/if/else/end stay in the stream so the
// codegen pass can reconstruct nesting.
type Func struct {
	TypeIndex uint32
	Type      *FuncType // resolved from the module's type section
	Locals    []ValType // locals declared in the body, after the params
	Body      []Instr
}

// NumParams is a convenience for codegen: params occupy the low local slots.
func (f *Func) NumParams() int { return len(f.Type.Params) }

// NumLocals is the total local count (params + declared locals).
func (f *Func) NumLocals() int { return len(f.Type.Params) + len(f.Locals) }

// Export names a module entity. Only function exports (Kind==0) are meaningful
// in this subset.
type Export struct {
	Name  string
	Kind  byte
	Index uint32
}

// MemType is a linear memory's page limits (1 page = 64 KiB).
type MemType struct {
	Min    uint32
	Max    uint32 // 0 when unbounded
	HasMax bool
}

// DataSegment is an active data-section entry: Bytes copied to memory at Offset.
type DataSegment struct {
	Offset int
	Bytes  []byte
}

// Module is the decoded IR.
type Module struct {
	Types   []FuncType
	Funcs   []Func
	Exports []Export
	Memory  *MemType // nil when the module declares no memory
	Data    []DataSegment
}

// FuncExport returns the export with the given name if it's a function export.
func (m *Module) FuncExport(name string) (*Export, bool) {
	for i := range m.Exports {
		if m.Exports[i].Kind == 0 && m.Exports[i].Name == name {
			return &m.Exports[i], true
		}
	}
	return nil, false
}

// Instr is one decoded instruction. Only the immediate relevant to Op is set:
//   - U32: label depth (br/br_if), index (local.*, global.*, call)
//   - I64: integer const value, or a block type (for block/loop/if)
//   - F64: float const value (f32 widened to f64)
type Instr struct {
	Op  Opcode
	U32 uint32
	I64 int64
	F64 float64
}

// BlockTypeEmpty is the block-type immediate meaning "no result" (0x40).
// Stored in Instr.I64 for block/loop/if.
const BlockTypeEmpty int64 = -0x40 // 0x40 sign-extended as an s33 is -64
