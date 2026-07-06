package wasm

import (
	"fmt"

	"github.com/nooga/paserati/pkg/vm"
)

// Codegen translates the wasm IR into paserati bytecode. Phases 2–3 cover a
// single function's numeric core plus structured control flow: consts,
// local.get/set/tee, numeric binops, drop, return, and block/loop/if/br/br_if.
// call (cross-function) and memory are Phase 4+ and error clearly.
//
// Register model — the stack→register lowering:
//   - Locals occupy the low band R0..R(numLocals-1). Params are the first
//     numParams (matching paserati's convention: args arrive in R0..). Declared
//     locals follow and are zero-initialised.
//   - The wasm operand stack maps *directly* onto registers above the locals:
//     depth d ↔ register (base+d), base = numLocals. A binop reads the top two
//     registers and rewrites the lower in place — no free list, no moves.
//
// Control-flow lowering — structured labels to flat jumps:
//   - block/if are forward labels: a branch to them jumps to just past their
//     `end`, backpatched once that offset is known.
//   - loop is a backward label: a branch jumps to the loop header, known on
//     entry.
//   - br_if inverts through OpJumpIfFalse (the VM has no jump-if-true).
//   - Only empty block types are supported (branches carry no values), which
//     keeps the register mapping consistent across labels.

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

// ctrlFrame tracks one open block/loop/if for branch resolution.
type ctrlFrame struct {
	op         Opcode
	startDepth int   // operand-stack depth at frame entry (after popping an if cond)
	header     int   // loop: code offset of the branch target
	brPatches  []int // block/if: jump-operand positions to patch to the frame's end
	elsePatch  int   // if: operand pos of the cond-false jump; -1 once resolved
	hasElse    bool
}

type funcGen struct {
	c           *vm.Chunk
	fn          *Func
	base        int  // first operand-stack register (== numLocals)
	depth       int  // current operand-stack depth
	maxReg      int  // high-water register count
	ctrl        []*ctrlFrame
	unreachable bool // in a dead region after an unconditional branch/return
}

