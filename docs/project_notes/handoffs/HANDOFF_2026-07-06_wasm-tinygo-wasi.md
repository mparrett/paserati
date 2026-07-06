# Session Handoff

**Created:** 2026-07-06T09:58:57-0700
**Session ID:** 00905e77-139a-4b9e-a2c5-bc34e8738bf4
**Working Directory:** /Users/matt/projects-new/3p/paserati-wasm

## What to read first

This is a **git worktree** (`paserati-wasm`), branch `feat/wasm-transpile` off
`origin/main` — NOT the main paserati checkout (`../paserati`, on a different
branch). Everything is committed but **nothing is pushed** (fork-disposable; user
does not want pushes). The project is a from-scratch **wasm → paserati-bytecode
transpiler** in `pkg/wasm/`; the current mission is running a **TinyGo
`-target=wasi` hello-world** through it. All pure-computation codegen now works —
the next wall is the **WASI host** (the fun part).

## Summary

Built the wasm engine from a toy subset up to a real target this session: knocked
down the decoder gate (parses a full TinyGo WASI binary), then cleared the entire
compute frontier in codegen — globals, exact i64 + floats, bulk memory, select,
unreachable, br_table, unsigned i32 ops, rotates, and the imported-func index
fix. The committed 20 KB TinyGo `hello.wasm` now compiles across all 43 functions
right up to its first host call (`random_get`). 28 `pkg/wasm` tests green;
`TestScripts` green.

## Current State

Branch `feat/wasm-transpile` (worktree `paserati-wasm`), off `origin/main`
(94a0c39). Clean working tree. Session commits (newest first):

```
8eaced3  wasm: codegen — bulk mem, select, unreachable, br_table, unsigned i32, rotates
fbf9ea8  wasm: codegen — i64 (exact) and f32/f64 value tier
774aa23  wasm: codegen — module globals (global.get/set)
10a82e7  wasm: decoder parses full modules (imports/tables/globals/elems/bulk mem)
45d2d9a  wasm: backlog — WasmEdge reference + notes pointers
83509ae  wasm: add idea/backlog log
c4368f4  wasm: peephole pass (copy-prop + dead-store elim)
67dad37  wasm: add pkg/wasm README
d64992d  wasm: Phase 5 — linear memory (load/store over ArrayBuffer)
a298251  wasm: cmd/paserati-wasm — CLI runner
669a503  wasm: Phase 4 — intra-module calls (recursion, mutual recursion)
0a79068  wasm: Phase 3 — structured control flow → flat jumps
877c230  wasm: Phase 2 — straight-line codegen (IR → bytecode)
c903a4a  wasm: Phase 1 — binary decoder → typed IR
1f732cd  wasm: design notes + Phase 0 bytecode spike
```

Package layout (`pkg/wasm/`): `decode.go` (bytes→IR), `opcode.go`, `module.go`
(IR), `codegen.go` (IR→symbolic instrs), `asm.go` (peephole + encode),
`memory.go` (linear memory + load/store/bulk helpers), `globals.go`,
`runtime.go` (i64.add + unsigned/rotate helpers). CLI: `cmd/paserati-wasm`.

## Uncommitted State / Untouched

- **Uncommitted:** none — working tree clean, all committed.
- **`scratch/` (gitignored, load-bearing — do NOT delete):**
  - `scratch/compile-probe/` — the wall-prober. `go run ./scratch/compile-probe
    scratch/tinygo-hello/hello.wasm` prints the next unsupported opcode/feature.
    This is the core inner-loop tool; use it after every codegen addition.
  - `scratch/tinygo-hello/` — `main.go` + `hello.wasm` + `hello.wat` (the target
    binary; also committed as `pkg/wasm/testdata/tinygo_wasi_hello.wasm`).
  - `scratch/analyze`, `scratch/dis`, `scratch/wasmdump` — decode/disassembly
    probes. `scratch/*.ts` are stray paserati repros, ignore.
- **Untouched deliberately:** `../paserati` (main checkout, other branch) — don't
  touch. call_indirect and memory.grow codegen — not yet reached (they come after
  the host call in function order), so intentionally unimplemented.

## Gotchas

