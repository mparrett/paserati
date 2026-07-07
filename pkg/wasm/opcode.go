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

// Additional opcodes seen in real (TinyGo/LLVM) output. Decoding-level support:
// these parse into the IR; codegen may not lower all of them yet.
const (
	OpBrTable      Opcode = 0x0e // vec<labelidx> + default labelidx
	OpCallIndirect Opcode = 0x11 // typeidx + tableidx
	OpSelectT      Opcode = 0x1c // select with an explicit result-type vector

	// i64 arithmetic / bitwise (named for codegen; decode via the numeric range).
	// Carried as BigInt for exact 64-bit fidelity, so each is a native helper.
	OpI64Add    Opcode = 0x7c
	OpI64Sub    Opcode = 0x7d
	OpI64Mul    Opcode = 0x7e
	OpI64DivS   Opcode = 0x7f
	OpI64DivU   Opcode = 0x80
	OpI64RemS   Opcode = 0x81
	OpI64RemU   Opcode = 0x82
	OpI64And    Opcode = 0x83
	OpI64Or     Opcode = 0x84
	OpI64Xor    Opcode = 0x85
	OpI64Shl    Opcode = 0x86
	OpI64ShrS   Opcode = 0x87
	OpI64ShrU   Opcode = 0x88
	OpI64Rotl   Opcode = 0x89
	OpI64Rotr   Opcode = 0x8a
	OpI64Clz    Opcode = 0x79
	OpI64Ctz    Opcode = 0x7a
	OpI64Popcnt Opcode = 0x7b

	// i64 comparisons (named for codegen; decode via the numeric range).
	OpI64Eqz Opcode = 0x50
	OpI64Eq  Opcode = 0x51
	OpI64Ne  Opcode = 0x52
	OpI64LtS Opcode = 0x53
	OpI64LtU Opcode = 0x54
	OpI64GtS Opcode = 0x55
	OpI64GtU Opcode = 0x56
	OpI64LeS Opcode = 0x57
	OpI64LeU Opcode = 0x58
	OpI64GeS Opcode = 0x59
	OpI64GeU Opcode = 0x5a

	// i32 bit-count ops (named for codegen; decode via the numeric range).
	OpI32Clz    Opcode = 0x67
	OpI32Ctz    Opcode = 0x68
	OpI32Popcnt Opcode = 0x69

	// f64 arithmetic and comparisons (named for codegen; decode via the numeric
	// range). add/sub/mul/div and compares map onto paserati's number opcodes;
	// the rest lower as helpers.
	OpF64Eq       Opcode = 0x61
	OpF64Ne       Opcode = 0x62
	OpF64Lt       Opcode = 0x63
	OpF64Gt       Opcode = 0x64
	OpF64Le       Opcode = 0x65
	OpF64Ge       Opcode = 0x66
	OpF64Abs      Opcode = 0x99
	OpF64Neg      Opcode = 0x9a
	OpF64Ceil     Opcode = 0x9b
	OpF64Floor    Opcode = 0x9c
	OpF64Trunc    Opcode = 0x9d
	OpF64Nearest  Opcode = 0x9e
	OpF64Sqrt     Opcode = 0x9f
	OpF64Add      Opcode = 0xa0
	OpF64Sub      Opcode = 0xa1
	OpF64Mul      Opcode = 0xa2
	OpF64Div      Opcode = 0xa3
	OpF64Min      Opcode = 0xa4
	OpF64Max      Opcode = 0xa5
	OpF64Copysign Opcode = 0xa6

	// f32 arithmetic and comparisons. f32 is carried as float64, so results are
	// rounded back to float32 precision in the helpers; compares are exact on the
	// carried value and map onto the number opcodes.
	OpF32Eq       Opcode = 0x5b
	OpF32Ne       Opcode = 0x5c
	OpF32Lt       Opcode = 0x5d
	OpF32Gt       Opcode = 0x5e
	OpF32Le       Opcode = 0x5f
	OpF32Ge       Opcode = 0x60
	OpF32Abs      Opcode = 0x8b
	OpF32Neg      Opcode = 0x8c
	OpF32Ceil     Opcode = 0x8d
	OpF32Floor    Opcode = 0x8e
	OpF32Trunc    Opcode = 0x8f
	OpF32Nearest  Opcode = 0x90
	OpF32Sqrt     Opcode = 0x91
	OpF32Add      Opcode = 0x92
	OpF32Sub      Opcode = 0x93
	OpF32Mul      Opcode = 0x94
	OpF32Div      Opcode = 0x95
	OpF32Min      Opcode = 0x96
	OpF32Max      Opcode = 0x97
	OpF32Copysign Opcode = 0x98

	// Trapping float→int truncation (non-saturating) and int→f32 conversion.
	OpI32TruncF32S   Opcode = 0xa8
	OpI32TruncF32U   Opcode = 0xa9
	OpI32TruncF64S   Opcode = 0xaa
	OpI32TruncF64U   Opcode = 0xab
	OpI64TruncF32S   Opcode = 0xae
	OpI64TruncF32U   Opcode = 0xaf
	OpI64TruncF64S   Opcode = 0xb0
	OpI64TruncF64U   Opcode = 0xb1
	OpF32ConvertI32S Opcode = 0xb2
	OpF32ConvertI32U Opcode = 0xb3
	OpF32ConvertI64S Opcode = 0xb4
	OpF32ConvertI64U Opcode = 0xb5

	// Conversions (named for codegen; decode via the numeric range).
	OpI64ExtendI32S     Opcode = 0xac
	OpI64ExtendI32U     Opcode = 0xad
	OpI32ReinterpretF32 Opcode = 0xbc
	OpI64ReinterpretF64 Opcode = 0xbd
	OpF32ReinterpretI32 Opcode = 0xbe
	OpF64ReinterpretI64 Opcode = 0xbf
	OpI32WrapI64        Opcode = 0xa7
	OpF32DemoteF64      Opcode = 0xb6
	OpF64ConvertI32S    Opcode = 0xb7
	OpF64ConvertI32U    Opcode = 0xb8
	OpF64ConvertI64S    Opcode = 0xb9
	OpF64ConvertI64U    Opcode = 0xba
	OpF64PromoteF32     Opcode = 0xbb

	// Sign-extension ops (named for codegen; decode via the numeric range).
	OpI32Extend8S  Opcode = 0xc0
	OpI32Extend16S Opcode = 0xc1
	OpI64Extend8S  Opcode = 0xc2
	OpI64Extend16S Opcode = 0xc3
	OpI64Extend32S Opcode = 0xc4

	// Wider memory access (memarg immediate). i32 widths are declared above.
	OpI64Load    Opcode = 0x29
	OpF32Load    Opcode = 0x2a
	OpF64Load    Opcode = 0x2b
	OpI64Load8S  Opcode = 0x30
	OpI64Load8U  Opcode = 0x31
	OpI64Load16S Opcode = 0x32
	OpI64Load16U Opcode = 0x33
	OpI64Load32S Opcode = 0x34
	OpI64Load32U Opcode = 0x35
	OpI64Store   Opcode = 0x37
	OpF32Store   Opcode = 0x38
	OpF64Store   Opcode = 0x39
	OpI64Store8  Opcode = 0x3c
	OpI64Store16 Opcode = 0x3d
	OpI64Store32 Opcode = 0x3e

	// Synthetic internal opcodes for the 0xFC-prefixed bulk-memory ops (the wire
	// encoding is a two-byte 0xFC/subop; we map them into unused single-byte
	// space so Instr.Op stays a byte).
	OpMemoryInit Opcode = 0xe0 // dataidx in Instr.U32
	OpDataDrop   Opcode = 0xe1 // dataidx in Instr.U32
	OpMemoryCopy Opcode = 0xe2
	OpMemoryFill Opcode = 0xe3

	// Saturating float→int truncation (0xFC subops 0–7). f32 and f64 sources
	// collapse to the same behaviour because we carry f32 as float64, so the eight
	// wire ops map onto four synthetic opcodes.
	OpI32TruncSatS Opcode = 0xe4 // i32.trunc_sat_f{32,64}_s
	OpI32TruncSatU Opcode = 0xe5 // i32.trunc_sat_f{32,64}_u
	OpI64TruncSatS Opcode = 0xe6 // i64.trunc_sat_f{32,64}_s
	OpI64TruncSatU Opcode = 0xe7 // i64.trunc_sat_f{32,64}_u
)

