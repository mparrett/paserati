package wasm

import (
	"os"
	"testing"
)

func mustDecode(t *testing.T, path string) *Module {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m, err := Decode(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

func TestDecodeAdd(t *testing.T) {
	m := mustDecode(t, "testdata/add.wasm")

	if len(m.Types) != 1 {
		t.Fatalf("types: got %d, want 1", len(m.Types))
	}
	ft := m.Types[0]
	if len(ft.Params) != 2 || ft.Params[0] != I32 || ft.Params[1] != I32 {
		t.Errorf("params: got %v, want [i32 i32]", ft.Params)
	}
	if len(ft.Results) != 1 || ft.Results[0] != I32 {
		t.Errorf("results: got %v, want [i32]", ft.Results)
	}

	ex, ok := m.FuncExport("add")
	if !ok {
		t.Fatal(`export "add" not found`)
	}
	if ex.Index != 0 {
		t.Errorf("export index: got %d, want 0", ex.Index)
	}

	if len(m.Funcs) != 1 {
		t.Fatalf("funcs: got %d, want 1", len(m.Funcs))
	}
	fn := m.Funcs[0]
	if fn.NumParams() != 2 || fn.NumLocals() != 2 {
		t.Errorf("locals: params=%d total=%d, want 2/2", fn.NumParams(), fn.NumLocals())
	}

	wantBody := []Opcode{OpLocalGet, OpLocalGet, OpI32Add, OpEnd}
	if len(fn.Body) != len(wantBody) {
		t.Fatalf("body len: got %d, want %d (%v)", len(fn.Body), len(wantBody), opsOf(fn.Body))
	}
	for i, op := range wantBody {
		if fn.Body[i].Op != op {
			t.Errorf("body[%d]: got %s, want %s", i, fn.Body[i].Op, op)
		}
	}
	if fn.Body[0].U32 != 0 || fn.Body[1].U32 != 1 {
		t.Errorf("local.get indices: got %d,%d want 0,1", fn.Body[0].U32, fn.Body[1].U32)
	}
}

func TestDecodeFib(t *testing.T) {
	m := mustDecode(t, "testdata/fib.wasm")

	if _, ok := m.FuncExport("fib"); !ok {
		t.Fatal(`export "fib" not found`)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("funcs: got %d, want 1", len(m.Funcs))
	}
	fn := m.Funcs[0]
	if fn.NumParams() != 1 {
		t.Errorf("params: got %d, want 1", fn.NumParams())
	}
	// 4 declared locals (a, b, i, t) + 1 param.
	if fn.NumLocals() != 5 {
		t.Errorf("total locals: got %d, want 5", fn.NumLocals())
	}

	// The loop structure must survive decoding.
	counts := map[Opcode]int{}
	for _, ins := range fn.Body {
		counts[ins.Op]++
	}
	for _, op := range []Opcode{OpBlock, OpLoop, OpBrIf, OpBr, OpI32Add, OpI32GeS} {
		if counts[op] == 0 {
			t.Errorf("expected at least one %s in body, got none (%v)", op, opsOf(fn.Body))
		}
	}
	// br_if targets the outer block (depth 1), br targets the loop (depth 0).
	assertBranch(t, fn.Body, OpBrIf, 1)
	assertBranch(t, fn.Body, OpBr, 0)
}

func assertBranch(t *testing.T, body []Instr, op Opcode, wantDepth uint32) {
	t.Helper()
	for _, ins := range body {
		if ins.Op == op {
			if ins.U32 != wantDepth {
				t.Errorf("%s depth: got %d, want %d", op, ins.U32, wantDepth)
			}
			return
		}
	}
	t.Errorf("%s not found in body", op)
}

func opsOf(body []Instr) []Opcode {
	ops := make([]Opcode, len(body))
	for i, ins := range body {
		ops[i] = ins.Op
	}
	return ops
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte{0, 0, 0, 0}); err == nil {
		t.Error("expected error on bad magic, got nil")
	}
}
