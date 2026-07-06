package wasm

import "fmt"

// Opcode is a wasm instruction opcode (single-byte; the 0xFC prefix family is
// out of scope for this subset).
type Opcode byte

// Control flow.
const (
	OpUnreachable Opcode = 0x00
	OpNop         Opcode = 0x01
	OpBlock       Opcode = 0x02
	OpLoop        Opcode = 0x03
	OpIf          Opcode = 0x04
	OpElse        Opcode = 0x05
	OpEnd         Opcode = 0x0b
	OpBr          Opcode = 0x0c
	OpBrIf        Opcode = 0x0d
	OpReturn      Opcode = 0x0f
	OpCall        Opcode = 0x10
	OpDrop        Opcode = 0x1a
	OpSelect      Opcode = 0x1b
)

// Variable access.
const (
	OpLocalGet  Opcode = 0x20
	OpLocalSet  Opcode = 0x21
	OpLocalTee  Opcode = 0x22
	OpGlobalGet Opcode = 0x23
	OpGlobalSet Opcode = 0x24
)

// Constants.
const (
	OpI32Const Opcode = 0x41
	OpI64Const Opcode = 0x42
	OpF32Const Opcode = 0x43
	OpF64Const Opcode = 0x44
)

// i32 comparisons.
const (
	OpI32Eqz Opcode = 0x45
	OpI32Eq  Opcode = 0x46
	OpI32Ne  Opcode = 0x47
	OpI32LtS Opcode = 0x48
	OpI32LtU Opcode = 0x49
	OpI32GtS Opcode = 0x4a
	OpI32GtU Opcode = 0x4b
	OpI32LeS Opcode = 0x4c
	OpI32LeU Opcode = 0x4d
	OpI32GeS Opcode = 0x4e
	OpI32GeU Opcode = 0x4f
)

// Memory access (subset). memarg immediate = (align, offset).
const (
	OpI32Load    Opcode = 0x28
	OpI32Load8S  Opcode = 0x2c
	OpI32Load8U  Opcode = 0x2d
	OpI32Load16S Opcode = 0x2e
	OpI32Load16U Opcode = 0x2f
	OpI32Store   Opcode = 0x36
	OpI32Store8  Opcode = 0x3a
	OpI32Store16 Opcode = 0x3b
	OpMemorySize Opcode = 0x3f
	OpMemoryGrow Opcode = 0x40
)

// i32 arithmetic / bitwise.
const (
	OpI32Add  Opcode = 0x6a
	OpI32Sub  Opcode = 0x6b
	OpI32Mul  Opcode = 0x6c
	OpI32DivS Opcode = 0x6d
	OpI32DivU Opcode = 0x6e
	OpI32RemS Opcode = 0x6f
	OpI32RemU Opcode = 0x70
	OpI32And  Opcode = 0x71
	OpI32Or   Opcode = 0x72
	OpI32Xor  Opcode = 0x73
	OpI32Shl  Opcode = 0x74
	OpI32ShrS Opcode = 0x75
	OpI32ShrU Opcode = 0x76
	OpI32Rotl Opcode = 0x77
	OpI32Rotr Opcode = 0x78
)

// immKind describes the immediate operand(s) that follow an opcode in the
// binary stream. Anything not listed here has no immediate.
type immKind int

const (
	immNone      immKind = iota
	immU32               // one unsigned LEB128 (index / label depth)
	immBlockType         // block signature (single byte 0x40/valtype, or s33 LEB)
	immI32Const          // signed LEB128, 32-bit
	immI64Const          // signed LEB128, 64-bit
	immF32Const          // 4 raw little-endian bytes
	immF64Const          // 8 raw little-endian bytes
	immMemarg            // two u32 LEB128 (align, offset); offset kept in Instr.U32
	immMemory            // single memory-index byte (memory.size/grow)
)

