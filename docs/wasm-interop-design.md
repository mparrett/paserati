# WebAssembly Interop — Design Notes & Transpile Experiment

Status: exploratory. Captures three ways to make paserati and third-party
WebAssembly modules interoperate, then plans the one that's actually
interesting: **transpiling wasm directly into paserati bytecode**.

Paserati today has no wasm runtime, no `WebAssembly` global, and zero wasm
dependencies (`go.mod`: only `regexp2`, `x/text`, `x/perf`). It *is* a pure-Go
register VM that also cross-compiles cleanly to `js/wasm` and `wasip1/wasm`.
So there's nothing to link `.wat` against — any interop path is something we
build.

---

## The three approaches

### A. Host a pure-Go wasm runtime as a builtin  *(clean, "correct")*

Add [wazero](https://github.com/tetratelabs/wazero) — pure Go, no CGO, which
matches paserati's "no CGO, no wasm blobs" ethos. Expose a builtin roughly:

```ts
const { exports } = WebAssembly.instantiate(bytes, importObject);
exports.add(2, 3);
```

Each export becomes a `vm.NewNativeFunction` trampolining into wazero; the
instance's linear memory is backed by an `ArrayBufferObject`
(`pkg/vm/typed_array.go`). This is the way to run *arbitrary* third-party
`.wasm`. A few hundred lines, mostly value marshaling. Not twisted — it's just
what "run a wasm module" means.

### B. Transpile wasm → paserati bytecode  *(the experiment — see below)*

Skip interpretation entirely: lower a `.wasm`/`.wat` into a paserati `*Chunk`,
emitting real opcodes. Works because wasm's numeric core maps nearly 1:1 onto
paserati's instruction set. The engine runs the result as if it were compiled
from TypeScript. No new runtime dependency; the "runtime" is the VM we already
have.

### C. Two-wasm-in-a-browser  *(host-level glue)*

Compile paserati to `js/wasm`, load it and the third-party module in the same
JS host, bridge via `syscall/js`. Paserati never runs the wasm — the browser
does — but a paserati script can call the other module's exports through a
native-function shim. Least code, but pushes the wasm out of the engine.

**Recommendation:** A for real use, B for the craft. C only if we're already
targeting the browser.

---

## Why B is tractable: the instruction mapping

Wasm is a **stack machine**; paserati is a **register machine** (160 opcodes,
`pkg/vm/bytecode.go`). The numeric core lines up almost directly:

| wasm | paserati | notes |
|------|----------|-------|
| `i32.add` / `f64.add` | `OpAdd Rx Ry Rz` | paserati ops are type-generic over its number values |
| `i32.sub/mul/div` | `OpSubtract` / `OpMultiply` / `OpDivide` | |
| `i32.rem_s` | `OpRemainder` | |
| `i32.shl / shr_s / shr_u` | `OpShiftLeft` / `OpShiftRight` / `OpUnsignedShiftRight` | |
| `i32.and/or/xor` | `OpBitwiseAnd` / `OpBitwiseOr` / `OpBitwiseXor` | |
| `i32.lt_s / gt_s / eq` | `OpLess` / `OpGreater` / `OpEqual` | produce a boolean value |
| `i32.const N` | `OpLoadConst Rx K` (via `AddConstant`) | |
| `local.get / local.set` | `OpMove` between registers | locals ARE registers |
| `call $f` | `OpCall Rx FuncReg ArgCount` | |
| `return` | `OpReturn Rx` | |

The low-level emitter already exists on `Chunk` — no need to touch the register
allocator:

- `NewChunk()`
- `WriteOpCode(op, line)`
- `EmitByte(b)` — register operands
- `WriteUint16(val)` — 16-bit offsets/const indices, big-endian
- `AddConstant(v) uint16`
- `DisassembleChunk(name)` — eyeball the output while iterating

### The two hard seams

Everything interesting is here; the arithmetic is the easy 80%.

1. **Operand stack → registers.** Translate by *simulating* the wasm operand
   stack at compile time: maintain a virtual stack of register numbers. `i32.add`
   pops two register ids, allocates a result register, emits
   `OpAdd rDst rA rB`, pushes `rDst`. Wasm locals map to a fixed low register
   band; the operand stack uses a bump allocator above them. `MaxRegs` = high
   water mark. (paserati spills past 256 regs via `OpLoadSpill/StoreSpill` — out
   of scope for the spike; cap at 256.)

2. **Structured control flow → flat jumps.** Wasm `block`/`loop`/`if` +
   labeled `br N` / `br_if N` must lower to paserati's flat `OpJump Offset(16)`
   and `OpJumpIfFalse Rx Offset(16)`. The move:
   - Keep a control stack of frames, each recording its *branch target* — for a
     `block`/`if` that's the instruction *after* `end`; for a `loop` it's the
     loop *header*.
   - `br N` = unconditional jump to the Nth frame's target; `br_if N` =
     `OpJumpIfFalse` over an `OpJump` (or a conditional jump if we add one).
   - Forward jumps need **backpatching**: emit a placeholder `WriteUint16(0)`,
     record the code offset, fill in the real 16-bit delta once the target is
     known.
   - Offsets are 16-bit and relative — fine for the spike; note the ceiling.

i64 leans on paserati's int64 value payload / BigInt; `f32` collapses to f64.
Traps (div-by-zero, unreachable) can map to `OpThrow` and later the
`ExceptionTable`. Memory (`i32.load/store`) backs onto an `ArrayBufferObject` —
punt to a later phase.

---

## Experiment plan

Goal: hand a small `.wat` to a translator and have paserati execute it with the
right result. Prove B end to end on the numeric+control-flow subset. Keep it in
`scratch/` until it earns a home under `cmd/` or `pkg/`.

**Phase 0 — spike the substrate. ✅ DONE.**
`cmd/wasmspike/main.go` hand-builds `add` and an iterative `fib` as chunks,
wraps them, and executes: `fib(20) == 6765` etc. all green. Resolved facts the
rest of the work relies on:

- **Wrap:** `vm.NewFunction(arity, length, upvalueCount, registerSize, variadic,
  name, chunk, isGenerator, isAsync, isArrow, hasLocalCaptures) Value`. For a
  leaf numeric fn pass `upvalueCount=0`, the rest false.
- **Invoke:** `vm.Call(fn, vm.Undefined, []vm.Value{...}) (Value, error)`; read
  the result with `.ToFloat()`.
- **Calling convention:** args arrive in `R0..R(arity-1)`; body temporaries use
  registers above. `registerSize`/`chunk.MaxRegs` = high-water register + 1.
- **Emitter:** `WriteOpCode(op, line)` + `EmitByte`/`WriteUint16` (big-endian);
  `AddConstant(vm.Number(f))` dedupes and returns a uint16 index. `Lines` stays
  parallel automatically.
- **Verified encodings:** `OpLoadConst [op][reg][u16 idx]`,
  `OpAdd/OpLess [op][dst][a][b]`, `OpMove [op][dst][src]`, `OpReturn [op][reg]`,
  `OpJumpIfFalse [op][cond][i16 off]`, `OpJump [op][i16 off]`. Jump offsets are
  signed, relative to the ip **after** the 2 offset bytes.
- **Backpatching works** (proved by the fib loop): emit placeholder
  `WriteUint16(0)`, record the position, patch `Code[pos]`/`Code[pos+1]` once the
  target is known — the exact mechanism Phase 3 needs.

**Phase 1 — wasm front end. ✅ DONE.**
`pkg/wasm/` decodes the `.wasm` binary into a small typed IR — dependency-free,
no `.wat` text parser (fixtures are assembled with `wat2wasm`). Covers the
Type/Function/Export/Code sections, LEB128 (signed + unsigned), and the
numeric + control-flow opcode subset; anything outside it (memory, imports,
tables, SIMD) decodes as a clear "unsupported opcode" error rather than silent
misparse. `Decode([]byte) (*Module, error)` returns funcs (with resolved
signatures, locals, and a flat instruction stream that keeps
`block`/`loop`/`if`/`end` for the codegen pass) plus exports. Tests in
`decode_test.go` assert structure against `testdata/{add,fib}.wasm`; the fib
stream round-trips exactly (verified against the `.wat` source and wasmtime's
`fib(20)==6765`).

**Phase 2 — straight-line codegen. ✅ DONE.**
`pkg/wasm/codegen.go` — `CompileFunc(fn, name) (vm.Value, error)` lowers a
branch-free function to a callable. The stack→register scheme: locals hold the
low register band (params in R0.. by convention, declared locals zero-init'd);
the operand stack maps *directly* onto registers above them (depth `d` ↔
register `base+d`), so a binop reads the top two registers and rewrites the
lower in place — no free list, no moves. Handles const, `local.get/set/tee`,
`drop`, `return`, and the numeric binop table; control-flow/`call` opcodes error
as "not supported in phase 2". Tests compile+run `add`, `poly` (`2x²+3x+1`), and
`sq` (declared local), all cross-checked against wasmtime; the `poly`
disassembly confirms tight register reuse.

**Phase 3 — control flow. ✅ DONE.**
`codegen.go` grows a control-frame stack that lowers `block`/`loop`/`if`/`else`/
`br`/`br_if` to flat jumps. block/if are forward labels (branches backpatched to
their `end`); loop is a backward label (branch to the header, known on entry);
`br_if` inverts through `OpJumpIfFalse` since the VM has no jump-if-true. Only
empty block types (branches carry no values, so the depth↔register mapping stays
consistent); non-empty errors. Dead code after an unconditional `br`/`return`
allows only `end`/`else` until the region closes. Added `i32.eqz` (→ `OpNot`).
Tests run `fib` (`fib(20)==6765` through the real decode→codegen→VM path), `sum`
(loop), `max` (if, no else), `abs` (if/else), and `gcd` (loop + `br_if` + `eqz` +
`rem_s`) — all cross-checked against wasmtime. Codegen is correct but
un-peepholed (redundant `OpMove`s from copy-on-`local.get/set`).

**Phase 4 — calls.**
Intra-module `call`. Each wasm function → its own Chunk/function value; wire
`OpCall`. Target: recursive `fib`, mutual recursion.

**Phase 5 (stretch) — linear memory.**
`i32.load/store` over an `ArrayBufferObject`. Unlocks anything that touches
memory (string hashing, etc.). Likely a separate effort.

**Out of scope for the experiment:** imports/host functions, tables/`call_indirect`,
SIMD, threads, GC proposal, full trap semantics, >256 registers.

### Success criterion

`translate(fib.wasm)` produces a function value `f` such that `f(20) == 6765`,
with the whole path — parse → IR → Chunk → VM — running inside paserati and no
new runtime dependency in the execution path.

### Risks / unknowns

- ~~**Chunk→callable wrapping** (Phase 0) is the one genuine unknown~~ —
  resolved; see Phase 0 above.
- i32 wrap-around / overflow semantics differ from JS number math — the spike
  can ignore this, but a faithful translator needs explicit masking
  (`x | 0`-style) after arithmetic.
- 16-bit relative jump ceiling; fine for small modules, a real limit later.

---

## Related

- `docs/aot-compilation-design.md` — Chunk serialization / binary module format;
  same `*Chunk` artifact, and a natural place for translated-wasm blobs to live.
- `docs/native-module-interop-design.md` — the reflection FFI (`ModuleBuilder`)
  that approach A would build the `WebAssembly` builtin on top of.
