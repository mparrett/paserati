/*
 * Instantiation fidelity: imported memory is usable, and global initialisers
 * keep their type (exact i64, non-zero f32/f64). See testdata/module_init.wat.
 */

package wasm

import (
	"testing"

	"github.com/nooga/paserati/pkg/vm"
)

func TestModuleInitFidelity(t *testing.T) {
	m := mustDecode(t, "testdata/module_init.wasm")
	if m.Memory == nil {
		t.Fatal("imported memory did not populate m.Memory")
	}
	exports, _, err := CompileModuleWasi(m, nil, nil)
	if err != nil {
		t.Fatalf("CompileModuleWasi: %v", err)
	}
	machine := vm.NewVM()
	call := func(name string, args ...float64) float64 {
		t.Helper()
		fn, ok := exports[name]
		if !ok {
			t.Fatalf("export %q missing", name)
		}
		return callI(t, machine, fn, args...)
	}
	// 2^62+1 survives only with the exact i64 carrier: as float64 it rounds to
	// 2^62 and the low 32 bits come back 0 instead of 1.
	if got := call("i64lo"); got != 1 {
		t.Errorf("i64 global lost exactness: low bits = %v, want 1", got)
	}
	if got := call("f64init"); got != 2.5 {
		t.Errorf("f64 global init = %v, want 2.5", got)
	}
	if got := call("f32init"); got != 1.5 {
		t.Errorf("f32 global init = %v, want 1.5", got)
	}
	if got := call("store_load", 16, 42); got != 42 {
		t.Errorf("store/load through imported memory = %v, want 42", got)
	}
}
