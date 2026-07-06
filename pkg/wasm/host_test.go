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
