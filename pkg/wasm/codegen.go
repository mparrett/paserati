package wasm

import (
	"errors"
	"fmt"
	"io"

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
	OpI32Add: vm.OpAdd,
	OpI32Sub: vm.OpSubtract,
	OpI32Mul: vm.OpMultiply,
	// div_s/rem_s are NOT here: paserati's OpDivide is float division (9/10 → 0.9),
	// but wasm i32.div_s truncates toward zero. They lower to integer helpers.
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

	// f64 maps onto paserati's number ops directly (values are float64). NaN and
	// -0 semantics match: NaN compares unequal/false, -0 == 0.
	OpF64Add: vm.OpAdd,
	OpF64Sub: vm.OpSubtract,
	OpF64Mul: vm.OpMultiply,
	OpF64Div: vm.OpDivide,
	OpF64Eq:  vm.OpEqual,
	OpF64Ne:  vm.OpNotEqual,
	OpF64Lt:  vm.OpLess,
	OpF64Gt:  vm.OpGreater,
	OpF64Le:  vm.OpLessEqual,
	OpF64Ge:  vm.OpGreaterEqual,

	// f32 compares are exact on the carried float64 value.
	OpF32Eq: vm.OpEqual,
	OpF32Ne: vm.OpNotEqual,
	OpF32Lt: vm.OpLess,
	OpF32Gt: vm.OpGreater,
	OpF32Le: vm.OpLessEqual,
	OpF32Ge: vm.OpGreaterEqual,
}

// ctrlFrame tracks one open block/loop/if for branch resolution. Branch targets
// are symbolic labels (see asm.go), resolved to byte offsets during finish().
type ctrlFrame struct {
	op          Opcode
	startDepth  int       // operand-stack depth at frame entry (after popping an if cond)
	results     int       // values the frame leaves on the stack at its end
	branchArity int       // values a branch to this label carries (loop→params=0, block/if→results)
	headLabel   *asmInstr // loop: the branch target (header)
	endLabel    *asmInstr // block/if: branch target + end marker
	elseLabel   *asmInstr // if: cond-false target
	hasElse     bool
}

type funcGen struct {
	c           *vm.Chunk
	fn          *Func
	mod         *Module         // for resolving call targets; nil when compiling standalone
	vals        []vm.Value      // one callable Value per module function (for call)
	mem         *memory         // linear memory + load/store helpers; nil if none
	glob        *globals        // module globals + get/set helpers; nil if none
	rt          *rtHelpers      // shared stateless helpers (i64.add, unsigned i32 ops)
	imports     []importBinding // host bindings, indexed by wasm func-import index
	table       *funcTable      // function table for call_indirect; nil if none
	out         []*asmInstr     // symbolic instruction list, peepholed then encoded
	base        int             // first operand-stack register (== numLocals)
	depth       int             // current operand-stack depth
	maxReg      int             // high-water register count
	ctrl        []*ctrlFrame
	unreachable bool // in a dead region after an unconditional branch/return
	spill       bool // locals live in an array at R0 (function exceeds register file)
}

// CompileFunc lowers a single call-free function to a callable paserati Value.
// A `call` in the body errors — use CompileModule for functions that call.
func CompileFunc(fn *Func, name string) (vm.Value, error) {
	c := vm.NewChunk()
	val := vm.NewFunction(fn.NumParams(), fn.NumParams(), 0, 0, false, name, c, false, false, false, false)
	g := &funcGen{c: c, fn: fn, rt: newRTHelpers()}
	if err := g.compileInto(val); err != nil {
		return vm.Undefined, fmt.Errorf("%s: %w", name, err)
	}
	return val, nil
}

// CompileModule lowers a module with no host imports (or with imports routed to
// the process stdio) and returns the exported functions by name. For a WASI
// module whose exit code and output you want to control, use CompileModuleWasi.
func CompileModule(m *Module) (map[string]vm.Value, error) {
	exports, _, err := CompileModuleWasi(m, nil, nil)
	return exports, err
}