// CompileFunc lowers a single function to a callable paserati Value.
func CompileFunc(fn *Func, name string) (vm.Value, error) {
	g := &funcGen{c: vm.NewChunk(), fn: fn}
	g.base = fn.NumLocals()
	g.maxReg = g.base
	if g.base > 256 {
		return vm.Undefined, fmt.Errorf("%s: %d locals exceeds register file", name, g.base)
	}

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
		// Dead code after an unconditional branch/return: only structural
		// delimiters are meaningful until the region closes.
		if g.unreachable && ins.Op != OpEnd && ins.Op != OpElse {
			return fmt.Errorf("unreachable code before %s not supported", ins.Op)
		}

		switch ins.Op {
		case OpNop:

		case OpI32Const:
			dst := g.push()
			g.loadConst(dst, g.c.AddConstant(vm.Number(float64(int32(ins.I64)))))

		case OpLocalGet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.get %d out of range", ins.U32)
			}
			dst := g.push()
			g.emit(vm.OpMove, dst, byte(ins.U32))

		case OpLocalSet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.set %d out of range", ins.U32)
			}
			g.emit(vm.OpMove, byte(ins.U32), g.pop())

		case OpLocalTee:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.tee %d out of range", ins.U32)
			}
			g.emit(vm.OpMove, byte(ins.U32), g.peek()) // leaves value on the stack

		case OpI32Eqz:
			// eqz(x) == (x == 0); logical-not yields the boolean br_if wants.
			a := g.pop()
			g.emit(vm.OpNot, g.push(), a)

		case OpDrop:
			g.pop()

		case OpReturn:
			g.emitReturn()
			g.unreachable = true

		case OpBlock, OpLoop:
			if ins.I64 != BlockTypeEmpty {
				return fmt.Errorf("%s with non-empty block type unsupported", ins.Op)
			}
			f := &ctrlFrame{op: ins.Op, startDepth: g.depth, elsePatch: -1}
			if ins.Op == OpLoop {
				f.header = g.here()
			}
			g.ctrl = append(g.ctrl, f)

		case OpIf:
			if ins.I64 != BlockTypeEmpty {
				return fmt.Errorf("if with non-empty block type unsupported")
			}
			cond := g.pop()
			f := &ctrlFrame{op: OpIf, startDepth: g.depth, elsePatch: g.emitJumpIfFalse(cond)}
			g.ctrl = append(g.ctrl, f)

		case OpElse:
			f := g.ctrl[len(g.ctrl)-1]
			if f.op != OpIf {
				return fmt.Errorf("else without matching if")
			}
			if !g.unreachable {
				// End of the then-branch: skip over the else to the frame end.
				f.brPatches = append(f.brPatches, g.emitJump())
			}
			g.patchTo(f.elsePatch, g.here()) // cond-false lands at the else body
			f.elsePatch = -1
			f.hasElse = true
			g.depth = f.startDepth
			g.unreachable = false

		case OpEnd:
			if len(g.ctrl) == 0 {
				g.emitReturn() // function epilogue
				continue
			}
			f := g.ctrl[len(g.ctrl)-1]
			g.ctrl = g.ctrl[:len(g.ctrl)-1]
			if f.op == OpIf && f.elsePatch >= 0 {
				g.patchTo(f.elsePatch, g.here()) // if with no else: cond-false skips to end
			}
			for _, p := range f.brPatches {
				g.patchTo(p, g.here())
			}
			g.depth = f.startDepth // empty block type ⇒ no results
			g.unreachable = false

		case OpBr:
			if err := g.branchTo(ins.U32); err != nil {
				return err
			}
			g.unreachable = true

		case OpBrIf:
			cond := g.pop()
			skip := g.emitJumpIfFalse(cond) // fall through when the branch is not taken
			if err := g.branchTo(ins.U32); err != nil {
				return err
			}
			g.patchTo(skip, g.here())

		default:
			op, ok := binopOp[ins.Op]
			if !ok {
				return fmt.Errorf("opcode %s not supported yet", ins.Op)
			}
			b := g.pop()
			a := g.pop()
			dst := g.push() // == a's register (in place)
			g.emit3(op, dst, a, b)
		}
	}
	return nil
}

// branchTo emits a jump to the depth-th enclosing control frame (0 = innermost).
func (g *funcGen) branchTo(depth uint32) error {
	i := len(g.ctrl) - 1 - int(depth)
	if i < 0 {
		return fmt.Errorf("branch depth %d exceeds control stack", depth)
	}
	f := g.ctrl[i]
	if f.op == OpLoop {
		g.emitJumpTo(f.header) // backward target, known
	} else {
		f.brPatches = append(f.brPatches, g.emitJump()) // forward, patched at end
	}
	return nil
}

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

// --- emit + jump/backpatch helpers ---

func (g *funcGen) here() int { return len(g.c.Code) }

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

// emitJump writes OpJump with a placeholder offset and returns the operand
// position to backpatch.
func (g *funcGen) emitJump() int {
	g.c.WriteOpCode(vm.OpJump, 1)
	pos := g.here()
	g.c.WriteUint16(0)
	return pos
}

func (g *funcGen) emitJumpTo(target int) {
	g.patchTo(g.emitJump(), target)
}

// emitJumpIfFalse writes OpJumpIfFalse with a placeholder offset and returns the
// operand position to backpatch.
func (g *funcGen) emitJumpIfFalse(cond byte) int {
	g.c.WriteOpCode(vm.OpJumpIfFalse, 1)
	g.c.EmitByte(cond)
	pos := g.here()
	g.c.WriteUint16(0)
	return pos
}

// patchTo fills the 2-byte offset at pos so execution lands on target. Offsets
// are relative to the ip after the offset bytes (pos+2).
func (g *funcGen) patchTo(pos, target int) {
	off := int16(target - (pos + 2))
	g.c.Code[pos] = byte(uint16(off) >> 8)
	g.c.Code[pos+1] = byte(uint16(off) & 0xff)
}
