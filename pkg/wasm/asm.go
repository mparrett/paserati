package wasm

import "github.com/nooga/paserati/pkg/vm"

// The codegen emits into a symbolic instruction list rather than straight to
// bytes, so a peephole pass can run before jump offsets are fixed. Jumps target
// label markers (pointers), so removing instructions never invalidates a jump.
// finish() runs the peephole, lays out byte offsets, and encodes into the chunk.

type asmKind uint8

const (
	akOp       asmKind = iota // op + operand bytes
	akJump                    // OpJump → target label
	akCondJump                // OpJumpIfFalse condReg → target label
	akLabel                   // zero-size position marker (jump target)
)

type asmInstr struct {
	kind    asmKind
	op      vm.OpCode
	ops     []byte
	condReg byte
	target  *asmInstr // akJump/akCondJump
	dead    bool
	long    bool // akJump/akCondJump promoted to a 32-bit offset (branch relaxation)
	off     int  // byte offset, assigned during layout
}

// --- builder (funcGen methods) ---

func (g *funcGen) add(in *asmInstr) { g.out = append(g.out, in) }

func (g *funcGen) loadConst(reg byte, idx uint16) {
	g.add(&asmInstr{kind: akOp, op: vm.OpLoadConst, ops: []byte{reg, byte(idx >> 8), byte(idx)}})
}

func (g *funcGen) emit(op vm.OpCode, a, b byte) {
	g.add(&asmInstr{kind: akOp, op: op, ops: []byte{a, b}})
}

func (g *funcGen) emit3(op vm.OpCode, a, b, c byte) {
	g.add(&asmInstr{kind: akOp, op: op, ops: []byte{a, b, c}})
}

func (g *funcGen) emitOp0(op vm.OpCode)         { g.add(&asmInstr{kind: akOp, op: op}) }
func (g *funcGen) emitOp1(op vm.OpCode, a byte) { g.add(&asmInstr{kind: akOp, op: op, ops: []byte{a}}) }

func (g *funcGen) newLabel() *asmInstr   { return &asmInstr{kind: akLabel} }
func (g *funcGen) markLabel(l *asmInstr) { g.add(l) }
func (g *funcGen) jumpTo(l *asmInstr)    { g.add(&asmInstr{kind: akJump, op: vm.OpJump, target: l}) }
func (g *funcGen) condJumpTo(cond byte, l *asmInstr) {
	g.add(&asmInstr{kind: akCondJump, op: vm.OpJumpIfFalse, condReg: cond, target: l})
}

// --- layout + encode ---

func instrSize(in *asmInstr) int {
	switch in.kind {
	case akLabel:
		return 0
	case akJump:
		if in.long {
			return 5 // op + 4-byte offset
		}
		return 3 // op + 2-byte offset
	case akCondJump:
		if in.long {
			return 6 // op + condReg + 4-byte offset
		}
		return 4 // op + condReg + 2-byte offset
	default:
		return 1 + len(in.ops)
	}
}

// jumpDist returns the relative distance from a jump/branch to its target,
// measured from the byte just past the instruction (where the VM applies the
// offset). Requires offsets to be laid out.
func jumpDist(in *asmInstr) int {
	return in.target.off - (in.off + instrSize(in))
}

// finish peepholes, lays out offsets, and encodes into g.c. Jumps whose distance
// exceeds paserati's int16 range are promoted to the 32-bit long variants
// (OpJumpLong/OpJumpIfFalseLong); short jumps stay 2 bytes. Always succeeds — a
// 32-bit offset covers any function paserati can hold — so it returns false
// (kept for the caller's signature; jumps no longer force a stub).
func (g *funcGen) finish() bool {
	peephole(g.out)
	g.relaxJumps()

	for _, in := range g.out {
		if in.dead {
			continue
		}
		switch in.kind {
		case akLabel:
			// no bytes
		case akJump:
			if in.long {
				g.c.WriteOpCode(vm.OpJumpLong, 1)
				g.c.WriteUint32(uint32(int32(jumpDist(in))))
			} else {
				g.c.WriteOpCode(vm.OpJump, 1)
				g.c.WriteUint16(uint16(int16(jumpDist(in))))
			}
		case akCondJump:
			if in.long {
				g.c.WriteOpCode(vm.OpJumpIfFalseLong, 1)
				g.c.EmitByte(in.condReg)
				g.c.WriteUint32(uint32(int32(jumpDist(in))))
			} else {
				g.c.WriteOpCode(vm.OpJumpIfFalse, 1)
				g.c.EmitByte(in.condReg)
				g.c.WriteUint16(uint16(int16(jumpDist(in))))
			}
		default:
			g.c.WriteOpCode(in.op, 1)
			for _, b := range in.ops {
				g.c.EmitByte(b)
			}
		}
	}
	return false
}