// CompileModuleWasi lowers every function in the module (so calls resolve),
// wiring any wasi_snapshot_preview1 imports to a host over the linear memory, and
// returns the exported functions plus that host (nil when the module imports no
// functions). Writers default to the process stdio when nil. Two passes: create
// all callable Values first (empty chunks) so recursion and mutual recursion can
// reference peers, then fill each chunk in place.
func CompileModuleWasi(m *Module, stdout, stderr io.Writer) (map[string]vm.Value, *wasiHost, error) {
	var mem *memory
	if m.Memory != nil {
		var err error
		if mem, err = newMemory(m); err != nil {
			return nil, nil, err
		}
	}
	var glob *globals
	if len(m.Globals) > 0 {
		glob = newGlobals(m)
	}
	rt := newRTHelpers()

	var host *wasiHost
	var binds []importBinding
	if m.ImportedFuncCount > 0 {
		if mem == nil {
			return nil, nil, fmt.Errorf("module imports host functions but declares no memory")
		}
		host = newWASIHost(mem.getData, stdout, stderr)
		var err error
		if binds, err = buildImportBindings(m, host); err != nil {
			return nil, nil, err
		}
	}

	vals := make([]vm.Value, len(m.Funcs))
	for i := range m.Funcs {
		fn := &m.Funcs[i]
		vals[i] = vm.NewFunction(fn.NumParams(), fn.NumParams(), 0, 0, false,
			funcName(m, i), vm.NewChunk(), false, false, false, false)
	}
	table := newFuncTable(m, vals, binds)
	newGen := func(i int, spill bool) *funcGen {
		return &funcGen{c: vals[i].AsFunction().Chunk, fn: &m.Funcs[i], mod: m, vals: vals,
			mem: mem, glob: glob, rt: rt, imports: binds, table: table, spill: spill}
	}
	for i := range m.Funcs {
		err := newGen(i, forceSpillAll).compileInto(vals[i])
		// Too many registers for the direct mapping: retry with locals spilled to
		// an array. If even that overflows (huge param count — never in practice),
		// stub it so it traps only if called.
		if errors.Is(err, errRegOverflow) {
			resetChunk(vals[i])
			err = newGen(i, true).compileInto(vals[i])
		}
		if errors.Is(err, errRegOverflow) {
			resetChunk(vals[i])
			// The cause is either >256 registers or a jump beyond int16; -profile
			// distinguishes them. Both are paserati bytecode-format limits.
			compileStub(vals[i], &m.Funcs[i], fmt.Sprintf("wasm: %s exceeds bytecode limits (256 regs / 32KB jumps)", funcName(m, i)))
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", funcName(m, i), err)
		}
	}
	out := make(map[string]vm.Value)
	for _, e := range m.Exports {
		// Export indices count imported functions first; vals holds only the
		// defined functions, so shift past the imports.
		if e.Kind == 0 {
			d := int(e.Index) - m.ImportedFuncCount
			if d >= 0 && d < len(vals) {
				out[e.Name] = vals[d]
			}
		}
	}
	return out, host, nil
}

// funcName returns an export name for defined func i (0-based over m.Funcs) if it
// has one, else func<i>. Export indices count imports first, so shift by them.
func funcName(m *Module, i int) string {
	for _, e := range m.Exports {
		if e.Kind == 0 && int(e.Index)-m.ImportedFuncCount == i {
			return e.Name
		}
	}
	return fmt.Sprintf("func%d", i)
}

// errRegOverflow marks a function that needs more than paserati's 256-register
// (byte-indexed) file. Such functions are rare (one big init in a whole module);
// the module compiler stubs them rather than failing the whole compile.
var errRegOverflow = errors.New("register file overflow")

// forceSpillAll routes every function through the spill lowering — a debug knob
// for exercising the spiller on small functions. Off in normal builds.
var forceSpillAll = false

