# Session Handoff

**Created:** 2026-07-06T (evening, second session of the day)
**Session ID:** d56f4c88-a816-484f-9d16-5b2f8c5a579b
**Working Directory:** /Users/matt/projects-new/3p/paserati (main checkout) — but
**all the work is in the `paserati-wasm` worktree** on `feat/wasm-transpile`.

## What to read first

The wasm→paserati transpiler now runs **faithful float/int WASI programs
end-to-end** and gets a real TinyGo let-go binary **all the way to its goroutine
bootstrap** before trapping cleanly. It stops against **two stacked limits in
paserati's bytecode format** (not missing opcodes): the register file is 8-bit
(max 256) and jump offsets are 16-bit (±32 KB). Running let-go needs a **paserati
VM fork** widening both. Everything transpiler-side is done and committed. The
decision the team will discuss: **do the VM fork, or bank the milestone.** Full
write-up: `~/projects-new/project-docs/paserati/wasi-letgo-proposal-and-transpiler-gaps.md`.

## Summary

Took the transpiler from "hello runs" to "runs real compute, and knows exactly why
it can't run let-go." Landed the whole numeric + host frontier (trunc_sat decode,
full f32/f64 ISA, exact-BigInt i64, narrow i64 memory, the WASI import batch with
ENOSYS-stub fallback), fixed a latent `i32.div_s`-as-float-division bug, then built
a **register spiller** for oversized functions. Pushing a 4.6 MB TinyGo `-target=wasi`
let-go (3268 funcs) through it surfaced the two architectural walls and confirmed
what a VM fork would need.

## Current State

Branch `feat/wasm-transpile` (worktree `~/projects-new/3p/paserati-wasm`), off
`origin/main`, ahead 22, clean tree. Session commits (newest first):

```
ccd11e9  wasm: register spiller for oversized functions + jump-overflow guard
7ff0000  wasm: WASI import batch, narrow i64 mem, full float ISA, oversized-fn stub
87f92c7  wasm: f64 + i64 + conversions — run float/int programs faithfully
d5900c8  wasm: WASI host — run TinyGo wasi hello end to end
80b5433  wasm: codegen — i64 compares/xor, extend, reinterpret conversions   (prior session)
e8f92c8  docs: archive HANDOFF_2026-07-06_wasm-tinygo-wasi                    (prior session)
```

Verified: fixtures `tinygo_wasi_hello/floats/i64.wasm` match wasmtime byte-for-byte;
force-spill regression tests pass; `go test ./pkg/wasm/` + `TestScripts` green.

## Uncommitted State / Untouched

- **Uncommitted:** none in `paserati-wasm` — clean, all committed.
- **The proposal doc** `~/projects-new/project-docs/paserati/wasi-letgo-proposal-and-transpiler-gaps.md`
  was updated this session with the two-limits finding. It lives in the separate
  `project-docs` repo (not paserati) — commit it there if that repo tracks changes.
- **Scratchpad (session-local, NOT committed, load-bearing for repro):**
  - `scratchpad/letgo.wasm` — the 4.6 MB TinyGo wasi let-go build (the real target).
  - `scratchpad/heavy/heavy.wasm`, `scratchpad/floatp.wasm`, `scratchpad/i64t.wasm` —
    probe binaries. The float/i64 ones are also committed as `pkg/wasm/testdata/tinygo_*.wasm`.
  - `scratchpad/wasiprobe/` — a `compiler.Eval` let-go module (for the wazero demo idea).
- **Untouched deliberately:** the `let-go` repo and all its worktrees — I only
  *built* from `worktrees/let-go-tinygo-main` (`spike/wasi-gating`), never edited it.
  The classifier correctly blocked an attempt to `sed` its source; don't edit let-go's
  tree for a paserati experiment.

## Gotchas

- **Two worktrees:** `../paserati` is a different branch (perf work); all WASI work
  is in `../paserati-wasm`. Don't confuse them. Never push (fork is disposable).
- **The register spiller is correct but can't beat wall 2.** After it clears the
  256-register limit, the inflated (~3×) function blows the 16-bit jump limit. Both
  walls are paserati bytecode-format limits; see the doc's fork table.
- **let-go's func285 = TinyGo's asyncify goroutine wrapper** (`runtime.run$1$gowrapper`,
  560 locals). It's the *only* function over 256 locals (next is 90) and it's on the
  startup path — so it can't be skipped, and it's why the walls bite. `-opt=2` shrinks
  it to 362 (still over 256).
- **i64 fidelity is a per-target property of the *guest*, not us.** TinyGo makes
  `int` 32-bit on wasm → let-go's big-int math is already wrong in the emitted wasm;
  a faithful transpiler replays that. Only the stdlib-Go wasip1 build (24 MB) is
  faithful. See the table in the doc.
- **`i32.div_s` used to be float division** (`OpDivide`), silently zeroing float
  exponents. Fixed. If you add more i32 ops, don't route div/rem through `OpDivide`.
- **DisassembleChunk panics** on chunks with function/native constants ("value is not
  a function template") — can't disassemble real module functions with it. Scan
  `g.out` instead (that's how the jump-overflow bug got found).
- **`forceSpillAll` / spill tests:** `forceSpillAll` (package var, default false) routes
  every function through the spiller; `spill_test.go` uses it to prove spill-correctness
  on small fixtures. Keep it.

## Next Steps

Ordered by the decision the team makes first.

1. **Decide the fork (the gating call).** To run let-go — or any goroutine-using
   TinyGo wasi binary — paserati's VM needs **16-bit register operands** *and*
   **32-bit jump offsets**. That's a core-VM project (touches every opcode's operand
   encoding, the interpreter loop, the assembler, jump encode/decode), separate from
   the transpiler. Everything transpiler-side is ready for it (the spiller becomes
   unnecessary once registers are 16-bit).
2. **If not forking — measure the non-goroutine ceiling.** Build a TinyGo wasi binary
   that doesn't spawn goroutines (skips the asyncify wrapper entirely) and run it
   through `-run` to see how far the current transpiler actually gets. This tells us
   what's runnable *today* without the fork.
3. **Native int64 in the VM** — a perf optimization over BigInt-per-op helpers,
   orthogonal to correctness. Only if profiling shows i64 allocation matters.
4. **The upstream A-pitch** (separate track): the wasi gating is proven on
   `spike/wasi-gating`; the missing piece is a concrete wazero-embedding consumer.
   `scratchpad/wasiprobe/` is a starting point for the demo.

## Reproduction quick-ref

```bash
cd ~/projects-new/3p/paserati-wasm
go build -o paserati-wasm ./cmd/paserati-wasm/
./paserati-wasm -run pkg/wasm/testdata/tinygo_floats.wasm      # faithful float program
echo '(println (+ 1 2))' | ./paserati-wasm -run <scratch>/letgo.wasm
#   → wasm trap: wasm: func285 too large for register file   (clean degrade at wall 1/2)
go test ./pkg/wasm/                                            # incl. force-spill tests
```