// immKind describes the immediate operand(s) that follow an opcode in the
// binary stream. Anything not listed here has no immediate.
type immKind int

const (
	immNone         immKind = iota
	immU32                  // one unsigned LEB128 (index / label depth)
	immBlockType            // block signature (single byte 0x40/valtype, or s33 LEB)
	immI32Const             // signed LEB128, 32-bit
	immI64Const             // signed LEB128, 64-bit
	immF32Const             // 4 raw little-endian bytes
	immF64Const             // 8 raw little-endian bytes
	immMemarg               // two u32 LEB128 (align, offset); offset kept in Instr.U32
	immMemory               // single memory-index byte (memory.size/grow)
	immBrTable              // vec<labelidx> (Instr.Labels) + default labelidx (Instr.U32)
	immCallIndirect         // typeidx (Instr.U32) + tableidx (Instr.I64)
	immSelectT              // vec<valtype> — result types of a typed select (consumed)
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
		OpI32Store, OpI32Store8, OpI32Store16,
		OpI64Load, OpF32Load, OpF64Load,
		OpI64Load8S, OpI64Load8U, OpI64Load16S, OpI64Load16U, OpI64Load32S, OpI64Load32U,
		OpI64Store, OpF32Store, OpF64Store, OpI64Store8, OpI64Store16, OpI64Store32:
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
	case OpBrTable:
		return immBrTable, true
	case OpCallIndirect:
		return immCallIndirect, true
	case OpSelectT:
		return immSelectT, true
	default:
		// The numeric/comparison/conversion range (0x45–0xC4) is entirely
		// immediate-free in the MVP, so accept it wholesale — that covers i64/f32/
		// f64 arithmetic and conversions without enumerating every opcode.
		if op >= 0x45 && op <= 0xc4 {
			return immNone, true
		}
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
	OpBrTable: "br_table", OpCallIndirect: "call_indirect", OpSelectT: "select_t",
	OpI64Load: "i64.load", OpF32Load: "f32.load", OpF64Load: "f64.load",
	OpI64Store: "i64.store", OpF32Store: "f32.store", OpF64Store: "f64.store",
	OpMemoryInit: "memory.init", OpDataDrop: "data.drop",
	OpMemoryCopy: "memory.copy", OpMemoryFill: "memory.fill",
	OpI32TruncSatS: "i32.trunc_sat_s", OpI32TruncSatU: "i32.trunc_sat_u",
	OpI64TruncSatS: "i64.trunc_sat_s", OpI64TruncSatU: "i64.trunc_sat_u",
	OpI64Add: "i64.add", OpI64Sub: "i64.sub", OpI64Mul: "i64.mul",
	OpI64DivS: "i64.div_s", OpI64DivU: "i64.div_u", OpI64RemS: "i64.rem_s", OpI64RemU: "i64.rem_u",
	OpI64And: "i64.and", OpI64Or: "i64.or", OpI64Xor: "i64.xor",
	OpI64Shl: "i64.shl", OpI64ShrS: "i64.shr_s", OpI64ShrU: "i64.shr_u",
	OpI64Rotl: "i64.rotl", OpI64Rotr: "i64.rotr",
	OpI64Clz: "i64.clz", OpI64Ctz: "i64.ctz", OpI64Popcnt: "i64.popcnt",
	OpI64Eqz: "i64.eqz", OpI64Eq: "i64.eq", OpI64Ne: "i64.ne",
	OpI64LtS: "i64.lt_s", OpI64LtU: "i64.lt_u", OpI64GtS: "i64.gt_s", OpI64GtU: "i64.gt_u",
	OpI64LeS: "i64.le_s", OpI64LeU: "i64.le_u", OpI64GeS: "i64.ge_s", OpI64GeU: "i64.ge_u",
	OpI32Clz: "i32.clz", OpI32Ctz: "i32.ctz", OpI32Popcnt: "i32.popcnt",
	OpI64ExtendI32S: "i64.extend_i32_s", OpI64ExtendI32U: "i64.extend_i32_u",
	OpI32ReinterpretF32: "i32.reinterpret_f32", OpI64ReinterpretF64: "i64.reinterpret_f64",
	OpF32ReinterpretI32: "f32.reinterpret_i32", OpF64ReinterpretI64: "f64.reinterpret_i64",
	OpF64Eq: "f64.eq", OpF64Ne: "f64.ne", OpF64Lt: "f64.lt", OpF64Gt: "f64.gt",
	OpF64Le: "f64.le", OpF64Ge: "f64.ge",
	OpF64Abs: "f64.abs", OpF64Neg: "f64.neg", OpF64Sqrt: "f64.sqrt",
	OpF64Ceil: "f64.ceil", OpF64Floor: "f64.floor", OpF64Trunc: "f64.trunc", OpF64Nearest: "f64.nearest",
	OpF64Add: "f64.add", OpF64Sub: "f64.sub", OpF64Mul: "f64.mul", OpF64Div: "f64.div",
	OpF64Min: "f64.min", OpF64Max: "f64.max", OpF64Copysign: "f64.copysign",
	OpF32Eq: "f32.eq", OpF32Ne: "f32.ne", OpF32Lt: "f32.lt", OpF32Gt: "f32.gt",
	OpF32Le: "f32.le", OpF32Ge: "f32.ge",
	OpF32Abs: "f32.abs", OpF32Neg: "f32.neg", OpF32Sqrt: "f32.sqrt",
	OpF32Ceil: "f32.ceil", OpF32Floor: "f32.floor", OpF32Trunc: "f32.trunc", OpF32Nearest: "f32.nearest",
	OpF32Add: "f32.add", OpF32Sub: "f32.sub", OpF32Mul: "f32.mul", OpF32Div: "f32.div",
	OpF32Min: "f32.min", OpF32Max: "f32.max", OpF32Copysign: "f32.copysign",
	OpI32TruncF32S: "i32.trunc_f32_s", OpI32TruncF32U: "i32.trunc_f32_u",
	OpI32TruncF64S: "i32.trunc_f64_s", OpI32TruncF64U: "i32.trunc_f64_u",
	OpI64TruncF32S: "i64.trunc_f32_s", OpI64TruncF32U: "i64.trunc_f32_u",
	OpI64TruncF64S: "i64.trunc_f64_s", OpI64TruncF64U: "i64.trunc_f64_u",
	OpF32ConvertI32S: "f32.convert_i32_s", OpF32ConvertI32U: "f32.convert_i32_u",
	OpF32ConvertI64S: "f32.convert_i64_s", OpF32ConvertI64U: "f32.convert_i64_u",
	OpI32WrapI64: "i32.wrap_i64", OpF32DemoteF64: "f32.demote_f64", OpF64PromoteF32: "f64.promote_f32",
	OpF64ConvertI32S: "f64.convert_i32_s", OpF64ConvertI32U: "f64.convert_i32_u",
	OpF64ConvertI64S: "f64.convert_i64_s", OpF64ConvertI64U: "f64.convert_i64_u",
	OpI32Extend8S: "i32.extend8_s", OpI32Extend16S: "i32.extend16_s",
	OpI64Extend8S: "i64.extend8_s", OpI64Extend16S: "i64.extend16_s", OpI64Extend32S: "i64.extend32_s",
}

func (op Opcode) String() string {
	if s, ok := opNames[op]; ok {
		return s
	}
	return fmt.Sprintf("op(0x%02x)", byte(op))
}
