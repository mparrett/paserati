package wasm

import (
	"fmt"

	"github.com/nooga/paserati/pkg/vm"
)

// Codegen translates the wasm IR into paserati bytecode. Phase 2 handles
// straight-line functions (no branches): consts, local.get/set/tee, numeric
// binops, drop, return. Control flow (block/loop/if/br/br_if) and call are
// rejected with a clear error and land in Phases 3–4.
//
// Register model — the crux of the stack→register lowering:
//   - Locals occupy the low register band R0..R(numLocals-1). Params are the
//     first numParams of those (matching paserati's calling convention: args
//     arrive in R0..). Declared locals follow and are zero-initialised.
//   - The wasm operand stack maps *directly* onto registers above the locals:
//     stack depth d ↔ register (base+d), base = numLocals. Pushing bumps depth,
//     popping drops it. A binop reads the top two registers and writes the
//     lower of them in place — no free list, no moves.

// binopOp maps a wasm numeric binop to its paserati opcode. Comparisons are
// included for Phase 3 (br_if consumes them); note they yield a JS boolean,
// which is correct for branching but not yet for arithmetic use — see the doc's
// i32-semantics risk.
var binopOp = map[Opcode]vm.OpCode{
	OpI32Add:  vm.OpAdd,
	OpI32Sub:  vm.OpSubtract,
	OpI32Mul:  vm.OpMultiply,
	OpI32DivS: vm.OpDivide, // NOTE: not truncated-int-div yet
	OpI32RemS: vm.OpRemainder,
	OpI32And:  vm.OpBitwiseAnd,
	OpI32Or:   vm.OpBitwiseOr,
	OpI32Xor:  vm.OpBitwiseXor,
	OpI32Shl:  vm.OpShiftLeft,
	OpI32ShrS: vm.OpShiftRight,
	OpI32ShrU: vm.OpUnsignedShiftRight,
	OpI32Eq:   vm.OpEqual,
	OpI32Ne:   vm.OpNotEqual,
	OpI32LtS:  vm.OpLess,
	OpI32GtS:  vm.OpGreater,
	OpI32LeS:  vm.OpLessEqual,
	OpI32GeS:  vm.OpGreaterEqual,
}

type funcGen struct {
	c      *vm.Chunk
	fn     *Func
	base   int // first operand-stack register (== numLocals)
	depth  int // current operand-stack depth
	maxReg int // high-water register count, for MaxRegs
}

// CompileFunc lowers a single function to a callable paserati Value.
func CompileFunc(fn *Func, name string) (vm.Value, error) {
	g := &funcGen{c: vm.NewChunk(), fn: fn}
	g.base = fn.NumLocals()
	g.maxReg = g.base
	if g.base > 256 {
		return vm.Undefined, fmt.Errorf("%s: %d locals exceeds register file", name, g.base)
	}

	// Zero-initialise declared locals (params already hold the incoming args).
	if len(fn.Locals) > 0 {
		zero := g.c.AddConstant(vm.Number(0))
		for i := fn.NumParams(); i < fn.NumLocals(); i++ {
			g.loadConst(byte(i), zero)
		}
	}

	if err := g.emitBody(); err != nil {
		return vm.Undefined, fmt.Errorf("%s: %w", name, err)
	}

	g.c.MaxRegs = g.maxReg
	arity := fn.NumParams()
	return vm.NewFunction(arity, arity, 0, g.maxReg, false, name, g.c, false, false, false, false), nil
}

func (g *funcGen) emitBody() error {
	for _, ins := range g.fn.Body {
		switch ins.Op {
		case OpNop:
			// nothing

		case OpI32Const:
			dst := g.push()
			g.loadConst(dst, g.c.AddConstant(vm.Number(float64(int32(ins.I64)))))

		case OpLocalGet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.get %d out of range", ins.U32)
			}
			dst := g.push()
			g.emit(vm.OpMove, dst, byte(ins.U32)) // copy local → fresh temp

		case OpLocalSet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.set %d out of range", ins.U32)
			}
			src := g.pop()
			g.emit(vm.OpMove, byte(ins.U32), src)

		case OpLocalTee:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.tee %d out of range", ins.U32)
			}
			src := g.peek() // leaves the value on the stack
			g.emit(vm.OpMove, byte(ins.U32), src)

		case OpDrop:
			g.pop()

		case OpReturn:
			g.emitReturn()

		case OpEnd:
			// At Phase 2 there are no nested blocks, so any `end` is the
			// function epilogue.
			g.emitReturn()

		default:
			if pop, ok := binopOp[ins.Op]; ok {
				b := g.pop()
				a := g.pop()
				dst := g.push() // == a's register (in-place)
				g.emit3(pop, dst, a, b)
				continue
			}
			return fmt.Errorf("opcode %s not supported in phase 2", ins.Op)
		}
	}
	return nil
}

// emitReturn returns the top-of-stack value (single-result functions) or
// undefined for void functions.
func (g *funcGen) emitReturn() {
	if len(g.fn.Type.Results) == 0 || g.depth == 0 {
		g.c.WriteOpCode(vm.OpReturnUndefined, 1)
		return
	}
	src := g.pop()
	g.c.WriteOpCode(vm.OpReturn, 1)
	g.c.EmitByte(src)
}

// --- operand-stack ↔ register mapping ---

func (g *funcGen) push() byte {
	reg := g.base + g.depth
	g.depth++
	if g.base+g.depth > g.maxReg {
		g.maxReg = g.base + g.depth
	}
	return byte(reg)
}

func (g *funcGen) pop() byte {
	g.depth--
	return byte(g.base + g.depth)
}

func (g *funcGen) peek() byte { return byte(g.base + g.depth - 1) }

// --- emit helpers ---

func (g *funcGen) loadConst(reg byte, idx uint16) {
	g.c.WriteOpCode(vm.OpLoadConst, 1)
	g.c.EmitByte(reg)
	g.c.WriteUint16(idx)
}

func (g *funcGen) emit(op vm.OpCode, a, b byte) {
	g.c.WriteOpCode(op, 1)
	g.c.EmitByte(a)
	g.c.EmitByte(b)
}

func (g *funcGen) emit3(op vm.OpCode, a, b, c byte) {
	g.c.WriteOpCode(op, 1)
	g.c.EmitByte(a)
	g.c.EmitByte(b)
	g.c.EmitByte(c)
}