// immediateKind returns how to decode an opcode's immediate, and whether the
// opcode is in the supported subset at all.
func immediateKind(op Opcode) (immKind, bool) {
	switch op {
	case OpUnreachable, OpNop, OpElse, OpEnd, OpReturn, OpDrop, OpSelect,
		OpI32Eqz, OpI32Eq, OpI32Ne, OpI32LtS, OpI32LtU, OpI32GtS, OpI32GtU,
		OpI32LeS, OpI32LeU, OpI32GeS, OpI32GeU,
		OpI32Add, OpI32Sub, OpI32Mul, OpI32DivS, OpI32DivU, OpI32RemS, OpI32RemU,
		OpI32And, OpI32Or, OpI32Xor, OpI32Shl, OpI32ShrS, OpI32ShrU, OpI32Rotl, OpI32Rotr:
		return immNone, true
	case OpBlock, OpLoop, OpIf:
		return immBlockType, true
	case OpBr, OpBrIf, OpCall, OpLocalGet, OpLocalSet, OpLocalTee, OpGlobalGet, OpGlobalSet:
		return immU32, true
	case OpI32Load, OpI32Load8S, OpI32Load8U, OpI32Load16S, OpI32Load16U,
		OpI32Store, OpI32Store8, OpI32Store16:
		return immMemarg, true
	case OpMemorySize, OpMemoryGrow:
		return immMemory, true
	case OpI32Const:
		return immI32Const, true
	case OpI64Const:
		return immI64Const, true
	case OpF32Const:
		return immF32Const, true
	case OpF64Const:
		return immF64Const, true
	default:
		return immNone, false
	}
}

var opNames = map[Opcode]string{
	OpUnreachable: "unreachable", OpNop: "nop", OpBlock: "block", OpLoop: "loop",
	OpIf: "if", OpElse: "else", OpEnd: "end", OpBr: "br", OpBrIf: "br_if",
	OpReturn: "return", OpCall: "call", OpDrop: "drop", OpSelect: "select",
	OpLocalGet: "local.get", OpLocalSet: "local.set", OpLocalTee: "local.tee",
	OpGlobalGet: "global.get", OpGlobalSet: "global.set",
	OpI32Const: "i32.const", OpI64Const: "i64.const", OpF32Const: "f32.const", OpF64Const: "f64.const",
	OpI32Eqz: "i32.eqz", OpI32Eq: "i32.eq", OpI32Ne: "i32.ne",
	OpI32LtS: "i32.lt_s", OpI32LtU: "i32.lt_u", OpI32GtS: "i32.gt_s", OpI32GtU: "i32.gt_u",
	OpI32LeS: "i32.le_s", OpI32LeU: "i32.le_u", OpI32GeS: "i32.ge_s", OpI32GeU: "i32.ge_u",
	OpI32Add: "i32.add", OpI32Sub: "i32.sub", OpI32Mul: "i32.mul",
	OpI32DivS: "i32.div_s", OpI32DivU: "i32.div_u", OpI32RemS: "i32.rem_s", OpI32RemU: "i32.rem_u",
	OpI32And: "i32.and", OpI32Or: "i32.or", OpI32Xor: "i32.xor",
	OpI32Shl: "i32.shl", OpI32ShrS: "i32.shr_s", OpI32ShrU: "i32.shr_u",
	OpI32Rotl: "i32.rotl", OpI32Rotr: "i32.rotr",
	OpI32Load: "i32.load", OpI32Load8S: "i32.load8_s", OpI32Load8U: "i32.load8_u",
	OpI32Load16S: "i32.load16_s", OpI32Load16U: "i32.load16_u",
	OpI32Store: "i32.store", OpI32Store8: "i32.store8", OpI32Store16: "i32.store16",
	OpMemorySize: "memory.size", OpMemoryGrow: "memory.grow",
}

func (op Opcode) String() string {
	if s, ok := opNames[op]; ok {
		return s
	}
	return fmt.Sprintf("op(0x%02x)", byte(op))
}
