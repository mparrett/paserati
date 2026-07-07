package wasm

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunWasiHello is the milestone end-to-end check: a real TinyGo
// `-target=wasi` binary compiled through the transpiler and run to completion,
// writing to stdout via fd_write and ending through proc_exit(0).
func TestRunWasiHello(t *testing.T) {
	m := mustDecode(t, "testdata/tinygo_wasi_hello.wasm")

	var out, errBuf bytes.Buffer
	code, err := RunStart(m, &out, &errBuf)
	if err != nil {
		t.Fatalf("RunStart: %v (stderr: %q)", err, errBuf.String())
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(out.String()); got != "hello from tinygo wasi" {
		t.Fatalf("stdout = %q, want %q", got, "hello from tinygo wasi")
	}
}

// runOut compiles and runs a WASI command fixture, returning trimmed stdout.
func runOut(t *testing.T, path string) string {
	t.Helper()
	m := mustDecode(t, path)
	var out, errBuf bytes.Buffer
	code, err := RunStart(m, &out, &errBuf)
	if err != nil {
		t.Fatalf("%s: RunStart: %v (stderr: %q)", path, err, errBuf.String())
	}
	if code != 0 {
		t.Fatalf("%s: exit code = %d", path, code)
	}
	return strings.TrimSpace(out.String())
}

// TestRunFloats exercises the f64 arithmetic/compare/conversion + trunc_sat batch
// and the float→decimal formatter (which needs truncated i32.div_s and i64 math).
// The expected output is the wasmtime reference for tinygo_floats.wasm.
func TestRunFloats(t *testing.T) {
	want := "sum: +1.500000e+009 prod: -6.685379e+011 n: 214285736"
	if got := runOut(t, "testdata/tinygo_floats.wasm"); got != want {
		t.Fatalf("stdout =\n  %q\nwant\n  %q", got, want)
	}
}

// TestRunI64 exercises the exact 64-bit integer helpers (mul, shifts, div_u,
// rem_u) against the wasmtime reference for tinygo_i64.wasm.
func TestRunI64(t *testing.T) {
	want := strings.Join([]string{
		"mul: 1000000010000000021",
		"shl: 1099511627776",
		"shru: -1",
		"divu: 6148914691236517205",
		"remu: 1",
		"clz: 30",
	}, "\n")
	if got := runOut(t, "testdata/tinygo_i64.wasm"); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
}
