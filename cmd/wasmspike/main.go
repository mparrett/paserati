// Phase 0 spike for the wasm→bytecode experiment (docs/wasm-interop-design.md).
//
// Goal: prove we can hand-build a *vm.Chunk with the low-level emitter, wrap it
// in a callable function Value, and have the VM execute it with correct results.
// This resolves the one real unknown before any wasm front-end work: how a bare
// Chunk becomes an invocable function and how args arrive in registers.
//
// It also previews the two hard seams of the real translator:
//   - operand-stack → register allocation (done by hand here)
//   - structured control flow → flat jumps with backpatched 16-bit offsets
//
// Run: go run ./scratch/wasmspike
package main

import (
	"fmt"
	"os"

	"github.com/nooga/paserati/pkg/vm"
)

// asm is a tiny assembler over the Chunk emitter, with backpatchable jumps.
// Encodings verified against real compiler output:
//   OpLoadConst [op][reg][u16 constIdx]
//   OpAdd/OpLess [op][dst][srcA][srcB]
//   OpMove       [op][dst][src]
//   OpReturn     [op][reg]
//   OpJumpIfFalse[op][condReg][i16 off]   off relative to ip AFTER the 2 off bytes
//   OpJump       [op][i16 off]            same relative semantics
type asm struct{ c *vm.Chunk }

func newAsm() *asm { return &asm{c: vm.NewChunk()} }

func (a *asm) op(o vm.OpCode)  { a.c.WriteOpCode(o, 1) }
func (a *asm) b(r byte)        { a.c.EmitByte(r) }
func (a *asm) here() int       { return len(a.c.Code) }
func (a *asm) konst(f float64) uint16 { return a.c.AddConstant(vm.Number(f)) }

// loadConst: reg = Constants[idx]
func (a *asm) loadConst(reg byte, idx uint16) {
	a.op(vm.OpLoadConst)
	a.b(reg)
	a.c.WriteUint16(idx)
}

// tri: dst = srcA <op> srcB (Add, Less, ...)
func (a *asm) tri(o vm.OpCode, dst, srcA, srcB byte) {
	a.op(o)
	a.b(dst)
	a.b(srcA)
	a.b(srcB)
}

func (a *asm) move(dst, src byte) { a.op(vm.OpMove); a.b(dst); a.b(src) }
func (a *asm) ret(reg byte)       { a.op(vm.OpReturn); a.b(reg) }

// jumpIfFalse emits the op + placeholder offset, returns the offset byte position
// to patch later with patchTo.
func (a *asm) jumpIfFalse(cond byte) (patchPos int) {
	a.op(vm.OpJumpIfFalse)
	a.b(cond)
	patchPos = a.here()
	a.c.WriteUint16(0) // placeholder
	return
}

// jump emits an unconditional jump to an already-known target (e.g. a loop head).
func (a *asm) jump(target int) {
	a.op(vm.OpJump)
	patchPos := a.here()
	a.c.WriteUint16(0)
	a.patchTo(patchPos, target)
}

// patchTo fills a 2-byte offset at patchPos so the ip lands on target.
// Offset is relative to the ip AFTER the 2 offset bytes, i.e. patchPos+2.
func (a *asm) patchTo(patchPos, target int) {
	off := int16(target - (patchPos + 2))
	a.c.Code[patchPos] = byte(uint16(off) >> 8)
	a.c.Code[patchPos+1] = byte(uint16(off) & 0xff)
}

// wrap turns the chunk into a callable function Value.
func (a *asm) wrap(name string, arity, regSize int) vm.Value {
	a.c.MaxRegs = regSize
	return vm.NewFunction(arity, arity, 0, regSize, false, name, a.c, false, false, false, false)
}

// buildAdd: add(a, b) => a + b. Simplest case, no control flow.
// Convention: args land in R0, R1. Temp in R2.
func buildAdd() vm.Value {
	a := newAsm()
	a.tri(vm.OpAdd, 2, 0, 1) // R2 = R0 + R1
	a.ret(2)
	return a.wrap("add", 2, 3)
}

// buildFib: iterative fib(n). Previews the loop → flat-jump lowering.
//   R0=n(arg) R1=a R2=b R3=i R4=one R5=cond R6=t
func buildFib() vm.Value {
	a := newAsm()
	k0, k1 := a.konst(0), a.konst(1)
	a.loadConst(1, k0) // a = 0
	a.loadConst(2, k1) // b = 1
	a.loadConst(3, k0) // i = 0
	a.loadConst(4, k1) // one = 1

	loop := a.here()
	a.tri(vm.OpLess, 5, 3, 0)     // cond = i < n
	exit := a.jumpIfFalse(5)      // if !cond -> END (backpatch)
	a.tri(vm.OpAdd, 6, 1, 2)      // t = a + b
	a.move(1, 2)                  // a = b
	a.move(2, 6)                  // b = t
	a.tri(vm.OpAdd, 3, 3, 4)      // i = i + 1
	a.jump(loop)                  // back to loop head
	a.patchTo(exit, a.here())     // END:
	a.ret(1)                      // return a
	return a.wrap("fib", 1, 7)
}

func check(m *vm.VM, name string, fn vm.Value, args []vm.Value, want float64) bool {
	res, err := m.Call(fn, vm.Undefined, args)
	if err != nil {
		fmt.Printf("FAIL %s: call error: %v\n", name, err)
		return false
	}
	got := res.ToFloat()
	status := "ok  "
	pass := got == want
	if !pass {
		status = "FAIL"
	}
	fmt.Printf("%s %s(%v) = %v (want %v)\n", status, name, argf(args), got, want)
	return pass
}

func argf(args []vm.Value) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%v", a.ToFloat())
	}
	return s
}

func main() {
	add := buildAdd()
	fib := buildFib()

	// Disassemble to eyeball the hand-built bytecode.
	fmt.Println(add.AsFunction().Chunk.DisassembleChunk("add"))
	fmt.Println(buildFibChunkForDisasm())

	m := vm.NewVM()
	ok := true
	ok = check(m, "add", add, []vm.Value{vm.Number(2), vm.Number(3)}, 5) && ok
	ok = check(m, "add", add, []vm.Value{vm.Number(40), vm.Number(2)}, 42) && ok
	ok = check(m, "fib", fib, []vm.Value{vm.Number(10)}, 55) && ok
	ok = check(m, "fib", fib, []vm.Value{vm.Number(20)}, 6765) && ok
	ok = check(m, "fib", fib, []vm.Value{vm.Number(0)}, 0) && ok
	ok = check(m, "fib", fib, []vm.Value{vm.Number(1)}, 1) && ok

	if !ok {
		os.Exit(1)
	}
	fmt.Println("\nPhase 0 GREEN: hand-built chunks execute correctly.")
}

func buildFibChunkForDisasm() string {
	return buildFib().AsFunction().Chunk.DisassembleChunk("fib")
}
