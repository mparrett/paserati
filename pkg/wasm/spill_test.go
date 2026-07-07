package wasm

import (
	"bytes"
	"strings"
	"testing"
)

// forceSpillRun compiles a fixture with every function routed through the spill
// lowering (locals in an array at R0) and checks the output still matches the
// register-mapped result. This exercises the spiller on ordinary small functions
// where its correctness is easy to trust.
func forceSpillRun(t *testing.T, path, want string) {
	t.Helper()
	forceSpillAll = true
	defer func() { forceSpillAll = false }()
	m := mustDecode(t, path)
	var out, errb bytes.Buffer
	code, err := RunStart(m, &out, &errb)
	if err != nil {
		t.Fatalf("%s: RunStart: %v (stderr %q)", path, err, errb.String())
	}
	if code != 0 {
		t.Fatalf("%s: exit %d", path, code)
	}
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("%s spilled output:\n got %q\nwant %q", path, got, want)
	}
}

func TestSpillHello(t *testing.T) {
	forceSpillRun(t, "testdata/tinygo_wasi_hello.wasm", "hello from tinygo wasi")
}

func TestSpillFloats(t *testing.T) {
	forceSpillRun(t, "testdata/tinygo_floats.wasm",
		"sum: +1.500000e+009 prod: -6.685379e+011 n: 214285736")
}

func TestSpillI64(t *testing.T) {
	forceSpillRun(t, "testdata/tinygo_i64.wasm", strings.Join([]string{
		"mul: 1000000010000000021", "shl: 1099511627776", "shru: -1",
		"divu: 6148914691236517205", "remu: 1", "clz: 30",
	}, "\n"))
}
