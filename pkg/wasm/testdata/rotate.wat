(module
  (func $rotl (export "rotl") (param $x i32) (param $n i32) (result i32)
    (i32.rotl (local.get $x) (local.get $n))))
