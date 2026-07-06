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

func TestCompileFib(t *testing.T) {
	// The whole point: iterative fib through decode → codegen → VM, matching
	// wasmtime's fib(20) == 6765. Exercises block/loop/br_if/br.
	m, fn := compileExport(t, "testdata/fib.wasm", "fib")
	cases := []struct{ n, want float64 }{{0, 0}, {1, 1}, {10, 55}, {20, 6765}}
	for _, c := range cases {
		if got := callI(t, m, fn, c.n); got != c.want {
			t.Errorf("fib(%v) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestCompileSum(t *testing.T) {
	m, fn := compileExport(t, "testdata/sum.wasm", "sum")
	for _, c := range []struct{ n, want float64 }{{5, 15}, {10, 55}, {0, 0}} {
		if got := callI(t, m, fn, c.n); got != c.want {
			t.Errorf("sum(%v) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestCompileMax(t *testing.T) {
	// if without else.
	m, fn := compileExport(t, "testdata/max.wasm", "max")
	for _, c := range []struct{ a, b, want float64 }{{3, 7, 7}, {9, 2, 9}, {5, 5, 5}} {
		if got := callI(t, m, fn, c.a, c.b); got != c.want {
			t.Errorf("max(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCompileAbs(t *testing.T) {
	// if/else.
	m, fn := compileExport(t, "testdata/abs.wasm", "abs")
	for _, c := range []struct{ x, want float64 }{{-5, 5}, {7, 7}, {0, 0}} {
		if got := callI(t, m, fn, c.x); got != c.want {
			t.Errorf("abs(%v) = %v, want %v", c.x, got, c.want)
		}
	}
}

// compileModuleExport decodes, compiles the whole module (so calls resolve),
// and returns the named export with a fresh VM.
func compileModuleExport(t *testing.T, path, name string) (*vm.VM, vm.Value) {
	t.Helper()
	m := mustDecode(t, path)
	exports, err := CompileModule(m)
	if err != nil {
		t.Fatalf("compile module %s: %v", path, err)
	}
	fn, ok := exports[name]
	if !ok {
		t.Fatalf("export %q not found", name)
	}
	return vm.NewVM(), fn
}

func TestCompileRecursiveFib(t *testing.T) {
	// Recursion through OpCall + result-typed if.
	m, fn := compileModuleExport(t, "testdata/recfib.wasm", "fib")
	for _, c := range []struct{ n, want float64 }{{0, 0}, {1, 1}, {10, 55}, {20, 6765}} {
		if got := callI(t, m, fn, c.n); got != c.want {
			t.Errorf("fib(%v) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestCompileMutualRecursion(t *testing.T) {
	// is_even/is_odd call each other.
	m := mustDecode(t, "testdata/parity.wasm")
	exports, err := CompileModule(m)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	vmi := vm.NewVM()
	for _, c := range []struct {
		fn   string
		n    float64
		want float64
	}{{"is_even", 10, 1}, {"is_even", 7, 0}, {"is_odd", 7, 1}, {"is_odd", 4, 0}} {
		if got := callI(t, vmi, exports[c.fn], c.n); got != c.want {
			t.Errorf("%s(%v) = %v, want %v", c.fn, c.n, got, c.want)
		}
	}
}

func TestCompileMemoryLoad(t *testing.T) {
	// i32.load over a data segment (offset immediate on the second load).
	m, fn := compileModuleExport(t, "testdata/memsum4.wasm", "sum4")
	if got := callI(t, m, fn); got != 10 {
		t.Errorf("sum4() = %v, want 10", got)
	}
}

func TestCompileMemoryStoreLoad(t *testing.T) {
	m := mustDecode(t, "testdata/memrw.wasm")
	exports, err := CompileModule(m)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	vmi := vm.NewVM()
	if got := callI(t, vmi, exports["rw"], 42); got != 42 {
		t.Errorf("rw(42) = %v, want 42", got)
	}
	// store8 truncates to the low byte.
	if got := callI(t, vmi, exports["bytes"], 511); got != 255 {
		t.Errorf("bytes(511) = %v, want 255", got)
	}
}

func TestCompileMemoryLoop(t *testing.T) {
	// i32.load with a computed address inside a loop.
	m, fn := compileModuleExport(t, "testdata/memarrsum.wasm", "arrsum")
	for _, c := range []struct{ n, want float64 }{{5, 15}, {3, 6}, {0, 0}} {
		if got := callI(t, m, fn, c.n); got != c.want {
			t.Errorf("arrsum(%v) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestCompileMemoryOutOfBounds(t *testing.T) {
	// A load past the end of memory must fault at runtime, not corrupt.
	machine, fn := compileModuleExport(t, "testdata/memarrsum.wasm", "arrsum")
	// arrsum walks i*4; a huge n runs off the single 64 KiB page.
	if _, err := machine.Call(fn, vm.Undefined, []vm.Value{vm.Number(100000)}); err == nil {
		t.Error("expected out-of-bounds memory access to error, got nil")
	}
}

func TestCompileGlobalConst(t *testing.T) {
	m, fn := compileModuleExport(t, "testdata/global_const.wasm", "add_base")
	if got := callI(t, m, fn, 5); got != 105 {
		t.Errorf("add_base(5) = %v, want 105", got)
	}
}

func TestCompileGlobalMutablePersists(t *testing.T) {
	// A mutable global must persist across calls within one module instance:
	// inc starts g=10, so 15 then 20 (wasmtime resets per --invoke; we don't).
	m := mustDecode(t, "testdata/global_counter.wasm")
	exports, err := CompileModule(m)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	vmi := vm.NewVM()
	if got := callI(t, vmi, exports["inc"], 5); got != 15 {
		t.Errorf("first inc(5) = %v, want 15", got)
	}
	if got := callI(t, vmi, exports["inc"], 5); got != 20 {
		t.Errorf("second inc(5) = %v, want 20", got)
	}
	if got := callI(t, vmi, exports["peek"]); got != 20 {
		t.Errorf("peek() = %v, want 20", got)
	}
}

func TestCompileFuncRejectsCall(t *testing.T) {
	// CompileFunc (single-function) can't resolve calls.
	m := mustDecode(t, "testdata/recfib.wasm")
	if _, err := CompileFunc(&m.Funcs[0], "fib"); err == nil {
		t.Error("expected CompileFunc to reject a call, got nil")
	}
}

func TestCompileGcd(t *testing.T) {
	// loop + br_if + i32.eqz + i32.rem_s.
	m, fn := compileExport(t, "testdata/gcd.wasm", "gcd")
	for _, c := range []struct{ a, b, want float64 }{{48, 36, 12}, {1071, 462, 21}, {17, 5, 1}} {
		if got := callI(t, m, fn, c.a, c.b); got != c.want {
			t.Errorf("gcd(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