// compileInto runs codegen and fills val's chunk and register size. Two lowerings
// share the tail: the normal one maps each local to its own register (fast, but
// bounded by the 256-register file); the spill one (g.spill) parks all locals in
// an array at R0 so only the operand stack needs registers — used when a function
// has too many locals for the direct mapping.
func (g *funcGen) compileInto(val vm.Value) error {
	if err := g.emitAll(); err != nil {
		return err
	}
	// The operand stack lives above the locals; a deep stack can push past the
	// register file too. The emitted chunk is discarded when we stub, so the
	// byte-wrapped registers it may contain never run.
	if g.maxReg > 256 {
		return fmt.Errorf("%d registers: %w", g.maxReg, errRegOverflow)
	}
	// paserati encodes jump targets as int16 relative offsets. A function large
	// enough to need jumps beyond ±32 KB can't be represented; finish reports it
	// and we treat it like register overflow so the caller stubs the function
	// rather than run misaligned bytecode. (Spilling inflates code ~3×, so a huge
	// asyncify goroutine wrapper can trip this even when its registers fit.)
	if g.finish() {
		return fmt.Errorf("jump exceeds int16 offset: %w", errRegOverflow)
	}
	g.c.MaxRegs = g.maxReg
	val.AsFunction().RegisterSize = g.maxReg
	return nil
}

// emitAll lowers the prologue (register-mapped or spilled) and the body into
// g.out, leaving finish/encoding to the caller. Shared by real compilation and
// the profiler.
func (g *funcGen) emitAll() error {
	if g.spill {
		if err := g.spilledEntry(); err != nil {
			return err
		}
	} else {
		g.base = g.fn.NumLocals()
		g.maxReg = g.base
		if g.base > 256 {
			return fmt.Errorf("%d locals: %w", g.base, errRegOverflow)
		}
		if len(g.fn.Locals) > 0 {
			zero := g.c.AddConstant(vm.Number(0))
			for i := g.fn.NumParams(); i < g.fn.NumLocals(); i++ {
				g.loadConst(byte(i), zero)
			}
		}
	}
	return g.emitBody()
}

