(module
  (func $abs (export "abs") (param $x i32) (result i32)
    (local $r i32)
    (if (i32.lt_s (local.get $x) (i32.const 0))
      (then (local.set $r (i32.sub (i32.const 0) (local.get $x))))
      (else (local.set $r (local.get $x))))
    (local.get $r)))
