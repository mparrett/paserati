(module
  (memory 1)
  (func $rw (export "rw") (param $x i32) (result i32)
    (i32.store (i32.const 100) (local.get $x))
    (i32.load (i32.const 100)))
  (func $bytes (export "bytes") (param $x i32) (result i32)
    (i32.store8 (i32.const 0) (local.get $x))
    (i32.load8_u (i32.const 0))))
