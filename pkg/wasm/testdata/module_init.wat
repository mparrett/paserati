;; Instantiation-fidelity fixtures: an *imported* memory must be usable (it is
;; instantiated locally — this runtime is the embedder), i64 global
;; initialisers must stay exact past 2^53, and f32/f64 initialisers must not
;; decode as zero.
(module
  (import "env" "mem" (memory 1))
  (global $gi64 i64 (i64.const 4611686018427387905)) ;; 2^62 + 1
  (global $gf64 f64 (f64.const 2.5))
  (global $gf32 f32 (f32.const 1.5))
  (func (export "i64lo") (result i32)
    (i32.wrap_i64 (global.get $gi64)))
  (func (export "f64init") (result f64)
    (global.get $gf64))
  (func (export "f32init") (result f32)
    (global.get $gf32))
  (func (export "store_load") (param i32 i32) (result i32)
    (i32.store (local.get 0) (local.get 1))
    (i32.load (local.get 0)))
)
