(module
  (global $g (mut i32) (i32.const 10))
  (func $inc (export "inc") (param $n i32) (result i32)
    (global.set $g (i32.add (global.get $g) (local.get $n)))
    (global.get $g))
  (func $peek (export "peek") (result i32)
    (global.get $g)))
