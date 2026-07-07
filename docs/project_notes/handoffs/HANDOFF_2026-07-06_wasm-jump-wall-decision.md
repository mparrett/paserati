# Session Handoff — WASM transpiler: the jump wall is CLOSED; let-go runs

**Created:** 2026-07-06 (late). **Updated:** 2026-07-06 (later) — long jumps
done, let-go runs end to end; then a cross-repo round with the let-go team on a
goroutine-free build (see "Cross-repo" below — four builds run, stock paserati
ruled out, stop-here call made). This supersedes the "decision waiting" framing.
**Working dir:** work is in the **`paserati-wasm` worktree** on `feat/wasm-transpile`
(the `../paserati` checkout is a different branch — don't confuse them).

## DONE — the jump wall is gone, let-go runs (commit `da2a875`)

The decision (implement 32-bit long jumps vs hand the spec to the let-go team)
went **implement**. Result: **let-go runs end to end on paserati's VM,
byte-for-byte identical to wasmtime**, on both runtime-only TinyGo builds.

```
let-go runtime-only on wasi — precompiled .lgb, no compiler linked
  6 * 7 * 2      = 84
  sum 0..99      = 4950
  map inc [1 2 3]= [2 3 4]
  count "paserati"= 8
```

What shipped (`da2a875`, `feat/wasm-transpile`):

- **VM:** additive opcodes `OpJumpLong` / `OpJumpIfFalseLong` (32-bit big-endian
  offsets) in `pkg/vm/bytecode.go` (172/173) + decode in `pkg/vm/vm.go`, plus
  `Chunk.WriteUint32` and a `longJumpInstruction` disassembler. The stock TS
  compiler never emits them, so existing paserati bytecode is untouched.
- **wasm/asm:** `finish()` now runs a **branch-relaxation fixpoint**
  (`relaxJumps`): short jumps stay 2 bytes, only over-range ones promote to the
  long form. It no longer stubs on jump overflow (always succeeds).
- **wasm/profile:** jump distance no longer forces a stub; registers are the
  only remaining limit. Verdict string + `fp.Limit` updated.
- **Latent bug fixed (un-masked by un-stubbing):** `resetChunk` truncated
  `Constants` but left `AddConstant`'s dedup caches intact, so the direct→spilled
  retry of a large function got **stale constant indices** past the shorter pool
  (func488 loaded const 67 from a 25-entry pool). Now `Chunk.Reset()` clears the
  caches. This was NOT a long-jump bug — the crash was the 2nd instruction,
  before any jump executes.

Register widening was **never needed** — the spiller already covers it. This is
the whole reason the "16-bit registers + 32-bit jumps" fork collapsed to just
the jumps.

### Verification (all green)

- `-profile` on both runtime-only builds: **0 stubbed, 0 error** (opt2 2721
  funcs → 2720 direct + 1 spilled; optz 3048 → 3047 + 1 spilled).
- `-run <build> - < sample.lgb` for opt2 **and** optz: byte-for-byte identical
  to `wasmtime` (diff clean).
- Regression tests added: `pkg/vm/long_jump_test.go` (decode of both opcodes +
  the reset-cache fix), `pkg/wasm/asm_test.go` (relaxation: short stays
  `OpJump`, over-range promotes to `OpJumpLong`/`OpJumpIfFalseLong` with a
  correct 32-bit offset landing on target).
- `go test ./pkg/vm ./pkg/wasm ./tests` green; `go vet` clean.

## Cross-repo: goroutine-free let-go, and the four-build result

After long jumps shipped, the let-go team (nooga side, via their Claude) explored
removing the asyncify gowrapper — the source of the worst oversized functions.
On the wasm MVP target asyncify is TinyGo's *only* goroutine mechanism, and
wasip1 has no threads, so goroutines buy cooperative concurrency a wasi target
never uses. Their `spike/nogoroutine` branch (`-scheduler=none`, no asyncify)
boots and runs the sample. They dropped two goroutine-free artifacts and asked
whether that reaches **stock paserati** (no spiller, no long jumps).

Measured with `-profile` (all four run byte-identical to wasmtime):

| build | funcs | needs spiller (>256 regs) | needs long jumps (>32 KB) | worst jump |
|---|---|---|---|---|
| asyncify opt2      | 2721 | 1 (248 loc) | 22 | ~392 KB |
| asyncify optz      | 3048 | 1 (462 loc) | 11 | ~223 KB |
| nogoroutine opt2   | 2590 | 1 (246 loc) | 6  | ~110 KB |
| nogoroutine optz   | 2896 | 1 (460 loc) | 3  | ~130 KB |

**Conclusion: stock paserati is not reachable by `-opt` level alone.** Removing
goroutines killed the catastrophic case but not the pattern — one large core
function (eval/apply-shaped) stays big regardless. And the two `-opt` levels
trade the walls against each other: opt2 keeps the register wall marginal (~246
locals, only just over 256 with the operand stack) but leaves more long jumps;
optz halves the jumps but nearly doubles that function's locals. No level wins
both axes.

**Decision: stop chasing stock paserati.** The residue is small and already
handled cheaply — the spiller is transpiler-only (no VM change) and the long
jumps are two additive opcodes. paserati runs all four builds correctly today;
fighting TinyGo codegen to shave one function under both limits buys nothing at
runtime. Full exchange in the shared dir (see Artifacts): `NOTE-nogoroutine*.md`
(theirs), `NOTE-paserati-nogoroutine*-scan.md` (ours). An untried lever if anyone
resumes it: `-opt=1` / `-opt=s` might sit between opt2 and optz — not worth
gating on. Whether goroutine-free graduates from spike to a real build tag is
the let-go team's roadmap call.

## Repro quick-ref

```bash
cd ~/projects-new/3p/paserati-wasm && go build -o paserati-wasm ./cmd/paserati-wasm/
DIR=~/projects-new/3p/paserati/scratch/letgo-wasi-targets/runtime-only-tinygo
for b in optz opt2; do
  ./paserati-wasm -profile "$DIR/lg-runtime-only-tinygo-$b.wasm" | head -2      # 0 stubbed
  diff <(./paserati-wasm -run "$DIR/lg-runtime-only-tinygo-$b.wasm" - < "$DIR/sample.lgb") \
       <(wasmtime          "$DIR/lg-runtime-only-tinygo-$b.wasm" - < "$DIR/sample.lgb") \
    && echo "$b identical to wasmtime"
done
```

## Next Steps (both orthogonal — perf / outreach, NOT correctness)

1. **Native int64 in the VM.** BigInt helpers are correct but allocate; a native
   i64 path is a pure perf lever. Only worth it if profiling shows i64 alloc
   matters. Independent of everything above.
2. **A-track: the wazero embedding pitch to let-go upstream (nooga).** Gating is
   already proven on `spike/wasi-gating`. The deciding factor is a concrete
   consumer — a Go host that would embed let-go via wazero. **Pitch is drafted
   and ready to send:**
   `~/projects-new/project-docs/paserati/wazero-embedding-pitch-nooga.md` (leads
   with wazero embedding, names the number-fidelity cost, ends on a demo offer;
   not yet sent). Background case in the sibling
   `wasi-letgo-proposal-and-transpiler-gaps.md` (RESOLVED at the top for the
   transpiler side).
3. (Housekeeping) Nothing pending on the transpiler correctness frontier. The
   numeric ISA, WASI host, spiller, and now long jumps are all done.

## Gotchas (still true, cost real time)

- **Test with `-profile` first** — it settles "does build X run" in seconds and
  distinguishes register vs (formerly) jump limits.
- **DisassembleChunk panics** on chunks with function/native constants; scan
  `g.out` or the raw `c.Code` bytes instead.
- **i64 fidelity is the guest's, not ours:** TinyGo `int` is 32-bit → let-go
  big-int math is wrong in the emitted wasm; only the stdlib-Go 24 MB build is
  faithful. Don't "fix" it in the transpiler.
- **Never push; don't edit let-go's tree** (only build from its worktree).
- **The register spiller retry clears the chunk via `Chunk.Reset()`** — if you
  add new per-chunk caches to `pkg/vm`, remember to clear them in `Reset()` too,
  or the spilled retry will read stale indices.

## Artifacts (durable locations)

- **Team's runtime-only artifacts + the cross-repo note exchange (durable):**
  `~/projects-new/3p/paserati/scratch/letgo-wasi-targets/runtime-only-tinygo/`
  — four builds (`lg-runtime-only-tinygo-{opt2,optz}.wasm` = asyncify;
  `lg-nogoroutine-tinygo-{opt2,optz}.wasm` = goroutine-free), `sample.lgb`,
  `sample.lg`, README. Notes: `NOTE-nogoroutine*.md` / `NOTE-scheduler-none.md`
  (theirs), `NOTE-paserati-nogoroutine*-scan.md` (ours — the four-build scan and
  the stop-here call). Faithful 24 MB stdlib-Go build one dir up
  (`../lg-stdgo-wasip1.wasm`). The team's branch is `spike/nogoroutine` @
  `f7ee209` (local/unpushed).
- Float/i64 probes committed as `pkg/wasm/testdata/tinygo_{floats,i64}.wasm`.
  Regenerate letgo.wasm: `tinygo build -target=wasi -opt=z -no-debug -o x.wasm .`
  from `~/projects-new/3p/worktrees/let-go-tinygo-main`.

## Commit history this session (newest first)

```
da2a875  wasm+vm: 32-bit long jumps — let-go now runs end to end   <-- this session
686fd00  docs: handoff — jump wall is the sole blocker, long-jump fix scoped
d42e8cc  wasm: forward guest argv in -run; accurate over-limit stub message
5b72315  wasm: -profile mode — measure a module vs the register/jump limits
ccd11e9  wasm: register spiller for oversized functions + jump-overflow guard
7ff0000  wasm: WASI import batch, narrow i64 mem, full float ISA, oversized-fn stub
87f92c7  wasm: f64 + i64 + conversions — run float/int programs faithfully
d5900c8  wasm: WASI host — run TinyGo wasi hello end to end
```
