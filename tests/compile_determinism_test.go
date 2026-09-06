package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/nooga/paserati/pkg/driver"
	"github.com/nooga/paserati/pkg/vm"
)

// hashChunkDeep hashes a chunk's code together with every nested function
// chunk reachable through its constants. Registers are allocated per function
// body, so a top-level-only hash would miss where allocation actually varies.
func hashChunkDeep(c *vm.Chunk) string {
	h := sha256.New()
	seen := map[*vm.Chunk]bool{}
	var walk func(*vm.Chunk)
	walk = func(ch *vm.Chunk) {
		if ch == nil || seen[ch] {
			return
		}
		seen[ch] = true
		h.Write(ch.Code)
		for _, k := range ch.Constants {
			if k.Type() != vm.TypeFunction {
				continue
			}
			if fn := k.AsFunction(); fn != nil {
				walk(fn.Chunk)
			}
		}
	}
	walk(c)
	return hex.EncodeToString(h.Sum(nil))
}

// Compiling one source must always produce the same bytecode. The block below
// declares several locals that are reclaimed together at scope exit; the
// order they are returned to the free list decides which register the next
// temporary receives, so any order-dependence in scope-exit reclamation shows
// up here as a second variant (nooga#50).
func TestCompileIsDeterministic(t *testing.T) {
	const src = `
function f() {
  { let a = 1; let b = 2; let c = 3; a + b + c; }
  return 4 + 5;
}
f();
`
	const runs = 200
	var want string
	for i := 0; i < runs; i++ {
		chunk, errs := driver.CompileString(src)
		if len(errs) > 0 {
			t.Fatalf("compile %d: %v", i, errs)
		}
		got := hashChunkDeep(chunk)
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("compile %d produced different bytecode (%s) than compile 0 (%s)", i, got[:12], want[:12])
		}
	}
}
