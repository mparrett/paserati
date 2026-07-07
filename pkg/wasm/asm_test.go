package wasm

import (
	"testing"

	"github.com/nooga/paserati/pkg/vm"
)

// filler adds n barrier ops (4 bytes each) that the peephole leaves in place, so
// a jump over them has a predictable byte distance.
func filler(g *funcGen, n int) {
	for i := 0; i < n; i++ {
		g.add(&asmInstr{kind: akOp, op: vm.OpCall, ops: []byte{0, 0, 0}})
	}
}

func be32(b []byte) int32 {
	return int32(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
}

// A jump within int16 range stays the compact 2-byte-offset OpJump.
func TestFinishKeepsShortJump(t *testing.T) {
	g := &funcGen{c: vm.NewChunk()}
	end := g.newLabel()
	g.jumpTo(end)
	filler(g, 3) // 12 bytes — well under 32 KB
	g.markLabel(end)
	g.emitOp0(vm.OpReturnUndefined)
	g.finish()

	if got := vm.OpCode(g.c.Code[0]); got != vm.OpJump {
		t.Fatalf("short jump encoded as %v, want OpJump", got)
	}
}

// A jump past the int16 range is promoted to OpJumpLong with a correct 32-bit
// offset that lands exactly on the target.
func TestFinishPromotesLongJump(t *testing.T) {
	const n = 9000 // 9000*4 = 36000 bytes of filler — past the ±32 KB int16 limit
	g := &funcGen{c: vm.NewChunk()}
	end := g.newLabel()
	g.jumpTo(end)
	filler(g, n)
	g.markLabel(end)
	g.emitOp0(vm.OpReturnUndefined)
	g.finish()

	if got := vm.OpCode(g.c.Code[0]); got != vm.OpJumpLong {
		t.Fatalf("over-range jump encoded as %v, want OpJumpLong", got)
	}
	off := be32(g.c.Code[1:5])
	target := 5 + int(off) // offset is relative to the byte past the 5-byte instruction
	if want := 5 + n*4; target != want {
		t.Fatalf("long jump target = %d, want %d", target, want)
	}
	if got := vm.OpCode(g.c.Code[target]); got != vm.OpReturnUndefined {
		t.Fatalf("long jump lands on %v, want OpReturnUndefined", got)
	}
}

// A conditional jump over the same distance promotes to OpJumpIfFalseLong while
// preserving its condition-register operand.
func TestFinishPromotesLongCondJump(t *testing.T) {
	const n = 9000
	g := &funcGen{c: vm.NewChunk()}
	end := g.newLabel()
	g.condJumpTo(7, end) // condReg R7
	filler(g, n)
	g.markLabel(end)
	g.emitOp0(vm.OpReturnUndefined)
	g.finish()

	if got := vm.OpCode(g.c.Code[0]); got != vm.OpJumpIfFalseLong {
		t.Fatalf("over-range cond jump encoded as %v, want OpJumpIfFalseLong", got)
	}
	if g.c.Code[1] != 7 {
		t.Fatalf("cond register operand = %d, want 7", g.c.Code[1])
	}
	off := be32(g.c.Code[2:6])
	target := 6 + int(off) // op + condReg + 4-byte offset
	if got := vm.OpCode(g.c.Code[target]); got != vm.OpReturnUndefined {
		t.Fatalf("long cond jump lands on %v, want OpReturnUndefined", got)
	}
}
