# pkg/wasm

Compiles a subset of WebAssembly directly into paserati bytecode — no wasm
runtime, no new dependency. A `.wasm` module is decoded to a small IR and lowered
to `*vm.Chunk`s that the paserati VM executes as if they'd been compiled from
TypeScript.

Background and phase-by-phase notes: [`docs/wasm-interop-design.md`](../../docs/wasm-interop-design.md).

## Usage

```go
data, _ := os.ReadFile("mod.wasm")
mod, err := wasm.Decode(data)                 // bytes → IR
exports, err := wasm.CompileModule(mod)       // IR → callable Values (calls resolve)

res, err := machine.Call(exports["fib"], vm.Undefined, []vm.Value{vm.Number(20)})
// res.ToFloat() == 6765
```

`CompileFunc(fn, name)` compiles a single call-free function; `CompileModule`
compiles the whole module so `call` (including recursion and mutual recursion)
resolves.

CLI: [`cmd/paserati-wasm`](../../cmd/paserati-wasm) wraps this to run a `.wasm`
from the shell.

## Supported subset

- **Types:** `i32` throughout; `i64`/`f32`/`f64` consts decode but arithmetic is
  i32-focused. Numbers are JS `float64` — no faithful i32 wrap-around yet.
- **Values/locals:** `i32.const`, `local.get/set/tee`, `drop`.
- **Arithmetic/bitwise/compare:** add, sub, mul, div_s, rem_s, and/or/xor,
  shl/shr_s/shr_u, eq/ne/lt_s/gt_s/le_s/ge_s, `i32.eqz`.
- **Control flow:** `block`, `loop`, `if`/`else`, `br`, `br_if`, `return`
  (empty and single-value block types).
- **Calls:** intra-module `call`, including recursion and mutual recursion.
- **Memory:** `i32.load/store` and 8/16-bit width variants, `memory.size`,
  active data segments — byte-addressed, little-endian, bounds-checked.

## Not supported

Imports/host functions, globals, tables / `call_indirect`, `memory.grow`, SIMD,
threads, the GC proposal, multi-value blocks, and faithful trap semantics.
Anything outside the subset fails with a clear error rather than misparsing.

## How the lowering works

- **Stack → registers.** wasm's operand stack maps directly onto registers above
  the locals band: depth `d` ↔ register `base+d`. A binop rewrites the top two
  registers in place — no free list.
- **Control flow → flat jumps.** A control-frame stack turns `block`/`if` into
  forward labels (backpatched to their `end`) and `loop` into a backward label;
  `br_if` inverts through `OpJumpIfFalse`.
- **Calls.** Two-pass module compile creates every function's `Value` first, so a
  callee can sit in a caller's constant pool before its body exists (this is what
  makes recursion work); `call` lowers to `OpCall`.
- **Memory.** A real paserati `ArrayBuffer` plus native load/store helpers that
  close over its bytes; each access is an `OpCall` into a helper.

## Files

| File | Role |
|------|------|
| `decode.go` | wasm binary → IR (sections, LEB128) |
| `opcode.go` | opcode constants + immediate kinds |
| `module.go` | IR types |
| `codegen.go` | IR → `*vm.Chunk` (stack→register, control flow, calls) |
| `memory.go` | linear memory + load/store helpers |
| `testdata/` | `.wat` sources and their assembled `.wasm` fixtures |

## Tests

`go test ./pkg/wasm/`. Fixtures are assembled from `.wat` with `wat2wasm`, and
expected outputs are cross-checked against `wasmtime`.
