# Session Handoff — WASM transpiler: the jump wall, and the one change left

**Created:** 2026-07-06 (late; supersedes HANDOFF_2026-07-06_wasm-letgo-two-walls,
which is now history — read this one).
**Session ID:** d56f4c88-a816-484f-9d16-5b2f8c5a579b
**Working dir:** work is in the **`paserati-wasm` worktree** on `feat/wasm-transpile`
(the `../paserati` checkout is a different branch — don't confuse them).

## What to read first — the decision waiting

The wasm→paserati transpiler runs faithful float/int WASI programs and now has a
**measured, single blocker** to running let-go: paserati encodes jump targets as
**int16 (±32 KB)**, and let-go's functions need jumps up to 400 KB. **The register
wall is solved** (spiller + the runtime-only build shrank the goroutine wrapper
under 256). Every over-limit function is jump-only.

**The one change left:** add `OpJumpLong` / `OpJumpIfFalseLong` (32-bit offsets)
to paserati's VM; our codegen emits them only when a jump exceeds int16 (short
jumps stay 2 bytes). Additive — existing paserati bytecode is untouched. ~2 decode
cases in the interpreter loop + emission in `pkg/wasm/asm.go`. **This is the
pending decision: implement it (I lean yes) vs hand the spec to the let-go team
(their VM).** Full write-up: `~/projects-new/project-docs/paserati/wasi-letgo-proposal-and-transpiler-gaps.md`
(see the "UPDATE 2026-07-06 (late)" section at top).

## The measurement (why the fork shrank to one change)

Team shipped a compiler-free `runtime_only` TinyGo wasi build. Ran `-profile`
(new this session) on it:

| build | funcs | stubbed | register-only | jump-only |
|---|---|---|---|---|
| runtime-only opt2 (10 MB) | 2721 | 22 | 0 | 22 |
| runtime-only optz (7.2 MB) | 3048 | 11 | 0 | 11 |

Zero register-only stubs. `-run <optz> - < sample.lgb` traps on **func348**, a
jump-overflow function on the basic eval path → can't skip it. So the original
"16-bit registers + 32-bit jumps" fork collapses to just the jumps.

## Current State

Branch `feat/wasm-transpile`, clean tree, ahead ~27, nothing pushed. Key commits
this session (newest first):

```
d42e8cc  wasm: forward guest argv in -run; accurate over-limit stub message
5b72315  wasm: -profile mode — measure a module vs the register/jump limits
16c1d8e  docs: handoff update — jumps dominate
6e54a8a  docs: handoff 2026-07-06_wasm-letgo-two-walls
ccd11e9  wasm: register spiller for oversized functions + jump-overflow guard
7ff0000  wasm: WASI import batch, narrow i64 mem, full float ISA, oversized-fn stub
87f92c7  wasm: f64 + i64 + conversions — run float/int programs faithfully
d5900c8  wasm: WASI host — run TinyGo wasi hello end to end
```

What works, verified vs wasmtime: full float/f32/f64 + exact-BigInt i64 ISA;
trunc_sat; narrow i64 memory; WASI host (real imports + ENOSYS-stub fallback);
register spiller; `-profile`; `-run mod - < prog` (argv forwarding for stdin).
`go test ./pkg/wasm/` + `TestScripts` green.

## Artifacts (durable locations)

- **Team's runtime-only artifacts (durable, on disk):**
  `~/projects-new/3p/paserati/scratch/letgo-wasi-targets/runtime-only-tinygo/`
  — `lg-runtime-only-tinygo-opt2.wasm`, `-optz.wasm`, `sample.lgb`, `sample.lg`,
  README. Faithful 24 MB stdlib-Go build one dir up (`../lg-stdgo-wasip1.wasm`).
- **Session scratchpad (EPHEMERAL — /private/tmp, will vanish):**
  `scratchpad/letgo.wasm` (full with-compiler opt=z build, 4.6 MB — showed 11
  stubbed), `scratchpad/heavy/`, `scratchpad/floatp.wasm`. The float/i64 probes are
  committed as `pkg/wasm/testdata/tinygo_{floats,i64}.wasm`. Regenerate letgo.wasm:
  `tinygo build -target=wasi -opt=z -no-debug -o x.wasm .` from
  `~/projects-new/3p/worktrees/let-go-tinygo-main`.

## Gotchas (cost real time this session)

- **Jumps, not registers, are the wall.** The stub message used to say "register
  file" — misleading; most over-limit funcs are jump-only. Use `-profile` to see
  which limit. Fixed the message in d42e8cc.
- **`-profile` is the instrument:** `./paserati-wasm -profile x.wasm` → verdict +
  per-function limit. Built via `emitAll` + `layoutMaxJump` (compileInto refactor).
- **DisassembleChunk panics** on chunks with function/native constants; scan
  `g.out` instead (that's how the jump bug was found).
- **The register spiller is correct** (force-spill tests in `spill_test.go`) but
  doesn't unblock let-go — jumps do. It's still the right foundation.
- **i64 fidelity is the guest's, not ours:** TinyGo `int` is 32-bit → let-go
  big-int math wrong in the emitted wasm; only the stdlib-Go 24 MB build is
  faithful. Don't "fix" it in the transpiler.
- **Never push; don't edit let-go's tree** (only build from its worktree).

## Next Steps

1. **Decide + (likely) implement the long-jump opcodes.** `OpJumpLong` /
   `OpJumpIfFalseLong`, 32-bit offset, in `pkg/vm` (bytecode.go opcodes + the
   OpJump/OpJumpIfFalse decode in vm.go) and emit from `pkg/wasm/asm.go` finish()
   when `|dist| > 32767`. Then `-profile` should show 0 stubbed and
   `-run <optz> - < sample.lgb` should print the let-go banner + `6*7*2 = 84` etc.
   That's the killer demo.
2. Re-profile after: expect all-direct; run the sample; A/B against wasmtime output
   in the README.
3. (Later) native int64 in the VM — perf only, orthogonal. And the A-track wazero
   embedding pitch — gating proven on `spike/wasi-ro-tinygo`.

## Repro quick-ref

```bash
cd ~/projects-new/3p/paserati-wasm && go build -o paserati-wasm ./cmd/paserati-wasm/
DIR=~/projects-new/3p/paserati/scratch/letgo-wasi-targets/runtime-only-tinygo
./paserati-wasm -profile "$DIR/lg-runtime-only-tinygo-optz.wasm"      # verdict
./paserati-wasm -run "$DIR/lg-runtime-only-tinygo-optz.wasm" - < "$DIR/sample.lgb"
#   → today: trap on func348 (jump overflow). After long-jumps: the let-go banner.
wasmtime "$DIR/lg-runtime-only-tinygo-optz.wasm" - < "$DIR/sample.lgb"  # reference
```