// layoutMaxJump peepholes and lays out offsets (like finish, but without
// encoding) and returns the largest absolute jump distance. For diagnostics.
func (g *funcGen) layoutMaxJump() int {
	peephole(g.out)
	off := 0
	for _, in := range g.out {
		if in.dead {
			continue
		}
		in.off = off
		off += instrSize(in)
	}
	max := 0
	for _, in := range g.out {
		if in.dead {
			continue
		}
		var d int
		switch in.kind {
		case akJump:
			d = in.target.off - (in.off + 3)
		case akCondJump:
			d = in.target.off - (in.off + 4)
		default:
			continue
		}
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

// spilledEntry lowers the prologue of a spilled function: R0 holds a fresh
// locals array (mkLocals) with the params copied in; the operand stack then runs
// from R1. Incoming params occupy R0..R(np-1) on entry, so the array is built in
// scratch above them, populated, then moved down to R0.
func (g *funcGen) spilledEntry() error {
	np := g.fn.NumParams()
	nl := g.fn.NumLocals()
	scratch := np // above the incoming params
	g.base = 1    // R0 reserved for the locals array
	g.maxReg = scratch + 2
	if g.maxReg > 256 {
		// Even params + 2 scratch overflow — nothing we can do (never happens for
		// real TinyGo output, whose param counts are small).
		return fmt.Errorf("%d params: %w", np, errRegOverflow)
	}

	// arr = mkLocals(nl), landing in R_scratch.
	g.loadConst(byte(scratch), g.c.AddConstant(g.rt.mkLocals))
	g.loadConst(byte(scratch+1), g.c.AddConstant(vm.Number(float64(nl))))
	g.emit3(vm.OpCall, byte(scratch), byte(scratch), 1)

	// arr[i] = param_i (params still live in R0..R(np-1)).
	for i := 0; i < np; i++ {
		g.loadConst(byte(scratch+1), g.c.AddConstant(vm.Number(float64(i))))
		g.emit3(vm.OpSetIndex, byte(scratch), byte(scratch+1), byte(i))
	}
	if scratch != 0 {
		g.emit(vm.OpMove, 0, byte(scratch)) // R0 = the array
	}
	return nil
}

// spillIdx loads local index i into a scratch register and returns it. Index
// constants are cached so a hot local doesn't bloat the constant pool.
func (g *funcGen) spillIdx(reg byte, i uint32) {
	g.loadConst(reg, g.c.AddConstant(vm.Number(float64(i))))
}

// resetChunk clears a chunk so codegen can retry into it from scratch (finish
// appends, and AddConstant accumulates, so a fresh attempt must start empty).
func resetChunk(val vm.Value) {
	c := val.AsFunction().Chunk
	c.Code = c.Code[:0]
	c.Constants = c.Constants[:0]
	c.Lines = c.Lines[:0]
}

// compileStub replaces a function's body with one that throws — used for
// functions the register file can't hold. If the guest never calls it, the
// module runs; if it does, the throw surfaces as a clean runtime trap.
func compileStub(val vm.Value, fn *Func, msg string) {
	f := val.AsFunction()
	c := f.Chunk
	c.Code = c.Code[:0]
	g := &funcGen{c: c, fn: fn, rt: newRTHelpers()}
	g.base = fn.NumParams()
	if g.base > 255 {
		g.base = 255
	}
	g.maxReg = g.base + 1
	t := byte(g.base)
	g.loadConst(t, c.AddConstant(vm.NewString(msg)))
	g.emitOp1(vm.OpThrow, t)
	g.finish()
	c.MaxRegs = g.maxReg
	f.RegisterSize = g.maxReg
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

		case OpI64Const:
			// Carried as BigInt for exact 64-bit fidelity.
			dst := g.push()
			g.loadConst(dst, g.c.AddConstant(i64Value(ins.I64)))

		case OpF32Const, OpF64Const:
			// f32 is widened to f64 by the decoder; both carry as a Number.
			dst := g.push()
			g.loadConst(dst, g.c.AddConstant(vm.Number(ins.F64)))

		case OpI64Add:
			g.emitHelperBinop(g.rt.i64add)

		case OpLocalGet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.get %d out of range", ins.U32)
			}
			if g.spill {
				dst := g.push()
				idx := byte(g.base + g.depth) // scratch just above the pushed value
				g.growReg(int(idx) + 1)
				g.spillIdx(idx, ins.U32)
				g.emit3(vm.OpGetIndex, dst, 0, idx) // dst = locals[idx]
			} else {
				dst := g.push()
				g.emit(vm.OpMove, dst, byte(ins.U32))
			}

		case OpLocalSet:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.set %d out of range", ins.U32)
			}
			if g.spill {
				val := g.pop()
				idx := byte(g.base + g.depth + 1) // scratch above the popped value
				g.growReg(int(idx) + 1)
				g.spillIdx(idx, ins.U32)
				g.emit3(vm.OpSetIndex, 0, idx, val) // locals[idx] = val
			} else {
				g.emit(vm.OpMove, byte(ins.U32), g.pop())
			}

		case OpLocalTee:
			if int(ins.U32) >= g.fn.NumLocals() {
				return fmt.Errorf("local.tee %d out of range", ins.U32)
			}
			if g.spill {
				val := g.peek() // stays on the stack
				idx := byte(g.base + g.depth)
				g.growReg(int(idx) + 1)
				g.spillIdx(idx, ins.U32)
				g.emit3(vm.OpSetIndex, 0, idx, val)
			} else {
				g.emit(vm.OpMove, byte(ins.U32), g.peek()) // leaves value on the stack
			}

		case OpI32Eqz:
			// eqz(x) == (x == 0); logical-not yields the boolean br_if wants.
			a := g.pop()
			g.emit(vm.OpNot, g.push(), a)

		case OpDrop:
			g.pop()

		case OpSelect, OpSelectT:
			g.emitSelect()

		case OpUnreachable:
			g.emitUnreachable()

		case OpBrTable:
			if err := g.emitBrTable(ins.Labels, ins.U32); err != nil {
				return err
			}

		case OpMemoryCopy:
			if err := g.emitMemBulk(g.memBulkCopy()); err != nil {
				return err
			}

		case OpMemoryFill:
			if err := g.emitMemBulk(g.memBulkFill()); err != nil {
				return err
			}

		case OpReturn:
			g.emitReturn()
			g.unreachable = true

		case OpBlock, OpLoop:
			results, err := blockResults(ins.I64)
			if err != nil {
				return err
			}
			f := &ctrlFrame{op: ins.Op, startDepth: g.depth, results: results}
			if ins.Op == OpLoop {
				f.headLabel = g.newLabel() // branch target is the header
				g.markLabel(f.headLabel)
			} else {
				f.branchArity = results
				f.endLabel = g.newLabel()
			}
			g.ctrl = append(g.ctrl, f)

		case OpIf:
			results, err := blockResults(ins.I64)
			if err != nil {
				return err
			}
			cond := g.pop()
			f := &ctrlFrame{op: OpIf, startDepth: g.depth, results: results,
				branchArity: results, elseLabel: g.newLabel(), endLabel: g.newLabel()}
			g.condJumpTo(cond, f.elseLabel)
			g.ctrl = append(g.ctrl, f)

		case OpElse:
			f := g.ctrl[len(g.ctrl)-1]
			if f.op != OpIf {
				return fmt.Errorf("else without matching if")
			}
			if !g.unreachable {
				g.jumpTo(f.endLabel) // end of then-branch: skip the else
			}
			g.markLabel(f.elseLabel) // cond-false lands at the else body
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
			if f.op == OpIf && !f.hasElse {
				g.markLabel(f.elseLabel) // no else: cond-false skips to end
			}
			if f.endLabel != nil {
				g.markLabel(f.endLabel)
			}
			g.depth = f.startDepth + f.results
			g.unreachable = false

		case OpBr:
			if err := g.branchTo(ins.U32); err != nil {
				return err
			}
			g.unreachable = true

		case OpBrIf:
			cond := g.pop()
			skip := g.newLabel()
			g.condJumpTo(cond, skip) // fall through (to skip) when not taken
			if err := g.branchTo(ins.U32); err != nil {
				return err
			}
			g.markLabel(skip)

		case OpCall:
			if err := g.emitCall(ins.U32); err != nil {
				return err
			}

		case OpCallIndirect:
			if err := g.emitCallIndirect(ins.U32); err != nil {
				return err
			}

		case OpI32Load, OpI32Load8U, OpI32Load8S, OpI32Load16U, OpI32Load16S,
			OpI64Load, OpI64Load8S, OpI64Load8U, OpI64Load16S, OpI64Load16U,
			OpI64Load32S, OpI64Load32U, OpF32Load, OpF64Load:
			if err := g.emitLoad(g.loadHelper(ins.Op), ins.U32); err != nil {
				return err
			}

		case OpI32Store, OpI32Store8, OpI32Store16,
			OpI64Store, OpI64Store8, OpI64Store16, OpI64Store32,
			OpF32Store, OpF64Store:
			if err := g.emitStore(g.storeHelper(ins.Op), ins.U32); err != nil {
				return err
			}

		case OpMemorySize:
			if err := g.emitMemSize(); err != nil {
				return err
			}

		case OpMemoryGrow:
			if err := g.emitMemGrow(); err != nil {
				return err
			}

		case OpGlobalGet:
			if err := g.emitGlobalGet(ins.U32); err != nil {
				return err
			}

		case OpGlobalSet:
			if err := g.emitGlobalSet(ins.U32); err != nil {
				return err
			}

		default:
			if helper, ok := g.rt.unaryHelper(ins.Op); ok {
				g.emitHelperUnop(helper) // extend / reinterpret
				continue
			}
			if helper, ok := g.rt.unsignedHelper(ins.Op); ok {
				g.emitHelperBinop(helper) // unsigned i32 compares / div / rem, i64 gt_s/lt_s/xor
				continue
			}
			if helper, ok := g.rt.binaryHelper(ins.Op); ok {
				g.emitHelperBinop(helper) // i64 arithmetic / bitwise / compares
				continue
			}
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
// Any values the label carries are moved into the target's result slot first,
// so they land where post-branch code expects them.
func (g *funcGen) branchTo(depth uint32) error {
	i := len(g.ctrl) - 1 - int(depth)
	if i < 0 {
		return fmt.Errorf("branch depth %d exceeds control stack", depth)
	}
	f := g.ctrl[i]
	for k := 0; k < f.branchArity; k++ {
		src := byte(g.base + g.depth - f.branchArity + k)
		dst := byte(g.base + f.startDepth + k)
		if src != dst {
			g.emit(vm.OpMove, dst, src)
		}
	}
	if f.op == OpLoop {
		g.jumpTo(f.headLabel) // backward target
	} else {
		g.jumpTo(f.endLabel) // forward target
	}
	return nil
}

// emitCall lowers `call funcIdx`. Args sit on top of the operand stack; paserati
// wants [funcReg][arg0..argN-1] contiguous. We stage the callee value and a copy
// of the args into fresh registers above the stack, then reclaim the arg slots
// for the result.
func (g *funcGen) emitCall(funcIdx uint32) error {
	if g.mod == nil {
		return fmt.Errorf("call requires module compilation (use CompileModule)")
	}
	// wasm func indices count imported functions first; our vals/Funcs hold only
	// the defined functions. Imported calls dispatch to the host binding; defined
	// calls to the peer Value. Both share the staging path below.
	imported := g.mod.ImportedFuncCount
	var calleeVal vm.Value
	var sig *FuncType
	if int(funcIdx) < imported {
		if int(funcIdx) >= len(g.imports) {
			return fmt.Errorf("no host binding for imported func %d", funcIdx)
		}
		b := g.imports[funcIdx]
		calleeVal, sig = b.val, b.sig
	} else {
		defIdx := int(funcIdx) - imported
		if defIdx >= len(g.mod.Funcs) {
			return fmt.Errorf("call %d out of range", funcIdx)
		}
		calleeVal, sig = g.vals[defIdx], g.mod.Funcs[defIdx].Type
	}
	argCount := len(sig.Params)
	results := len(sig.Results)
	if results > 1 {
		return fmt.Errorf("call to multi-result func unsupported")
	}
	if argCount > 255 {
		return fmt.Errorf("call arity %d exceeds OpCall operand", argCount)
	}

	staging := g.base + g.depth // funcReg, then args, live above the stack
	if staging+1+argCount > g.maxReg {
		g.maxReg = staging + 1 + argCount
	}
	g.loadConst(byte(staging), g.c.AddConstant(calleeVal))
	for k := 0; k < argCount; k++ {
		src := byte(g.base + g.depth - argCount + k)
		g.emit(vm.OpMove, byte(staging+1+k), src)
	}

	g.depth -= argCount
	dest := byte(g.base + g.depth) // reclaimed first-arg slot
	g.emit3(vm.OpCall, dest, byte(staging), byte(argCount))
	if results == 1 {
		g.push() // result lands in dest
	}
	return nil
}

// emitCallIndirect lowers `call_indirect typeIdx tableIdx`. Stack: [args..,
// idx]. It resolves the callee through the table.get helper (which traps on a bad
// index or signature), then does an ordinary OpCall. The static typeIdx fixes the
// arity, so no runtime signature is needed to stage the args.
func (g *funcGen) emitCallIndirect(typeIdx uint32) error {
	if g.table == nil {
		return fmt.Errorf("call_indirect without a function table")
	}
	if int(typeIdx) >= len(g.mod.Types) {
		return fmt.Errorf("call_indirect type %d out of range", typeIdx)
	}
	sig := &g.mod.Types[typeIdx]
	argCount := len(sig.Params)
	results := len(sig.Results)
	if results > 1 {
		return fmt.Errorf("call_indirect to multi-result type unsupported")
	}
	if argCount > 255 {
		return fmt.Errorf("call_indirect arity %d exceeds OpCall operand", argCount)
	}

	// Pop the table index (top of stack); the args stay below it. After the pop,
	// funcReg == the popped index's register — we read it into the table.get
	// staging *before* the call overwrites funcReg with the resolved callee.
	idxReg := byte(g.base + g.depth - 1)
	g.depth--
	funcReg := g.base + g.depth
	need := funcReg + 4 // table.get staging: fn, idx, typeIdx (+ result slot)
	if funcReg+1+argCount > need {
		need = funcReg + 1 + argCount
	}
	if need > g.maxReg {
		g.maxReg = need
	}

	g.loadConst(byte(funcReg+1), g.c.AddConstant(g.table.get))
	g.emit(vm.OpMove, byte(funcReg+2), idxReg)
	g.loadConst(byte(funcReg+3), g.c.AddConstant(vm.Number(float64(typeIdx))))
	g.emit3(vm.OpCall, byte(funcReg), byte(funcReg+1), 2) // callee → funcReg

	// [funcReg][arg0..argN-1]: copy the args up after the resolved callee.
	for k := 0; k < argCount; k++ {
		src := byte(g.base + g.depth - argCount + k)
		g.emit(vm.OpMove, byte(funcReg+1+k), src)
	}
	g.depth -= argCount
	dest := byte(g.base + g.depth)
	g.emit3(vm.OpCall, dest, byte(funcReg), byte(argCount))
	if results == 1 {
		g.push()
	}
	return nil
}

// blockResults maps a block-type immediate to its result count. Empty → 0, a
// single valtype → 1; type-index (multi-value) signatures are unsupported.
func blockResults(bt int64) (int, error) {
	switch bt {
	case BlockTypeEmpty:
		return 0, nil
	case -1, -2, -3, -4: // i32/i64/f32/f64 as s33 valtypes
		return 1, nil
	default:
		return 0, fmt.Errorf("block type 0x%x (multi-value) unsupported", uint64(bt))
	}
}

// emitSelect lowers `select` (stack: a, b, cond) to result = cond ? a : b.
// The result reuses a's register, so the true branch needs no move.
func (g *funcGen) emitSelect() {
	cond := byte(g.base + g.depth - 1)
	b := byte(g.base + g.depth - 2)
	g.depth -= 3
	result := g.push() // == a's slot; already holds a
	useB := g.newLabel()
	end := g.newLabel()
	g.condJumpTo(cond, useB) // cond false → use b
	g.jumpTo(end)            // cond true → a already in place
	g.markLabel(useB)
	g.emit(vm.OpMove, result, b)
	g.markLabel(end)
}

// emitUnreachable traps: throw a wasm-unreachable error. Marks the rest of the
// block dead.
func (g *funcGen) emitUnreachable() {
	t := byte(g.base + g.depth)
	if g.base+g.depth+1 > g.maxReg {
		g.maxReg = g.base + g.depth + 1
	}
	g.loadConst(t, g.c.AddConstant(vm.NewString("wasm: unreachable executed")))
	g.emitOp1(vm.OpThrow, t)
	g.unreachable = true
}

// emitBrTable lowers `br_table` to a compare chain: for each case i, if the
// popped index equals i branch to labels[i], else fall through; a final
// unconditional branch handles the default. (paserati has no computed jump.)
func (g *funcGen) emitBrTable(labels []uint32, def uint32) error {
	idx := byte(g.base + g.depth - 1)
	g.depth-- // pop the index (its register value stays live above the stack)
	cmp := byte(g.base + g.depth + 1)
	ic := byte(g.base + g.depth + 2)
	if int(ic)+1 > g.maxReg {
		g.maxReg = int(ic) + 1
	}
	for i, lbl := range labels {
		g.loadConst(ic, g.c.AddConstant(vm.Number(float64(i))))
		g.emit3(vm.OpEqual, cmp, idx, ic)
		skip := g.newLabel()
		g.condJumpTo(cmp, skip) // idx != i → skip this case
		if err := g.branchTo(lbl); err != nil {
			return err
		}
		g.markLabel(skip)
	}
	if err := g.branchTo(def); err != nil {
		return err
	}
	g.unreachable = true
	return nil
}

func (g *funcGen) emitReturn() {
	if len(g.fn.Type.Results) == 0 || g.depth == 0 {
		g.emitOp0(vm.OpReturnUndefined)
		return
	}
	g.emitOp1(vm.OpReturn, g.pop())
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

// growReg raises the high-water register count to at least n.
func (g *funcGen) growReg(n int) {
	if n > g.maxReg {
		g.maxReg = n
	}
}
