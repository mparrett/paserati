package wasm

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nooga/paserati/pkg/vm"
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

// TestSpillRetryOnOverflow exercises the path the forced runs above bypass:
// bigspill.wat declares 300 locals, so the direct register mapping must fail
// with errRegOverflow and CompileModuleWasi must recover via resetChunk + the
// spilled retry. A regression in that automatic recovery (e.g. the stale
// const-pool caches resetChunk once missed) fails here, not just under
// forceSpillAll. Expected value is the wasmtime reference.
func TestSpillRetryOnOverflow(t *testing.T) {
	m := mustDecode(t, "testdata/bigspill.wasm")
	mp, err := Profile(m)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if !strings.Contains(mp.String(), "spilled") {
		t.Fatalf("expected bigspill to need the spiller; profile:\n%s", mp.String())
	}
	exports, _, err := CompileModuleWasi(m, nil, nil)
	if err != nil {
		t.Fatalf("CompileModuleWasi: %v", err)
	}
	machine := vm.NewVM()
	if got := callI(t, machine, exports["bigsum"], 5); got != 44855 {
		t.Errorf("bigsum(5) = %v, want 44855 (5 + sum 0..299)", got)
	}
}
