;; High-bit / wrap semantics fixtures: i32 arithmetic must wrap mod 2^32 and
;; unsigned ops must return the signed-i32 carry representation. Each export
;; isolates one op; the *_then_* exports chain an unsigned result into a
;; signed follow-up, which is where a wrong (unsigned) representation leaks.
(module
  (func (export "add") (param i32 i32) (result i32)
    (i32.add (local.get 0) (local.get 1)))
  (func (export "sub") (param i32 i32) (result i32)
    (i32.sub (local.get 0) (local.get 1)))
  (func (export "mul") (param i32 i32) (result i32)
    (i32.mul (local.get 0) (local.get 1)))
  (func (export "div_u") (param i32 i32) (result i32)
    (i32.div_u (local.get 0) (local.get 1)))
  (func (export "rem_u") (param i32 i32) (result i32)
    (i32.rem_u (local.get 0) (local.get 1)))
  (func (export "shr_u") (param i32 i32) (result i32)
    (i32.shr_u (local.get 0) (local.get 1)))
  (func (export "rotl") (param i32 i32) (result i32)
    (i32.rotl (local.get 0) (local.get 1)))
  (func (export "divu_then_lts") (param i32 i32 i32) (result i32)
    (i32.lt_s (i32.div_u (local.get 0) (local.get 1)) (local.get 2)))
  (func (export "divu_then_eq") (param i32 i32 i32) (result i32)
    (i32.eq (i32.div_u (local.get 0) (local.get 1)) (local.get 2)))
)
