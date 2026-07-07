# Dev report — let-go on paserati wasm (wasi enablement)

Joint report, two sessions: **let-go / joint-xsofy** side and **paserati** side.
Period: 2026-07-06 → 07. Status: **final** (both sides confirmed).

---

## TL;DR

let-go bytecode now runs **end-to-end on paserati's transpiler + VM, byte-for-byte
identical to wasmtime**, with **zero int64 / value-model fork**. Getting there took
a small, additive change on each side: on the let-go side a goroutine-free wasi
build (removing the asyncify gowrapper); on the paserati side two additive jump
opcodes + a register spiller (stock TS bytecode untouched). The feared "16-bit
registers + 32-bit ints" VM fork collapsed to none.

## Background — two tracks

The work began as a sequel to a TinyGo wasi hello-world: the natural next step was
a *heavy* real binary to stress paserati's wasm→bytecode transpiler. let-go looked
ideal, but **didn't build for wasi**. That split the work in two:

- **Track A (let-go side):** should let-go support a `wasip1` build — and is a
  wazero-embeddable, sandboxed let-go worth pitching upstream to nooga?
- **Track B (paserati side):** what does the transpiler hit on a heavy real binary,
  and which gaps are worth closing?

Independent, but synergistic: once let-go builds for wasi it *is* the heavy stress
target for B.

## Ideas & experiments (chronological)

| # | Experiment | Side | Result |
|---|---|---|---|
| 1 | wasip1 build gating (3 build tags) | let-go | builds; stock-Go 24 MB (64-bit faithful) + TinyGo 9 MB (32-bit int) |
| 2 | `runtime_only` (compiler-free) build | let-go | drops pkg/compiler+resolver; composes with wasi |
| 3 | stdin `.lgb` ingestion (`-`) | let-go | zero-filesystem load path for the host |
| 4 | run heavy `.lgb` through the transpiler | paserati | hit the two walls (below) |
| 5 | goroutine-free build (`-scheduler=none`) | let-go | removes asyncify gowrapper; 10 MB→8.1 MB |
| 6 | `-opt` sweep (opt2 vs optz) | both | trades the two walls; wins neither alone |
| 7 | register spiller | paserati | covers >256-register functions, transpiler-only |
| 8 | `OpJumpLong`/`OpJumpIfFalseLong` + branch relaxation | paserati | clears the jump wall additively |

## Findings

### let-go side

- **wasi is a 3-line gate**, not a port — extend the `!js` platform gates to
  `!wasip1` (readline REPL + `pkg/rt/term.go`). Toolchain-independent (stock Go and
  TinyGo fail identically pre-fix). `spike/wasi-gating` @ `6565ebe`.
- **The 64-bit-int "wall" is TinyGo-specific, not wasi-specific.** Stock-Go wasip1
  is numerically faithful (10^18, 2^62 exact); TinyGo's `int` is 32-bit. The
  toolchain choice is a size-vs-fidelity knob (24 MB faithful vs ~9 MB 32-bit).
- **`runtime_only` is a bounded packaging change**, already ~90% latent in the tree
  (`spike/wasi-ro-tinygo` @ `33c39ad`, base `runtime-only-build` @ `28c6dd4`). It's
  the trust/embedding surface for the wazero pitch.
- **Goroutine-free let-go is viable and the right wasi shape.** On the wasm MVP,
  asyncify is TinyGo's only goroutine mechanism and it *is* the gowrapper; it buys
  cooperative concurrency, never parallelism (wasip1 has no threads). Removing it
  is a bounded, feature-scoping fork (pods gated, pmap sequential, scope.go
  concurrency synchronous + a goroutine-free cancel). `spike/nogoroutine` @ `f7ee209`.
- **The residual fat function is eval/apply-shaped, inherent to TinyGo's lowering** —
  asyncify made it monstrous; without it, merely large. So "remove the wrapper →
  stock paserati" got most of the way, not all.

### paserati side

- **Two structural limits in the bytecode format:** registers are byte-indexed
  (≤256/function) and jump offsets are int16 (±32 KB). The asyncify gowrapper —
  TinyGo's goroutine trampoline — tripped both (in the with-compiler build, 560
  locals and ~557 KB jumps).
- **Removing goroutines killed the catastrophic case** (the ~392 KB trampoline) and
  cut jump-overflowing functions 22→6; `-opt=z` cut them to 3 but nearly doubled
  the worst register function (246→460 locals). No single `-opt` wins both axes —
  and every build still has exactly one spilled function, so the spiller is
  load-bearing regardless (see scan table).
- **Register widening was never needed — the spiller covers it.** A function over
  256 parks all locals in an array at R0 (`mkLocals`; `local.get/set` →
  `OpGetIndex`/`OpSetIndex`), leaving only the operand stack in registers; normal
  functions keep the fast direct-register path. Transpiler-only, no VM change —
  which is why the "16-bit registers" half of the feared fork evaporated.