- **Never push; commit locally only.** Fork is disposable PR-staging.
- **`scratch/` tools are gitignored but essential** — especially `compile-probe`.
  Rebuild them from the handoff if cleaned (trivial main.go wrappers over
  `wasm.Decode`/`wasm.CompileModule`).
- **Trust `.wat` over `wasm-objdump -d`** for opcode inventory — objdump
  undercounts branches (it hid every br/br_if/call_indirect on the first pass).
  Methodology: `docs/wasm-gap-analysis-methodology.md`.
- **i32 values are carried as signed float64.** i32 wrap-around is NOT faithful
  (uses JS math); it works because values stay in range. Unsigned ops reinterpret
  via `asU32` in `runtime.go`. This is a known fidelity gap (backlog), fine so far.
- **i64 is carried as BigInt** for exact 64-bit (paserati has no int64 value —
  `IntegerValue` is int32-only). Read back with `asI64` (memory.go). Tests use
  values > 2^53 to prove it.
- **`Chunk.DisassembleChunk` stack-overflows on self-referential (recursive)
  chunks** — the VM disassembler recurses into function constants. Don't
  disassemble recursive/whole-module chunks; execution is unaffected.
- **wasmtime resets globals/memory per `--invoke`** — it can't show cross-call
  persistence. Test persistence within one paserati module instance instead (see
  `TestCompileGlobalMutablePersists`).
- **Helper-call pattern:** globals/memory/i64/unsigned ops all lower to `OpCall`
  into native helpers closing over shared state, with compile-time constants
  (index/offset) folded in as const args. `OpCall` is a peephole barrier, so
  helper-heavy code optimizes less (expected).

## In Progress

**The WASI host** — the next wall. `emitCall` (codegen.go ~line 340) currently
errors on a call to an imported function with a clear message
(`call to imported func wasi_snapshot_preview1.random_get (WASI host) not
supported yet`). To finish: implement the 3 imports as native functions over the
module's memory `ArrayBuffer`, and route imported-func calls to them.

## Next Steps

1. **WASI host (the milestone).** Implement native helpers for the 3 imports:
   - `fd_write(fd, iovs, iovs_len, nwritten)` — read iovec structs from memory,
     write bytes to stdout (fd 1) / stderr (fd 2), store bytes-written.
   - `proc_exit(code)` — unwind to a clean exit (throw a sentinel the runner
     catches, or a dedicated result).
   - `random_get(buf, len)` — fill memory[buf:buf+len] with random bytes.
   Build a per-module import table (name→native Value) and have `emitCall` emit an
   `OpCall` to the import's helper when `funcIdx < ImportedFuncCount`. Pass the
   memory buffer so helpers can read/write it (memory helpers already close over
   `data []byte`; reuse that access).
2. **call_indirect** (8× in the binary) — build the function table from the Elem
   segment (`m.Elems`), map table index → the defined func `Value`, type-check the
   signature (`m.Types[typeidx]`), emit `OpCall`.
3. **memory.grow** (1×) — the backing buffer must reallocate; today the memory
   helpers close over a fixed `data []byte` slice, so move it behind a pointer/
   indirection first (already flagged in `docs/wasm-interop-backlog.md`).
4. **`_start` bootstrap** — a runner (extend `cmd/paserati-wasm` or a new mode)
   that sets up argv/env in memory, calls the `_start` export, and treats
   `proc_exit` as the exit. Success = it prints "hello from tinygo wasi".
5. Re-run `scratch/compile-probe` after each step to find the next wall.

## References

- Design + phase log: `docs/wasm-interop-design.md`
- Backlog / ideas: `docs/wasm-interop-backlog.md` (WasmEdge ref for host layer)
- Methodology: `docs/wasm-gap-analysis-methodology.md`
- Package guide: `pkg/wasm/README.md`
- External notes (`~/projects-new/project-docs/paserati/`):
  `wasm-tinygo-wasi-hello-spike.md` (the gap analysis + checklist),
  `wasm-transpile-i64-and-targets.md` (i64 design, target distinction, TinyGo).
- Cross-project: `../let-go` (nooga Clojure-in-Go, js/wasm); `../joint-xsofy`
  (TinyGo-wasm scars — stack-overflow saga, `-stack-size=1MB`). Nobody uses wasip1.