// relaxJumps lays out byte offsets and promotes any jump whose distance
// overflows int16 to its long (32-bit) form. Promotion only grows the code, so
// distances are monotonically non-decreasing across iterations — the fixpoint
// converges (each round promotes at least one jump or halts).
func (g *funcGen) relaxJumps() {
	for {
		layout(g.out)
		changed := false
		for _, in := range g.out {
			if in.dead || in.long {
				continue
			}
			if in.kind != akJump && in.kind != akCondJump {
				continue
			}
			if d := jumpDist(in); d > 32767 || d < -32768 {
				in.long = true
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func layout(out []*asmInstr) {
	off := 0
	for _, in := range out {
		if in.dead {
			continue
		}
		in.off = off
		off += instrSize(in)
	}
}

// --- peephole ---

// pureRRR is the set of three-register value ops (dst, a, b) with no side
// effects — every wasm binop/compare lowers to one of these.
var pureRRR = func() map[vm.OpCode]bool {
	m := map[vm.OpCode]bool{}
	for _, op := range binopOp {
		m[op] = true
	}
	return m
}()

// opRW reports the operand indices a modeled pure-write op reads and the index
// it writes. ok is false for anything with side effects or implicit register use
// (OpCall, OpReturn*, …), which the passes treat as basic-block barriers.
func opRW(op vm.OpCode) (reads []int, write int, ok bool) {
	switch op {
	case vm.OpMove, vm.OpNot:
		return []int{1}, 0, true
	case vm.OpLoadConst:
		return nil, 0, true // operands after reg are a const index, not registers
	default:
		if pureRRR[op] {
			return []int{1, 2}, 0, true
		}
		return nil, 0, false
	}
}

func peephole(out []*asmInstr) {
	copyPropagate(out)
	deadStoreElim(out)
}

// copyPropagate rewrites source operands that are known copies of another
// register back to the original, within each basic block. This lets binops read
// locals directly instead of through copy-on-get temporaries.
func copyPropagate(out []*asmInstr) {
	copyOf := map[byte]byte{}
	resolve := func(r byte) byte {
		if s, ok := copyOf[r]; ok {
			return s
		}
		return r
	}
	invalidate := func(reg byte) {
		delete(copyOf, reg)
		for k, v := range copyOf {
			if v == reg { // reg changed, so anything aliasing it is stale
				delete(copyOf, k)
			}
		}
	}
	reset := func() { copyOf = map[byte]byte{} }

	for _, in := range out {
		if in.dead {
			continue
		}
		switch in.kind {
		case akLabel, akJump:
			reset()
		case akCondJump:
			in.condReg = resolve(in.condReg)
			reset() // ends the block
		case akOp:
			if in.op == vm.OpReturn {
				if len(in.ops) > 0 {
					in.ops[0] = resolve(in.ops[0])
				}
				reset()
				continue
			}
			reads, w, ok := opRW(in.op)
			if !ok {
				reset() // barrier: may read/write arbitrary registers
				continue
			}
			for _, ri := range reads {
				in.ops[ri] = resolve(in.ops[ri])
			}
			if in.op == vm.OpMove {
				d, s := in.ops[0], in.ops[1]
				if d == s {
					in.dead = true // self-move
					continue
				}
				invalidate(d)
				copyOf[d] = s
			} else {
				invalidate(in.ops[w])
			}
		}
	}
}

// deadStoreElim removes a pure-write op whose destination is overwritten before
// it is read, within the same basic block. Always safe: the value provably never
// escapes the block (it is redefined before block end), so cross-block liveness
// is irrelevant.
func deadStoreElim(out []*asmInstr) {
	for i, in := range out {
		if in.dead || in.kind != akOp || in.op == vm.OpReturn {
			continue
		}
		_, w, ok := opRW(in.op)
		if !ok {
			continue
		}
		d := in.ops[w]
		for j := i + 1; j < len(out); j++ {
			nx := out[j]
			if nx.dead {
				continue
			}
			if instrReads(nx, d) {
				break // used → live
			}
			if instrWrites(nx, d) {
				in.dead = true // redefined before use → dead store
				break
			}
			if isBoundary(nx) {
				break // block end → keep conservatively
			}
		}
	}
}

func instrReads(in *asmInstr, d byte) bool {
	switch in.kind {
	case akCondJump:
		return in.condReg == d
	case akOp:
		if in.op == vm.OpReturn {
			return len(in.ops) > 0 && in.ops[0] == d
		}
		reads, _, ok := opRW(in.op)
		if !ok {
			return true // barrier may read d
		}
		for _, ri := range reads {
			if in.ops[ri] == d {
				return true
			}
		}
	}
	return false
}

func instrWrites(in *asmInstr, d byte) bool {
	if in.kind != akOp || in.op == vm.OpReturn {
		return false
	}
	_, w, ok := opRW(in.op)
	return ok && in.ops[w] == d
}

func isBoundary(in *asmInstr) bool {
	switch in.kind {
	case akLabel, akJump, akCondJump:
		return true
	case akOp:
		if in.op == vm.OpReturn {
			return true
		}
		_, _, ok := opRW(in.op)
		return !ok // unmodeled op (barrier)
	}
	return false
}
