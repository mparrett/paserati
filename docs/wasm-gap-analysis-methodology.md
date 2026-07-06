# Methodology: gap analysis for a real wasm target

How we turn "could paserati run X?" into a concrete, ordered checklist instead of
speculation. Repeat this for every new target binary (a TinyGo program, a Rust
`wasip1` module, etc.).

## Steps

1. **Produce the smallest real binary.** Compile the actual thing, smallest
   config. For Go: `tinygo build -target=wasi -no-debug -opt=z -o x.wasm .`
   (TinyGo, not stock Go — far smaller runtime). Confirm it *works* first:
   `wasmtime x.wasm` — that's ground truth for any later comparison.

2. **Inventory the structure.** `wasm-objdump -x x.wasm` → sections, **imports**
   (the host surface you'd have to implement), function/table/memory/global
   counts, exports, start. Imports and function count are the headline numbers.

3. **Get the authoritative opcode set.** `wasm2wat x.wasm` then grep the folded
   text for the instructions that matter (control flow, calls, i64/float
   arithmetic, bulk/atomic memory). **Trust the `.wat`, not `wasm-objdump -d`** —
   objdump's disassembly undercounts branches (it missed every `br`/`br_if`/
   `call_indirect` on our first pass). A per-opcode histogram tells you what's
   actually exercised vs. merely present.

4. **Run it through our decoder.** `wasm.Decode(x.wasm)` → the first hard gate
   (for the TinyGo hello-world: the Import section). Fix that, re-run, find the
   next wall. Iterate. Decode-before-codegen: get the whole module to *parse*
   before worrying about lowering any of it.

5. **Tally have / need.** One table: feature · #uses · difficulty · note. Separate
   the reliefs (things feared that don't actually appear — e.g. i64 arithmetic was
   2 ops, zero float arithmetic) from the real gaps. #uses tells you what to build
   first; the reliefs tell you what to defer.

## Why it works

The abstract epic ("run Go wasm") is unbounded and discouraging. A specific 20 KB
binary has a *finite* opcode set, a *finite* import list, and a decoder that fails
at one concrete spot. The gap becomes a checklist you can burn down and re-measure
against the same binary. Ground truth (`wasmtime`) means every step is verifiable.

## Worked example

`docs/../project-docs/paserati/wasm-tinygo-wasi-hello-spike.md` (external notes) —
the TinyGo `wasi` hello-world: 3 WASI imports, 43 functions, i64 nearly all opaque
(2 adds), zero float arithmetic. Turned the epic into ~10 bounded features.
