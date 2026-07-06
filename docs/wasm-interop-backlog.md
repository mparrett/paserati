# wasm-interop — backlog & idea log

Running list of "someday" ideas for the wasm→paserati-bytecode work. Companion
to [`wasm-interop-design.md`](./wasm-interop-design.md) (which tracks what's
*done*). This file is where half-formed ideas land before they're worth a design.
Nothing here is committed to; it's a menu.

Status legend: 💡 idea · 🔬 worth a spike · 📐 needs design first.

---

## Near-term codegen polish

- 🔬 **Global-liveness DCE.** The current dead-store elimination is basic-block
  scoped, so it keeps stores that are dead only across a block boundary (e.g. a
  copy-on-`local.get` temp whose reader got copy-propagated away, or a zero-init
  of a local that's always written before read). A proper backward liveness
  dataflow over the instruction CFG would remove them. `OpCall` needs its
  implicit read range (`funcReg..funcReg+argCount`) modeled in use/def. ~60–80
  lines; would squeeze out most of the remaining `OpMove`s.
- 💡 **Const-load / move coalescing.** `OpLoadConst T,k; OpMove D,T` (T dead) →
  `OpLoadConst D,k`. Catches `local.set (i32.const k)` and the loop-prologue
  local inits. Constant propagation on the symbolic list.
- 💡 **Peephole the redundant zero-inits** once liveness exists — a declared
  local that's assigned before any read doesn't need its wasm-mandated zero-init.

## Wasm engine completeness (toward a general engine)

Ordered roughly by "unlocks the most, costs the least first."

- 📐 **i64 as a first-class value.** paserati numbers are `float64`, which can't
  hold a full 64-bit int. Real programs (esp. Go, where `int` is 64-bit) use i64
  everywhere. Options: lean on paserati's existing int64/BigInt representation,
  or carry wasm i64 as a distinct compiled type. This is the single biggest
  semantic gap and gates most "real module" work.
- 📐 **f32/f64 arithmetic + conversions** (`trunc`/`convert`/`extend`/`wrap`/
  `reinterpret`), with bit-faithful NaN/-0/rounding. Sign-extension ops
  (`i32.extend8_s`, …).
- 💡 **Faithful integer semantics.** i32/i64 wraparound, div/rem traps
  (div-by-zero, `INT_MIN/-1`), shifts mod width, rotates. Today we use JS number
  math directly — fine for the current fixtures, wrong in general.
- 📐 **Tables + `call_indirect`.** Essential for anything with function pointers,
  interfaces, closures, or `switch`-on-func. Needs a function table and a
  type-checked indirect call. Go leans on this constantly.
- 💡 **Globals** (mutable + immutable). Cheap; Go uses a global for the stack
  pointer.
- 📐 **`memory.grow`.** Requires the backing buffer to reallocate and stay
  consistent — the current "native helpers close over a fixed `[]byte`" design
  must move behind a pointer/indirection so grow is visible. Go's heap grows, so
  this is required for Go.
- 💡 **`br_table`** (switch), **`select`**, **`unreachable`** trap, **bulk
  memory** (`memory.copy`/`fill`), **multi-value blocks**.
- 💡 **Trap semantics.** OOB / div-by-zero / bad-indirect-call should trap in a
  way the host observes as a fault (today: a Go error surfaced as a VM exception,
  which is roughly right).

## Host / runtime interface

- 📐 **Minimal WASI (`wasip1`) host.** Implement the handful of WASI imports a
  simple program needs: `fd_write` (stdout), `fd_read`, `clock_time_get`,
  `random_get`, `proc_exit`, `args_get`/`environ_get`, and a `poll_oneoff` stub.
  This unlocks non-Go WASI programs (Rust/C → `wasip1`) that do stdio.
- 📐 **Go `wasip1` bootstrap.** Export/entry wiring (`_start`), set up argv/env in
  linear memory, run the scheduler to completion for non-blocking programs.
- 💡 **Broaden WASI + cooperative scheduling** for programs that sleep/block/do
  file I/O (`poll_oneoff`, timers, fd tables).

## Reach / exposure

- 💡 **`WebAssembly.instantiate` builtin** (approach A from the design doc):
  expose the engine to paserati *scripts*, memory as an `ArrayBuffer`, exports as
  callable functions. Turns this from a Go API into a JS-visible feature.
- 💡 **Expose linear memory as an exported `ArrayBuffer`** so host code can read
  results a module leaves in memory.

## The "run arbitrary Go wasm" epic  📐

Long-horizon thought experiment (see chat 2026-07-06). Verdict: **target WASI,
not js/wasm.** The ladder:

1. Finish the wasm **core** (i64, floats+conversions, tables/`call_indirect`,
   globals, `memory.grow`, `br_table`, traps). Makes paserati a general engine for
   *pure computational* wasm (hand-written, or Rust/C with no imports).
2. A **minimal WASI host** → non-Go WASI stdio programs run.
3. **Go `wasip1` bootstrap** → a trivial Go program (`println("hi")`) prints and
   exits. Note: even hello-world drags in the whole Go runtime (GC, scheduler,
   allocator) compiled *into* the wasm — bounded but large.
4. Broaden WASI + scheduling for Go programs that block / do I/O.
5. (Probably never) **js/wasm + `syscall/js`** — you'd be reimplementing a JS
   host + browser APIs. The one poetic angle: paserati *is* a JS engine, so
   `js.Value` could map onto paserati's own object model — Go-wasm hosted by the
   very JS engine it calls out to. Full circle, huge effort.

Hard parts that gate everything: **i64 fidelity**, **`memory.grow`**,
**`call_indirect`**, and the **open-ended import surface**. "Arbitrary" is doing
a lot of work — networking/threads/CGO need host support that may never exist.
First real milestone past today's subset: a no-import pure-compute module using
i64 + floats + `call_indirect`.
