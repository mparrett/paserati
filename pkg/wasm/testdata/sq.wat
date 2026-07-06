(module
  (func $sq (export "sq") (param $x i32) (result i32)
    (local $t i32)
    (local.set $t (i32.mul (local.get $x) (local.get $x)))
    (local.get $t)))