- **The jump wall was closed additively.** Two new opcodes `OpJumpLong` /
  `OpJumpIfFalseLong` (32-bit offsets) + a branch-relaxation fixpoint (`relaxJumps`):
  short jumps stay 2 bytes, only over-range ones promote; promotion only grows
  code, so the fixpoint converges monotonically. The stock TS compiler never emits
  the long forms, so existing paserati bytecode is byte-for-byte unchanged. Commit
  `da2a875` on `feat/wasm-transpile`, with regression tests (`long_jump_test.go`,
  `asm_test.go`).
- **Latent bug fixed, un-masked by un-stubbing:** the direct→spilled retry reset a
  chunk's constants but not `AddConstant`'s dedup caches, so the retry got stale
  indices past the shorter pool (one function loaded constant 67 from a 25-entry
  pool). `Chunk.Reset()` now clears the caches. Not a long-jump bug — the crash was
  the second instruction, before any jump executes.
- **i64 runs on exact-BigInt helpers** — correct for full 64-bit fidelity, no
  register-width or int64 VM change needed. It allocates, so native i64 is a perf
  lever, not a correctness gate.

### Joint result

**All four builds** (asyncify + nogoroutine × opt2 + optz) run **byte-identical to
wasmtime**. **Zero int64/VM fork.** The residue (a few long jumps + one fat
function) lives entirely in paserati's *additive* machinery — nothing in let-go's
value model or eval core changed, and stock TS bytecode is untouched.

## Key measurements

Build sizes (TinyGo wasi, runtime-only):

| build | size | asyncify refs | gowrapper funcs |
|---|---|---|---|
| runtime_only (asyncify) opt2 | 10 MB | 6 | 12 |
| nogoroutine opt2 | 8.1 MB | 0 | 0 |
| nogoroutine optz | 5.5 MB | 0 | 0 |

Transpiler scan (paserati):

| | asyncify opt2 | asyncify optz | nogoroutine opt2 | nogoroutine optz |
|---|---|---|---|---|
| funcs | 2721 | 3048 | 2590 | 2896 |
| needs spiller (>256 regs) | 1 (248 loc) | 1 (462 loc) | 1 (246 loc) | 1 (460 loc) |
| needs long jumps (>32 KB) | 22 | 11 | 6 | 3 |
| worst jump | ~392 KB | ~218 KB | ~110 KB | ~130 KB |

Jump distances in KiB (÷1024). Every build has exactly **one** function over the
256-register file, so the spiller is load-bearing regardless of build; `-opt` level
only moves how many functions need long jumps and how deep the one spilled function
goes.

## Next steps

All orthogonal, all perf/outreach — **not correctness** (correctness is done).

**let-go side**
1. **Productionize goroutine-free behind a `nogoroutine` build tag** so native/browser
   keep real goroutines. Plan written; open question: whether xsofy's own concurrency
   usage lets *the game* ship on it, or it's transpiler-target-only.
2. **Track A — the wazero embedding pitch to upstream (nooga).** Gating is proven;
   the deciding factor is a concrete embedding consumer.
3. **Outbound draft cleanups (held for human review):** tinygo-support PR int-width
   precision fix; runtime-only PR proposal→implemented; the TinyGo stack-overflow
   issue framing.

**paserati side** (priority order; none urgent — correctness is done)
1. **Bytecode self-check pass.** A verifier that jump targets land on instruction
   boundaries and constant indices are in range. Cheap insurance that would have
   caught the `resetChunk` bug at compile time — highest value as the transpiler
   takes on more machine-generated binaries.
2. **Smarter spilling.** The current spiller parks *all* locals in an array
   (inflating code ~3×, which is what pushes functions into long-jump range). Keeping
   hot locals in registers and spilling only the overflow would cut both code size
   and jump pressure — the one lever that shrinks *both* walls at once.
3. **Upstream the long-jump opcodes.** Additive and self-contained — a clean candidate
   to offer upstream (nooga's paserati) independent of the transpiler, which stays on
   `feat/wasm-transpile`.
4. **Native i64 in the VM.** wasm `i64` is exact-BigInt today; a native int64 value
   type cuts allocation. Pure perf, only if profiling shows i64 alloc dominates.
   (Distinct from let-go's *own* 64-bit fidelity, which is the guest-side TinyGo
   32-bit-`int` issue — that one is let-go's/toolchain's, not paserati's.)

## How we worked (protocol)

Two Claude sessions, sibling repos, no shared VCS: a shared drop dir for artifacts +
`NOTE-*.md` message bus (measured-result-first), a tmux doorbell for real-time
nudges, each side building from its own tree. Codified in `PROTOCOL.md` (same dir)
and both sides' memory. This report was itself produced through it — drafted by the
let-go side, findings/next-steps contributed by the paserati side, synthesized here.

---

*Let-go side branches (local, unpushed): `spike/wasi-gating` `6565ebe`,
`spike/wasi-runtime-only` `1989edd`, `spike/wasi-ro-tinygo` `33c39ad`,
`spike/nogoroutine` `f7ee209`. Paserati: `feat/wasm-transpile` incl. `da2a875`.*
