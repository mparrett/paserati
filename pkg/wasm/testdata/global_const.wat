(module
  (global $base i32 (i32.const 100))
  (func $add_base (export "add_base") (param $x i32) (result i32)
    (i32.add (local.get $x) (global.get $base))))
