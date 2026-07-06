(module
  (memory 1)
  (func $f64rt (export "f64rt") (param $x f64) (result f64)
    (f64.store (i32.const 8) (local.get $x))
    (f64.load (i32.const 8)))
  (func $f32rt (export "f32rt") (param $x f32) (result f32)
    (f32.store (i32.const 16) (local.get $x))
    (f32.load (i32.const 16))))
