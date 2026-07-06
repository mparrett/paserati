package wasm

import (
	"testing"

	"github.com/nooga/paserati/pkg/vm"
)

// compileExport decodes a fixture, compiles the named export, and returns a
// callable Value plus a fresh VM to run it on.
func compileExport(t *testing.T, path, name string) (*vm.VM, vm.Value) {
	t.Helper()
	m := mustDecode(t, path)
	ex, ok := m.FuncExport(name)
	if !ok {
		t.Fatalf("export %q not found in %s", name, path)
	}
	fn, err := CompileFunc(&m.Funcs[ex.Index], name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return vm.NewVM(), fn
}

func callI(t *testing.T, m *vm.VM, fn vm.Value, args ...float64) float64 {
	t.Helper()
	vals := make([]vm.Value, len(args))
	for i, a := range args {
		vals[i] = vm.Number(a)
	}
	res, err := m.Call(fn, vm.Undefined, vals)
	if err != nil {
		t.Fatalf("call error: %v", err)
	}
	return res.ToFloat()
}

func TestCompileAdd(t *testing.T) {
	m, fn := compileExport(t, "testdata/add.wasm", "add")
	cases := []struct{ a, b, want float64 }{{2, 3, 5}, {40, 2, 42}, {-1, 1, 0}}
	for _, c := range cases {
		if got := callI(t, m, fn, c.a, c.b); got != c.want {
			t.Errorf("add(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCompilePoly(t *testing.T) {
	m, fn := compileExport(t, "testdata/poly.wasm", "poly")
	// 2*x*x + 3*x + 1, cross-checked against wasmtime.
	cases := []struct{ x, want float64 }{{5, 66}, {0, 1}, {10, 231}}
	for _, c := range cases {
		if got := callI(t, m, fn, c.x); got != c.want {
			t.Errorf("poly(%v) = %v, want %v", c.x, got, c.want)
		}
	}
}

func TestCompileSq(t *testing.T) {
	// Exercises a zero-initialised declared local + local.set/get.
	m, fn := compileExport(t, "testdata/sq.wasm", "sq")
	if got := callI(t, m, fn, 7); got != 49 {
		t.Errorf("sq(7) = %v, want 49", got)
	}
}

func TestCompileRejectsControlFlow(t *testing.T) {
	// fib uses block/loop/br_if — must be rejected until Phase 3.
	m := mustDecode(t, "testdata/fib.wasm")
	if _, err := CompileFunc(&m.Funcs[0], "fib"); err == nil {
		t.Error("expected phase-2 rejection of control flow, got nil")
	}
}
